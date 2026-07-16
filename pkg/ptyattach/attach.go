package ptyattach

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	remotefsv1 "flyingEirc/Rclaude/api/proto/remotefs/v1"
	"flyingEirc/Rclaude/pkg/auth"
	"flyingEirc/Rclaude/pkg/config"
	"flyingEirc/Rclaude/pkg/ptyclient"
	"flyingEirc/Rclaude/pkg/transport"
)

const defaultTerm = "xterm-256color"

// terminalResetSequence 撤销远端程序可能透传给本地终端仿真器、而断开时来不及
// 自行恢复的模式。透传架构下本地不解析字节流（区别于 mosh 的终端仿真方案，
// 见 docs/reference/mosh.md），只能在会话结束时保守复位；不支持某序列的终端
// 会按未知 CSI 忽略。实测残留案例：kitty keyboard protocol 未弹栈导致每次按键
// 回显 "9;1:3u" 之类的 release 事件残片。
const terminalResetSequence = "\x1b[<u\x1b[<u\x1b[<u" + // kitty 键盘协议：弹栈（多推少弹为空操作）
	"\x1b[=0;1u" + // kitty 键盘协议：当前层标志清零兜底
	"\x1b[>4;0m" + // xterm modifyOtherKeys 关闭
	"\x1b[?1049l" + // 退出 alternate screen
	"\x1b[?2004l" + // bracketed paste 关闭
	"\x1b[?1000l\x1b[?1002l\x1b[?1003l\x1b[?1006l" + // 鼠标上报及 SGR 编码关闭
	"\x1b[?1004l" + // focus 上报关闭
	"\x1b[?1l\x1b>" + // 光标键/小键盘回到普通模式
	"\x1b[?7h" + // 自动换行恢复
	"\x1b[0m" + // SGR 属性清零
	"\x1b[0 q" + // 光标形状恢复默认
	"\x1b[?25h" // 光标恢复可见

var (
	errTTYRequired      = errors.New("ptyattach: stdin and stdout must both be interactive terminals")
	errEmptyServerToken = errors.New("ptyattach: daemon config must include server.token")
)

// ExitError carries the process exit code mapped from how the remote PTY
// session ended. Quiet suppresses printing Message before exiting.
type ExitError struct {
	Code    int
	Message string
	Quiet   bool
}

func (e *ExitError) Error() string {
	if e == nil {
		return ""
	}
	if e.Message != "" {
		return e.Message
	}
	return fmt.Sprintf("exit status %d", e.Code)
}

// Options configures a single attach run.
type Options struct {
	// ConfigPath points at the daemon YAML config (server address/token).
	ConfigPath string
	// Agent is the program the remote session runs, declared by the user on
	// the rclaude command line (-g/--agent): a bare name resolved via the
	// server's PATH or an absolute path on the server.
	Agent string
	// OnAttached, if non-nil, runs once when the attach handshake succeeds.
	OnAttached func()
}

// Run attaches the local terminal to the remote agent PTY session described
// by the daemon config. Mapped session endings are returned as *ExitError.
func Run(ctx context.Context, opts Options) error {
	deps := defaultCommandDeps()
	deps.onAttached = opts.OnAttached
	deps.agent = opts.Agent
	return runCommand(ctx, deps, opts.ConfigPath)
}

type loadedConfig struct {
	Address  string
	Token    string
	FrameMax int
	TLS      *transport.TLSConfig
}

type dialConfig struct {
	Address string
	Token   string
	TLS     *transport.TLSConfig
}

type commandDeps struct {
	stdin      io.ReadCloser
	stdout     io.Writer
	loadConfig func(string) (loadedConfig, error)
	terminal   terminalController
	dialPTY    func(context.Context, dialConfig) (ptyclient.Stream, io.Closer, error)
	stdinFD    int
	stdoutFD   int
	termName   string
	agent      string
	onAttached func()
}

type commandRuntime struct {
	termSession terminalSession
	stream      ptyclient.Stream
	closer      io.Closer
	stdin       io.ReadCloser
	stdout      io.Writer
	stopBridge  func()
	frameMax    int
}

func defaultCommandDeps() commandDeps {
	return commandDeps{
		stdin:      os.Stdin,
		stdout:     os.Stdout,
		loadConfig: loadClientConfigFromDaemon,
		terminal:   nativeTerminalController{},
		dialPTY:    dialRemotePTY,
		stdinFD:    int(os.Stdin.Fd()),
		stdoutFD:   int(os.Stdout.Fd()),
		termName:   os.Getenv("TERM"),
	}
}

func runCommand(ctx context.Context, deps commandDeps, configPath string) (err error) {
	if ctx == nil {
		ctx = context.Background()
	}

	cfg, err := deps.loadConfig(configPath)
	if err != nil {
		return err
	}
	if ttyErr := validateCommandTTY(deps); ttyErr != nil {
		return ttyErr
	}

	runtime, err := prepareCommandRuntime(ctx, deps, cfg)
	if err != nil {
		return err
	}
	defer closeCommandRuntime(&err, runtime)

	result := ptyclient.New(ptyclient.Config{
		Stream:     runtime.stream,
		Stdin:      runtime.stdin,
		Stdout:     deps.stdout,
		Resizes:    runtime.termSession.Resizes,
		FrameMax:   runtime.frameMax,
		OnAttached: deps.onAttached,
		Attach: ptyclient.AttachParams{
			InitialSize: runtime.termSession.InitialSize,
			Term:        commandTermName(deps.termName),
			Agent:       deps.agent,
		},
	}).Run(ctx)

	return exitStatusFromResult(result)
}

func validateCommandTTY(deps commandDeps) error {
	if !deps.terminal.IsTerminal(deps.stdinFD) || !deps.terminal.IsTerminal(deps.stdoutFD) {
		return errTTYRequired
	}
	return nil
}

func prepareCommandRuntime(ctx context.Context, deps commandDeps, cfg loadedConfig) (commandRuntime, error) {
	termSession, err := deps.terminal.Prepare(ctx, deps.stdinFD, deps.stdoutFD)
	if err != nil {
		return commandRuntime{}, err
	}

	stream, closer, err := deps.dialPTY(ctx, dialConfig{
		Address: cfg.Address,
		Token:   cfg.Token,
		TLS:     cfg.TLS,
	})
	if err != nil {
		if restoreErr := termSession.Restore(); restoreErr != nil {
			return commandRuntime{}, errors.Join(err, fmt.Errorf("ptyattach: restore terminal: %w", restoreErr))
		}
		return commandRuntime{}, err
	}

	stdin := deps.stdin
	var stopBridge func()
	if stdin != nil {
		stdin, stopBridge = newPTYStdinBridge(stdin)
	}

	return commandRuntime{
		termSession: termSession,
		stream:      stream,
		closer:      closer,
		stdin:       stdin,
		stdout:      deps.stdout,
		stopBridge:  stopBridge,
		frameMax:    clientFrameMax(int64(cfg.FrameMax)),
	}, nil
}

func closeCommandRuntime(runErr *error, runtime commandRuntime) {
	if runtime.stopBridge != nil {
		runtime.stopBridge()
	}
	if runtime.closer != nil {
		if closeErr := runtime.closer.Close(); closeErr != nil && runErr != nil && *runErr == nil {
			*runErr = closeErr
		}
	}
	// 会话建立后的所有结束路径（含远端异常断开）都要复位终端仿真器，
	// 写失败只能放弃（终端多半已不可写），仍继续恢复 termios。
	writeTerminalReset(runtime.stdout)
	if restoreErr := runtime.termSession.Restore(); restoreErr != nil && runErr != nil && *runErr == nil {
		*runErr = fmt.Errorf("ptyattach: restore terminal: %w", restoreErr)
	}
}

func writeTerminalReset(out io.Writer) {
	if out == nil {
		return
	}
	if _, err := io.WriteString(out, terminalResetSequence); err != nil {
		return
	}
}

func commandTermName(termName string) string {
	termName = strings.TrimSpace(termName)
	if termName == "" {
		return defaultTerm
	}
	return termName
}

func loadClientConfigFromDaemon(path string) (loadedConfig, error) {
	cfg, err := config.LoadDaemon(path)
	if err != nil {
		return loadedConfig{}, err
	}

	token := strings.TrimSpace(cfg.Server.Token)
	if token == "" {
		return loadedConfig{}, errEmptyServerToken
	}

	return loadedConfig{
		Address:  strings.TrimSpace(cfg.Server.Address),
		Token:    token,
		FrameMax: clientFrameMax(cfg.PTY.FrameMaxBytes),
		TLS:      dialTLS(cfg.Server.TLS),
	}, nil
}

// dialTLS 把 config 的 TLS 段映射为 transport 层的拨号 TLS 选项；
// 未启用时返回 nil，Dial 走明文，等价于改造前行为。
func dialTLS(cfg config.ServerTLSConfig) *transport.TLSConfig {
	if !cfg.Enabled {
		return nil
	}
	return &transport.TLSConfig{
		ServerName:         cfg.ServerName,
		CAFile:             cfg.CAFile,
		InsecureSkipVerify: cfg.InsecureSkipVerify,
	}
}

func clientFrameMax(n int64) int {
	if n <= 0 {
		return int(config.DefaultPTYFrameMaxBytes)
	}
	maxInt := int(^uint(0) >> 1)
	if n > int64(maxInt) {
		return maxInt
	}
	return int(n)
}

// Bridge stdin through a private pipe so ptyclient can close its reader to
// unblock background pumps without closing the real terminal handle first.
func newPTYStdinBridge(src io.ReadCloser) (io.ReadCloser, func()) {
	reader, writer := io.Pipe()
	var once sync.Once

	go func() {
		_, err := io.Copy(writer, src)
		if err != nil && !errors.Is(err, io.EOF) {
			_ = writer.CloseWithError(err)
			return
		}
		closePipeWriter(writer)
	}()

	return reader, func() {
		once.Do(func() {
			closePipeWriter(writer)
		})
	}
}

func closePipeWriter(writer *io.PipeWriter) {
	if writer == nil {
		return
	}
	if err := writer.Close(); err != nil {
		return
	}
}

func dialRemotePTY(ctx context.Context, cfg dialConfig) (ptyclient.Stream, io.Closer, error) {
	conn, err := transport.Dial(ctx, transport.DialOptions{Address: cfg.Address, TLS: cfg.TLS})
	if err != nil {
		return nil, nil, err
	}

	stream, err := openRemotePTYStream(ctx, conn, cfg.Token)
	if err != nil {
		closeIgnoringError(conn)
		return nil, nil, err
	}

	return stream, conn, nil
}

func closeIgnoringError(closer io.Closer) {
	if closer == nil {
		return
	}
	if err := closer.Close(); err != nil {
		return
	}
}

func openRemotePTYStream(
	ctx context.Context,
	conn *grpc.ClientConn,
	token string,
) (ptyclient.Stream, error) {
	outCtx := auth.NewOutgoingContext(ctx, token)
	client := remotefsv1.NewRemotePTYClient(conn)
	stream, err := client.Attach(outCtx)
	if err != nil {
		return nil, fmt.Errorf("ptyattach: attach remote pty: %w", err)
	}
	return stream, nil
}

func exitStatusFromResult(result ptyclient.ExitResult) error {
	if result.ServerError != nil {
		return &ExitError{
			Code:    serverErrorExitCode(result.ServerError),
			Message: serverErrorMessage(result.ServerError),
		}
	}
	if result.Err != nil {
		if st, ok := status.FromError(result.Err); ok {
			return &ExitError{
				Code:    grpcStatusExitCode(st.Code()),
				Message: grpcStatusMessage(st),
			}
		}
		if errors.Is(result.Err, context.Canceled) {
			return &ExitError{Code: 130, Quiet: true}
		}
		return result.Err
	}

	if result.Signal > 0 {
		return &ExitError{Code: 128 + int(result.Signal), Quiet: true}
	}
	if result.Code != 0 {
		return &ExitError{Code: int(result.Code), Quiet: true}
	}
	return nil
}

func serverErrorExitCode(serverErr *remotefsv1.Error) int {
	switch serverErr.GetKind() {
	case remotefsv1.Error_KIND_DAEMON_NOT_CONNECTED:
		return 2
	case remotefsv1.Error_KIND_SESSION_BUSY:
		return 3
	case remotefsv1.Error_KIND_SPAWN_FAILED:
		return 4
	case remotefsv1.Error_KIND_RATE_LIMITED:
		return 5
	case remotefsv1.Error_KIND_PROTOCOL:
		return 6
	case remotefsv1.Error_KIND_UNAUTHENTICATED:
		return 1
	default:
		return 1
	}
}

func serverErrorMessage(serverErr *remotefsv1.Error) string {
	if serverErr.GetKind() == remotefsv1.Error_KIND_SPAWN_FAILED {
		return spawnFailedMessage(serverErr.GetMessage())
	}

	if msg := strings.TrimSpace(serverErr.GetMessage()); msg != "" {
		return msg
	}

	switch serverErr.GetKind() {
	case remotefsv1.Error_KIND_DAEMON_NOT_CONNECTED:
		return "daemon offline, run daemon first"
	case remotefsv1.Error_KIND_SESSION_BUSY:
		return "another agent session is active"
	case remotefsv1.Error_KIND_RATE_LIMITED:
		return "too many attach requests"
	case remotefsv1.Error_KIND_PROTOCOL:
		return "protocol error"
	case remotefsv1.Error_KIND_UNAUTHENTICATED:
		return "auth failed"
	default:
		return "remote pty attach failed"
	}
}

func spawnFailedMessage(detail string) string {
	msg := "failed to start remote agent process on Server"
	if detail = strings.TrimSpace(detail); detail != "" {
		msg += ": " + detail
	}
	msg += "; check the -g/--agent name resolves on the Server (PATH or absolute path), /workspace/<user_id>, and Server-side agent login"
	return msg
}

func grpcStatusExitCode(code codes.Code) int {
	switch code {
	case codes.Unauthenticated:
		return 1
	default:
		return 1
	}
}

func grpcStatusMessage(st *status.Status) string {
	if st == nil {
		return "remote pty attach failed"
	}
	if st.Code() == codes.Unauthenticated {
		return "auth failed"
	}
	if msg := strings.TrimSpace(st.Message()); msg != "" {
		return msg
	}
	return "remote pty attach failed"
}

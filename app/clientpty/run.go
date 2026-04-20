package main

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

var (
	errTTYRequired      = errors.New("clientpty: stdin and stdout must both be interactive terminals")
	errEmptyServerToken = errors.New("clientpty: daemon config must include server.token")
)

type exitStatus struct {
	code    int
	message string
	quiet   bool
}

func (e *exitStatus) Error() string {
	if e == nil {
		return ""
	}
	if e.message != "" {
		return e.message
	}
	return fmt.Sprintf("exit status %d", e.code)
}

type loadedConfig struct {
	Address  string
	Token    string
	FrameMax int
}

type dialConfig struct {
	Address string
	Token   string
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
}

type commandRuntime struct {
	termSession terminalSession
	stream      ptyclient.Stream
	closer      io.Closer
	stdin       io.ReadCloser
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
		Stream:   runtime.stream,
		Stdin:    runtime.stdin,
		Stdout:   deps.stdout,
		Resizes:  runtime.termSession.Resizes,
		FrameMax: runtime.frameMax,
		Attach: ptyclient.AttachParams{
			InitialSize: runtime.termSession.InitialSize,
			Term:        commandTermName(deps.termName),
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
	})
	if err != nil {
		if restoreErr := termSession.Restore(); restoreErr != nil {
			return commandRuntime{}, errors.Join(err, fmt.Errorf("clientpty: restore terminal: %w", restoreErr))
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
	if restoreErr := runtime.termSession.Restore(); restoreErr != nil && runErr != nil && *runErr == nil {
		*runErr = fmt.Errorf("clientpty: restore terminal: %w", restoreErr)
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
	}, nil
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
	conn, err := transport.Dial(ctx, transport.DialOptions{Address: cfg.Address})
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
		return nil, fmt.Errorf("clientpty: attach remote pty: %w", err)
	}
	return stream, nil
}

func exitStatusFromResult(result ptyclient.ExitResult) error {
	if result.ServerError != nil {
		return &exitStatus{
			code:    serverErrorExitCode(result.ServerError),
			message: serverErrorMessage(result.ServerError),
		}
	}
	if result.Err != nil {
		if st, ok := status.FromError(result.Err); ok {
			return &exitStatus{
				code:    grpcStatusExitCode(st.Code()),
				message: grpcStatusMessage(st),
			}
		}
		if errors.Is(result.Err, context.Canceled) {
			return &exitStatus{code: 130, quiet: true}
		}
		return result.Err
	}

	if result.Signal > 0 {
		return &exitStatus{code: 128 + int(result.Signal), quiet: true}
	}
	if result.Code != 0 {
		return &exitStatus{code: int(result.Code), quiet: true}
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
	if msg := strings.TrimSpace(serverErr.GetMessage()); msg != "" {
		return msg
	}

	switch serverErr.GetKind() {
	case remotefsv1.Error_KIND_DAEMON_NOT_CONNECTED:
		return "daemon offline, run daemon first"
	case remotefsv1.Error_KIND_SESSION_BUSY:
		return "another claude session is active"
	case remotefsv1.Error_KIND_SPAWN_FAILED:
		return "failed to start remote claude process"
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

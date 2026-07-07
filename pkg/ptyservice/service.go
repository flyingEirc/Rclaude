package ptyservice

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"reflect"
	"strings"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	remotefsv1 "flyingEirc/Rclaude/api/proto/remotefs/v1"
	"flyingEirc/Rclaude/pkg/auth"
	"flyingEirc/Rclaude/pkg/config"
	"flyingEirc/Rclaude/pkg/logx"
	"flyingEirc/Rclaude/pkg/ptyhost"
)

var (
	ErrMissingUserID    = errors.New("ptyservice: missing user id in context")
	ErrNilRegistry      = errors.New("ptyservice: nil registry")
	ErrNilSpawner       = errors.New("ptyservice: nil spawner")
	ErrNilHost          = errors.New("ptyservice: nil host")
	ErrFrameTooLarge    = errors.New("ptyservice: client frame exceeds max bytes")
	ErrFirstFrameAttach = errors.New("ptyservice: first client frame must be attach")
	errApplicationSent  = errors.New("ptyservice: application error sent")
)

type Registry interface {
	LookupDaemon(userID string) bool
	RegisterPTY(userID string) (sessionID string, ok bool, err error)
	UnregisterPTY(userID, sessionID string)
}

type Host interface {
	Stdin() io.Writer
	Stdout() io.Reader
	Resize(ptyhost.WindowSize) error
	Shutdown(graceful bool) error
	Wait(ctx context.Context) (ptyhost.ExitInfo, error)
}

type Spawner interface {
	Spawn(ptyhost.SpawnReq) (Host, error)
}

type AttachLimiter interface {
	Wait(ctx context.Context, userID string) error
}

type InputLimiter interface {
	Wait(ctx context.Context, userID string, n int) error
}

type Config struct {
	Registry     Registry
	Spawner      Spawner
	AttachLimit  AttachLimiter
	InputLimit   InputLimiter
	Binary       string
	Args         []string
	Workspace    string
	EnvWhitelist []string
	FrameMax     int64
	GracefulStop time.Duration
	Logger       logx.Logger
}

type Service struct {
	remotefsv1.UnimplementedRemotePTYServer

	cfg Config
}

func New(cfg Config) (*Service, error) {
	if cfg.Registry == nil {
		return nil, ErrNilRegistry
	}
	if cfg.Spawner == nil {
		return nil, ErrNilSpawner
	}
	// An empty cfg.Binary is intentional: prepareSpawn then defaults to the
	// user's interactive login shell instead of a server-pinned program.
	if cfg.Workspace == "" {
		cfg.Workspace = config.DefaultPTYWorkspaceRoot
	}
	if len(cfg.EnvWhitelist) == 0 {
		cfg.EnvWhitelist = append([]string(nil), config.DefaultPTYEnvPassthrough...)
	}
	if cfg.FrameMax <= 0 {
		cfg.FrameMax = config.DefaultPTYFrameMaxBytes
	}
	if cfg.GracefulStop <= 0 {
		cfg.GracefulStop = config.DefaultPTYGracefulShutdown
	}
	if cfg.Logger == nil {
		cfg.Logger = logx.Nop()
	}
	cfg.Args = append([]string(nil), cfg.Args...)
	return &Service{cfg: cfg}, nil
}

func (s *Service) Attach(stream grpc.BidiStreamingServer[remotefsv1.ClientFrame, remotefsv1.ServerFrame]) error {
	return s.attach(stream)
}

func (s *Service) attach(stream grpcBidiStream) error {
	userID, attach, err := receiveAttachRequest(stream)
	if err != nil {
		return normalizeApplicationStop(err)
	}
	s.logAttachRequested(userID, attach)

	sessionID, err := s.reserveSession(stream, userID)
	if err != nil {
		return normalizeApplicationStop(err)
	}
	defer s.cfg.Registry.UnregisterPTY(userID, sessionID)

	if limitErr := s.enforceAttachLimit(stream, userID); limitErr != nil {
		return normalizeApplicationStop(limitErr)
	}

	host, cwd, err := s.startHost(stream, userID, attach)
	if err != nil {
		return normalizeApplicationStop(err)
	}

	predictive := attach.GetPredictiveEcho()
	if err := sendAttachedFrame(stream, sessionID, cwd, predictive); err != nil {
		cleanupHost(host)
		return err
	}
	s.cfg.Logger.Info("pty attached",
		"user_id", userID,
		"session_id", sessionID,
		"cwd", cwd,
		"predictive_echo", predictive,
	)

	return s.runAttached(stream, userID, sessionID, host, predictive)
}

func receiveAttachRequest(stream grpcBidiStream) (string, *remotefsv1.AttachReq, error) {
	userID, ok := auth.UserIDFromContext(stream.Context())
	if !ok {
		return "", nil, status.Error(codes.Unauthenticated, ErrMissingUserID.Error())
	}

	first, err := stream.Recv()
	if err != nil {
		return "", nil, err
	}
	attach := first.GetAttach()
	if attach == nil {
		return "", nil, applicationError(stream, remotefsv1.Error_KIND_PROTOCOL, ErrFirstFrameAttach.Error())
	}
	return userID, attach, nil
}

func (s *Service) reserveSession(stream grpcBidiStream, userID string) (string, error) {
	if !s.cfg.Registry.LookupDaemon(userID) {
		s.logAttachRejected(userID, "daemon_not_connected", nil)
		return "", applicationError(stream, remotefsv1.Error_KIND_DAEMON_NOT_CONNECTED, "daemon not connected")
	}

	sessionID, ok, err := s.cfg.Registry.RegisterPTY(userID)
	if err != nil {
		s.logAttachRejected(userID, "registry_error", err)
		return "", applicationError(stream, remotefsv1.Error_KIND_INTERNAL, err.Error())
	}
	if !ok {
		s.logAttachRejected(userID, "session_busy", nil)
		return "", applicationError(stream, remotefsv1.Error_KIND_SESSION_BUSY, "pty session already attached")
	}
	return sessionID, nil
}

func (s *Service) enforceAttachLimit(stream grpcBidiStream, userID string) error {
	if s.cfg.AttachLimit == nil {
		return nil
	}
	waitErr := s.cfg.AttachLimit.Wait(stream.Context(), userID)
	if waitErr != nil {
		s.logAttachRejected(userID, "attach_rate_limited", waitErr)
		return applicationError(stream, remotefsv1.Error_KIND_RATE_LIMITED, waitErr.Error())
	}
	return nil
}

// spawnPlan is the fully resolved command the PTY host will exec.
type spawnPlan struct {
	binary string
	args   []string
	cwd    string
	env    []string
}

func (s *Service) startHost(
	stream grpcBidiStream,
	userID string,
	attach *remotefsv1.AttachReq,
) (Host, string, error) {
	plan, err := s.prepareSpawn(userID, attach)
	if err != nil {
		s.cfg.Logger.Warn("pty spawn failed",
			"user_id", userID,
			"binary", s.binaryLabel(),
			"err", err,
		)
		return nil, "", applicationError(stream, remotefsv1.Error_KIND_SPAWN_FAILED, err.Error())
	}

	s.cfg.Logger.Info("pty spawn starting",
		"user_id", userID,
		"binary", plan.binary,
		"args_count", len(plan.args),
		"cwd", plan.cwd,
	)
	host, err := s.cfg.Spawner.Spawn(ptyhost.SpawnReq{
		Binary:          plan.binary,
		Args:            plan.args,
		Cwd:             plan.cwd,
		Env:             plan.env,
		InitSize:        fromProtoSize(attach.GetInitialSize()),
		GracefulTimeout: s.cfg.GracefulStop,
	})
	if err != nil {
		s.logSpawnFailed(userID, plan, err)
		return nil, "", applicationError(stream, remotefsv1.Error_KIND_SPAWN_FAILED, err.Error())
	}
	if isNilHost(host) {
		s.logSpawnFailed(userID, plan, ErrNilHost)
		return nil, "", applicationError(stream, remotefsv1.Error_KIND_SPAWN_FAILED, ErrNilHost.Error())
	}
	return host, plan.cwd, nil
}

func (s *Service) logSpawnFailed(userID string, plan spawnPlan, err error) {
	s.cfg.Logger.Warn("pty spawn failed",
		"user_id", userID,
		"binary", plan.binary,
		"args_count", len(plan.args),
		"cwd", plan.cwd,
		"err", err,
	)
}

func (s *Service) prepareSpawn(userID string, attach *remotefsv1.AttachReq) (spawnPlan, error) {
	binary, args, err := s.resolveCommand()
	if err != nil {
		return spawnPlan{}, err
	}
	cwd, err := ptyhost.ResolveCwd(s.cfg.Workspace, userID)
	if err != nil {
		return spawnPlan{}, err
	}
	env := ptyhost.BuildEnv(envMap(os.Environ()), s.cfg.EnvWhitelist, attach.GetTerm())
	return spawnPlan{binary: binary, args: args, cwd: cwd, env: env}, nil
}

// resolveCommand returns the binary and argv to spawn. With no configured
// binary it defaults to the user's interactive login shell so the passthrough
// is a working terminal (ls/cd, launch claude/codex) instead of a
// server-pinned tool.
func (s *Service) resolveCommand() (string, []string, error) {
	if strings.TrimSpace(s.cfg.Binary) == "" {
		return ptyhost.LoginShell()
	}
	binary, err := ptyhost.ResolveBinary(s.cfg.Binary)
	if err != nil {
		return "", nil, fmt.Errorf("resolve pty binary %q: %w", s.cfg.Binary, err)
	}
	return binary, append([]string(nil), s.cfg.Args...), nil
}

// binaryLabel names the configured program for logs, distinguishing the
// login-shell default from an explicit binary.
func (s *Service) binaryLabel() string {
	if strings.TrimSpace(s.cfg.Binary) == "" {
		return "(login shell)"
	}
	return s.cfg.Binary
}

func (s *Service) runAttached(
	stream grpcBidiStream,
	userID string,
	sessionID string,
	host Host,
	predictive bool,
) error {
	stdoutCh := make(chan stdoutEvent, 8)
	clientCh := make(chan clientEvent, 8)
	exitCh := make(chan exitEvent, 1)

	go pumpStdout(host.Stdout(), int(s.cfg.FrameMax), stdoutCh)
	go pumpClientFrames(stream, clientCh)
	go waitHost(host, exitCh)

	runtime := newAttachRuntime(stream, host)
	var tickCh <-chan time.Time
	if predictive {
		runtime.echo = &echoTracker{}
		ticker := time.NewTicker(echoTickInterval)
		defer ticker.Stop()
		tickCh = ticker.C
	}

	for !runtime.readyToExit() {
		nextStdout, nextClient, err := s.processAttachEvent(runtime, userID, sessionID, attachChans{
			stdout: stdoutCh,
			client: clientCh,
			exit:   exitCh,
			tick:   tickCh,
		})
		if err != nil {
			return normalizeApplicationStop(err)
		}
		stdoutCh = nextStdout
		clientCh = nextClient
	}
	return runtime.finish()
}

type attachChans struct {
	stdout chan stdoutEvent
	client chan clientEvent
	exit   chan exitEvent
	tick   <-chan time.Time
}

func (s *Service) processAttachEvent(
	runtime *attachRuntime,
	userID string,
	sessionID string,
	chans attachChans,
) (chan stdoutEvent, chan clientEvent, error) {
	select {
	case result, ok := <-chans.stdout:
		nextStdout, err := runtime.handleStdoutEvent(chans.stdout, result, ok)
		return nextStdout, chans.client, err
	case result, ok := <-chans.client:
		nextClient, err := s.handleClientEvent(runtime, userID, chans.client, result, ok)
		return chans.stdout, nextClient, err
	case result := <-chans.exit:
		s.logPTYExited(userID, sessionID, result)
		return chans.stdout, chans.client, runtime.handleExitEvent(result)
	case now := <-chans.tick:
		nextStdout, err := runtime.handleEchoTick(chans.stdout, now)
		return nextStdout, chans.client, err
	}
}

func (s *Service) handleClientEvent(
	runtime *attachRuntime,
	userID string,
	clientCh chan clientEvent,
	result clientEvent,
	ok bool,
) (chan clientEvent, error) {
	if !ok {
		return nil, nil
	}
	if result.err != nil {
		runtime.closeStream()
		return nil, nil
	}

	done, kind, err := s.handleClientFrame(runtime, userID, result.frame)
	if err != nil {
		runtime.shutdown(true)
		waitHostSilently(runtime.host)
		return clientCh, runtime.sendApplicationError(kind, err.Error())
	}
	if done {
		runtime.shutdown(true)
		return nil, nil
	}
	return clientCh, nil
}

func (s *Service) handleClientFrame(
	runtime *attachRuntime,
	userID string,
	frame *remotefsv1.ClientFrame,
) (bool, remotefsv1.Error_Kind, error) {
	if payload := frame.GetStdin(); payload != nil {
		return s.handleClientStdin(runtime, userID, payload)
	}
	if resize := frame.GetResize(); resize != nil {
		return s.handleClientResize(runtime.host, resize)
	}
	if frame.GetDetach() != nil {
		return true, 0, nil
	}
	return false, remotefsv1.Error_KIND_PROTOCOL, ErrFirstFrameAttach
}

func (s *Service) handleClientStdin(
	runtime *attachRuntime,
	userID string,
	payload []byte,
) (bool, remotefsv1.Error_Kind, error) {
	if int64(len(payload)) > s.cfg.FrameMax {
		return false, remotefsv1.Error_KIND_PROTOCOL, ErrFrameTooLarge
	}
	if s.cfg.InputLimit != nil {
		waitErr := s.cfg.InputLimit.Wait(runtime.stream.Context(), userID, len(payload))
		if waitErr != nil {
			return false, remotefsv1.Error_KIND_RATE_LIMITED, fmt.Errorf("stdin limited: %w", waitErr)
		}
	}
	if _, err := runtime.host.Stdin().Write(payload); err != nil {
		return false, remotefsv1.Error_KIND_INTERNAL, err
	}
	runtime.echo.record(len(payload), time.Now())
	return false, 0, nil
}

func (s *Service) handleClientResize(
	host Host,
	resize *remotefsv1.Resize,
) (bool, remotefsv1.Error_Kind, error) {
	if err := host.Resize(fromProtoSize(resize)); err != nil {
		return false, remotefsv1.Error_KIND_INTERNAL, err
	}
	return false, 0, nil
}

func (s *Service) logAttachRequested(userID string, attach *remotefsv1.AttachReq) {
	size := attach.GetInitialSize()
	s.cfg.Logger.Info("pty attach requested",
		"user_id", userID,
		"term", attach.GetTerm(),
		"cols", size.GetCols(),
		"rows", size.GetRows(),
	)
}

func (s *Service) logAttachRejected(userID string, reason string, err error) {
	attrs := []any{
		"user_id", userID,
		"reason", reason,
	}
	if err != nil {
		attrs = append(attrs, "err", err)
	}
	s.cfg.Logger.Warn("pty attach rejected", attrs...)
}

func (s *Service) logPTYExited(userID string, sessionID string, result exitEvent) {
	attrs := []any{
		"user_id", userID,
		"session_id", sessionID,
		"code", result.info.Code,
		"signal", result.info.Signal,
	}
	if result.err != nil {
		attrs = append(attrs, "err", result.err)
		s.cfg.Logger.Warn("pty exited", attrs...)
		return
	}
	s.cfg.Logger.Info("pty exited", attrs...)
}

func sendApplicationError(stream grpcBidiStream, kind remotefsv1.Error_Kind, message string) error {
	if message == "" {
		message = kind.String()
	}
	return stream.Send(&remotefsv1.ServerFrame{
		Payload: &remotefsv1.ServerFrame_Error{
			Error: &remotefsv1.Error{
				Kind:    kind,
				Message: message,
			},
		},
	})
}

func fromProtoSize(size *remotefsv1.Resize) ptyhost.WindowSize {
	if size == nil {
		return ptyhost.WindowSize{}
	}
	return ptyhost.WindowSize{
		Cols:   size.GetCols(),
		Rows:   size.GetRows(),
		XPixel: size.GetXPixel(),
		YPixel: size.GetYPixel(),
	}
}

func envMap(pairs []string) map[string]string {
	out := make(map[string]string, len(pairs))
	for _, pair := range pairs {
		key, value, ok := strings.Cut(pair, "=")
		if !ok || key == "" {
			continue
		}
		out[key] = value
	}
	return out
}

func pumpStdout(r io.Reader, frameMax int, ch chan<- stdoutEvent) {
	defer close(ch)
	if frameMax <= 0 {
		frameMax = int(config.DefaultPTYFrameMaxBytes)
	}
	buf := make([]byte, frameMax)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			payload := append([]byte(nil), buf[:n]...)
			ch <- stdoutEvent{data: payload}
		}
		if err != nil {
			ch <- stdoutEvent{err: err}
			return
		}
	}
}

func pumpClientFrames(stream grpcBidiStream, ch chan<- clientEvent) {
	defer close(ch)
	for {
		frame, err := stream.Recv()
		if err != nil {
			ch <- clientEvent{err: err}
			return
		}
		ch <- clientEvent{frame: frame}
	}
}

func waitHost(host Host, ch chan<- exitEvent) {
	info, err := host.Wait(context.Background())
	ch <- exitEvent{info: info, err: err}
}

func sendAttachedFrame(stream grpcBidiStream, sessionID string, cwd string, echoAck bool) error {
	return stream.Send(&remotefsv1.ServerFrame{
		Payload: &remotefsv1.ServerFrame_Attached{
			Attached: &remotefsv1.Attached{
				SessionId: sessionID,
				Cwd:       cwd,
				EchoAck:   echoAck,
			},
		},
	})
}

func sendExitedFrame(stream grpcBidiStream, info ptyhost.ExitInfo) error {
	return stream.Send(&remotefsv1.ServerFrame{
		Payload: &remotefsv1.ServerFrame_Exited{
			Exited: &remotefsv1.Exited{
				Code:   info.Code,
				Signal: info.Signal,
			},
		},
	})
}

func cleanupHost(host Host) {
	shutdownHostSilently(host, true)
	waitHostSilently(host)
}

func shutdownHostSilently(host Host, graceful bool) {
	if isNilHost(host) {
		return
	}
	if err := host.Shutdown(graceful); err != nil {
		return
	}
}

func waitHostSilently(host Host) {
	if isNilHost(host) {
		return
	}
	if _, err := host.Wait(context.Background()); err != nil {
		return
	}
}

func isNilHost(host Host) bool {
	if host == nil {
		return true
	}
	value := reflect.ValueOf(host)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

func applicationError(stream grpcBidiStream, kind remotefsv1.Error_Kind, message string) error {
	if err := sendApplicationError(stream, kind, message); err != nil {
		return err
	}
	return errApplicationSent
}

func normalizeApplicationStop(err error) error {
	if errors.Is(err, errApplicationSent) {
		return nil
	}
	return err
}

type attachRuntime struct {
	stream       grpcBidiStream
	host         Host
	streamOpen   bool
	stdoutClosed bool
	hostExited   bool
	exitInfo     ptyhost.ExitInfo
	shutdownOnce sync.Once
	echo         *echoTracker
}

func newAttachRuntime(stream grpcBidiStream, host Host) *attachRuntime {
	return &attachRuntime{
		stream:     stream,
		host:       host,
		streamOpen: true,
	}
}

func (r *attachRuntime) readyToExit() bool {
	return r.hostExited && r.stdoutClosed
}

func (r *attachRuntime) finish() error {
	if !r.streamOpen {
		return nil
	}
	return sendExitedFrame(r.stream, r.exitInfo)
}

func (r *attachRuntime) handleStdoutEvent(
	stdoutCh chan stdoutEvent,
	result stdoutEvent,
	ok bool,
) (chan stdoutEvent, error) {
	if !ok {
		r.stdoutClosed = true
		return nil, nil
	}
	if result.err != nil {
		return r.handleStdoutError(stdoutCh, result.err)
	}
	if len(result.data) == 0 || !r.streamOpen {
		return stdoutCh, nil
	}
	if err := r.stream.Send(&remotefsv1.ServerFrame{
		Payload: &remotefsv1.ServerFrame_Stdout{Stdout: result.data},
	}); err != nil {
		r.closeStream()
	}
	return stdoutCh, nil
}

func (r *attachRuntime) handleStdoutError(stdoutCh chan stdoutEvent, err error) (chan stdoutEvent, error) {
	if errors.Is(err, io.EOF) {
		r.stdoutClosed = true
		return nil, nil
	}
	r.shutdown(true)
	waitHostSilently(r.host)
	return stdoutCh, r.sendApplicationError(remotefsv1.Error_KIND_INTERNAL, err.Error())
}

// handleEchoTick advances the echo-ack watermark. Pending stdout is drained
// first so an ack never overtakes the echo bytes it vouches for.
func (r *attachRuntime) handleEchoTick(stdoutCh chan stdoutEvent, now time.Time) (chan stdoutEvent, error) {
	nextStdout, err := r.drainStdout(stdoutCh)
	if err != nil {
		return nextStdout, err
	}
	r.sendEchoAck(now)
	return nextStdout, nil
}

func (r *attachRuntime) drainStdout(stdoutCh chan stdoutEvent) (chan stdoutEvent, error) {
	for stdoutCh != nil {
		select {
		case result, ok := <-stdoutCh:
			next, err := r.handleStdoutEvent(stdoutCh, result, ok)
			if err != nil {
				return next, err
			}
			stdoutCh = next
		default:
			return stdoutCh, nil
		}
	}
	return stdoutCh, nil
}

func (r *attachRuntime) sendEchoAck(now time.Time) {
	if !r.streamOpen {
		return
	}
	offset, ok := r.echo.watermark(now)
	if !ok {
		return
	}
	if err := r.stream.Send(echoAckFrame(offset)); err != nil {
		r.closeStream()
	}
}

func (r *attachRuntime) handleExitEvent(result exitEvent) error {
	r.hostExited = true
	r.exitInfo = result.info
	if result.err != nil {
		return r.sendApplicationError(remotefsv1.Error_KIND_INTERNAL, result.err.Error())
	}
	return nil
}

func (r *attachRuntime) shutdown(graceful bool) {
	r.shutdownOnce.Do(func() {
		shutdownHostSilently(r.host, graceful)
	})
}

func (r *attachRuntime) closeStream() {
	r.streamOpen = false
	r.shutdown(true)
}

func (r *attachRuntime) sendApplicationError(kind remotefsv1.Error_Kind, message string) error {
	if !r.streamOpen {
		return nil
	}
	return applicationError(r.stream, kind, message)
}

type stdoutEvent struct {
	data []byte
	err  error
}

type clientEvent struct {
	frame *remotefsv1.ClientFrame
	err   error
}

type exitEvent struct {
	info ptyhost.ExitInfo
	err  error
}

type grpcBidiStream interface {
	Send(*remotefsv1.ServerFrame) error
	Recv() (*remotefsv1.ClientFrame, error)
	Context() context.Context
}

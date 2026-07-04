package syncer

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"time"

	backoff "github.com/cenkalti/backoff/v4"
	"golang.org/x/sync/errgroup"

	remotefsv1 "flyingEirc/Rclaude/api/proto/remotefs/v1"
	"flyingEirc/Rclaude/pkg/audit"
	"flyingEirc/Rclaude/pkg/config"
	"flyingEirc/Rclaude/pkg/logx"
	"flyingEirc/Rclaude/pkg/ratelimit"
	"flyingEirc/Rclaude/pkg/transport"
)

var (
	// ErrNilConfig indicates that RunOptions.Config was not provided.
	ErrNilConfig = errors.New("syncer: nil daemon config")

	daemonHeartbeatInterval     = 15 * time.Second
	daemonReconnectInitialDelay = time.Second
	daemonReconnectMaxDelay     = 30 * time.Second
	daemonReconnectMaxElapsed   time.Duration
	daemonOutgoingBufferSize    = 64
	daemonWatchEventBufferSize  = 64
)

// RunOptions contains the dependencies required to run the daemon loop.
type RunOptions struct {
	Config *config.DaemonConfig
	Logger logx.Logger
	Dialer func(context.Context, string) (net.Conn, error)
	// OnReady, if non-nil, is invoked exactly once when the daemon
	// establishes its first session with the server (initial file tree sent).
	OnReady func()
	// StartupFailFast makes Run return the session error immediately while
	// no session has ever been established, instead of retrying internally
	// with backoff. After the first successful session the reconnect loop
	// behaves as usual.
	StartupFailFast bool
}

// runtimeDeps bundles process-lifetime dependencies shared by every
// reconnect session.
type runtimeDeps struct {
	logger          logx.Logger
	sensitiveFilter *SensitiveFilter
	auditor         *audit.Recorder
	everEstablished bool
}

// Run blocks until ctx is canceled or an unrecoverable configuration error occurs.
func Run(ctx context.Context, opts RunOptions) error {
	ctx, deps, err := prepareRun(ctx, opts)
	if err != nil {
		return err
	}
	defer closeAuditor(deps.auditor, deps.logger)
	return runDaemonLoop(ctx, opts, deps)
}

func prepareRun(
	ctx context.Context,
	opts RunOptions,
) (context.Context, *runtimeDeps, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if opts.Config == nil {
		return nil, nil, ErrNilConfig
	}
	if err := opts.Config.Validate(); err != nil {
		return nil, nil, fmt.Errorf("syncer: validate daemon config: %w", err)
	}
	sensitiveFilter, err := NewSensitiveFilter(opts.Config.Workspace.SensitivePatterns)
	if err != nil {
		return nil, nil, err
	}

	logger := opts.Logger
	if logger == nil {
		logger = logx.Nop()
	}
	auditor, err := newAuditor(ctx, opts.Config.Audit, logger)
	if err != nil {
		return nil, nil, err
	}
	deps := &runtimeDeps{
		logger:          logger,
		sensitiveFilter: sensitiveFilter,
		auditor:         auditor,
	}
	return logx.WithContext(ctx, logger), deps, nil
}

func newAuditor(
	ctx context.Context,
	cfg config.AuditConfig,
	logger logx.Logger,
) (*audit.Recorder, error) {
	if !cfg.Enabled {
		return nil, nil
	}
	store, err := audit.OpenSQL(ctx, audit.SQLOptions{
		Driver: cfg.Driver,
		DSN:    cfg.DSN,
		Table:  cfg.Table,
	})
	if err != nil {
		return nil, fmt.Errorf("syncer: open audit store: %w", err)
	}
	logger.Info("file operation audit enabled", "driver", cfg.Driver, "table", cfg.Table)
	return audit.NewRecorder(store, cfg.QueueSize, logger), nil
}

func closeAuditor(auditor *audit.Recorder, logger logx.Logger) {
	if auditor == nil {
		return
	}
	if err := auditor.Close(); err != nil {
		logger.Warn("close audit recorder", "err", err)
	}
}

func runDaemonLoop(
	ctx context.Context,
	opts RunOptions,
	deps *runtimeDeps,
) error {
	retry := newReconnectBackOff()
	for {
		stop, err := runDaemonIteration(ctx, opts, retry, deps)
		if stop {
			return err
		}
	}
}

func runDaemonIteration(
	ctx context.Context,
	opts RunOptions,
	retry *backoff.ExponentialBackOff,
	deps *runtimeDeps,
) (bool, error) {
	if ctx.Err() != nil {
		return true, nil
	}

	established, err := runSession(ctx, opts, deps)
	if err == nil || ctx.Err() != nil {
		return true, nil
	}
	if opts.StartupFailFast && !deps.everEstablished {
		return true, err
	}

	delay, retryErr := nextRetryDelay(retry, established, err, deps.logger)
	if retryErr != nil {
		return true, retryErr
	}
	if !waitForRetry(ctx, delay) {
		return true, nil
	}
	return false, nil
}

func nextRetryDelay(
	retry *backoff.ExponentialBackOff,
	established bool,
	err error,
	logger logx.Logger,
) (time.Duration, error) {
	if established {
		retry.Reset()
	}

	delay := retry.NextBackOff()
	if delay == backoff.Stop {
		return 0, fmt.Errorf("syncer: reconnect exhausted: %w", err)
	}
	logger.Warn("daemon session ended", "err", err, "retry_in", delay)
	return delay, nil
}

func runSession(
	ctx context.Context,
	opts RunOptions,
	deps *runtimeDeps,
) (bool, error) {
	logger := deps.logger
	conn, err := transport.Dial(ctx, transport.DialOptions{
		Address: opts.Config.Server.Address,
		Dialer:  opts.Dialer,
	})
	if err != nil {
		return false, fmt.Errorf("syncer: dial server: %w", err)
	}
	defer closeConn(conn, logger)

	sessionCtx, cancel := context.WithCancel(logx.WithContext(ctx, logger))
	defer cancel()

	stream, err := transport.OpenStream(sessionCtx, conn, opts.Config.Server.Token)
	if err != nil {
		return false, fmt.Errorf("syncer: open stream: %w", err)
	}

	tree, err := Scan(ScanOptions{
		Root:            opts.Config.Workspace.Path,
		Excludes:        opts.Config.Workspace.Exclude,
		SensitiveFilter: deps.sensitiveFilter,
	})
	if err != nil {
		return false, fmt.Errorf("syncer: initial scan: %w", err)
	}
	if err := stream.Send(&remotefsv1.DaemonMessage{
		Msg: &remotefsv1.DaemonMessage_FileTree{
			FileTree: &remotefsv1.FileTree{Files: tree},
		},
	}); err != nil {
		return false, fmt.Errorf("syncer: send initial file tree: %w", err)
	}
	markEstablished(opts, deps)

	return true, serveStream(sessionCtx, cancel, stream, opts, deps)
}

// markEstablished records the first successful session establishment and
// fires the OnReady callback exactly once. Only the reconnect-loop goroutine
// touches deps.everEstablished, so no locking is needed.
func markEstablished(opts RunOptions, deps *runtimeDeps) {
	if deps.everEstablished {
		return
	}
	deps.everEstablished = true
	if opts.OnReady != nil {
		opts.OnReady()
	}
}

func closeConn(conn io.Closer, logger logx.Logger) {
	if err := conn.Close(); err != nil {
		logger.Warn("close grpc connection", "err", err)
	}
}

func serveStream(
	ctx context.Context,
	cancel context.CancelFunc,
	stream remotefsv1.RemoteFS_ConnectClient,
	opts RunOptions,
	deps *runtimeDeps,
) error {
	logger := deps.logger
	sendQueue := make(chan *remotefsv1.DaemonMessage, daemonOutgoingBufferSize)
	watchEvents := make(chan *remotefsv1.FileChange, daemonWatchEventBufferSize)
	locker := newPathLocker()
	selfWrites := newSelfWriteFilter(opts.Config.SelfWriteTTL)
	handleOpts := HandleOptions{
		Root:            opts.Config.Workspace.Path,
		Locker:          locker,
		SelfWrites:      selfWrites,
		SensitiveFilter: deps.sensitiveFilter,
		ReadLimiter:     ratelimit.NewBytesPerSecond(opts.Config.RateLimit.ReadBytesPerSec),
		WriteLimiter:    ratelimit.NewBytesPerSecond(opts.Config.RateLimit.WriteBytesPerSec),
	}
	if deps.auditor != nil {
		handleOpts.Auditor = deps.auditor
	}

	group, groupCtx := errgroup.WithContext(ctx)
	start := func(fn func(context.Context) error) {
		group.Go(func() error {
			err := fn(groupCtx)
			if err != nil {
				cancel()
			}
			return err
		})
	}

	start(func(ctx context.Context) error {
		return runSendLoop(ctx, stream, sendQueue)
	})
	start(func(ctx context.Context) error {
		return runRecvLoop(ctx, stream, handleOpts, sendQueue)
	})
	start(func(ctx context.Context) error {
		return Watch(ctx, WatchOptions{
			Root:            opts.Config.Workspace.Path,
			Excludes:        opts.Config.Workspace.Exclude,
			SensitiveFilter: deps.sensitiveFilter,
			Events:          watchEvents,
			Logger:          logger,
			SelfWrites:      selfWrites,
		})
	})
	start(func(ctx context.Context) error {
		return forwardWatchEvents(ctx, watchEvents, sendQueue)
	})
	start(func(ctx context.Context) error {
		return runHeartbeatLoop(ctx, sendQueue)
	})

	err := group.Wait()
	if ctx.Err() != nil || errors.Is(err, context.Canceled) {
		return nil
	}
	if errors.Is(err, io.EOF) {
		return err
	}
	return err
}

func runSendLoop(
	ctx context.Context,
	stream remotefsv1.RemoteFS_ConnectClient,
	sendQueue <-chan *remotefsv1.DaemonMessage,
) error {
	for {
		select {
		case <-ctx.Done():
			return nil
		case msg := <-sendQueue:
			if msg == nil {
				continue
			}
			if err := stream.Send(msg); err != nil {
				return fmt.Errorf("syncer: stream send: %w", err)
			}
		}
	}
}

func runRecvLoop(
	ctx context.Context,
	stream remotefsv1.RemoteFS_ConnectClient,
	handleOpts HandleOptions,
	sendQueue chan<- *remotefsv1.DaemonMessage,
) error {
	for {
		msg, err := stream.Recv()
		if err != nil {
			return fmt.Errorf("syncer: stream recv: %w", err)
		}

		switch body := msg.GetMsg().(type) {
		case *remotefsv1.ServerMessage_Request:
			response := Handle(ctx, body.Request, handleOpts)
			if !queueDaemonMessage(ctx, sendQueue, &remotefsv1.DaemonMessage{
				Msg: &remotefsv1.DaemonMessage_Response{Response: response},
			}) {
				return nil
			}
		case *remotefsv1.ServerMessage_Heartbeat:
			continue
		default:
			continue
		}
	}
}

func forwardWatchEvents(
	ctx context.Context,
	events <-chan *remotefsv1.FileChange,
	sendQueue chan<- *remotefsv1.DaemonMessage,
) error {
	for {
		select {
		case <-ctx.Done():
			return nil
		case change := <-events:
			if change == nil {
				continue
			}
			if !queueDaemonMessage(ctx, sendQueue, &remotefsv1.DaemonMessage{
				Msg: &remotefsv1.DaemonMessage_Change{Change: change},
			}) {
				return nil
			}
		}
	}
}

func runHeartbeatLoop(
	ctx context.Context,
	sendQueue chan<- *remotefsv1.DaemonMessage,
) error {
	ticker := time.NewTicker(daemonHeartbeatInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			heartbeat := &remotefsv1.Heartbeat{Timestamp: time.Now().Unix()}
			if !queueDaemonMessage(ctx, sendQueue, &remotefsv1.DaemonMessage{
				Msg: &remotefsv1.DaemonMessage_Heartbeat{Heartbeat: heartbeat},
			}) {
				return nil
			}
		}
	}
}

func queueDaemonMessage(
	ctx context.Context,
	sendQueue chan<- *remotefsv1.DaemonMessage,
	msg *remotefsv1.DaemonMessage,
) bool {
	select {
	case <-ctx.Done():
		return false
	case sendQueue <- msg:
		return true
	}
}

func newReconnectBackOff() *backoff.ExponentialBackOff {
	return backoff.NewExponentialBackOff(
		backoff.WithInitialInterval(daemonReconnectInitialDelay),
		backoff.WithMaxInterval(daemonReconnectMaxDelay),
		backoff.WithMaxElapsedTime(daemonReconnectMaxElapsed),
	)
}

func waitForRetry(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

package syncer

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"time"

	backoff "github.com/cenkalti/backoff/v4"
	"golang.org/x/sync/errgroup"

	remotefsv1 "flyingEirc/Rclaude/api/proto/remotefs/v1"
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
	Logger *slog.Logger
	Dialer func(context.Context, string) (net.Conn, error)
}

// Run blocks until ctx is canceled or an unrecoverable configuration error occurs.
func Run(ctx context.Context, opts RunOptions) error {
	ctx, logger, sensitiveFilter, err := prepareRun(ctx, opts)
	if err != nil {
		return err
	}
	return runDaemonLoop(ctx, opts, logger, sensitiveFilter)
}

func prepareRun(
	ctx context.Context,
	opts RunOptions,
) (context.Context, *slog.Logger, *SensitiveFilter, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if opts.Config == nil {
		return nil, nil, nil, ErrNilConfig
	}
	if err := opts.Config.Validate(); err != nil {
		return nil, nil, nil, fmt.Errorf("syncer: validate daemon config: %w", err)
	}
	sensitiveFilter, err := NewSensitiveFilter(opts.Config.Workspace.SensitivePatterns)
	if err != nil {
		return nil, nil, nil, err
	}

	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return logx.WithContext(ctx, logger), logger, sensitiveFilter, nil
}

func runDaemonLoop(
	ctx context.Context,
	opts RunOptions,
	logger *slog.Logger,
	sensitiveFilter *SensitiveFilter,
) error {
	retry := newReconnectBackOff()
	for {
		stop, err := runDaemonIteration(ctx, opts, logger, retry, sensitiveFilter)
		if stop {
			return err
		}
	}
}

func runDaemonIteration(
	ctx context.Context,
	opts RunOptions,
	logger *slog.Logger,
	retry *backoff.ExponentialBackOff,
	sensitiveFilter *SensitiveFilter,
) (bool, error) {
	if ctx.Err() != nil {
		return true, nil
	}

	established, err := runSession(ctx, opts, logger, sensitiveFilter)
	if err == nil || ctx.Err() != nil {
		return true, nil
	}

	delay, retryErr := nextRetryDelay(retry, established, err, logger)
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
	logger *slog.Logger,
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
	logger *slog.Logger,
	sensitiveFilter *SensitiveFilter,
) (bool, error) {
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
		SensitiveFilter: sensitiveFilter,
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

	return true, serveStream(sessionCtx, cancel, stream, opts, logger, sensitiveFilter)
}

func closeConn(conn io.Closer, logger *slog.Logger) {
	if err := conn.Close(); err != nil {
		logger.Warn("close grpc connection", "err", err)
	}
}

func serveStream(
	ctx context.Context,
	cancel context.CancelFunc,
	stream remotefsv1.RemoteFS_ConnectClient,
	opts RunOptions,
	logger *slog.Logger,
	sensitiveFilter *SensitiveFilter,
) error {
	sendQueue := make(chan *remotefsv1.DaemonMessage, daemonOutgoingBufferSize)
	watchEvents := make(chan *remotefsv1.FileChange, daemonWatchEventBufferSize)
	locker := newPathLocker()
	selfWrites := newSelfWriteFilter(opts.Config.SelfWriteTTL)
	handleOpts := HandleOptions{
		Root:            opts.Config.Workspace.Path,
		Locker:          locker,
		SelfWrites:      selfWrites,
		SensitiveFilter: sensitiveFilter,
		ReadLimiter:     ratelimit.NewBytesPerSecond(opts.Config.RateLimit.ReadBytesPerSec),
		WriteLimiter:    ratelimit.NewBytesPerSecond(opts.Config.RateLimit.WriteBytesPerSec),
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
			SensitiveFilter: sensitiveFilter,
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

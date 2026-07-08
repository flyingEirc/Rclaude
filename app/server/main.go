package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"google.golang.org/grpc"
	"google.golang.org/grpc/keepalive"

	remotefsv1 "flyingEirc/Rclaude/api/proto/remotefs/v1"
	"flyingEirc/Rclaude/pkg/auth"
	"flyingEirc/Rclaude/pkg/config"
	"flyingEirc/Rclaude/pkg/fusefs"
	"flyingEirc/Rclaude/pkg/logx"
	"flyingEirc/Rclaude/pkg/session"
)

func main() {
	// Trap SIGTERM as well as SIGINT: pkill/kill/systemd stop all send SIGTERM,
	// and the shutdown path must run so the deferred FUSE unmount fires. Without
	// this the process is killed outright, leaving a stale "/workspace" mount
	// ("Transport endpoint is not connected") that blocks the next startup.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cmd, err := newRootCommand()
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := cmd.ExecuteContext(ctx); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func newRootCommand() (*cobra.Command, error) {
	var configPath string

	cmd := &cobra.Command{
		Use:           "rclaude-server",
		Short:         "Run the Rclaude server",
		Args:          cobra.NoArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runServer(cmd.Context(), configPath)
		},
	}
	cmd.Flags().StringVar(&configPath, "config", "", "Path to the server YAML config")
	if err := cmd.MarkFlagRequired("config"); err != nil {
		return nil, fmt.Errorf("server: mark config flag required: %w", err)
	}
	return cmd, nil
}

func runServer(ctx context.Context, configPath string) error {
	cfg, logger, manager, service, verifier, err := prepareRuntime(configPath)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := logger.Close(); closeErr != nil {
			_, _ = fmt.Fprintln(os.Stderr, closeErr)
		}
	}()
	return runPreparedServer(ctx, cfg, logger, manager, service, verifier)
}

func runPreparedServer(
	ctx context.Context,
	cfg *config.ServerConfig,
	logger logx.Logger,
	manager *session.Manager,
	service *session.Service,
	verifier auth.Verifier,
) error {
	mkdirErr := os.MkdirAll(cfg.FUSE.Mountpoint, 0o750)
	if mkdirErr != nil {
		return fmt.Errorf("server: ensure mountpoint %q: %w", cfg.FUSE.Mountpoint, mkdirErr)
	}
	mounted, err := mountWorkspace(ctx, cfg, logger, manager)
	if err != nil {
		return err
	}
	defer func() {
		warnClose(logger, "close fuse mount", mounted.Close(), os.ErrClosed)
	}()

	ptyService, err := newPTYService(cfg, manager, logger)
	if err != nil {
		return fmt.Errorf("server: build pty service: %w", err)
	}

	listener, grpcServer, err := newGRPCServer(cfg, logger, verifier, service, ptyService)
	if err != nil {
		return err
	}
	defer func() {
		warnClose(logger, "close listener", listener.Close(), net.ErrClosed)
	}()

	logger.Info("server started", "listen", cfg.Listen, "mountpoint", cfg.FUSE.Mountpoint)
	return serveUntilDone(ctx, logger, grpcServer, listener)
}

func prepareRuntime(
	configPath string,
) (*config.ServerConfig, *logx.FileLogger, *session.Manager, *session.Service, auth.Verifier, error) {
	cfg, err := config.LoadServer(configPath)
	if err != nil {
		return nil, nil, nil, nil, nil, err
	}
	logger, err := newLogger(cfg)
	if err != nil {
		return nil, nil, nil, nil, nil, err
	}

	manager := session.NewManager(session.ManagerOptions{
		RequestTimeout:         cfg.RequestTimeout,
		CacheMaxBytes:          cfg.Cache.MaxBytes,
		OfflineReadOnlyTTL:     cfg.OfflineReadOnlyTTL,
		PrefetchEnabled:        cfg.Prefetch.Enabled,
		PrefetchMaxFileBytes:   cfg.Prefetch.MaxFileBytes,
		PrefetchMaxFilesPerDir: cfg.Prefetch.MaxFilesPerDir,
	})
	service, err := session.NewService(manager)
	if err != nil {
		return nil, nil, nil, nil, nil, err
	}
	verifier := auth.NewStaticVerifier(cfg.Auth.Tokens)
	return cfg, logger, manager, service, verifier, nil
}

func mountWorkspace(
	ctx context.Context,
	cfg *config.ServerConfig,
	logger logx.Logger,
	manager *session.Manager,
) (fusefs.Mounted, error) {
	mounted, err := fusefs.Mount(logx.WithContext(ctx, logger), fusefs.Options{
		Mountpoint: cfg.FUSE.Mountpoint,
		Manager:    manager,
	})
	if err != nil {
		return nil, err
	}
	return mounted, nil
}

func newGRPCServer(
	cfg *config.ServerConfig,
	logger logx.Logger,
	verifier auth.Verifier,
	service *session.Service,
	ptyService remotefsv1.RemotePTYServer,
) (net.Listener, *grpc.Server, error) {
	listener, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		return nil, nil, fmt.Errorf("server: listen %q: %w", cfg.Listen, err)
	}
	// recovery 拦截器置于最外层，先于 auth 执行，以兜住整条 handler 同步栈的 panic。
	// keepalive：服务端主动 PING 对端，死连接在 Time+Timeout 内触发 stream 报错，
	// 走既有 shutdown/UnregisterPTY 清理路径，避免 activePTY 泄漏卡死重连。
	// Caddy 前置时对端是回源连接（通常 loopback），探测客户端主要靠明文直连模式。
	// EnforcementPolicy 必须放宽到 MinTime < 客户端 Time（gRPC 默认 5 分钟会把
	// 30s 一次的客户端 PING 判为滥用并 GOAWAY），配对约束见 pkg/config。
	grpcServer := grpc.NewServer(
		grpc.KeepaliveParams(keepalive.ServerParameters{
			Time:    config.DefaultGRPCKeepaliveTime,
			Timeout: config.DefaultGRPCKeepaliveTimeout,
		}),
		grpc.KeepaliveEnforcementPolicy(keepalive.EnforcementPolicy{
			MinTime:             config.DefaultGRPCKeepaliveMinTime,
			PermitWithoutStream: true,
		}),
		grpc.ChainStreamInterceptor(
			recoveryStreamInterceptor(logger),
			auth.StreamServerInterceptor(verifier),
		),
	)
	remotefsv1.RegisterRemoteFSServer(grpcServer, service)
	if ptyService != nil {
		remotefsv1.RegisterRemotePTYServer(grpcServer, ptyService)
	}
	return listener, grpcServer, nil
}

func serveUntilDone(
	ctx context.Context,
	logger logx.Logger,
	grpcServer *grpc.Server,
	listener net.Listener,
) error {
	serveErrCh := make(chan error, 1)
	go func() {
		serveErrCh <- grpcServer.Serve(listener)
	}()

	select {
	case <-ctx.Done():
		logger.Info("server shutting down")
		stopGRPCServer(logger, grpcServer)
		err := <-serveErrCh
		if err == nil || errors.Is(err, grpc.ErrServerStopped) {
			return nil
		}
		return fmt.Errorf("server: grpc serve: %w", err)
	case err := <-serveErrCh:
		if err == nil || errors.Is(err, grpc.ErrServerStopped) {
			return nil
		}
		return fmt.Errorf("server: grpc serve: %w", err)
	}
}

// serverShutdownGrace bounds how long a signal-triggered graceful stop waits for
// in-flight RPCs (notably the long-lived daemon Connect stream) before the server
// is force-stopped, guaranteeing the deferred FUSE unmount always runs.
const serverShutdownGrace = 5 * time.Second

// stopGRPCServer tries a graceful stop but falls back to a hard Stop once the
// grace period elapses, so an open stream cannot block shutdown — and the FUSE
// unmount — indefinitely.
func stopGRPCServer(logger logx.Logger, grpcServer *grpc.Server) {
	stopped := make(chan struct{})
	go func() {
		grpcServer.GracefulStop()
		close(stopped)
	}()

	timer := time.NewTimer(serverShutdownGrace)
	defer timer.Stop()
	select {
	case <-stopped:
	case <-timer.C:
		logger.Warn("graceful stop timed out; forcing stop", "grace", serverShutdownGrace)
		grpcServer.Stop()
		<-stopped
	}
}

// newLogger 构建写入本地日志文件的 logger；终端不输出任何日志。
func newLogger(cfg *config.ServerConfig) (*logx.FileLogger, error) {
	return logx.New(logx.Options{
		Level:      cfg.Log.Level,
		Format:     logx.Format(cfg.Log.Format),
		Dir:        cfg.Log.Dir,
		Filename:   "rclaude-server.log",
		MaxSizeMB:  cfg.Log.MaxSizeMB,
		MaxBackups: cfg.Log.MaxBackups,
		MaxAgeDays: cfg.Log.MaxAgeDays,
	})
}

func warnClose(logger logx.Logger, msg string, err error, ignored error) {
	if err != nil && !errors.Is(err, ignored) {
		logger.Warn(msg, "err", err)
	}
}

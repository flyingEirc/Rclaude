package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"

	"github.com/spf13/cobra"
	"google.golang.org/grpc"

	remotefsv1 "flyingEirc/Rclaude/api/proto/remotefs/v1"
	"flyingEirc/Rclaude/pkg/auth"
	"flyingEirc/Rclaude/pkg/config"
	"flyingEirc/Rclaude/pkg/fusefs"
	"flyingEirc/Rclaude/pkg/logx"
	"flyingEirc/Rclaude/pkg/session"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
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
	return runPreparedServer(ctx, cfg, logger, manager, service, verifier)
}

func runPreparedServer(
	ctx context.Context,
	cfg *config.ServerConfig,
	logger *slog.Logger,
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

	listener, grpcServer, err := newGRPCServer(cfg, verifier, service)
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
) (*config.ServerConfig, *slog.Logger, *session.Manager, *session.Service, auth.Verifier, error) {
	cfg, err := config.LoadServer(configPath)
	if err != nil {
		return nil, nil, nil, nil, nil, err
	}
	logger, err := newLogger(cfg)
	if err != nil {
		return nil, nil, nil, nil, nil, err
	}

	manager := session.NewManager(session.ManagerOptions{
		RequestTimeout: cfg.RequestTimeout,
		CacheMaxBytes:  cfg.Cache.MaxBytes,
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
	logger *slog.Logger,
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
	verifier auth.Verifier,
	service *session.Service,
) (net.Listener, *grpc.Server, error) {
	listener, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		return nil, nil, fmt.Errorf("server: listen %q: %w", cfg.Listen, err)
	}
	grpcServer := grpc.NewServer(
		grpc.StreamInterceptor(auth.StreamServerInterceptor(verifier)),
	)
	remotefsv1.RegisterRemoteFSServer(grpcServer, service)
	return listener, grpcServer, nil
}

func serveUntilDone(
	ctx context.Context,
	logger *slog.Logger,
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
		grpcServer.GracefulStop()
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

func newLogger(cfg *config.ServerConfig) (*slog.Logger, error) {
	level := slog.LevelInfo
	if cfg != nil && cfg.Log.Level != "" {
		if err := level.UnmarshalText([]byte(cfg.Log.Level)); err != nil {
			return nil, fmt.Errorf("server: parse log level %q: %w", cfg.Log.Level, err)
		}
	}
	format := logx.FormatJSON
	if cfg != nil && cfg.Log.Format != "" {
		format = logx.Format(cfg.Log.Format)
	}
	return logx.New(logx.Options{
		Level:  level,
		Format: format,
	}), nil
}

func warnClose(logger *slog.Logger, msg string, err error, ignored error) {
	if err != nil && !errors.Is(err, ignored) {
		logger.Warn(msg, "err", err)
	}
}

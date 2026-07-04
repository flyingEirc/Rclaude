package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"

	"github.com/spf13/cobra"

	"flyingEirc/Rclaude/pkg/config"
	"flyingEirc/Rclaude/pkg/logx"
	"flyingEirc/Rclaude/pkg/syncer"
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
		Use:           "rclaude-daemon",
		Short:         "Run the local Rclaude daemon",
		Args:          cobra.NoArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runDaemon(cmd.Context(), configPath)
		},
	}
	cmd.Flags().StringVar(&configPath, "config", "", "Path to the daemon YAML config")
	if err := cmd.MarkFlagRequired("config"); err != nil {
		return nil, fmt.Errorf("client: mark config flag required: %w", err)
	}

	return cmd, nil
}

func runDaemon(ctx context.Context, configPath string) error {
	cfg, err := config.LoadDaemon(configPath)
	if err != nil {
		return err
	}
	logger, err := newLogger(cfg)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := logger.Close(); closeErr != nil {
			_, _ = fmt.Fprintln(os.Stderr, closeErr)
		}
	}()
	return syncer.Run(logx.WithContext(ctx, logger), syncer.RunOptions{
		Config: cfg,
		Logger: logger,
	})
}

// newLogger 构建写入本地日志文件的 logger；终端不输出任何日志。
func newLogger(cfg *config.DaemonConfig) (*logx.FileLogger, error) {
	return logx.New(logx.Options{
		Level:      cfg.Log.Level,
		Format:     logx.Format(cfg.Log.Format),
		Dir:        cfg.Log.Dir,
		Filename:   "rclaude-daemon.log",
		MaxSizeMB:  cfg.Log.MaxSizeMB,
		MaxBackups: cfg.Log.MaxBackups,
		MaxAgeDays: cfg.Log.MaxAgeDays,
	})
}

// Command rclaude is the unified local entry: it starts the daemon and the
// remote claude PTY attach concurrently, coordinating startup failures over
// an in-process event bus (pkg/startup). The terminal only ever shows one
// status line per component; everything else goes to the log file.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"

	"github.com/asaskevich/EventBus"
	"github.com/spf13/cobra"

	"flyingEirc/Rclaude/pkg/config"
	"flyingEirc/Rclaude/pkg/logx"
	"flyingEirc/Rclaude/pkg/ptyattach"
	"flyingEirc/Rclaude/pkg/startup"
	"flyingEirc/Rclaude/pkg/syncer"
)

const logFilename = "rclaude.log"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	cmd, err := newRootCommand()
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := cmd.ExecuteContext(ctx); err != nil {
		exitOnError(err)
	}
}

// exitOnError maps the command error to an exit code. Startup/session
// details are already on the terminal status lines or in the log file, so
// only bootstrap errors (config/logger) are printed.
func exitOnError(err error) {
	var exitErr *ptyattach.ExitError
	if errors.As(err, &exitErr) {
		os.Exit(exitErr.Code)
	}
	if errors.Is(err, errStartupFailed) || errors.Is(err, errRunFailed) {
		os.Exit(1)
	}
	_, _ = fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}

func newRootCommand() (*cobra.Command, error) {
	var configPath string

	cmd := &cobra.Command{
		Use:           "rclaude",
		Short:         "Start the local daemon and attach to the remote claude PTY session",
		Args:          cobra.NoArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runUnified(cmd.Context(), configPath)
		},
	}
	cmd.Flags().StringVar(&configPath, "config", "", "Path to the daemon YAML config")
	if err := cmd.MarkFlagRequired("config"); err != nil {
		return nil, fmt.Errorf("rclaude: mark config flag required: %w", err)
	}

	return cmd, nil
}

func runUnified(ctx context.Context, configPath string) error {
	cfg, err := config.LoadDaemon(configPath)
	if err != nil {
		return err
	}
	logger, err := logx.New(logx.Options{
		Level:      cfg.Log.Level,
		Format:     logx.Format(cfg.Log.Format),
		Dir:        cfg.Log.Dir,
		Filename:   logFilename,
		MaxSizeMB:  cfg.Log.MaxSizeMB,
		MaxBackups: cfg.Log.MaxBackups,
		MaxAgeDays: cfg.Log.MaxAgeDays,
	})
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := logger.Close(); closeErr != nil {
			_, _ = fmt.Fprintln(os.Stderr, closeErr)
		}
	}()

	return runCoordinated(logx.WithContext(ctx, logger), cfg, configPath, logger)
}

func runCoordinated(
	ctx context.Context,
	cfg *config.DaemonConfig,
	configPath string,
	logger logx.Logger,
) error {
	coord, err := startup.New(startup.Options{
		Bus:        EventBus.New(),
		Logger:     logger,
		MaxRetries: cfg.Startup.MaxRetries,
		RetryDelay: cfg.Startup.RetryDelay,
	}, daemonSpec(cfg, logger), ptySpec(configPath))
	if err != nil {
		return err
	}

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	events, err := coord.Run(runCtx)
	if err != nil {
		return err
	}
	return supervise(events, cancel, logger, os.Stdout)
}

func daemonSpec(cfg *config.DaemonConfig, logger logx.Logger) startup.Spec {
	return startup.Spec{
		Name: startup.ComponentDaemon,
		Run: func(ctx context.Context, ready func()) error {
			return syncer.Run(ctx, syncer.RunOptions{
				Config:          cfg,
				Logger:          logger,
				OnReady:         ready,
				StartupFailFast: true,
			})
		},
	}
}

func ptySpec(configPath string) startup.Spec {
	return startup.Spec{
		Name: startup.ComponentPTY,
		Run: func(ctx context.Context, ready func()) error {
			return ptyattach.Run(ctx, ptyattach.Options{
				ConfigPath: configPath,
				OnAttached: ready,
			})
		},
	}
}

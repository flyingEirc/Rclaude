// Command rclaude is the unified local entry: it starts the daemon and the
// remote agent PTY attach concurrently, coordinating startup failures over
// an in-process event bus (pkg/startup). The agent program the remote session
// runs is declared on the command line (-g/--agent). The terminal only ever
// shows one status line per component; everything else goes to the log file.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/asaskevich/EventBus"
	"github.com/spf13/cobra"

	"flyingEirc/Rclaude/pkg/config"
	"flyingEirc/Rclaude/pkg/logx"
	"flyingEirc/Rclaude/pkg/ptyattach"
	"flyingEirc/Rclaude/pkg/startup"
	"flyingEirc/Rclaude/pkg/syncer"
)

const (
	logFilename = "rclaude.log"
	// shutdownGraceTimeout bounds how long a graceful shutdown may take after a
	// termination signal before the process forces an exit.
	shutdownGraceTimeout = 10 * time.Second
)

func main() {
	cmd, err := newRootCommand()
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := cmd.ExecuteContext(context.Background()); err != nil {
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
	var agent string

	cmd := &cobra.Command{
		Use:           "rclaude -g <agent> -c <config>",
		Short:         "Start the local daemon and attach to the remote agent PTY session",
		Args:          cobra.NoArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if strings.TrimSpace(agent) == "" {
				return errors.New("rclaude: --agent must not be blank (e.g. -g claude, -g codex)")
			}
			return runUnified(cmd.Context(), configPath, agent)
		},
	}
	cmd.Flags().StringVarP(&configPath, "config", "c", "", "Path to the daemon YAML config")
	cmd.Flags().StringVarP(&agent, "agent", "g", "",
		"Agent program the remote session runs: a bare name resolved via the server's PATH (e.g. claude, codex) or an absolute path on the server")
	for _, flag := range []string{"config", "agent"} {
		if err := cmd.MarkFlagRequired(flag); err != nil {
			return nil, fmt.Errorf("rclaude: mark %s flag required: %w", flag, err)
		}
	}

	return cmd, nil
}

func runUnified(ctx context.Context, configPath string, agent string) error {
	cfg, err := config.LoadDaemon(configPath)
	if err != nil {
		return err
	}
	// 启动目录即工作区根：rclaude 必须在项目根目录运行，该目录名成为服务端
	// /workspace/{userid}/{项目名}/ 中的项目名。
	workspaceRoot, err := syncer.ResolveWorkspaceRoot()
	if err != nil {
		return fmt.Errorf("%w\nrun rclaude from your project root directory", err)
	}
	// 日志策略写死：全等级（debug）落地、JSON 格式，目录与轮转用 logx 默认。
	logger, err := logx.New(logx.Options{
		Level:    "debug",
		Format:   logx.FormatJSON,
		Filename: logFilename,
	})
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := logger.Close(); closeErr != nil {
			_, _ = fmt.Fprintln(os.Stderr, closeErr)
		}
	}()

	ctx, cancel := context.WithCancel(logx.WithContext(ctx, logger))
	defer cancel()
	stopWatch := watchShutdownSignals(logger, cancel)
	defer stopWatch()

	return runCoordinated(ctx, cfg, workspaceRoot, configPath, agent, logger)
}

// shutdownSignals lists the signals that trigger a graceful shutdown: SIGINT
// (Ctrl-C), SIGTERM (kill), and SIGHUP (controlling terminal/window closed).
// Catching SIGHUP is what makes closing the whole terminal drain and stop the
// daemon and PTY cleanly, instead of the process being killed with no cleanup
// and no exit logs.
func shutdownSignals() []os.Signal {
	return []os.Signal{os.Interrupt, syscall.SIGTERM, syscall.SIGHUP}
}

// watchShutdownSignals runs a dedicated goroutine that turns termination
// signals into an ordered, logged shutdown: the first signal cancels the run
// context so the daemon can finish in-flight file operations and the PTY can
// flush before both exit; a second signal, or a grace-period timeout, forces
// an immediate exit. It returns a stop function that detaches the handler once
// the run finishes on its own.
func watchShutdownSignals(logger logx.Logger, cancel context.CancelFunc) func() {
	sigCh := make(chan os.Signal, 2)
	signal.Notify(sigCh, shutdownSignals()...)
	done := make(chan struct{})

	go func() {
		defer signal.Stop(sigCh)
		select {
		case <-done:
			return
		case sig := <-sigCh:
			logger.Info("shutdown signal received, draining then stopping", "signal", sig.String())
			cancel()
		}
		forceExitOnSecondSignal(logger, sigCh, done)
	}()

	return func() { close(done) }
}

// forceExitOnSecondSignal waits out the graceful shutdown, forcing an exit if a
// second signal arrives or the grace period elapses before the run finishes.
func forceExitOnSecondSignal(logger logx.Logger, sigCh <-chan os.Signal, done <-chan struct{}) {
	timer := time.NewTimer(shutdownGraceTimeout)
	defer timer.Stop()

	select {
	case <-done:
	case sig := <-sigCh:
		logger.Warn("second shutdown signal, forcing exit", "signal", sig.String())
		os.Exit(130)
	case <-timer.C:
		logger.Warn("graceful shutdown timed out, forcing exit", "timeout", shutdownGraceTimeout.String())
		os.Exit(130)
	}
}

func runCoordinated(
	ctx context.Context,
	cfg *config.DaemonConfig,
	workspaceRoot string,
	configPath string,
	agent string,
	logger logx.Logger,
) error {
	coord, err := startup.New(startup.Options{
		Bus:        EventBus.New(),
		Logger:     logger,
		MaxRetries: config.DefaultStartupMaxRetries,
		RetryDelay: config.DefaultStartupRetryDelay,
	}, daemonSpec(cfg, workspaceRoot, logger), ptySpec(configPath, agent))
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

func daemonSpec(cfg *config.DaemonConfig, workspaceRoot string, logger logx.Logger) startup.Spec {
	return startup.Spec{
		Name: startup.ComponentDaemon,
		Run: func(ctx context.Context, ready func()) error {
			return syncer.Run(ctx, syncer.RunOptions{
				Config:          cfg,
				WorkspaceRoot:   workspaceRoot,
				Logger:          logger,
				OnReady:         ready,
				StartupFailFast: true,
			})
		},
	}
}

func ptySpec(configPath string, agent string) startup.Spec {
	return startup.Spec{
		Name: startup.ComponentPTY,
		// The PTY can only attach once the daemon has registered with the
		// server, so gate its first attempt on the daemon being up instead of
		// racing it and failing the first attach with "daemon not connected".
		DependsOn: []startup.Component{startup.ComponentDaemon},
		Run: func(ctx context.Context, ready func()) error {
			return ptyattach.Run(ctx, ptyattach.Options{
				ConfigPath: configPath,
				Agent:      agent,
				OnAttached: ready,
			})
		},
	}
}

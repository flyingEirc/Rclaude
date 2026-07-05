package main

import (
	"errors"
	"fmt"
	"io"

	"flyingEirc/Rclaude/pkg/logx"
	"flyingEirc/Rclaude/pkg/ptyattach"
	"flyingEirc/Rclaude/pkg/startup"
)

var (
	// errStartupFailed reports that at least one component never started;
	// the status lines already told the user, so the exit stays silent.
	errStartupFailed = errors.New("rclaude: startup failed")
	// errRunFailed reports a fatal post-startup component failure; details
	// are in the log file.
	errRunFailed = errors.New("rclaude: component failed after startup")
)

// supervisor owns the terminal print contract and the final exit decision.
type supervisor struct {
	out         io.Writer
	logger      logx.Logger
	cancel      func()
	started     map[startup.Component]bool
	failed      map[startup.Component]bool
	ptyExit     error
	ptyDone     bool
	daemonFatal bool
}

// supervise consumes coordinator events until the channel closes and turns
// them into the terminal status lines plus the process exit error.
func supervise(
	events <-chan startup.Event,
	cancel func(),
	logger logx.Logger,
	out io.Writer,
) error {
	s := &supervisor{
		out:     out,
		logger:  logger,
		cancel:  cancel,
		started: make(map[startup.Component]bool),
		failed:  make(map[startup.Component]bool),
	}
	for event := range events {
		s.handle(event)
	}
	return s.finish()
}

func (s *supervisor) handle(event startup.Event) {
	switch event.Kind {
	case startup.KindStarted:
		s.started[event.Component] = true
		s.printStatus(event.Component, true)
	case startup.KindGaveUp:
		s.markFailed(event.Component)
	case startup.KindAborted:
		s.cancel()
	case startup.KindExited:
		s.onExited(event)
	}
}

func (s *supervisor) onExited(event startup.Event) {
	if event.Component == startup.ComponentPTY {
		s.ptyDone = true
		s.ptyExit = event.Err
		s.logPTYSessionEnded(event.Err)
		s.cancel()
		return
	}
	if event.Err != nil {
		s.daemonFatal = true
		s.logger.Error("daemon exited unexpectedly", "err", event.Err)
		s.cancel()
		return
	}
	// Graceful daemon stop (e.g. after a shutdown signal): record it so the
	// user sees the daemon winding down, not just the PTY.
	s.logger.Info("daemon stopped")
}

// logPTYSessionEnded logs the PTY exit, keeping a normal termination (clean
// exit or a signal-driven quiet exit) at info and only surfacing real session
// errors at error level.
func (s *supervisor) logPTYSessionEnded(err error) {
	var exitErr *ptyattach.ExitError
	if err == nil || (errors.As(err, &exitErr) && exitErr.Quiet) {
		s.logger.Info("pty session ended")
		return
	}
	s.logger.Error("pty session ended", "err", err)
}

func (s *supervisor) finish() error {
	for _, component := range []startup.Component{startup.ComponentDaemon, startup.ComponentPTY} {
		if !s.started[component] {
			s.markFailed(component)
		}
	}
	switch {
	case len(s.failed) > 0:
		return errStartupFailed
	case s.daemonFatal:
		return errRunFailed
	case s.ptyDone:
		return s.ptyExitError()
	default:
		return nil
	}
}

// ptyExitError keeps *ptyattach.ExitError (exit-code mapping) as is and
// collapses any other session error to a silent failure; details were
// already logged by onExited.
func (s *supervisor) ptyExitError() error {
	if s.ptyExit == nil {
		return nil
	}
	var exitErr *ptyattach.ExitError
	if errors.As(s.ptyExit, &exitErr) {
		return exitErr
	}
	return errRunFailed
}

func (s *supervisor) markFailed(component startup.Component) {
	if s.failed[component] || s.started[component] {
		return
	}
	s.failed[component] = true
	s.printStatus(component, false)
}

// printStatus writes the only lines this binary may show on the terminal.
// \r\n keeps the output readable when the terminal is already in raw mode.
func (s *supervisor) printStatus(component startup.Component, ok bool) {
	state := "started"
	if !ok {
		state = "start failed"
	}
	if _, err := fmt.Fprintf(s.out, "%s %s\r\n", component, state); err != nil {
		s.logger.Warn("write status line", "component", component, "err", err)
	}
}

package main

import (
	"bytes"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"flyingEirc/Rclaude/pkg/logx"
	"flyingEirc/Rclaude/pkg/ptyattach"
	"flyingEirc/Rclaude/pkg/startup"
)

func runSupervise(t *testing.T, events []startup.Event) (out string, canceled bool, err error) {
	t.Helper()
	ch := make(chan startup.Event, len(events))
	for _, e := range events {
		ch <- e
	}
	close(ch)

	var buf bytes.Buffer
	err = supervise(ch, func() { canceled = true }, logx.Nop(), &buf)
	return buf.String(), canceled, err
}

func TestSuperviseHappySession(t *testing.T) {
	out, canceled, err := runSupervise(t, []startup.Event{
		{Component: startup.ComponentDaemon, Kind: startup.KindStarted, Attempt: 1},
		{Component: startup.ComponentPTY, Kind: startup.KindStarted, Attempt: 1},
		{Component: startup.ComponentPTY, Kind: startup.KindExited},
		{Component: startup.ComponentDaemon, Kind: startup.KindExited},
	})
	require.NoError(t, err)
	assert.Equal(t, "daemon started\r\npty started\r\n", out)
	assert.True(t, canceled, "pty session end must stop the daemon")
}

func TestSupervisePTYGaveUp(t *testing.T) {
	out, _, err := runSupervise(t, []startup.Event{
		{Component: startup.ComponentDaemon, Kind: startup.KindStarted, Attempt: 1},
		{Component: startup.ComponentPTY, Kind: startup.KindGaveUp, Attempt: 4, Err: errors.New("boom")},
		{Component: startup.ComponentPTY, Kind: startup.KindAborted, Err: errors.New("boom")},
		{Component: startup.ComponentDaemon, Kind: startup.KindExited},
	})
	require.ErrorIs(t, err, errStartupFailed)
	assert.Equal(t, "daemon started\r\npty start failed\r\n", out)
}

func TestSuperviseBothFailedAbort(t *testing.T) {
	out, _, err := runSupervise(t, []startup.Event{
		{Component: startup.ComponentPTY, Kind: startup.KindAborted, Err: errors.New("all down")},
	})
	require.ErrorIs(t, err, errStartupFailed)
	assert.Equal(t, "daemon start failed\r\npty start failed\r\n", out)
}

func TestSupervisePropagatesPTYExitCode(t *testing.T) {
	wantErr := &ptyattach.ExitError{Code: 5, Message: "rate limited"}
	_, _, err := runSupervise(t, []startup.Event{
		{Component: startup.ComponentDaemon, Kind: startup.KindStarted, Attempt: 1},
		{Component: startup.ComponentPTY, Kind: startup.KindStarted, Attempt: 1},
		{Component: startup.ComponentPTY, Kind: startup.KindExited, Err: wantErr},
		{Component: startup.ComponentDaemon, Kind: startup.KindExited},
	})
	var exitErr *ptyattach.ExitError
	require.ErrorAs(t, err, &exitErr)
	assert.Equal(t, 5, exitErr.Code)
}

func TestSuperviseDaemonFatalAfterStart(t *testing.T) {
	out, canceled, err := runSupervise(t, []startup.Event{
		{Component: startup.ComponentDaemon, Kind: startup.KindStarted, Attempt: 1},
		{Component: startup.ComponentPTY, Kind: startup.KindStarted, Attempt: 1},
		{Component: startup.ComponentDaemon, Kind: startup.KindExited, Err: errors.New("db gone")},
		{Component: startup.ComponentPTY, Kind: startup.KindExited, Err: &ptyattach.ExitError{Code: 130, Quiet: true}},
	})
	require.ErrorIs(t, err, errRunFailed)
	assert.Equal(t, "daemon started\r\npty started\r\n", out)
	assert.True(t, canceled)
}

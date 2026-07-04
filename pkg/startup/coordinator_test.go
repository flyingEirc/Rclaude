package startup_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/asaskevich/EventBus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"flyingEirc/Rclaude/pkg/logx"
	"flyingEirc/Rclaude/pkg/startup"
)

const eventTimeout = 5 * time.Second

// recordLogger is a race-safe logx.Logger capturing rendered entries.
type recordLogger struct {
	mu      sync.Mutex
	entries []string
}

func (l *recordLogger) record(level, msg string, kv ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.entries = append(l.entries, level+" "+msg+" "+fmt.Sprintln(kv...))
}

func (l *recordLogger) Debug(msg string, kv ...any) { l.record("debug", msg, kv...) }
func (l *recordLogger) Info(msg string, kv ...any)  { l.record("info", msg, kv...) }
func (l *recordLogger) Warn(msg string, kv ...any)  { l.record("warn", msg, kv...) }
func (l *recordLogger) Error(msg string, kv ...any) { l.record("error", msg, kv...) }
func (l *recordLogger) With(...any) logx.Logger     { return l }

func (l *recordLogger) contains(sub string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, entry := range l.entries {
		if strings.Contains(entry, sub) {
			return true
		}
	}
	return false
}

// failureObserver counts Failure events crossing the bus.
type failureObserver struct {
	count atomic.Int32
}

func observeFailures(t *testing.T, bus EventBus.Bus) *failureObserver {
	t.Helper()
	obs := &failureObserver{}
	require.NoError(t, bus.Subscribe(startup.TopicStartupFailed, func(startup.Failure) {
		obs.count.Add(1)
	}))
	return obs
}

// blockingSpec becomes ready immediately and runs until ctx is canceled.
func blockingSpec(name startup.Component) startup.Spec {
	return startup.Spec{
		Name: name,
		Run: func(ctx context.Context, ready func()) error {
			ready()
			<-ctx.Done()
			return nil
		},
	}
}

// failNTimesSpec fails the first n attempts, then behaves like blockingSpec.
func failNTimesSpec(name startup.Component, n int, calls *atomic.Int32) startup.Spec {
	return startup.Spec{
		Name: name,
		Run: func(ctx context.Context, ready func()) error {
			if calls.Add(1) <= int32(n) {
				return errors.New("boom")
			}
			ready()
			<-ctx.Done()
			return nil
		},
	}
}

func collectUntilClosed(t *testing.T, events <-chan startup.Event) []startup.Event {
	t.Helper()
	var out []startup.Event
	deadline := time.After(eventTimeout)
	for {
		select {
		case e, ok := <-events:
			if !ok {
				return out
			}
			out = append(out, e)
		case <-deadline:
			t.Fatalf("timed out collecting events, got %v", out)
		}
	}
}

func waitEvent(t *testing.T, events <-chan startup.Event, want func(startup.Event) bool) startup.Event {
	t.Helper()
	deadline := time.After(eventTimeout)
	for {
		select {
		case e, ok := <-events:
			require.True(t, ok, "event channel closed while waiting")
			if want(e) {
				return e
			}
		case <-deadline:
			t.Fatal("timed out waiting for event")
		}
	}
}

func kindCount(events []startup.Event, kind startup.EventKind) int {
	n := 0
	for _, e := range events {
		if e.Kind == kind {
			n++
		}
	}
	return n
}

func newCoordinator(t *testing.T, opts startup.Options, specs ...startup.Spec) *startup.Coordinator {
	t.Helper()
	coord, err := startup.New(opts, specs...)
	require.NoError(t, err)
	return coord
}

func TestBothComponentsStart(t *testing.T) {
	bus := EventBus.New()
	logger := &recordLogger{}
	obs := observeFailures(t, bus)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	coord := newCoordinator(t,
		startup.Options{Bus: bus, Logger: logger, MaxRetries: 3},
		blockingSpec(startup.ComponentDaemon),
		blockingSpec(startup.ComponentPTY),
	)
	events, err := coord.Run(ctx)
	require.NoError(t, err)

	// Started 事件的先后顺序不定，按集合收齐两个组件。
	seen := map[startup.Component]bool{}
	for len(seen) < 2 {
		e := waitEvent(t, events, func(e startup.Event) bool {
			return e.Kind == startup.KindStarted
		})
		seen[e.Component] = true
	}
	assert.True(t, seen[startup.ComponentDaemon])
	assert.True(t, seen[startup.ComponentPTY])

	cancel()
	rest := collectUntilClosed(t, events)
	assert.Equal(t, 2, kindCount(rest, startup.KindExited))
	assert.Equal(t, int32(0), obs.count.Load(), "success must not publish onto the bus")
}

func TestPeerSuccessDrivesRetryUntilSuccess(t *testing.T) {
	bus := EventBus.New()
	logger := &recordLogger{}
	obs := observeFailures(t, bus)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var ptyCalls atomic.Int32
	coord := newCoordinator(t,
		startup.Options{Bus: bus, Logger: logger, MaxRetries: 3},
		blockingSpec(startup.ComponentDaemon),
		failNTimesSpec(startup.ComponentPTY, 2, &ptyCalls),
	)
	events, err := coord.Run(ctx)
	require.NoError(t, err)

	started := waitEvent(t, events, func(e startup.Event) bool {
		return e.Kind == startup.KindStarted && e.Component == startup.ComponentPTY
	})
	assert.Equal(t, 3, started.Attempt)
	assert.Equal(t, int32(3), ptyCalls.Load())
	assert.Equal(t, int32(2), obs.count.Load(), "each failed attempt goes back onto the bus")
	assert.True(t, logger.contains("startup retry notified"))
	assert.True(t, logger.contains("startup retry received"))
	assert.True(t, logger.contains("startup attempt failed"))

	cancel()
	rest := collectUntilClosed(t, events)
	assert.Equal(t, 0, kindCount(rest, startup.KindAborted))
}

func TestRetriesExhaustedGiveUpAndAbort(t *testing.T) {
	bus := EventBus.New()
	logger := &recordLogger{}
	obs := observeFailures(t, bus)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var ptyCalls atomic.Int32
	coord := newCoordinator(t,
		startup.Options{Bus: bus, Logger: logger, MaxRetries: 2},
		blockingSpec(startup.ComponentDaemon),
		failNTimesSpec(startup.ComponentPTY, 99, &ptyCalls),
	)
	events, err := coord.Run(ctx)
	require.NoError(t, err)

	all := collectUntilClosed(t, events)
	assert.Equal(t, 1, kindCount(all, startup.KindGaveUp))
	assert.Equal(t, 1, kindCount(all, startup.KindAborted))
	assert.Equal(t, 1, kindCount(all, startup.KindExited), "started daemon exits on abort")
	assert.Equal(t, int32(3), ptyCalls.Load(), "initial attempt + 2 retries")
	assert.Equal(t, int32(3), obs.count.Load())
	assert.True(t, logger.contains("startup gave up"))
	assert.True(t, logger.contains("startup aborted"))
}

func TestBothFailedAbortsWithoutRetry(t *testing.T) {
	bus := EventBus.New()
	logger := &recordLogger{}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var daemonCalls, ptyCalls atomic.Int32
	coord := newCoordinator(t,
		startup.Options{Bus: bus, Logger: logger, MaxRetries: 3},
		failNTimesSpec(startup.ComponentDaemon, 99, &daemonCalls),
		failNTimesSpec(startup.ComponentPTY, 99, &ptyCalls),
	)
	events, err := coord.Run(ctx)
	require.NoError(t, err)

	all := collectUntilClosed(t, events)
	assert.Equal(t, 1, kindCount(all, startup.KindAborted))
	assert.Equal(t, 0, kindCount(all, startup.KindStarted))
	assert.Equal(t, int32(1), daemonCalls.Load(), "no retry without a started peer")
	assert.Equal(t, int32(1), ptyCalls.Load(), "no retry without a started peer")
	assert.False(t, logger.contains("startup retry notified"))
}

func TestRetryDelayIsApplied(t *testing.T) {
	bus := EventBus.New()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var mu sync.Mutex
	var attemptTimes []time.Time
	spec := startup.Spec{
		Name: startup.ComponentPTY,
		Run: func(ctx context.Context, ready func()) error {
			mu.Lock()
			attemptTimes = append(attemptTimes, time.Now())
			calls := len(attemptTimes)
			mu.Unlock()
			if calls == 1 {
				return errors.New("boom")
			}
			ready()
			<-ctx.Done()
			return nil
		},
	}

	coord := newCoordinator(t,
		startup.Options{Bus: bus, Logger: logx.Nop(), MaxRetries: 1, RetryDelay: 100 * time.Millisecond},
		blockingSpec(startup.ComponentDaemon),
		spec,
	)
	events, err := coord.Run(ctx)
	require.NoError(t, err)

	waitEvent(t, events, func(e startup.Event) bool {
		return e.Kind == startup.KindStarted && e.Component == startup.ComponentPTY
	})
	mu.Lock()
	require.Len(t, attemptTimes, 2)
	gap := attemptTimes[1].Sub(attemptTimes[0])
	mu.Unlock()
	assert.GreaterOrEqual(t, gap, 100*time.Millisecond)
	cancel()
	collectUntilClosed(t, events)
}

func TestReadyThenImmediateExitCountsAsStarted(t *testing.T) {
	bus := EventBus.New()
	obs := observeFailures(t, bus)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	exitErr := errors.New("session over")
	spec := startup.Spec{
		Name: startup.ComponentPTY,
		Run: func(_ context.Context, ready func()) error {
			ready()
			return exitErr
		},
	}

	coord := newCoordinator(t,
		startup.Options{Bus: bus, Logger: logx.Nop(), MaxRetries: 3},
		blockingSpec(startup.ComponentDaemon),
		spec,
	)
	events, err := coord.Run(ctx)
	require.NoError(t, err)

	exited := waitEvent(t, events, func(e startup.Event) bool {
		return e.Kind == startup.KindExited && e.Component == startup.ComponentPTY
	})
	require.ErrorIs(t, exited.Err, exitErr)
	assert.Equal(t, int32(0), obs.count.Load())
	cancel()
	collectUntilClosed(t, events)
}

func TestNewValidation(t *testing.T) {
	bus := EventBus.New()
	_, err := startup.New(startup.Options{}, blockingSpec("a"), blockingSpec("b"))
	assert.ErrorIs(t, err, startup.ErrNilBus)

	_, err = startup.New(startup.Options{Bus: bus}, blockingSpec("a"))
	assert.ErrorIs(t, err, startup.ErrNoSpecs)

	_, err = startup.New(startup.Options{Bus: bus}, blockingSpec("a"), blockingSpec("a"))
	assert.ErrorIs(t, err, startup.ErrDuplicateComponent)
}

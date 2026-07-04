package startup

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"flyingEirc/Rclaude/pkg/logx"
)

var (
	// ErrNilBus indicates Options.Bus was not provided.
	ErrNilBus = errors.New("startup: nil bus")
	// ErrNoSpecs indicates Run was configured without components.
	ErrNoSpecs = errors.New("startup: at least two components are required")
	// ErrDuplicateComponent indicates two specs share the same name.
	ErrDuplicateComponent = errors.New("startup: duplicate component name")
	// errExitedBeforeReady marks a Run that returned nil before signaling
	// readiness; it still counts as a failed startup attempt.
	errExitedBeforeReady = errors.New("startup: component exited before becoming ready")
)

const (
	failBufferSize  = 16
	retryBufferSize = 8
	eventBufferSize = 16
)

// Spec describes one managed component.
type Spec struct {
	Name Component
	// Run performs the component's work. It must call ready exactly once as
	// soon as startup succeeded and keep running afterwards. Returning
	// before ready was called counts as a failed startup attempt.
	Run func(ctx context.Context, ready func()) error
}

// Options configures a Coordinator.
type Options struct {
	Bus Bus
	// Logger records every bus event; nil falls back to the ctx logger.
	Logger logx.Logger
	// MaxRetries is the number of retries beyond the initial attempt.
	MaxRetries int
	// RetryDelay is the pause between a retry notification and the attempt.
	RetryDelay time.Duration
}

type phase uint8

const (
	phasePending phase = iota
	phaseStarted
	phaseWaitingRetry
	phaseGaveUp
)

type componentState struct {
	phase       phase
	lastFailure Failure
}

// Coordinator drives the startup protocol for a fixed set of components.
type Coordinator struct {
	opts        Options
	specs       []Spec
	logger      logx.Logger
	maxAttempts int

	mu     sync.Mutex
	states map[Component]*componentState

	failCh   chan Failure
	retryChs map[Component]chan RetryRequest
	events   chan Event

	cancel    context.CancelFunc
	abortOnce sync.Once
}

// New validates the specs and builds a Coordinator; Run may be called once.
func New(opts Options, specs ...Spec) (*Coordinator, error) {
	if opts.Bus == nil {
		return nil, ErrNilBus
	}
	if len(specs) < 2 {
		return nil, ErrNoSpecs
	}
	if opts.MaxRetries < 0 {
		opts.MaxRetries = 0
	}

	states := make(map[Component]*componentState, len(specs))
	retryChs := make(map[Component]chan RetryRequest, len(specs))
	for _, spec := range specs {
		if _, dup := states[spec.Name]; dup {
			return nil, fmt.Errorf("%w: %s", ErrDuplicateComponent, spec.Name)
		}
		states[spec.Name] = &componentState{}
		retryChs[spec.Name] = make(chan RetryRequest, retryBufferSize)
	}

	return &Coordinator{
		opts:        opts,
		specs:       specs,
		logger:      opts.Logger,
		maxAttempts: opts.MaxRetries + 1,
		states:      states,
		failCh:      make(chan Failure, failBufferSize),
		retryChs:    retryChs,
		events:      make(chan Event, eventBufferSize),
	}, nil
}

// Run subscribes the protocol handlers and launches every component. The
// returned channel delivers Started/GaveUp/Aborted/Exited events and closes
// once all components have finished.
func (c *Coordinator) Run(ctx context.Context) (<-chan Event, error) {
	if c.logger == nil {
		c.logger = logx.FromContext(ctx)
	}

	runCtx, cancel := context.WithCancel(ctx)
	c.cancel = cancel

	onFailed, onRetry := c.busHandlers()
	if err := c.opts.Bus.Subscribe(TopicStartupFailed, onFailed); err != nil {
		cancel()
		return nil, fmt.Errorf("startup: subscribe %s: %w", TopicStartupFailed, err)
	}
	if err := c.opts.Bus.Subscribe(TopicStartupRetry, onRetry); err != nil {
		cancel()
		return nil, fmt.Errorf("startup: subscribe %s: %w", TopicStartupRetry, err)
	}

	coordDone := make(chan struct{})
	go func() {
		defer close(coordDone)
		c.coordinate(runCtx)
	}()

	var wg sync.WaitGroup
	for _, spec := range c.specs {
		wg.Add(1)
		go func(spec Spec) {
			defer wg.Done()
			c.runComponent(runCtx, spec)
		}(spec)
	}

	go func() {
		wg.Wait()
		cancel()
		<-coordDone
		c.unsubscribe(onFailed, onRetry)
		close(c.events)
	}()

	return c.events, nil
}

// busHandlers returns the synchronous bus subscribers. They only forward
// into channels: EventBus.Publish runs them while holding the bus lock, so
// they must neither block nor publish.
func (c *Coordinator) busHandlers() (func(Failure), func(RetryRequest)) {
	onFailed := func(f Failure) {
		select {
		case c.failCh <- f:
		default:
			c.logger.Error("startup failure event dropped: queue full",
				"component", f.Component, "attempt", f.Attempt)
		}
	}
	onRetry := func(r RetryRequest) {
		ch, ok := c.retryChs[r.Component]
		if !ok {
			c.logger.Error("startup retry event for unknown component", "component", r.Component)
			return
		}
		select {
		case ch <- r:
		default:
			c.logger.Error("startup retry event dropped: queue full",
				"component", r.Component, "attempt", r.Attempt)
		}
	}
	return onFailed, onRetry
}

func (c *Coordinator) unsubscribe(onFailed func(Failure), onRetry func(RetryRequest)) {
	if err := c.opts.Bus.Unsubscribe(TopicStartupFailed, onFailed); err != nil {
		c.logger.Warn("startup unsubscribe failed topic", "err", err)
	}
	if err := c.opts.Bus.Unsubscribe(TopicStartupRetry, onRetry); err != nil {
		c.logger.Warn("startup unsubscribe retry topic", "err", err)
	}
}

// coordinate reacts to failures put on the bus: a started peer drives the
// retry, while a dead-end (no component able to drive) aborts startup.
func (c *Coordinator) coordinate(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case failure := <-c.failCh:
			c.onFailure(failure)
		}
	}
}

func (c *Coordinator) onFailure(failure Failure) {
	c.logger.Error("startup attempt failed",
		"component", failure.Component, "attempt", failure.Attempt, "err", failure.Err)

	c.mu.Lock()
	state := c.states[failure.Component]
	if state.phase != phaseGaveUp {
		state.phase = phaseWaitingRetry
		state.lastFailure = failure
	}
	peerStarted, allDown := c.peerSummaryLocked(failure.Component)
	c.mu.Unlock()

	switch {
	case peerStarted != "":
		if failure.Attempt < c.maxAttempts {
			c.publishRetry(peerStarted, failure.Component, failure.Attempt+1)
		}
	case allDown:
		c.abort(failure.Component, fmt.Errorf("startup: all components failed, last: %w", failure.Err))
	}
}

// peerSummaryLocked reports a started peer (if any) and whether every other
// component is already in a failed state. Callers hold c.mu.
func (c *Coordinator) peerSummaryLocked(self Component) (started Component, allDown bool) {
	allDown = true
	for name, state := range c.states {
		if name == self {
			continue
		}
		switch state.phase {
		case phaseStarted:
			return name, false
		case phaseWaitingRetry, phaseGaveUp:
			// still down, keep scanning
		case phasePending:
			allDown = false
		}
	}
	return "", allDown
}

func (c *Coordinator) publishRetry(from, to Component, attempt int) {
	c.logger.Info("startup retry notified",
		"component", to, "attempt", attempt, "notified_by", from)
	c.opts.Bus.Publish(TopicStartupRetry, RetryRequest{Component: to, Attempt: attempt})
}

func (c *Coordinator) abort(trigger Component, reason error) {
	c.abortOnce.Do(func() {
		c.logger.Error("startup aborted", "trigger", trigger, "err", reason)
		c.events <- Event{Component: trigger, Kind: KindAborted, Err: reason}
		c.cancel()
	})
}

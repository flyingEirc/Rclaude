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
	// ErrUnknownDependency indicates a Spec.DependsOn names a component that
	// has no Spec.
	ErrUnknownDependency = errors.New("startup: unknown dependency component")
	// ErrSelfDependency indicates a Spec lists itself in DependsOn.
	ErrSelfDependency = errors.New("startup: component depends on itself")
	// ErrDependencyCycle indicates the DependsOn edges form a cycle.
	ErrDependencyCycle = errors.New("startup: dependency cycle")
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
	// DependsOn lists components that must reach the started phase before this
	// component's first attempt runs. It removes the guaranteed first-attempt
	// failure when a component (e.g. the PTY) can only start once another
	// (e.g. the daemon) is up. Entries are validated for unknown names,
	// self-references, and cycles by New.
	DependsOn []Component
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

	// deps maps a component to the set of components it depends on.
	deps map[Component]map[Component]struct{}
	// startedChs[name] is closed once name reaches the started phase, so
	// dependents can gate their first attempt on it.
	startedChs map[Component]chan struct{}

	failCh     chan Failure
	retryChs   map[Component]chan RetryRequest
	events     chan Event
	overflowCh chan Component // signals coordinate() to abort when a bus-event is dropped

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
	startedChs := make(map[Component]chan struct{}, len(specs))
	for _, spec := range specs {
		if _, dup := states[spec.Name]; dup {
			return nil, fmt.Errorf("%w: %s", ErrDuplicateComponent, spec.Name)
		}
		states[spec.Name] = &componentState{}
		retryChs[spec.Name] = make(chan RetryRequest, retryBufferSize)
		startedChs[spec.Name] = make(chan struct{})
	}

	deps, err := buildDependencies(specs, states)
	if err != nil {
		return nil, err
	}

	return &Coordinator{
		opts:        opts,
		specs:       specs,
		logger:      opts.Logger,
		maxAttempts: opts.MaxRetries + 1,
		states:      states,
		deps:        deps,
		startedChs:  startedChs,
		failCh:      make(chan Failure, failBufferSize),
		retryChs:    retryChs,
		events:      make(chan Event, eventBufferSize),
		overflowCh:  make(chan Component, 1),
	}, nil
}

// buildDependencies validates the DependsOn edges and returns, per component,
// the set of components it depends on.
func buildDependencies(
	specs []Spec,
	known map[Component]*componentState,
) (map[Component]map[Component]struct{}, error) {
	deps := make(map[Component]map[Component]struct{}, len(specs))
	for _, spec := range specs {
		set := make(map[Component]struct{}, len(spec.DependsOn))
		for _, dep := range spec.DependsOn {
			if dep == spec.Name {
				return nil, fmt.Errorf("%w: %s", ErrSelfDependency, spec.Name)
			}
			if _, ok := known[dep]; !ok {
				return nil, fmt.Errorf("%w: %s -> %s", ErrUnknownDependency, spec.Name, dep)
			}
			set[dep] = struct{}{}
		}
		deps[spec.Name] = set
	}
	if err := ensureAcyclic(specs, deps); err != nil {
		return nil, err
	}
	return deps, nil
}

const (
	nodeWhite = iota
	nodeGray
	nodeBlack
)

// ensureAcyclic rejects DependsOn graphs with a cycle, which would otherwise
// deadlock every component on the cycle in awaitDependencies.
func ensureAcyclic(specs []Spec, deps map[Component]map[Component]struct{}) error {
	color := make(map[Component]int, len(specs))
	for _, spec := range specs {
		if color[spec.Name] != nodeWhite {
			continue
		}
		if err := visitForCycle(spec.Name, deps, color); err != nil {
			return err
		}
	}
	return nil
}

func visitForCycle(node Component, deps map[Component]map[Component]struct{}, color map[Component]int) error {
	color[node] = nodeGray
	for dep := range deps[node] {
		switch color[dep] {
		case nodeGray:
			return fmt.Errorf("%w: %s -> %s", ErrDependencyCycle, node, dep)
		case nodeWhite:
			if err := visitForCycle(dep, deps, color); err != nil {
				return err
			}
		}
	}
	color[node] = nodeBlack
	return nil
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
//
// When a forwarding channel is full the handler signals overflowCh so that
// coordinate() can abort startup rather than silently stranding a component
// in awaitRetry() with no further signal.
func (c *Coordinator) busHandlers() (func(Failure), func(RetryRequest)) {
	onFailed := func(f Failure) {
		select {
		case c.failCh <- f:
		default:
			c.logger.Error("startup: failure event dropped (queue full), aborting",
				"component", f.Component, "attempt", f.Attempt)
			c.signalOverflow(f.Component)
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
			c.logger.Error("startup: retry event dropped (queue full), aborting",
				"component", r.Component, "attempt", r.Attempt)
			c.signalOverflow(r.Component)
		}
	}
	return onFailed, onRetry
}

// signalOverflow sends the affected component onto overflowCh for coordinate()
// to pick up and abort. The send is non-blocking: if overflowCh already holds
// a signal the first one is sufficient to trigger the abort.
func (c *Coordinator) signalOverflow(comp Component) {
	select {
	case c.overflowCh <- comp:
	default:
	}
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
// It also aborts immediately if a bus-event was dropped (overflow), which
// would otherwise strand a component in awaitRetry() indefinitely.
func (c *Coordinator) coordinate(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case failure := <-c.failCh:
			c.onFailure(failure)
		case comp := <-c.overflowCh:
			c.abort(comp, fmt.Errorf("startup: event queue overflow for %s; aborting to prevent hang", comp))
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
	hasDependent := c.hasPendingDependentLocked(failure.Component)
	c.mu.Unlock()

	switch {
	case peerStarted != "":
		if failure.Attempt < c.maxAttempts {
			c.publishRetry(peerStarted, failure.Component, failure.Attempt+1)
		}
	case hasDependent:
		// No started peer, but a dependent is blocked waiting on this
		// component: it can never drive our retry, so drive it ourselves until
		// we come up (unblocking the dependent) or exhaust retries. Exhaustion
		// is handled by runComponent -> markGaveUp -> abort.
		if failure.Attempt < c.maxAttempts {
			c.publishRetry(failure.Component, failure.Component, failure.Attempt+1)
		}
	case allDown:
		c.abort(failure.Component, fmt.Errorf("startup: all components failed, last: %w", failure.Err))
	}
}

// hasPendingDependentLocked reports whether any other component is still
// pending its first attempt while depending on self, meaning it is blocked in
// awaitDependencies and cannot drive self's retry. Callers hold c.mu.
func (c *Coordinator) hasPendingDependentLocked(self Component) bool {
	for name, state := range c.states {
		if name == self || state.phase != phasePending {
			continue
		}
		if c.dependsOn(name, self) {
			return true
		}
	}
	return false
}

// dependsOn reports whether component declared dependency in its DependsOn.
func (c *Coordinator) dependsOn(component, dependency Component) bool {
	set, ok := c.deps[component]
	if !ok {
		return false
	}
	_, ok = set[dependency]
	return ok
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

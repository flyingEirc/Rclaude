package startup

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// runComponent drives one component through the initial attempt and
// bus-driven retries until it starts (and then runs to completion), gives
// up, or the context ends.
func (c *Coordinator) runComponent(ctx context.Context, spec Spec) {
	if !c.awaitDependencies(ctx, spec) {
		return
	}
	for attempt := 1; attempt <= c.maxAttempts; attempt++ {
		started, err := c.runAttempt(ctx, spec, attempt)
		if started {
			c.events <- Event{Component: spec.Name, Kind: KindExited, Attempt: attempt, Err: err}
			return
		}
		if ctx.Err() != nil {
			return
		}

		// A failure is always put back on the bus; success never is.
		c.opts.Bus.Publish(TopicStartupFailed, Failure{
			Component: spec.Name,
			Attempt:   attempt,
			Err:       err,
		})
		if attempt == c.maxAttempts {
			c.markGaveUp(spec.Name, attempt, err)
			return
		}
		if !c.awaitRetry(ctx, spec.Name) {
			return
		}
	}
}

// awaitDependencies blocks until every component in spec.DependsOn has reached
// the started phase, so a dependent's first attempt never runs before its
// dependency is up. It reports false when ctx ended first (for example a
// dependency gave up and aborted startup).
func (c *Coordinator) awaitDependencies(ctx context.Context, spec Spec) bool {
	for _, dep := range spec.DependsOn {
		select {
		case <-ctx.Done():
			return false
		case <-c.startedChs[dep]:
		}
	}
	return true
}

// runAttempt runs spec.Run once. It reports started=true as soon as ready
// fired, in which case it blocks until Run returns and hands back Run's
// error as the component's exit error.
func (c *Coordinator) runAttempt(ctx context.Context, spec Spec, attempt int) (bool, error) {
	readyCh := make(chan struct{})
	var once sync.Once
	ready := func() { once.Do(func() { close(readyCh) }) }

	errCh := make(chan error, 1)
	go func() { errCh <- spec.Run(ctx, ready) }()

	select {
	case <-readyCh:
		c.markStarted(spec.Name, attempt)
		return true, <-errCh
	case err := <-errCh:
		select {
		case <-readyCh:
			// Ready fired just before Run returned: startup did succeed.
			c.markStarted(spec.Name, attempt)
			return true, err
		default:
		}
		if err == nil {
			err = errExitedBeforeReady
		}
		return false, err
	}
}

// markStarted flips the component to started and drives retries for peers
// whose failures arrived while this component was still pending.
func (c *Coordinator) markStarted(name Component, attempt int) {
	c.mu.Lock()
	alreadyStarted := c.states[name].phase == phaseStarted
	c.states[name].phase = phaseStarted
	pending := c.pendingPeerFailuresLocked(name)
	c.mu.Unlock()

	// Release any dependents blocked in awaitDependencies. markStarted runs at
	// most once per component, but guard the close to stay panic-safe.
	if !alreadyStarted {
		close(c.startedChs[name])
	}

	c.logger.Info("startup succeeded", "component", name, "attempt", attempt)
	c.events <- Event{Component: name, Kind: KindStarted, Attempt: attempt}

	for _, failure := range pending {
		if failure.Attempt < c.maxAttempts {
			c.publishRetry(name, failure.Component, failure.Attempt+1)
		}
	}
}

// pendingPeerFailuresLocked lists the last failure of every other component
// currently waiting for a retry notification. Callers hold c.mu.
func (c *Coordinator) pendingPeerFailuresLocked(self Component) []Failure {
	var pending []Failure
	for name, state := range c.states {
		if name != self && state.phase == phaseWaitingRetry {
			pending = append(pending, state.lastFailure)
		}
	}
	return pending
}

func (c *Coordinator) markGaveUp(name Component, attempts int, err error) {
	c.mu.Lock()
	c.states[name].phase = phaseGaveUp
	c.mu.Unlock()

	c.logger.Error("startup gave up", "component", name, "attempts", attempts, "err", err)
	c.events <- Event{Component: name, Kind: KindGaveUp, Attempt: attempts, Err: err}
	c.abort(name, fmt.Errorf("startup: %s gave up after %d attempts: %w", name, attempts, err))
}

// awaitRetry blocks until the bus delivers a retry notification for name,
// then applies the configured delay. It reports false when ctx ended first.
func (c *Coordinator) awaitRetry(ctx context.Context, name Component) bool {
	select {
	case <-ctx.Done():
		return false
	case req := <-c.retryChs[name]:
		c.logger.Info("startup retry received", "component", name, "attempt", req.Attempt)
		return c.sleepRetryDelay(ctx)
	}
}

func (c *Coordinator) sleepRetryDelay(ctx context.Context) bool {
	if c.opts.RetryDelay <= 0 {
		return true
	}
	timer := time.NewTimer(c.opts.RetryDelay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

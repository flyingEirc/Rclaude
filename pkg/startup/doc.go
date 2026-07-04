// Package startup coordinates the concurrent startup of the unified entry's
// components (daemon and pty) over a shared in-process event bus.
//
// Protocol: every component runs its first attempt concurrently. A failed
// attempt is always published as a Failure on TopicStartupFailed. When a
// component that has started successfully observes a peer failure, a
// RetryRequest is published on TopicStartupRetry telling the failed component
// to try again (successful startups are never published back to the bus).
// A component gives up after MaxRetries retries beyond the initial attempt;
// startup also aborts when every component is in a failed state, because only
// a successfully started peer may drive retries.
//
// Bus caveat: EventBus.Publish invokes synchronous subscribers while holding
// the bus lock, so subscribers here only forward events into channels; all
// publishing happens on coordinator/runner goroutines.
package startup

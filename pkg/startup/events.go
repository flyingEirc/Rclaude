package startup

// Component identifies one startup flow managed by the unified entry.
type Component string

// Components managed by the unified rclaude entry.
const (
	ComponentDaemon Component = "daemon"
	ComponentPTY    Component = "pty"
)

// Event-bus topics used by the startup protocol.
const (
	// TopicStartupFailed carries Failure values published by a component
	// whose startup attempt failed.
	TopicStartupFailed = "startup.failed"
	// TopicStartupRetry carries RetryRequest values published on behalf of a
	// successfully started peer to tell the failed component to retry.
	TopicStartupRetry = "startup.retry"
)

// Failure is put on the bus every time a startup attempt fails.
type Failure struct {
	Component Component
	Attempt   int
	Err       error
}

// RetryRequest tells a failed component to run the given attempt.
type RetryRequest struct {
	Component Component
	Attempt   int
}

// Bus is the minimal event-bus surface required by the coordinator,
// satisfied by github.com/asaskevich/EventBus.
type Bus interface {
	Subscribe(topic string, fn interface{}) error
	Unsubscribe(topic string, handler interface{}) error
	Publish(topic string, args ...interface{})
}

// EventKind classifies coordinator notifications delivered to the caller.
type EventKind uint8

// Coordinator event kinds.
const (
	// KindStarted reports that a component's startup succeeded.
	KindStarted EventKind = iota
	// KindGaveUp reports that a component exhausted its retries.
	KindGaveUp
	// KindAborted reports that startup was aborted because no component is
	// left to drive retries (or a component gave up).
	KindAborted
	// KindExited reports that a component's Run returned after a successful
	// startup.
	KindExited
)

// Event is delivered to the caller on the channel returned by Run.
type Event struct {
	Component Component
	Kind      EventKind
	Attempt   int
	Err       error
}

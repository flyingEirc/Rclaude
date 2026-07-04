package audit

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

const (
	// DefaultQueueSize is the recorder queue capacity used when the
	// configured size is not positive.
	DefaultQueueSize = 256

	saveTimeout = 5 * time.Second
)

// Recorder buffers records in a bounded queue and writes them to a Store from
// a single background goroutine. Enqueueing never blocks the caller: when the
// queue is full the record is dropped and a warning is logged, so auditing can
// never stall or fail a file operation.
type Recorder struct {
	store  Store
	logger *slog.Logger
	queue  chan *Record
	done   chan struct{}

	mu     sync.RWMutex
	closed bool
}

// NewRecorder starts the background writer. queueSize falls back to
// DefaultQueueSize when not positive; logger falls back to slog.Default.
func NewRecorder(store Store, queueSize int, logger *slog.Logger) *Recorder {
	if queueSize <= 0 {
		queueSize = DefaultQueueSize
	}
	if logger == nil {
		logger = slog.Default()
	}
	r := &Recorder{
		store:  store,
		logger: logger,
		queue:  make(chan *Record, queueSize),
		done:   make(chan struct{}),
	}
	go r.run()
	return r
}

// Record enqueues rec without blocking. Records are dropped (with a warning)
// when the queue is full, and ignored after Close.
func (r *Recorder) Record(rec *Record) {
	if rec == nil {
		return
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.closed {
		return
	}
	select {
	case r.queue <- rec:
	default:
		r.logger.Warn("audit queue full, record dropped",
			"operation", rec.Operation, "path", rec.Path)
	}
}

// Close stops accepting records, drains the queue, and closes the store.
// Subsequent calls wait for the drain and return nil.
func (r *Recorder) Close() error {
	r.mu.Lock()
	alreadyClosed := r.closed
	r.closed = true
	if !alreadyClosed {
		close(r.queue)
	}
	r.mu.Unlock()

	<-r.done
	if alreadyClosed {
		return nil
	}
	return r.store.Close()
}

func (r *Recorder) run() {
	defer close(r.done)
	for rec := range r.queue {
		r.save(rec)
	}
}

func (r *Recorder) save(rec *Record) {
	ctx, cancel := context.WithTimeout(context.Background(), saveTimeout)
	defer cancel()
	if err := r.store.Save(ctx, rec); err != nil {
		r.logger.Error("audit save failed",
			"err", err, "operation", rec.Operation, "path", rec.Path)
	}
}

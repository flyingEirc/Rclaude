package syncer

import (
	"sync"
	"time"

	"flyingEirc/Rclaude/pkg/safepath"
)

type selfWriteFilter struct {
	ttl   time.Duration
	nowFn func() time.Time

	mu      sync.Mutex
	entries map[string]time.Time
}

func newSelfWriteFilter(ttl time.Duration) *selfWriteFilter {
	return &selfWriteFilter{
		ttl:     ttl,
		nowFn:   time.Now,
		entries: make(map[string]time.Time),
	}
}

func (f *selfWriteFilter) Remember(paths ...string) {
	if f == nil || f.ttl <= 0 {
		return
	}

	expiry := f.nowFn().Add(f.ttl)

	f.mu.Lock()
	defer f.mu.Unlock()
	for _, path := range paths {
		f.entries[safepath.ToSlash(path)] = expiry
	}
}

func (f *selfWriteFilter) ShouldSuppress(path string) bool {
	if f == nil || f.ttl <= 0 {
		return false
	}

	now := f.nowFn()
	key := safepath.ToSlash(path)

	f.mu.Lock()
	defer f.mu.Unlock()

	for candidate, expiry := range f.entries {
		if !expiry.After(now) {
			delete(f.entries, candidate)
		}
	}

	expiry, ok := f.entries[key]
	return ok && expiry.After(now)
}

func (f *selfWriteFilter) size() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.entries)
}

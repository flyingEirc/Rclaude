package syncer

import (
	"sort"
	"sync"

	"flyingEirc/Rclaude/pkg/safepath"
)

type pathLocker struct {
	mu    sync.Mutex
	locks map[string]*pathEntry
}

type pathEntry struct {
	mu       sync.Mutex
	refCount int
}

func newPathLocker() *pathLocker {
	return &pathLocker{locks: make(map[string]*pathEntry)}
}

func (l *pathLocker) Lock(path string) func() {
	if l == nil {
		return func() {}
	}

	key := safepath.ToSlash(path)

	l.mu.Lock()
	entry, ok := l.locks[key]
	if !ok {
		entry = &pathEntry{}
		l.locks[key] = entry
	}
	entry.refCount++
	l.mu.Unlock()

	entry.mu.Lock()

	return func() {
		entry.mu.Unlock()

		l.mu.Lock()
		entry.refCount--
		if entry.refCount == 0 {
			delete(l.locks, key)
		}
		l.mu.Unlock()
	}
}

func (l *pathLocker) LockMany(paths ...string) func() {
	if l == nil {
		return func() {}
	}

	keys := make([]string, 0, len(paths))
	seen := make(map[string]struct{}, len(paths))
	for _, path := range paths {
		key := safepath.ToSlash(path)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		keys = append(keys, key)
	}
	sort.Strings(keys)

	releases := make([]func(), 0, len(keys))
	for _, key := range keys {
		releases = append(releases, l.Lock(key))
	}

	return func() {
		for i := len(releases) - 1; i >= 0; i-- {
			releases[i]()
		}
	}
}

func (l *pathLocker) size() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.locks)
}

package syncer

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPathLocker_SerializesSamePath(t *testing.T) {
	t.Parallel()

	locker := newPathLocker()

	var mu sync.Mutex
	order := make([]int, 0, 2)
	started := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		unlock := locker.Lock("a/b")
		close(started)
		time.Sleep(20 * time.Millisecond)
		mu.Lock()
		order = append(order, 1)
		mu.Unlock()
		unlock()
	}()

	go func() {
		defer wg.Done()
		<-started
		unlock := locker.Lock("a/b")
		mu.Lock()
		order = append(order, 2)
		mu.Unlock()
		unlock()
	}()

	wg.Wait()
	require.Equal(t, []int{1, 2}, order)
}

func TestPathLocker_ConcurrentDifferentPaths(t *testing.T) {
	t.Parallel()

	locker := newPathLocker()
	var concurrent atomic.Int32
	var maxConcurrent atomic.Int32
	var wg sync.WaitGroup

	for i := range 8 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			unlock := locker.Lock(string(rune('a' + i)))
			current := concurrent.Add(1)
			for {
				seen := maxConcurrent.Load()
				if current <= seen || maxConcurrent.CompareAndSwap(seen, current) {
					break
				}
			}
			time.Sleep(10 * time.Millisecond)
			concurrent.Add(-1)
			unlock()
		}(i)
	}

	wg.Wait()
	assert.Greater(t, maxConcurrent.Load(), int32(1))
}

func TestPathLocker_RefCountReclaim(t *testing.T) {
	t.Parallel()

	locker := newPathLocker()
	unlock := locker.Lock("x")
	unlock()
	assert.Equal(t, 0, locker.size())
}

func TestPathLocker_NormalizesSlash(t *testing.T) {
	t.Parallel()

	locker := newPathLocker()
	unlock := locker.Lock(`a\b`)
	defer unlock()

	gotLock := make(chan struct{})
	go func() {
		release := locker.Lock("a/b")
		close(gotLock)
		release()
	}()

	select {
	case <-gotLock:
		t.Fatal("expected normalized path lock to block")
	case <-time.After(20 * time.Millisecond):
	}
}

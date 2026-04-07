package syncer

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSelfWriteFilter_RememberWithinTTL(t *testing.T) {
	t.Parallel()

	now := time.Unix(1000, 0)
	filter := newSelfWriteFilter(2 * time.Second)
	filter.nowFn = func() time.Time { return now }

	filter.Remember("a/b.txt")
	now = now.Add(time.Second)

	require.True(t, filter.ShouldSuppress("a/b.txt"))
}

func TestSelfWriteFilter_ExpiredAfterTTL(t *testing.T) {
	t.Parallel()

	now := time.Unix(1000, 0)
	filter := newSelfWriteFilter(2 * time.Second)
	filter.nowFn = func() time.Time { return now }

	filter.Remember("a/b.txt")
	now = now.Add(3 * time.Second)

	assert.False(t, filter.ShouldSuppress("a/b.txt"))
}

func TestSelfWriteFilter_MultiplePaths(t *testing.T) {
	t.Parallel()

	now := time.Unix(1000, 0)
	filter := newSelfWriteFilter(2 * time.Second)
	filter.nowFn = func() time.Time { return now }

	filter.Remember("a/old.txt", "a/new.txt")
	assert.True(t, filter.ShouldSuppress("a/old.txt"))
	assert.True(t, filter.ShouldSuppress("a/new.txt"))
}

func TestSelfWriteFilter_LazyGC(t *testing.T) {
	t.Parallel()

	now := time.Unix(1000, 0)
	filter := newSelfWriteFilter(time.Second)
	filter.nowFn = func() time.Time { return now }

	for i := range 5 {
		filter.Remember(string(rune('a' + i)))
	}
	require.Equal(t, 5, filter.size())

	now = now.Add(2 * time.Second)
	assert.False(t, filter.ShouldSuppress("missing"))
	assert.Equal(t, 0, filter.size())
}

func TestSelfWriteFilter_RememberRefreshesExpiry(t *testing.T) {
	t.Parallel()

	now := time.Unix(1000, 0)
	filter := newSelfWriteFilter(2 * time.Second)
	filter.nowFn = func() time.Time { return now }

	filter.Remember("a")
	now = now.Add(time.Second)
	filter.Remember("a")
	now = now.Add(1500 * time.Millisecond)

	assert.True(t, filter.ShouldSuppress("a"))
}

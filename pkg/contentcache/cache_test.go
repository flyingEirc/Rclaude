package contentcache

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCacheGetPutHit(t *testing.T) {
	t.Parallel()

	cache := New(32)

	sig := Signature{Size: 5, ModTime: 10}

	require.True(t, cache.Put("dir/file.txt", sig, []byte("hello")))

	got, ok := cache.Get("dir/file.txt", sig)

	require.True(t, ok)

	assert.Equal(t, []byte("hello"), got)
}

func TestCacheSignatureMismatchEvicts(t *testing.T) {
	t.Parallel()

	cache := New(32)

	require.True(t, cache.Put("a.txt", Signature{Size: 5, ModTime: 1}, []byte("hello")))

	_, ok := cache.Get("a.txt", Signature{Size: 5, ModTime: 2})

	assert.False(t, ok)

	assert.Equal(t, 0, cache.Len())
}

func TestCacheEvictsLRUByBytes(t *testing.T) {
	t.Parallel()

	cache := New(6)

	require.True(t, cache.Put("a.txt", Signature{Size: 3, ModTime: 1}, []byte("aaa")))

	require.True(t, cache.Put("b.txt", Signature{Size: 4, ModTime: 1}, []byte("bbbb")))

	_, ok := cache.Get("a.txt", Signature{Size: 3, ModTime: 1})

	assert.False(t, ok)

	got, ok := cache.Get("b.txt", Signature{Size: 4, ModTime: 1})

	require.True(t, ok)

	assert.Equal(t, []byte("bbbb"), got)
}

func TestCacheOversizedValueNotStored(t *testing.T) {
	t.Parallel()

	cache := New(3)

	assert.False(t, cache.Put("a.txt", Signature{Size: 4, ModTime: 1}, []byte("long")))

	_, ok := cache.Get("a.txt", Signature{Size: 4, ModTime: 1})

	assert.False(t, ok)
}

func TestCacheInvalidatePrefix(t *testing.T) {
	t.Parallel()

	cache := New(64)

	require.True(t, cache.Put("dir/a.txt", Signature{Size: 1, ModTime: 1}, []byte("a")))

	require.True(t, cache.Put("dir/sub/b.txt", Signature{Size: 1, ModTime: 1}, []byte("b")))

	require.True(t, cache.Put("other/c.txt", Signature{Size: 1, ModTime: 1}, []byte("c")))

	cache.InvalidatePrefix("dir")

	_, ok := cache.Get("dir/a.txt", Signature{Size: 1, ModTime: 1})

	assert.False(t, ok)

	_, ok = cache.Get("dir/sub/b.txt", Signature{Size: 1, ModTime: 1})

	assert.False(t, ok)

	got, ok := cache.Get("other/c.txt", Signature{Size: 1, ModTime: 1})

	require.True(t, ok)

	assert.Equal(t, []byte("c"), got)
}

func TestCacheDisabled(t *testing.T) {
	t.Parallel()

	cache := New(0)

	assert.False(t, cache.Put("a.txt", Signature{Size: 1, ModTime: 1}, []byte("a")))

	_, ok := cache.Get("a.txt", Signature{Size: 1, ModTime: 1})

	assert.False(t, ok)
}

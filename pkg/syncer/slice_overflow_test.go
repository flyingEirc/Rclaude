package syncer

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSliceContentOverflowSafe 保证 sliceContent 在 length 接近 MaxInt64 时不会因
// offset+length 回绕为负而越界 panic，并返回正确截断。
func TestSliceContentOverflowSafe(t *testing.T) {
	t.Parallel()
	data := []byte("hello world")

	require.NotPanics(t, func() {
		got := sliceContent(data, 6, math.MaxInt64)
		assert.Equal(t, []byte("world"), got)
	})

	// 常规行为不变。
	assert.Equal(t, []byte("hel"), sliceContent(data, 0, 3))
	assert.Equal(t, []byte("world"), sliceContent(data, 6, 0))
	assert.Empty(t, sliceContent(data, 100, 5))
	// 精确到末尾。
	assert.Equal(t, []byte("world"), sliceContent(data, 6, 5))
}

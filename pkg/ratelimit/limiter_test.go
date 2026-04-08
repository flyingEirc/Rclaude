package ratelimit

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestByteLimiterDisabled(t *testing.T) {
	t.Parallel()

	limiter := NewBytesPerSecond(0)
	start := time.Now()
	require.NoError(t, limiter.WaitBytes(context.Background(), 4096))
	assert.False(t, limiter.Enabled())
	assert.Less(t, time.Since(start), 50*time.Millisecond)
}

func TestByteLimiterWaitBytesOverBurst(t *testing.T) {
	t.Parallel()

	limiter := NewBytesPerSecond(1000)
	start := time.Now()
	require.NoError(t, limiter.WaitBytes(context.Background(), 1500))
	elapsed := time.Since(start)
	assert.GreaterOrEqual(t, elapsed, 400*time.Millisecond)
	assert.Less(t, elapsed, 2*time.Second)
}

func TestByteLimiterWaitBytesHonorsContext(t *testing.T) {
	t.Parallel()

	limiter := NewBytesPerSecond(1)
	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()

	start := time.Now()
	err := limiter.WaitBytes(ctx, 2)
	require.Error(t, err)
	assert.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Less(t, time.Since(start), 300*time.Millisecond)
}

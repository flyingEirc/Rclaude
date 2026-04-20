package ratelimit

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLimiterDisabled(t *testing.T) {
	t.Parallel()

	limiter := New(0, 0)
	start := time.Now()
	require.NoError(t, limiter.Wait(context.Background(), 1))
	assert.False(t, limiter.Enabled())
	assert.Less(t, time.Since(start), 50*time.Millisecond)
}

func TestLimiterWaitOverConfiguredBurst(t *testing.T) {
	t.Parallel()

	limiter := New(1000, 100)
	start := time.Now()
	require.NoError(t, limiter.Wait(context.Background(), 150))
	elapsed := time.Since(start)
	assert.GreaterOrEqual(t, elapsed, 40*time.Millisecond)
	assert.Less(t, elapsed, time.Second)
}

func TestLimiterWaitHonorsContext(t *testing.T) {
	t.Parallel()

	limiter := New(1, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()

	start := time.Now()
	err := limiter.Wait(ctx, 2)
	require.Error(t, err)
	assert.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Less(t, time.Since(start), 300*time.Millisecond)
}

func TestNewPTYAttachLimiterFallsBackToRateBurst(t *testing.T) {
	t.Parallel()

	limiter := NewPTYAttachLimiter(2, 0)
	start := time.Now()
	require.NoError(t, limiter.Wait(context.Background(), 2))
	assert.True(t, limiter.Enabled())
	assert.Less(t, time.Since(start), 50*time.Millisecond)
}

func TestByteLimiterWaitBytesOverDefaultBurst(t *testing.T) {
	t.Parallel()

	limiter := NewBytesPerSecond(1000)
	start := time.Now()
	require.NoError(t, limiter.WaitBytes(context.Background(), 1500))
	elapsed := time.Since(start)
	assert.GreaterOrEqual(t, elapsed, 400*time.Millisecond)
	assert.Less(t, elapsed, 2*time.Second)
}

func TestNewPTYStdinLimiterUsesConfiguredBurst(t *testing.T) {
	t.Parallel()

	limiter := NewPTYStdinLimiter(1000, 100)
	start := time.Now()
	require.NoError(t, limiter.WaitBytes(context.Background(), 150))
	elapsed := time.Since(start)
	assert.True(t, limiter.Enabled())
	assert.GreaterOrEqual(t, elapsed, 40*time.Millisecond)
	assert.Less(t, elapsed, time.Second)
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

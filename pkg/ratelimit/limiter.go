package ratelimit

import (
	"context"
	"math"
	"sync"
	"time"
)

// ByteLimiter applies a byte-per-second budget using a small token bucket.
// A nil or disabled limiter lets all traffic pass immediately.
type ByteLimiter struct {
	mu     sync.Mutex
	rate   int64
	burst  float64
	tokens float64
	last   time.Time
}

func NewBytesPerSecond(limit int64) *ByteLimiter {
	if limit <= 0 {
		return &ByteLimiter{}
	}
	now := time.Now()
	return &ByteLimiter{
		rate:   limit,
		burst:  float64(limit),
		tokens: float64(limit),
		last:   now,
	}
}

func (l *ByteLimiter) Enabled() bool {
	return l != nil && l.rate > 0
}

func (l *ByteLimiter) WaitBytes(ctx context.Context, n int) error {
	if !l.Enabled() || n <= 0 {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}

	remaining := float64(n)
	for remaining > 0 {
		waitFor := time.Duration(0)

		l.mu.Lock()
		l.refillLocked(time.Now())
		if l.tokens > 0 {
			consume := minFloat64(remaining, l.tokens)
			l.tokens -= consume
			remaining -= consume
		}
		if remaining > 0 {
			nextChunk := minFloat64(remaining, l.burst)
			need := nextChunk - l.tokens
			if need < 1 {
				need = 1
			}
			waitFor = durationForBytes(need, l.rate)
		}
		l.mu.Unlock()

		if remaining <= 0 {
			return nil
		}
		if err := waitContext(ctx, waitFor); err != nil {
			return err
		}
	}
	return nil
}

func (l *ByteLimiter) refillLocked(now time.Time) {
	if !l.Enabled() {
		return
	}
	if l.last.IsZero() {
		l.last = now
		return
	}
	elapsed := now.Sub(l.last)
	if elapsed <= 0 {
		return
	}
	l.tokens += float64(l.rate) * elapsed.Seconds()
	if l.tokens > l.burst {
		l.tokens = l.burst
	}
	l.last = now
}

func waitContext(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
			return nil
		}
	}

	timer := time.NewTimer(d)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func durationForBytes(bytes float64, rate int64) time.Duration {
	if bytes <= 0 || rate <= 0 {
		return 0
	}
	nanos := math.Ceil(bytes / float64(rate) * float64(time.Second))
	if nanos < 1 {
		nanos = 1
	}
	return time.Duration(nanos)
}

func minFloat64(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}

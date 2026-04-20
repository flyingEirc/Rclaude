package ratelimit

import (
	"context"
	"math"
	"sync"
	"time"
)

// Limiter applies a generic units-per-second budget using a token bucket.
// A nil or disabled limiter lets all traffic pass immediately.
type Limiter struct {
	mu     sync.Mutex
	rate   int64
	burst  float64
	tokens float64
	last   time.Time
}

// ByteLimiter preserves the existing bytes-oriented API on top of Limiter.
type ByteLimiter struct {
	limiter *Limiter
}

// New constructs a generic units-per-second limiter.
func New(rate int64, burst int64) *Limiter {
	if rate <= 0 {
		return &Limiter{}
	}

	effectiveBurst := burst
	if effectiveBurst <= 0 {
		effectiveBurst = rate
	}

	now := time.Now()
	return &Limiter{
		rate:   rate,
		burst:  float64(effectiveBurst),
		tokens: float64(effectiveBurst),
		last:   now,
	}
}

// NewBytesPerSecond constructs a byte limiter whose burst equals its rate.
func NewBytesPerSecond(limit int64) *ByteLimiter {
	return NewBytesPerSecondBurst(limit, limit)
}

// NewBytesPerSecondBurst constructs a byte limiter with an explicit burst size.
func NewBytesPerSecondBurst(limit int64, burst int64) *ByteLimiter {
	return &ByteLimiter{limiter: New(limit, burst)}
}

// NewPTYAttachLimiter constructs a request limiter for PTY attach attempts.
func NewPTYAttachLimiter(qps int, burst int) *Limiter {
	return New(int64(qps), int64(burst))
}

// NewPTYStdinLimiter constructs a byte limiter for PTY stdin traffic.
func NewPTYStdinLimiter(bps int64, burst int64) *ByteLimiter {
	return NewBytesPerSecondBurst(bps, burst)
}

// Enabled reports whether the limiter is actively enforcing a budget.
func (l *Limiter) Enabled() bool {
	return l != nil && l.rate > 0
}

// Wait blocks until n units fit within the configured budget or ctx ends.
func (l *Limiter) Wait(ctx context.Context, n int64) error {
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
			waitFor = durationForUnits(need, l.rate)
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

// Enabled reports whether the wrapped byte limiter is actively enforcing a budget.
func (l *ByteLimiter) Enabled() bool {
	return l != nil && l.limiter != nil && l.limiter.Enabled()
}

// WaitBytes blocks until n bytes fit within the configured budget or ctx ends.
func (l *ByteLimiter) WaitBytes(ctx context.Context, n int) error {
	if l == nil || l.limiter == nil {
		return nil
	}
	return l.limiter.Wait(ctx, int64(n))
}

func (l *Limiter) refillLocked(now time.Time) {
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

func durationForUnits(units float64, rate int64) time.Duration {
	if units <= 0 || rate <= 0 {
		return 0
	}
	nanos := math.Ceil(units / float64(rate) * float64(time.Second))
	if nanos < 1 {
		nanos = 1
	}
	return time.Duration(nanos)
}

func minFloat64(a float64, b float64) float64 {
	if a < b {
		return a
	}
	return b
}

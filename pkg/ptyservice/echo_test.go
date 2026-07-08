package ptyservice

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestEchoTrackerWatermarkHoldsForEchoTimeout(t *testing.T) {
	tracker := &echoTracker{}
	base := time.Unix(1000, 0)

	tracker.record(3, base)

	_, ok := tracker.watermark(base.Add(echoTimeout - time.Millisecond))
	require.False(t, ok, "too early to acknowledge")

	offset, ok := tracker.watermark(base.Add(echoTimeout))
	require.True(t, ok)
	require.Equal(t, uint64(3), offset)

	_, ok = tracker.watermark(base.Add(echoTimeout * 2))
	require.False(t, ok, "watermark must not repeat")
}

func TestEchoTrackerCoalescesWrites(t *testing.T) {
	tracker := &echoTracker{}
	base := time.Unix(1000, 0)

	tracker.record(1, base)
	tracker.record(2, base.Add(10*time.Millisecond))
	tracker.record(4, base.Add(200*time.Millisecond))

	offset, ok := tracker.watermark(base.Add(echoTimeout + 10*time.Millisecond))
	require.True(t, ok)
	require.Equal(t, uint64(3), offset, "only writes older than the timeout are covered")

	offset, ok = tracker.watermark(base.Add(300 * time.Millisecond))
	require.True(t, ok)
	require.Equal(t, uint64(7), offset)
}

func TestEchoTrackerNilSafe(t *testing.T) {
	var tracker *echoTracker
	tracker.record(5, time.Now())
	_, ok := tracker.watermark(time.Now())
	require.False(t, ok)
}

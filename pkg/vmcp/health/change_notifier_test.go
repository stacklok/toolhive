// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package health

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// notifierRecorder subscribes to a changeNotifier and records delivered
// generations on a buffered channel so tests can assert delivery counts and
// ordering without unbounded blocking.
type notifierRecorder struct {
	fires chan uint64
}

func newNotifierRecorder(t *testing.T, n *changeNotifier) *notifierRecorder {
	t.Helper()
	r := &notifierRecorder{fires: make(chan uint64, 64)}
	n.subscribe(func(gen uint64) { r.fires <- gen })
	return r
}

// next waits for one delivery, failing the test after a bounded timeout.
func (r *notifierRecorder) next(t *testing.T) uint64 {
	t.Helper()
	select {
	case gen := <-r.fires:
		return gen
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for change notification")
		return 0
	}
}

// none asserts no delivery arrives within wait.
func (r *notifierRecorder) none(t *testing.T, wait time.Duration) {
	t.Helper()
	select {
	case gen := <-r.fires:
		t.Fatalf("unexpected change notification with generation %d", gen)
	case <-time.After(wait):
	}
}

func TestChangeNotifier_DeliversLeadingEdgeImmediately(t *testing.T) {
	t.Parallel()

	n := newChangeNotifier(time.Hour) // window never elapses within the test
	t.Cleanup(n.stop)
	rec := newNotifierRecorder(t, n)

	n.notify()

	assert.Equal(t, uint64(1), rec.next(t),
		"first change after a quiet period must be delivered immediately")
	rec.none(t, 50*time.Millisecond)
}

func TestChangeNotifier_CoalescesBurstIntoOneTrailingDelivery(t *testing.T) {
	t.Parallel()

	// A wide window keeps the burst below reliably inside it even under CI
	// scheduling delays; the trailing wait is event-driven, so the window
	// bounds the test's runtime rather than padding it.
	n := newChangeNotifier(500 * time.Millisecond)
	t.Cleanup(n.stop)
	rec := newNotifierRecorder(t, n)

	// Leading delivery for the first change...
	n.notify()
	assert.Equal(t, uint64(1), rec.next(t))

	// ...then a burst inside the window coalesces into exactly one trailing
	// delivery carrying the latest generation.
	n.notify()
	n.notify()
	n.notify()
	assert.Equal(t, uint64(4), rec.next(t),
		"burst must collapse into one trailing delivery with the latest generation")
	rec.none(t, 200*time.Millisecond)
}

func TestChangeNotifier_NoTrailingDeliveryWithoutNewChange(t *testing.T) {
	t.Parallel()

	n := newChangeNotifier(50 * time.Millisecond)
	t.Cleanup(n.stop)
	rec := newNotifierRecorder(t, n)

	n.notify()
	assert.Equal(t, uint64(1), rec.next(t))

	// A single change produces a single delivery: nothing trails it.
	rec.none(t, 150*time.Millisecond)
}

func TestChangeNotifier_StopCancelsPendingTrailingDelivery(t *testing.T) {
	t.Parallel()

	// Wide window so the second notify reliably lands inside it (scheduling a
	// trailing delivery) rather than firing leading-edge under CI delays.
	n := newChangeNotifier(500 * time.Millisecond)
	rec := newNotifierRecorder(t, n)

	n.notify()
	require.Equal(t, uint64(1), rec.next(t))
	n.notify() // schedules a trailing delivery

	n.stop()

	rec.none(t, 600*time.Millisecond)
}

func TestChangeNotifier_StoppedNeverDelivers(t *testing.T) {
	t.Parallel()

	n := newChangeNotifier(time.Millisecond)
	rec := newNotifierRecorder(t, n)

	n.stop()
	n.notify()

	rec.none(t, 50*time.Millisecond)
}

func TestChangeNotifier_SubsequentWindowsDeliverAgain(t *testing.T) {
	t.Parallel()

	n := newChangeNotifier(30 * time.Millisecond)
	t.Cleanup(n.stop)
	rec := newNotifierRecorder(t, n)

	n.notify()
	first := rec.next(t)

	// After the window has elapsed, the next change is again delivered
	// immediately (leading edge), with a strictly greater generation.
	time.Sleep(60 * time.Millisecond)
	n.notify()
	second := rec.next(t)

	assert.Greater(t, second, first, "generations must be strictly increasing across deliveries")
}

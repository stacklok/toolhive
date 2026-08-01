// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package streamable

import (
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/exp/jsonrpc2"
)

func newTestNotification(t *testing.T, method string) jsonrpc2.Message {
	t.Helper()
	msg, err := jsonrpc2.NewNotification(method, map[string]any{"ok": true})
	require.NoError(t, err)
	return msg
}

// recvWithTimeout receives a single message from ch or fails the test after a
// bounded wait, per the "always add timeouts to blocking barriers" testing rule.
func recvWithTimeout(t *testing.T, ch <-chan jsonrpc2.Message) jsonrpc2.Message {
	t.Helper()
	select {
	case msg := <-ch:
		return msg
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for dispatched message")
		return nil
	}
}

// assertNoDelivery proves ch does not receive a message within a short bounded
// wait. It is intentionally shorter than recvWithTimeout's timeout since it is
// expected to always run to completion (there is nothing to wait on).
func assertNoDelivery(t *testing.T, ch <-chan jsonrpc2.Message) {
	t.Helper()
	select {
	case msg := <-ch:
		t.Fatalf("expected no delivery, but received: %v", msg)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestServerStreamRegistry_RegisterDispatchDeregister(t *testing.T) {
	t.Parallel()

	r := newServerStreamRegistry()
	assert.Equal(t, 0, r.streamCount(), "registry should start empty")

	s := r.register("session-1")
	assert.Equal(t, 1, r.streamCount())

	msg := newTestNotification(t, "notifications/progress")
	r.broadcast(msg)

	got := recvWithTimeout(t, s.data)
	assert.Equal(t, msg, got)

	r.deregister("session-1", s)
	assert.Equal(t, 0, r.streamCount())

	// A dispatch after deregister must not reach the evicted stream's data
	// channel (the channel itself is never closed -- see serverStream's doc).
	r.broadcast(newTestNotification(t, "notifications/progress"))
	assertNoDelivery(t, s.data)
}

func TestServerStreamRegistry_PerSessionSingleDelivery(t *testing.T) {
	t.Parallel()

	r := newServerStreamRegistry()
	s1 := r.register("session-1")
	s2 := r.register("session-2")
	t.Cleanup(func() {
		r.deregister("session-1", s1)
		r.deregister("session-2", s2)
	})

	msg := newTestNotification(t, "notifications/resources/updated")
	r.broadcast(msg)

	assert.Equal(t, msg, recvWithTimeout(t, s1.data))
	assert.Equal(t, msg, recvWithTimeout(t, s2.data))

	// Each session must receive exactly one copy -- no duplicate second delivery.
	assertNoDelivery(t, s1.data)
	assertNoDelivery(t, s2.data)
}

func TestServerStreamRegistry_OneStreamPerSessionEvicts(t *testing.T) {
	t.Parallel()

	r := newServerStreamRegistry()
	first := r.register("session-1")

	second := r.register("session-1")
	t.Cleanup(func() { r.deregister("session-1", second) })

	assert.Equal(t, 1, r.streamCount(), "second register must evict, not add, an entry")

	// The first stream's stop channel must be closed to signal eviction.
	select {
	case _, ok := <-first.stop:
		assert.False(t, ok, "first stream's stop channel must be closed")
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for evicted stream's stop channel to close")
	}

	msg := newTestNotification(t, "notifications/progress")
	r.broadcast(msg)

	// Only the second (current) stream receives the dispatched message.
	assert.Equal(t, msg, recvWithTimeout(t, second.data))
	assertNoDelivery(t, first.data)
}

// TestServerStreamRegistry_FullChannelDropsWithoutBlocking proves dispatch
// never blocks on a full consumer, and that a slow stream does not prevent
// delivery to other, healthy streams.
func TestServerStreamRegistry_FullChannelDropsWithoutBlocking(t *testing.T) {
	t.Parallel()

	r := newServerStreamRegistry()
	full := r.register("full")
	healthy := r.register("healthy")
	t.Cleanup(func() {
		r.deregister("full", full)
		r.deregister("healthy", healthy)
	})

	// Saturate the "full" stream's buffer.
	for i := 0; i < serverStreamBufferSize; i++ {
		r.broadcast(newTestNotification(t, "notifications/progress"))
	}
	// Drain "healthy" of the same backlog so it isn't itself full for the
	// assertion below.
	for i := 0; i < serverStreamBufferSize; i++ {
		recvWithTimeout(t, healthy.data)
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		r.broadcast(newTestNotification(t, "notifications/message"))
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("dispatch blocked on a full channel instead of dropping")
	}

	// The healthy stream still receives the new message even though the
	// other stream's buffer was full.
	msg := recvWithTimeout(t, healthy.data)
	assert.NotNil(t, msg)
}

func TestServerStreamRegistry_DeregisterIsIdempotentAndCloseAllClears(t *testing.T) {
	t.Parallel()

	r := newServerStreamRegistry()
	sa := r.register("a")
	r.register("b")
	require.Equal(t, 2, r.streamCount())

	r.closeAll()
	assert.Equal(t, 0, r.streamCount())

	// Deregistering an already-removed stream is a no-op, not a panic.
	assert.NotPanics(t, func() { r.deregister("a", sa) })
}

// TestServerStreamRegistry_DispatchAfterDeregisterIsNoop proves a dispatch that
// races a deregister neither panics nor blocks: the removed stream simply
// does not receive the message.
func TestServerStreamRegistry_DispatchAfterDeregisterIsNoop(t *testing.T) {
	t.Parallel()

	r := newServerStreamRegistry()
	s := r.register("gone")
	r.deregister("gone", s)

	assert.NotPanics(t, func() {
		r.broadcast(newTestNotification(t, "notifications/progress"))
	})
	assert.Equal(t, 0, r.streamCount())
}

// TestServerStreamRegistry_ConcurrentDispatchAndEvict is the Blocker-1
// regression test: it stresses concurrent dispatch (which sends on
// serverStream.data) against concurrent register-on-same-session (which
// evicts, closing the evicted stream's stop channel), deregister, and a
// drainer reading data for each stream. Run with -race; the invariant under
// test is that data is never closed, so dispatch's send can never race a
// close and panic.
func TestServerStreamRegistry_ConcurrentDispatchAndEvict(t *testing.T) {
	t.Parallel()

	const (
		sessionID    = "stress-session"
		numWorkers   = 8
		numRounds    = 500
		waitTimeout  = 20 * time.Second
		drainTimeout = 5 * time.Second
	)

	r := newServerStreamRegistry()

	var workersWG sync.WaitGroup
	var drainersWG sync.WaitGroup
	testDone := make(chan struct{})

	// Dispatcher goroutines: hammer dispatch concurrently.
	for i := 0; i < numWorkers; i++ {
		workersWG.Add(1)
		go func() {
			defer workersWG.Done()
			for j := 0; j < numRounds; j++ {
				r.broadcast(newTestNotification(t, "notifications/progress"))
			}
		}()
	}

	// Register/evict/deregister goroutines: force eviction of the same session
	// repeatedly while dispatch is hammering it. Each newly registered stream
	// gets its own drainer so dispatch's non-blocking send has somewhere to go
	// in the common case, though correctness (no panic, no block) must hold
	// regardless of whether anything is draining.
	for i := 0; i < numWorkers; i++ {
		workersWG.Add(1)
		go func() {
			defer workersWG.Done()
			for j := 0; j < numRounds; j++ {
				s := r.register(sessionID)

				drainersWG.Add(1)
				go func(s *serverStream) {
					defer drainersWG.Done()
					for {
						select {
						case <-s.data:
						case <-s.stop:
							return
						case <-testDone:
							return
						}
					}
				}(s)

				r.deregister(sessionID, s)
			}
		}()
	}

	allWorkersDone := make(chan struct{})
	go func() {
		workersWG.Wait()
		close(allWorkersDone)
	}()

	select {
	case <-allWorkersDone:
	case <-time.After(waitTimeout):
		close(testDone)
		t.Fatal("concurrent dispatch/evict/deregister stress test timed out")
	}

	// Signal any drainer whose stream was never evicted (i.e. it was the last
	// one registered) to stop, then bound how long we wait for the drainers to
	// exit cleanly.
	close(testDone)
	allDrainersDone := make(chan struct{})
	go func() {
		drainersWG.Wait()
		close(allDrainersDone)
	}()
	select {
	case <-allDrainersDone:
	case <-time.After(drainTimeout):
		t.Fatal("drainer goroutines did not exit after testDone was closed")
	}

	// At most one stream (the last registered) should remain.
	assert.LessOrEqual(t, r.streamCount(), 1)
}

func TestServerStreamRegistry_ManySessionsStreamCount(t *testing.T) {
	t.Parallel()

	r := newServerStreamRegistry()
	streams := make([]*serverStream, 0, 10)
	for i := 0; i < 10; i++ {
		streams = append(streams, r.register("session-"+strconv.Itoa(i)))
	}
	t.Cleanup(func() {
		for i, s := range streams {
			r.deregister("session-"+strconv.Itoa(i), s)
		}
	})

	assert.Equal(t, 10, r.streamCount())
}

// TestServerStreamRegistry_DispatchToHitsOnlyTarget verifies dispatchTo
// delivers a message to the single stream registered for its route key, and
// no other currently-registered stream receives it -- the property routeProgress
// and routeResourceUpdated depend on to avoid cross-session leakage.
func TestServerStreamRegistry_DispatchToHitsOnlyTarget(t *testing.T) {
	t.Parallel()

	r := newServerStreamRegistry()
	target := r.register("target-session")
	other := r.register("other-session")
	t.Cleanup(func() {
		r.deregister("target-session", target)
		r.deregister("other-session", other)
	})

	msg := newTestNotification(t, "notifications/progress")
	r.dispatchTo("target-session", msg)

	assert.Equal(t, msg, recvWithTimeout(t, target.data))
	assertNoDelivery(t, other.data)
}

// TestServerStreamRegistry_DispatchToUnknownKeyIsNoop verifies dispatchTo for
// a route key with no registered stream neither panics nor blocks.
func TestServerStreamRegistry_DispatchToUnknownKeyIsNoop(t *testing.T) {
	t.Parallel()

	r := newServerStreamRegistry()
	assert.NotPanics(t, func() {
		r.dispatchTo("no-such-session", newTestNotification(t, "notifications/progress"))
	})
}

// TestServerStreamRegistry_DeliverToMany verifies deliverToMany delivers a
// message to every present stream among routeKeys, and does not deliver it to
// a registered stream whose session is NOT in routeKeys.
func TestServerStreamRegistry_DeliverToMany(t *testing.T) {
	t.Parallel()

	r := newServerStreamRegistry()
	subA := r.register("subscriber-a")
	subB := r.register("subscriber-b")
	notSub := r.register("not-a-subscriber")
	t.Cleanup(func() {
		r.deregister("subscriber-a", subA)
		r.deregister("subscriber-b", subB)
		r.deregister("not-a-subscriber", notSub)
	})

	msg := newTestNotification(t, "notifications/resources/updated")
	r.deliverToMany([]string{"subscriber-a", "subscriber-b", "unknown-session"}, msg)

	assert.Equal(t, msg, recvWithTimeout(t, subA.data))
	assert.Equal(t, msg, recvWithTimeout(t, subB.data))
	assertNoDelivery(t, notSub.data)
}

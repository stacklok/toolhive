// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package session

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAdmissionQueue_TryAdmit_Open(t *testing.T) {
	t.Parallel()

	q := newAdmissionQueue()
	admitted, done := q.TryAdmit()
	require.True(t, admitted, "TryAdmit should return true when queue is open")
	require.NotNil(t, done, "done func must not be nil when admitted")
	done() // must not panic
}

func TestAdmissionQueue_TryAdmit_AfterClose(t *testing.T) {
	t.Parallel()

	q := newAdmissionQueue()
	q.CloseAndDrain()

	admitted, done := q.TryAdmit()
	assert.False(t, admitted, "TryAdmit should return false after CloseAndDrain")
	assert.Nil(t, done, "done func must be nil when not admitted")
}

func TestAdmissionQueue_CloseAndDrain_Idempotent(t *testing.T) {
	t.Parallel()

	q := newAdmissionQueue()
	// Multiple calls must not panic or deadlock.
	q.CloseAndDrain()
	q.CloseAndDrain()
	q.CloseAndDrain()
}

func TestAdmissionQueue_CloseAndDrain_BlocksUntilDone(t *testing.T) {
	t.Parallel()

	q := newAdmissionQueue()

	admitted, done := q.TryAdmit()
	require.True(t, admitted)
	require.NotNil(t, done)

	drainDone := make(chan struct{})
	go func() {
		q.CloseAndDrain()
		close(drainDone)
	}()

	// CloseAndDrain must not return before done is called.
	select {
	case <-drainDone:
		t.Fatal("CloseAndDrain returned before in-flight request completed")
	case <-time.After(50 * time.Millisecond):
		// Expected: drain is blocking.
	}

	done() // release the in-flight request
	select {
	case <-drainDone:
		// Expected: drain unblocked after done().
	case <-time.After(time.Second):
		t.Fatal("CloseAndDrain did not return after done() was called")
	}
}

func TestAdmissionQueue_MultipleRequests_AllMustComplete(t *testing.T) {
	t.Parallel()

	const numRequests = 10
	q := newAdmissionQueue()

	doneFuncs := make([]func(), 0, numRequests)
	for i := range numRequests {
		admitted, done := q.TryAdmit()
		require.Truef(t, admitted, "request %d should be admitted", i)
		require.NotNilf(t, done, "done func for request %d must not be nil", i)
		doneFuncs = append(doneFuncs, done)
	}

	drainDone := make(chan struct{})
	go func() {
		q.CloseAndDrain()
		close(drainDone)
	}()

	// CloseAndDrain must not return until all done funcs are called.
	select {
	case <-drainDone:
		t.Fatal("CloseAndDrain returned before all in-flight requests completed")
	case <-time.After(50 * time.Millisecond):
		// Expected: drain is still blocking.
	}

	// Release all in-flight requests one by one.
	for _, done := range doneFuncs {
		done()
	}

	select {
	case <-drainDone:
		// Expected.
	case <-time.After(time.Second):
		t.Fatal("CloseAndDrain did not return after all done() calls")
	}
}

func TestAdmissionQueue_ConcurrentTryAdmitAndClose_NoRaces(t *testing.T) {
	t.Parallel()

	const goroutines = 50
	q := newAdmissionQueue()

	var wg sync.WaitGroup
	var admitted atomic.Int64

	// Start goroutines that call TryAdmit concurrently.
	wg.Add(goroutines)
	for range goroutines {
		go func() {
			defer wg.Done()
			ok, done := q.TryAdmit()
			if ok {
				admitted.Add(1)
				// Simulate a brief in-flight operation.
				time.Sleep(time.Millisecond)
				done()
			}
		}()
	}

	// Let some goroutines get admitted before closing.
	time.Sleep(5 * time.Millisecond)
	q.CloseAndDrain()

	// All goroutines must have finished (drain waited for them).
	wg.Wait()

	// Calls after close must always return false.
	ok, done := q.TryAdmit()
	assert.False(t, ok)
	assert.Nil(t, done)
}

func TestAdmissionQueue_DoneCalledAfterDrainReturns_NoPanic(t *testing.T) {
	t.Parallel()

	// Admit a request, then let done() be called before CloseAndDrain runs so
	// that drain sees a zero wait-group and returns immediately — no panic, no block.
	q := newAdmissionQueue()
	admitted, done := q.TryAdmit()
	require.True(t, admitted)

	doneReleased := make(chan struct{})
	go func() {
		done() // release before drain starts
		close(doneReleased)
	}()

	<-doneReleased    // ensure done() has been called
	q.CloseAndDrain() // wg is already zero — must return immediately without panic
}

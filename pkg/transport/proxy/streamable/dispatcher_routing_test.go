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

// --- progress tokens ---

func TestSessionRouter_ProgressTokenRecordLookupDrop(t *testing.T) {
	t.Parallel()

	r := newSessionRouter()

	_, ok := r.lookupProgressToken("pt-1")
	assert.False(t, ok, "unrecorded token must not be found")

	deliver := make(chan jsonrpc2.Message, 1)
	r.recordProgressToken("pt-1", progressRoute{deliver: deliver, originalToken: "client-token"})

	route, ok := r.lookupProgressToken("pt-1")
	require.True(t, ok)
	assert.Equal(t, "client-token", route.originalToken)
	assert.Equal(t, deliver, route.deliver)

	r.dropProgressToken("pt-1")
	_, ok = r.lookupProgressToken("pt-1")
	assert.False(t, ok, "dropped token must no longer be found")

	// Dropping an unrecorded token is a no-op, not a panic.
	assert.NotPanics(t, func() { r.dropProgressToken("never-recorded") })
}

// --- subscriptions ---

func TestSessionRouter_AddSubscription_ReportsFirstForURI(t *testing.T) {
	t.Parallel()

	r := newSessionRouter()

	assert.True(t, r.addSubscription("uri-1", "sess-a"), "first subscriber for a uri must report first=true")
	assert.False(t, r.addSubscription("uri-1", "sess-b"), "second subscriber for the same uri must report first=false")
	assert.True(t, r.addSubscription("uri-2", "sess-a"), "first subscriber for a DIFFERENT uri must report first=true")

	assert.ElementsMatch(t, []string{"sess-a", "sess-b"}, r.subscribersOf("uri-1"))
	assert.ElementsMatch(t, []string{"sess-a"}, r.subscribersOf("uri-2"))
}

func TestSessionRouter_RemoveSubscription_ReportsLastForURI(t *testing.T) {
	t.Parallel()

	r := newSessionRouter()
	r.addSubscription("uri-1", "sess-a")
	r.addSubscription("uri-1", "sess-b")

	assert.False(t, r.removeSubscription("uri-1", "sess-a"), "removing a non-last subscriber must report last=false")
	assert.ElementsMatch(t, []string{"sess-b"}, r.subscribersOf("uri-1"))

	assert.True(t, r.removeSubscription("uri-1", "sess-b"), "removing the final subscriber must report last=true")
	assert.Nil(t, r.subscribersOf("uri-1"), "uri entry must be cleaned up once its last subscriber leaves")

	// Removing from a uri with no recorded subscribers at all is a no-op.
	assert.False(t, r.removeSubscription("never-subscribed", "sess-a"))
}

func TestSessionRouter_SubscribersOf_UnknownURI(t *testing.T) {
	t.Parallel()

	r := newSessionRouter()
	assert.Nil(t, r.subscribersOf("no-such-uri"))
}

// --- log level ---

func TestSessionRouter_SetLogLevel_MaxReconciliation(t *testing.T) {
	t.Parallel()

	r := newSessionRouter()

	maxChanged, newMax := r.setLogLevel("sess-a", 3)
	assert.True(t, maxChanged, "first session's level always changes the max")
	assert.Equal(t, 3, newMax)

	maxChanged, newMax = r.setLogLevel("sess-b", 1)
	assert.False(t, maxChanged, "a LOWER verbosity from another session must not change the max")
	assert.Equal(t, 3, newMax)

	maxChanged, newMax = r.setLogLevel("sess-b", 7)
	assert.True(t, maxChanged, "a HIGHER verbosity must raise the max")
	assert.Equal(t, 7, newMax)

	// Lowering sess-b (the current max holder) without removing sess-a (rank 3)
	// must NOT drop the max below 3.
	maxChanged, newMax = r.setLogLevel("sess-b", 0)
	assert.True(t, maxChanged)
	assert.Equal(t, 3, newMax, "max must fall back to the remaining highest tracked session")

	r.dropLogLevel("sess-a")
	r.dropLogLevel("sess-b")
	// Dropping already-removed sessions is a no-op.
	assert.NotPanics(t, func() { r.dropLogLevel("sess-a") })
}

// --- purgeSession ---

func TestSessionRouter_PurgeSession(t *testing.T) {
	t.Parallel()

	r := newSessionRouter()
	r.addSubscription("uri-1", "sess-a")
	r.addSubscription("uri-1", "sess-b")
	r.addSubscription("uri-2", "sess-a")
	r.setLogLevel("sess-a", 7)
	r.setLogLevel("sess-b", 2)

	maxChanged, newMax := r.purgeSession("sess-a")
	assert.True(t, maxChanged, "removing the max-rank session must change the max")
	assert.Equal(t, 2, newMax)

	assert.ElementsMatch(t, []string{"sess-b"}, r.subscribersOf("uri-1"))
	assert.Nil(t, r.subscribersOf("uri-2"), "uri-2 had only sess-a, so it must be fully removed")

	// Purging a session with no tracked state at all reports no change.
	maxChanged, newMax = r.purgeSession("never-tracked")
	assert.False(t, maxChanged)
	assert.Equal(t, 2, newMax)
}

// --- reap ---

func TestSessionRouter_Reap_DropsInactiveSessionState(t *testing.T) {
	t.Parallel()

	r := newSessionRouter()
	r.addSubscription("uri-1", "active-sess")
	r.addSubscription("uri-1", "expired-sess")
	r.addSubscription("uri-2", "expired-sess")
	r.setLogLevel("active-sess", 3)
	r.setLogLevel("expired-sess", 7)

	active := map[string]bool{"active-sess": true, "expired-sess": false}
	isActive := func(sess string) bool { return active[sess] }

	r.reap(isActive)

	assert.ElementsMatch(t, []string{"active-sess"}, r.subscribersOf("uri-1"))
	assert.Nil(t, r.subscribersOf("uri-2"), "uri-2 had only the expired session, so it must be fully removed")

	_, newMax := r.setLogLevel("active-sess", 3) // re-set to read back the max without mutating anything else
	assert.Equal(t, 3, newMax, "expired session's log level must have been reaped")
}

func TestSessionRouter_Reap_NoStaleSessionsIsNoop(t *testing.T) {
	t.Parallel()

	r := newSessionRouter()
	r.addSubscription("uri-1", "sess-a")
	r.setLogLevel("sess-a", 5)

	r.reap(func(string) bool { return true })

	assert.ElementsMatch(t, []string{"sess-a"}, r.subscribersOf("uri-1"))
}

// TestSessionRouter_ConcurrentAccess hammers all three registries
// concurrently across many goroutines and sessions/URIs. Run with -race: the
// invariant under test is that each registry's own mutex fully serializes
// access to it (see the "one synchronization primitive per data structure"
// style rule) -- there is no cross-registry invariant to check here, only the
// absence of races/panics/deadlocks.
func TestSessionRouter_ConcurrentAccess(t *testing.T) {
	t.Parallel()

	const (
		numWorkers = 16
		numRounds  = 200
	)

	r := newSessionRouter()
	var wg sync.WaitGroup

	for i := 0; i < numWorkers; i++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			sess := "sess-" + strconv.Itoa(worker%4)
			uri := "uri-" + strconv.Itoa(worker%3)
			ptGlobal := "pt-" + strconv.Itoa(worker)

			for j := 0; j < numRounds; j++ {
				r.addSubscription(uri, sess)
				r.subscribersOf(uri)
				r.removeSubscription(uri, sess)

				r.setLogLevel(sess, j%8)
				r.dropLogLevel(sess)

				r.recordProgressToken(ptGlobal, progressRoute{deliver: make(chan jsonrpc2.Message, 1), originalToken: j})
				r.lookupProgressToken(ptGlobal)
				r.dropProgressToken(ptGlobal)

				r.purgeSession(sess)
				r.reap(func(string) bool { return true })
			}
		}(i)
	}

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("concurrent sessionRouter stress test timed out")
	}
}

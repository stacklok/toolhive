// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package callbinding

import (
	"context"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testClock is a settable clock for MemoryRecord.
type testClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *testClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func newTestRecord() (*MemoryRecord, *testClock) {
	clock := &testClock{now: time.Unix(1_800_000_000, 0)}
	record := NewMemoryRecord()
	record.now = clock.Now
	return record, clock
}

func TestMemoryRecordStartsEachKeyOnce(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	record, clock := newTestRecord()
	call := Call{SessionID: "s", RequestID: "7", Started: clock.Now()}
	expiry := clock.Now().Add(time.Minute)

	started, err := record.Start(ctx, "k", call, expiry)
	require.NoError(t, err)
	assert.True(t, started)

	got, ok, err := record.Lookup(ctx, "k")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, call, got)

	started, err = record.Start(ctx, "k", Call{SessionID: "other"}, expiry.Add(time.Hour))
	require.NoError(t, err)
	assert.False(t, started, "a key starts one call")
	got, _, err = record.Lookup(ctx, "k")
	require.NoError(t, err)
	assert.Equal(t, call, got, "a refused Start changes nothing")

	started, err = record.Start(ctx, "other-key", call, expiry)
	require.NoError(t, err)
	assert.True(t, started, "keys are independent")
}

func TestMemoryRecordEndKeepsKeyUsed(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	record, clock := newTestRecord()
	expiry := clock.Now().Add(time.Minute)

	started, err := record.Start(ctx, "k", Call{RequestID: "1"}, expiry)
	require.NoError(t, err)
	require.True(t, started)
	require.NoError(t, record.End(ctx, "k"))
	require.NoError(t, record.End(ctx, "k"), "ending twice is not an error")
	require.NoError(t, record.End(ctx, "unknown"), "ending an unknown key is not an error")

	got, ok, err := record.Lookup(ctx, "k")
	require.NoError(t, err)
	require.True(t, ok, "an ended call stays recorded until expiry")
	assert.True(t, got.Ended)
	assert.Equal(t, "1", got.RequestID)

	started, err = record.Start(ctx, "k", Call{RequestID: "2"}, expiry)
	require.NoError(t, err)
	assert.False(t, started, "an ended call still uses up its key until expiry")
}

func TestMemoryRecordReleaseFreesKey(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	record, clock := newTestRecord()
	expiry := clock.Now().Add(time.Minute)

	started, err := record.Start(ctx, "k", Call{RequestID: "1"}, expiry)
	require.NoError(t, err)
	require.True(t, started)
	started, err = record.Start(ctx, "k", Call{RequestID: "2"}, expiry)
	require.NoError(t, err)
	require.False(t, started, "a recorded key stays used until released")

	require.NoError(t, record.Release(ctx, "k"))
	require.NoError(t, record.Release(ctx, "unknown"), "releasing an unknown key is not an error")
	_, ok, err := record.Lookup(ctx, "k")
	require.NoError(t, err)
	assert.False(t, ok, "a released call is forgotten")

	started, err = record.Start(ctx, "k", Call{RequestID: "3"}, expiry)
	require.NoError(t, err)
	assert.True(t, started, "a released key can start a call again")
}

func TestMemoryRecordExpiry(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	record, clock := newTestRecord()

	started, err := record.Start(ctx, "k", Call{RequestID: "1"}, clock.Now().Add(time.Minute))
	require.NoError(t, err)
	require.True(t, started)

	clock.Advance(time.Minute - time.Nanosecond)
	_, ok, err := record.Lookup(ctx, "k")
	require.NoError(t, err)
	assert.True(t, ok, "recorded until the expiry")

	clock.Advance(time.Nanosecond)
	_, ok, err = record.Lookup(ctx, "k")
	require.NoError(t, err)
	assert.False(t, ok, "forgotten at the expiry")

	started, err = record.Start(ctx, "k", Call{RequestID: "2"}, clock.Now().Add(time.Minute))
	require.NoError(t, err)
	assert.True(t, started, "an expired key can be recorded again")
}

func TestMemoryRecordSweepsExpiredEntries(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	record, clock := newTestRecord()

	for i := range 10 {
		_, err := record.Start(ctx, strconv.Itoa(i), Call{}, clock.Now().Add(time.Second))
		require.NoError(t, err)
	}
	clock.Advance(memorySweepInterval)
	_, err := record.Start(ctx, "fresh", Call{}, clock.Now().Add(time.Hour))
	require.NoError(t, err)

	record.mu.Lock()
	defer record.mu.Unlock()
	assert.Len(t, record.entries, 1, "expired entries are dropped")
}

// TestMemoryRecordConcurrentStart checks that of many concurrent Starts for
// one key exactly one wins. Run it with -race.
func TestMemoryRecordConcurrentStart(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	record := NewMemoryRecord()
	expiry := time.Now().Add(time.Minute)

	const workers = 64
	var wins atomic.Int32
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			started, err := record.Start(ctx, "k", Call{RequestID: strconv.Itoa(i)}, expiry)
			assert.NoError(t, err)
			if started {
				wins.Add(1)
			}
			_, _, err = record.Lookup(ctx, "k")
			assert.NoError(t, err)
			assert.NoError(t, record.End(ctx, "other"))
		}()
	}
	close(start)
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		require.FailNow(t, "concurrent Starts did not finish")
	}
	assert.Equal(t, int32(1), wins.Load())
}

// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package authserver

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/authserver/storage"
)

// fakeClock is a settable clock for tests that must not sleep.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func TestRejectedRefreshes_NilAndZeroValuesMiss(t *testing.T) {
	t.Parallel()

	var nilMemory *rejectedRefreshes
	nilMemory.add(storage.UpstreamTokenRowID("row"), "rt", errors.New("rejected"))
	assert.Nil(t, nilMemory.lookup(storage.UpstreamTokenRowID("row"), "rt"))

	zero := &rejectedRefreshes{}
	zero.add(storage.UpstreamTokenRowID("row"), "rt", errors.New("rejected"))
	assert.Nil(t, zero.lookup(storage.UpstreamTokenRowID("row"), "rt"))
}

func TestRejectedRefreshes_AddAndLookup(t *testing.T) {
	t.Parallel()

	clock := newFakeClock()
	memory, err := newRejectedRefreshes(10, time.Minute, clock.Now)
	require.NoError(t, err)

	want := errors.New("rejected")
	memory.add("row-a", "rt", want)

	assert.Same(t, want, memory.lookup("row-a", "rt"))

	assert.Nil(t, memory.lookup("row-a", "other-rt"), "a different refresh token must not match")
	assert.Nil(t, memory.lookup("row-b", "rt"), "a different row must not match")
}

func TestRejectedRefreshes_ExpiresOnLookup(t *testing.T) {
	t.Parallel()

	clock := newFakeClock()
	memory, err := newRejectedRefreshes(10, time.Minute, clock.Now)
	require.NoError(t, err)
	memory.add("row", "rt", errors.New("rejected"))

	clock.Advance(time.Minute - time.Second)
	assert.NotNil(t, memory.lookup("row", "rt"))

	clock.Advance(time.Second)
	assert.Nil(t, memory.lookup("row", "rt"), "entry must expire once the TTL has passed")
	assert.Zero(t, memory.cache.Len(), "an expired entry is removed on lookup")
}

func TestRejectedRefreshes_EvictsOldestAtCapacity(t *testing.T) {
	t.Parallel()

	memory, err := newRejectedRefreshes(2, time.Minute, newFakeClock().Now)
	require.NoError(t, err)
	memory.add("row-1", "rt", errors.New("1"))
	memory.add("row-2", "rt", errors.New("2"))
	memory.add("row-3", "rt", errors.New("3"))

	assert.Nil(t, memory.lookup("row-1", "rt"))
	assert.NotNil(t, memory.lookup("row-3", "rt"))
}

func TestNewRejectedRefreshes_RejectsInvalidInput(t *testing.T) {
	t.Parallel()

	for _, size := range []int{0, -1} {
		_, err := newRejectedRefreshes(size, time.Minute, time.Now)
		require.Error(t, err, "size %d", size)
	}
	_, err := newRejectedRefreshes(1, 0, time.Now)
	require.Error(t, err, "a zero TTL would remember nothing")
}

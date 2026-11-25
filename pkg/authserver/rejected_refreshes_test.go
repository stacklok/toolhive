// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package authserver

import (
	"errors"
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/oauth2"

	"github.com/stacklok/toolhive/pkg/authserver/storage"
	"github.com/stacklok/toolhive/pkg/authserver/upstream"
	"github.com/stacklok/toolhive/pkg/oauthproto/oautherr"
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

// rejectionErr builds the error an upstream provider returns for a rejected
// refresh.
func rejectionErr() error {
	return fmt.Errorf("upstream refresh failed: %w - secret provider text: %w", upstream.ErrGrantRejected,
		&oauth2.RetrieveError{
			Response:         &http.Response{StatusCode: http.StatusBadRequest, Status: http.StatusText(http.StatusBadRequest)},
			ErrorCode:        "invalid_grant",
			ErrorDescription: "secret provider text",
			Body:             []byte("secret provider body"),
		})
}

var testTokenExpiry = time.Unix(1_700_000_000, 0)

func tok(refreshToken, accessToken string) *storage.UpstreamTokens {
	return &storage.UpstreamTokens{RefreshToken: refreshToken, AccessToken: accessToken, ExpiresAt: testTokenExpiry}
}

func TestRejectedRefreshes_NilAndZeroValuesMiss(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		memory *rejectedRefreshes
	}{
		{name: "nil", memory: nil},
		{name: "zero", memory: &rejectedRefreshes{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			tt.memory.add(0, "row", tok("rt", "at"), rejectionErr())
			assert.Nil(t, tt.memory.lookup(0, "row", tok("rt", "at")))
		})
	}
}

func TestRejectedRefreshes_AddAndLookup(t *testing.T) {
	t.Parallel()

	clock := newFakeClock()
	memory, err := newRejectedRefreshes(10, time.Minute, clock.Now)
	require.NoError(t, err)
	memory.add(1, "row-a", tok("rt", "at"), rejectionErr())

	require.ErrorIs(t, memory.lookup(1, "row-a", tok("rt", "at")), upstream.ErrGrantRejected)

	assert.Nil(t, memory.lookup(1, "row-a", tok("other-rt", "at")), "a different refresh token must not match")
	assert.Nil(t, memory.lookup(1, "row-a", tok("rt", "other-at")), "a different access token must not match")
	sameTokensNewExpiry := tok("rt", "at")
	sameTokensNewExpiry.ExpiresAt = sameTokensNewExpiry.ExpiresAt.Add(time.Second)
	assert.Nil(t, memory.lookup(1, "row-a", sameTokensNewExpiry), "a different access token expiry must not match")
	assert.Nil(t, memory.lookup(1, "row-b", tok("rt", "at")), "a different row must not match")
	assert.Nil(t, memory.lookup(2, "row-a", tok("rt", "at")), "a different generation must not match")
}

func TestRejectedRefreshes_DigestSeparatesTokens(t *testing.T) {
	t.Parallel()

	memory, err := newRejectedRefreshes(10, time.Minute, newFakeClock().Now)
	require.NoError(t, err)
	memory.add(0, "row", tok("ab", "c"), rejectionErr())

	assert.Nil(t, memory.lookup(0, "row", tok("a", "bc")), "moving a byte between the tokens must not match")
	assert.NotNil(t, memory.lookup(0, "row", tok("ab", "c")))
}

func TestRejectedRefreshes_LookupBuildsFreshError(t *testing.T) {
	t.Parallel()

	memory, err := newRejectedRefreshes(10, time.Minute, newFakeClock().Now)
	require.NoError(t, err)
	memory.add(0, "row", tok("rt", "at"), rejectionErr())

	first := memory.lookup(0, "row", tok("rt", "at"))
	second := memory.lookup(0, "row", tok("rt", "at"))
	require.Error(t, first)
	require.Error(t, second)
	assert.NotSame(t, first, second)

	require.ErrorIs(t, first, upstream.ErrGrantRejected)
	assert.Contains(t, first.Error(), "remembered")
	assert.True(t, isRememberedRejection(first))
	assert.True(t, oautherr.IsRejectedRefreshGrant(first))

	got, ok := errors.AsType[*oauth2.RetrieveError](first)
	require.True(t, ok)
	assert.Equal(t, "invalid_grant", got.ErrorCode)
	require.NotNil(t, got.Response)
	assert.Equal(t, http.StatusBadRequest, got.Response.StatusCode)
	assert.Nil(t, got.Body)
	assert.Empty(t, got.ErrorDescription)
	assert.NotContains(t, first.Error(), "secret provider")
}

func TestRejectedRefreshes_DoesNotRememberWithoutRetrieveError(t *testing.T) {
	t.Parallel()

	memory, err := newRejectedRefreshes(10, time.Minute, newFakeClock().Now)
	require.NoError(t, err)

	memory.add(0, "row", tok("rt", "at"), fmt.Errorf("plain: %w", upstream.ErrGrantRejected))
	memory.add(0, "row", tok("rt2", "at"), fmt.Errorf("no response: %w",
		&oauth2.RetrieveError{ErrorCode: "invalid_grant"}))
	memory.add(0, "row", tok("rt3", "at"), nil)

	assert.Zero(t, memory.cache.Len())
}

func TestRejectedRefreshes_ExpiresOnLookup(t *testing.T) {
	t.Parallel()

	clock := newFakeClock()
	memory, err := newRejectedRefreshes(10, time.Minute, clock.Now)
	require.NoError(t, err)
	memory.add(0, "row", tok("rt", "at"), rejectionErr())

	clock.Advance(time.Minute - time.Second)
	assert.NotNil(t, memory.lookup(0, "row", tok("rt", "at")))

	clock.Advance(time.Second)
	assert.Nil(t, memory.lookup(0, "row", tok("rt", "at")), "entry must expire once the TTL has passed")
	assert.Zero(t, memory.cache.Len(), "an expired entry is removed on lookup")
}

func TestRejectedRefreshes_EvictsOldestAtCapacity(t *testing.T) {
	t.Parallel()

	memory, err := newRejectedRefreshes(2, time.Minute, newFakeClock().Now)
	require.NoError(t, err)
	for _, row := range []storage.UpstreamTokenRowID{"row-1", "row-2", "row-3"} {
		memory.add(0, row, tok("rt", "at"), rejectionErr())
	}

	assert.Nil(t, memory.lookup(0, "row-1", tok("rt", "at")))
	assert.NotNil(t, memory.lookup(0, "row-3", tok("rt", "at")))
}

func TestNewRejectedRefreshes_RejectsInvalidInput(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		size int
		ttl  time.Duration
	}{
		{name: "zero size", size: 0, ttl: time.Minute},
		{name: "negative size", size: -1, ttl: time.Minute},
		{name: "zero ttl would remember nothing", size: 1, ttl: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := newRejectedRefreshes(tt.size, tt.ttl, time.Now)
			require.Error(t, err)
		})
	}
}

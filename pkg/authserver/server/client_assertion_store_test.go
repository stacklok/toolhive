// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"context"
	"math"
	"net/http"
	"testing"
	"time"

	"github.com/ory/fosite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// recordingAssertionStore is hand-written rather than gomock: storage/mocks
// imports storage, which imports server, so using it here is an import cycle.
type recordingAssertionStore struct {
	fosite.Storage // unused methods panic if reached
	setCalls       int
	deadline       time.Time // the deadline of the last SetClientAssertionJWT call
}

func (s *recordingAssertionStore) SetClientAssertionJWT(_ context.Context, _ string, deadline time.Time) error {
	s.setCalls++
	s.deadline = deadline
	return nil
}

type recordingPARStore struct {
	recordingAssertionStore
	fosite.PARStorage // unused methods panic if reached
}

func TestClientAssertionExpiryStore_SetClientAssertionJWT(t *testing.T) {
	t.Parallel()

	now := time.Unix(1_800_000_000, 0).Add(250 * time.Millisecond)
	retention := ClientAssertionRetainUntil(now).Sub(now)
	const (
		expiredHint = "has expired or has no 'exp'"
		aheadHint   = "must be at most"
	)
	tests := []struct {
		name     string
		exp      time.Time
		wantHint string // empty means accepted
	}{
		{name: "zero Unix time", exp: time.Unix(0, 0), wantHint: expiredHint},
		{name: "zero time.Time", exp: time.Time{}, wantHint: expiredHint},
		{name: "wrapped overflow at MaxInt64", exp: time.Unix(math.MaxInt64, 0), wantHint: expiredHint},
		{name: "a day in the past", exp: now.Add(-24 * time.Hour), wantHint: expiredHint},
		{name: "exactly at the end of the retention", exp: now.Add(-retention), wantHint: expiredHint},
		{name: "one second inside the retention", exp: now.Add(-retention + time.Second)},
		{name: "half a second past a whole-second exp", exp: now.Add(-500 * time.Millisecond)},
		{name: "exactly now", exp: now},
		{name: "in the future", exp: now.Add(time.Minute)},
		{name: "exactly the maximum ahead", exp: now.Add(ClientAssertionMaxExpiryAhead)},
		{name: "one nanosecond beyond the maximum ahead", exp: now.Add(ClientAssertionMaxExpiryAhead + time.Nanosecond), wantHint: aheadHint},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			inner := &recordingAssertionStore{}
			store := newClientAssertionExpiryStore(inner, func() time.Time { return now })

			err := store.SetClientAssertionJWT(context.Background(), "jti", tt.exp)

			if tt.wantHint == "" {
				require.NoError(t, err)
				assert.Equal(t, 1, inner.setCalls)
				assert.True(t, ClientAssertionRetainUntil(tt.exp).Equal(inner.deadline),
					"the backend must get the marker deadline, not the exp: got %s", inner.deadline)
				return
			}
			var rfcErr *fosite.RFC6749Error
			require.ErrorAs(t, err, &rfcErr)
			assert.Equal(t, fosite.ErrInvalidClient.ErrorField, rfcErr.ErrorField)
			assert.Equal(t, http.StatusUnauthorized, rfcErr.CodeField)
			assert.Contains(t, rfcErr.HintField, tt.wantHint)
			assert.Zero(t, inner.setCalls, "a refused marker must not reach the backend")
		})
	}
}

func TestClientAssertionExpiryStore_Capabilities(t *testing.T) {
	t.Parallel()

	clock := time.Now

	t.Run("PARStorage is forwarded when the backend has it", func(t *testing.T) {
		t.Parallel()
		_, ok := newClientAssertionExpiryStore(&recordingPARStore{}, clock).(fosite.PARStorage)
		assert.True(t, ok)
	})

	t.Run("PARStorage is absent when the backend lacks it", func(t *testing.T) {
		t.Parallel()
		_, ok := newClientAssertionExpiryStore(&recordingAssertionStore{}, clock).(fosite.PARStorage)
		assert.False(t, ok)
	})

	t.Run("the PAR variant still checks exp", func(t *testing.T) {
		t.Parallel()
		inner := &recordingPARStore{}
		store := newClientAssertionExpiryStore(inner, clock)
		require.Error(t, store.SetClientAssertionJWT(context.Background(), "jti", time.Unix(0, 0)))
		assert.Zero(t, inner.setCalls)
	})
}

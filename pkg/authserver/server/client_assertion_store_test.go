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
}

func (s *recordingAssertionStore) SetClientAssertionJWT(context.Context, string, time.Time) error {
	s.setCalls++
	return nil
}

type recordingPARStore struct {
	recordingAssertionStore
	fosite.PARStorage // unused methods panic if reached
}

func TestClientAssertionExpiryStore_SetClientAssertionJWT(t *testing.T) {
	t.Parallel()

	now := time.Unix(1_800_000_000, 0).Add(250 * time.Millisecond)
	tests := []struct {
		name    string
		exp     time.Time
		refused bool
	}{
		{name: "zero Unix time", exp: time.Unix(0, 0), refused: true},
		{name: "zero time.Time", exp: time.Time{}, refused: true},
		{name: "wrapped overflow at MaxInt64", exp: time.Unix(math.MaxInt64, 0), refused: true},
		{name: "a day in the past", exp: now.Add(-24 * time.Hour), refused: true},
		{name: "half a second past a whole-second exp", exp: now.Add(-500 * time.Millisecond), refused: true},
		{name: "exactly now", exp: now, refused: true},
		{name: "one nanosecond ahead", exp: now.Add(time.Nanosecond)},
		{name: "in the future", exp: now.Add(time.Minute)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			inner := &recordingAssertionStore{}
			store := newClientAssertionExpiryStore(inner, func() time.Time { return now })

			err := store.SetClientAssertionJWT(context.Background(), "jti", tt.exp)

			if !tt.refused {
				require.NoError(t, err)
				assert.Equal(t, 1, inner.setCalls)
				return
			}
			var rfcErr *fosite.RFC6749Error
			require.ErrorAs(t, err, &rfcErr)
			assert.Equal(t, fosite.ErrInvalidClient.ErrorField, rfcErr.ErrorField)
			assert.Equal(t, http.StatusUnauthorized, rfcErr.CodeField)
			assert.Contains(t, rfcErr.HintField, "'exp' in the future")
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

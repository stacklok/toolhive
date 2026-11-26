// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

//nolint:paralleltest // parallel execution handled by the withStorage and withRedisStorage helpers
package storage

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestPendingAuthorization_SkippedUpstreamsRoundTrip pins that every
// PendingAuthorizationStorage backend stores and loads SkippedUpstreams
// unchanged, and that a pending written without the field loads with none skipped.
func TestPendingAuthorization_SkippedUpstreamsRoundTrip(t *testing.T) {
	t.Parallel()

	t.Run("memory", func(t *testing.T) {
		withStorage(t, func(ctx context.Context, s *MemoryStorage) {
			assertSkippedUpstreamsRoundTrip(ctx, t, s)
		})
	})
	t.Run("redis", func(t *testing.T) {
		withRedisStorage(t, func(ctx context.Context, s *RedisStorage, _ *miniredis.Miniredis) {
			assertSkippedUpstreamsRoundTrip(ctx, t, s)
		})
	})
}

func assertSkippedUpstreamsRoundTrip(ctx context.Context, t *testing.T, s PendingAuthorizationStorage) {
	t.Helper()
	cases := map[string][]string{
		"with-skipped": {"provider-2", "provider-3"},
		"without":      nil,
	}
	for state, skipped := range cases {
		pending := &PendingAuthorization{
			ClientID:             "test-client",
			RedirectURI:          "https://example.com/callback",
			ConsentStage:         ConsentStageApproved,
			UpstreamProviderName: "provider-3",
			ChainUpstreams:       []string{"provider-1", "provider-2", "provider-3"},
			SkippedUpstreams:     skipped,
			CreatedAt:            time.Now(),
		}
		require.NoError(t, s.StorePendingAuthorization(ctx, state, pending))

		loaded, err := s.LoadPendingAuthorization(ctx, state)
		require.NoError(t, err)
		if skipped == nil {
			assert.Empty(t, loaded.SkippedUpstreams, "a pending written without the field loads with none skipped")
		} else {
			assert.Equal(t, skipped, loaded.SkippedUpstreams)
		}
		assert.Equal(t, pending.ChainUpstreams, loaded.ChainUpstreams)
	}
}

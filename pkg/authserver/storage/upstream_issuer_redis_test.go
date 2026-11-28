// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package storage

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRedisStorage_UpstreamTokensIssuerRoundTrip(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	s, mr := newTestRedisStorage(t)
	t.Cleanup(func() { _ = s.Close() })

	initial := &UpstreamTokens{
		ProviderID:       "default",
		Issuer:           "https://idp-a.example.com",
		AccessToken:      "at-1",
		RefreshToken:     "rt-1",
		IDToken:          "id-1",
		ExpiresAt:        time.Now().Add(time.Hour).Truncate(time.Second),
		SessionExpiresAt: time.Now().Add(24 * time.Hour).Truncate(time.Second),
		UserID:           "user-1",
		UpstreamSubject:  "sub-1",
		ClientID:         "client-1",
	}
	require.NoError(t, s.StoreUpstreamTokens(ctx, "sess-1", "default", initial))

	got, err := s.GetUpstreamTokens(ctx, "sess-1", "default")
	require.NoError(t, err)
	assert.Equal(t, "https://idp-a.example.com", got.Issuer)

	latest, err := s.GetLatestUpstreamTokensForUser(ctx, "user-1", "default")
	require.NoError(t, err)
	assert.Equal(t, "https://idp-a.example.com", latest.Issuer)

	updated := *got
	updated.AccessToken = "at-2"
	updated.RefreshToken = "rt-2"
	require.NoError(t, s.CompareAndSwapUpstreamTokens(ctx, "sess-1", "default", "rt-1", &updated))

	afterCAS, err := s.GetUpstreamTokens(ctx, "sess-1", "default")
	require.NoError(t, err)
	assert.Equal(t, "https://idp-a.example.com", afterCAS.Issuer)

	// Legacy JSON rows without "issuer" decode with empty Issuer.
	legacyJSON, err := json.Marshal(map[string]any{
		"provider_id":        "legacy",
		"access_token":       "legacy-at",
		"refresh_token":      "legacy-rt",
		"expires_at":         time.Now().Add(time.Hour).Unix(),
		"session_expires_at": time.Now().Add(24 * time.Hour).Unix(),
		"user_id":            "user-legacy",
		"upstream_subject":   "sub-legacy",
		"client_id":          "client-legacy",
	})
	require.NoError(t, err)
	legacyKey := redisUpstreamKey(s.keyPrefix, "sess-legacy", "legacy")
	require.NoError(t, mr.Set(legacyKey, string(legacyJSON)))

	legacyGot, err := s.GetUpstreamTokens(ctx, "sess-legacy", "legacy")
	require.NoError(t, err)
	assert.Empty(t, legacyGot.Issuer)
	assert.True(t, UpstreamIssuerMatches(legacyGot.Issuer, "https://idp-a.example.com"))
}

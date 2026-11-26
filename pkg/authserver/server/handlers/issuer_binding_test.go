// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package handlers

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/authserver/storage"
	"github.com/stacklok/toolhive/pkg/authserver/upstream"
)

func TestHandler_CarryForwardIssuerBinding(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name           string
		priorIssuer    string
		currentIssuer  string
		priorSubject   string
		currentSubject string
		synthetic      bool
		wantCarry      bool
	}{
		{
			name:           "matching non-empty issuer carries forward RT",
			priorIssuer:    "https://idp-a.example.com",
			currentIssuer:  "https://idp-a.example.com",
			priorSubject:   "user-1",
			currentSubject: "user-1",
			wantCarry:      true,
		},
		{
			name:           "mismatched issuer refuses RT carry-forward even when subjects match",
			priorIssuer:    "https://idp-a.example.com",
			currentIssuer:  "https://idp-b.example.com",
			priorSubject:   "user-1",
			currentSubject: "user-1",
			wantCarry:      false,
		},
		{
			name:           "mismatched issuer refuses synthetic RT carry-forward",
			priorIssuer:    "https://oauth-a.example.com/token",
			currentIssuer:  "https://oauth-b.example.com/token",
			priorSubject:   "tk-old",
			currentSubject: "tk-new",
			synthetic:      true,
			wantCarry:      false,
		},
		{
			name:           "legacy empty prior issuer still carries forward onto current issuer",
			priorIssuer:    "",
			currentIssuer:  "https://idp-a.example.com",
			priorSubject:   "user-1",
			currentSubject: "user-1",
			wantCarry:      true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			handler, _, _ := handlerTestSetup(t)

			require.NoError(t, handler.storage.StoreUpstreamTokens(ctx, "old-session", "test-provider", &storage.UpstreamTokens{
				ProviderID:      "test-provider",
				Issuer:          tc.priorIssuer,
				AccessToken:     "prior-at",
				RefreshToken:    "prior-rt-secret",
				UserID:          "canonical-user-1",
				UpstreamSubject: tc.priorSubject,
				ExpiresAt:       time.Now().Add(time.Hour),
			}))

			newRow := &storage.UpstreamTokens{
				ProviderID:      "test-provider",
				Issuer:          tc.currentIssuer,
				AccessToken:     "new-at",
				RefreshToken:    "",
				UserID:          "canonical-user-1",
				UpstreamSubject: tc.currentSubject,
			}

			handler.maybeCarryForwardRefreshToken(
				ctx, newRow, "canonical-user-1", tc.currentSubject, "test-provider", tc.synthetic,
			)

			if tc.wantCarry {
				assert.Equal(t, "prior-rt-secret", newRow.RefreshToken)
			} else {
				assert.Empty(t, newRow.RefreshToken)
			}
		})
	}
}

func TestHandler_NextMissingUpstream_IssuerMismatch(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	handler, _, _ := handlerTestSetup(t)
	handler.upstreams = []NamedUpstream{
		{
			Name:     "idp-b-repointed",
			Issuer:   "https://idp-b.example.com",
			Provider: &mockIDPProvider{},
		},
	}

	// Even an unexpired stored row issued by IdP A must be treated as missing
	// when the upstream is now configured with IdP B.
	require.NoError(t, handler.storage.StoreUpstreamTokens(ctx, "sess-1", "idp-b-repointed", &storage.UpstreamTokens{
		ProviderID:  "idp-b-repointed",
		Issuer:      "https://idp-a.example.com",
		AccessToken: "unexpired-a-token",
		ExpiresAt:   time.Now().Add(time.Hour),
		UserID:      "user-1",
	}))

	missing, err := handler.nextMissingUpstream(ctx, "sess-1", []string{"idp-b-repointed"}, nil)
	require.NoError(t, err)
	assert.Equal(t, "idp-b-repointed", missing)

	// Update stored row to match IdP B's issuer -> leg is satisfied.
	require.NoError(t, handler.storage.StoreUpstreamTokens(ctx, "sess-1", "idp-b-repointed", &storage.UpstreamTokens{
		ProviderID:  "idp-b-repointed",
		Issuer:      "https://idp-b.example.com",
		AccessToken: "unexpired-b-token",
		ExpiresAt:   time.Now().Add(time.Hour),
		UserID:      "user-1",
	}))

	missing, err = handler.nextMissingUpstream(ctx, "sess-1", []string{"idp-b-repointed"}, nil)
	require.NoError(t, err)
	assert.Empty(t, missing)
}

func TestHandler_UpstreamIssuerForFallback(t *testing.T) {
	t.Parallel()

	handler, _, _ := handlerTestSetup(t)
	handler.upstreams = []NamedUpstream{
		{Name: "configured", Issuer: "https://configured.example.com", Provider: &mockIDPProvider{}},
		{Name: "unconfigured-mock", Provider: &mockIDPProvider{}},
	}

	assert.Equal(t, "https://configured.example.com",
		handler.upstreamIssuerFor("configured", &upstream.Identity{VerifiedIssuer: "https://idtoken.example.com"}))
	assert.Equal(t, "https://idtoken.example.com",
		handler.upstreamIssuerFor("unconfigured-mock", &upstream.Identity{VerifiedIssuer: "https://idtoken.example.com"}))
	assert.Empty(t, handler.upstreamIssuerFor("unconfigured-mock", nil))
}

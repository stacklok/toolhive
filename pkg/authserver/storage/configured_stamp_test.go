// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package storage

import (
	"crypto/rand"
	"crypto/rsa"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v3"
	"github.com/ory/fosite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/authserver/server/registration"
	"github.com/stacklok/toolhive/pkg/oauthproto"
)

const stampTestClientID = "stamp-client"

func TestConfiguredStamp_ReconcileStampsAndOtherWritesDoNot(t *testing.T) {
	t.Parallel()

	cimdID := "https://app.example/client.json"
	tests := []struct {
		name           string
		write          func(t *testing.T, s Storage) string
		wantConfigured bool
		wantDCRIssued  bool
		// wantAbsent is set for rows whose write must be refused and store nothing.
		wantAbsent bool
	}{
		{
			name: "RegisterClient refuses an already-stamped client",
			write: func(t *testing.T, s Storage) string {
				t.Helper()
				stamped, ok := registration.MarkConfigured(&fosite.DefaultClient{ID: stampTestClientID})
				require.True(t, ok)
				err := s.RegisterClient(t.Context(), stamped)
				require.ErrorContains(t, err, "must not carry the configured stamp")
				return stampTestClientID
			},
			wantAbsent: true,
		},
		{
			name: "ReconcileConfiguredClient refuses an already-stamped client",
			write: func(t *testing.T, s Storage) string {
				t.Helper()
				stamped, ok := registration.MarkConfigured(&fosite.DefaultClient{ID: stampTestClientID})
				require.True(t, ok)
				err := s.ReconcileConfiguredClient(t.Context(), stamped)
				require.ErrorContains(t, err, "already stamped")
				return stampTestClientID
			},
			wantAbsent: true,
		},
		{
			name: "reconcile stamps a delegate",
			write: func(t *testing.T, s Storage) string {
				t.Helper()
				require.NoError(t, s.ReconcileConfiguredClient(t.Context(),
					testDelegateClient(t, stampTestClientID, "secret", []string{"a"}, []string{"https://mcp.example"})))
				return stampTestClientID
			},
			wantConfigured: true,
		},
		{
			name: "reconcile stamps a bare default client",
			write: func(t *testing.T, s Storage) string {
				t.Helper()
				require.NoError(t, s.ReconcileConfiguredClient(t.Context(),
					&fosite.DefaultClient{ID: stampTestClientID, RedirectURIs: []string{"http://127.0.0.1/cb"}}))
				return stampTestClientID
			},
			wantConfigured: true,
		},
		{
			name: "RegisterClient of an embedder client is not stamped",
			write: func(t *testing.T, s Storage) string {
				t.Helper()
				require.NoError(t, s.RegisterClient(t.Context(), &fosite.DefaultClient{ID: stampTestClientID}))
				return stampTestClientID
			},
		},
		{
			name: "RegisterClient of a DCR client is not stamped",
			write: func(t *testing.T, s Storage) string {
				t.Helper()
				require.NoError(t, s.RegisterClient(t.Context(),
					registration.MarkDCRIssued(&fosite.DefaultClient{ID: stampTestClientID, Public: true})))
				return stampTestClientID
			},
			wantDCRIssued: true,
		},
		{
			name: "UpsertDCRIssuedClient is not stamped",
			write: func(t *testing.T, s Storage) string {
				t.Helper()
				require.NoError(t, s.UpsertDCRIssuedClient(t.Context(),
					registration.MarkDCRIssued(&fosite.DefaultClient{ID: cimdID, Public: true})))
				return cimdID
			},
			wantDCRIssued: true,
		},
	}
	for _, backend := range configuredClientBackends() {
		for _, tt := range tests {
			t.Run(backend.name+"/"+tt.name, func(t *testing.T) {
				t.Parallel()
				s := backend.new(t)
				id := tt.write(t, s)

				got, err := s.GetClient(t.Context(), id)
				if tt.wantAbsent {
					require.Error(t, err)
					return
				}
				require.NoError(t, err)
				assert.Equal(t, tt.wantConfigured, registration.IsConfigured(got))
				assert.Equal(t, tt.wantDCRIssued, registration.DCRIssued(got))
			})
		}
	}
}

func TestConfiguredStamp_OIDCClientKeepsJWKS(t *testing.T) {
	t.Parallel()

	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	client := &fosite.DefaultOpenIDConnectClient{
		DefaultClient: &fosite.DefaultClient{
			ID:            stampTestClientID,
			GrantTypes:    []string{"authorization_code"},
			ResponseTypes: []string{"code"},
			RedirectURIs:  []string{"https://app.example/cb"},
		},
		TokenEndpointAuthMethod:           oauthproto.TokenEndpointAuthMethodPrivateKeyJWT,
		TokenEndpointAuthSigningAlgorithm: "RS256",
		JSONWebKeys: &jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{
			Key: &privateKey.PublicKey, KeyID: "client-key", Algorithm: "RS256", Use: "sig",
		}}},
	}

	for _, backend := range configuredClientBackends() {
		t.Run(backend.name, func(t *testing.T) {
			t.Parallel()
			s := backend.new(t)
			require.NoError(t, s.ReconcileConfiguredClient(t.Context(), client))

			got, err := s.GetClient(t.Context(), stampTestClientID)
			require.NoError(t, err)
			require.True(t, registration.IsConfigured(got))
			oidc, ok := got.(fosite.OpenIDConnectClient)
			require.True(t, ok, "stamped client must still satisfy fosite.OpenIDConnectClient")
			assert.Equal(t, oauthproto.TokenEndpointAuthMethodPrivateKeyJWT, oidc.GetTokenEndpointAuthMethod())
			assert.Equal(t, "RS256", oidc.GetTokenEndpointAuthSigningAlgorithm())
			jwks := oidc.GetJSONWebKeys()
			require.NotNil(t, jwks)
			require.Len(t, jwks.Keys, 1)
			assert.Equal(t, "client-key", jwks.Keys[0].KeyID)
		})
	}
}

func TestConfiguredStamp_ReconcileRefusesUnsupportedClientType(t *testing.T) {
	t.Parallel()

	for _, backend := range configuredClientBackends() {
		t.Run(backend.name, func(t *testing.T) {
			t.Parallel()
			s := backend.new(t)

			err := s.ReconcileConfiguredClient(t.Context(), &mockClient{id: stampTestClientID})
			require.Error(t, err)
			assert.Contains(t, err.Error(), "has unsupported type")
			assert.Contains(t, err.Error(), "mockClient")

			_, err = s.GetClient(t.Context(), stampTestClientID)
			require.Error(t, err, "nothing may be stored for a refused client")
		})
	}
}

func TestConfiguredStamp_RedisOldFormatRows(t *testing.T) {
	t.Parallel()

	// Literals as released binaries wrote them: no "configured" member, a
	// hashed secret and the defaulted response types on the delegate.
	const delegateJSON = `{"id":"old-delegate","secret":"JDJhJDEwJGFiY2RlZmdoaWprbG1ub3BxcnN0dXZ3eHl6MDEyMzQ1Njc4OWFiY2RlZmdoaWprbG1ub3Bx","redirect_uris":null,` +
		`"grant_types":["urn:ietf:params:oauth:grant-type:token-exchange"],"response_types":["code"],` +
		`"scopes":["a"],"audience":["https://mcp.example"],"public":false}`
	const placeholderJSON = `{"id":"old-spiffe","redirect_uris":null,"grant_types":null,"response_types":null,` +
		`"scopes":["a"],"audience":["https://api.example.com"],"identity_fingerprint":"identity",` +
		`"public":false,"reserved":true}`

	s, _ := newTestRedisStorage(t)
	t.Cleanup(func() { _ = s.Close() })
	ctx := t.Context()

	placeholder := testPlaceholder(t, "old-spiffe", "identity", []string{"a"}, nil)
	for id, raw := range map[string]string{"old-delegate": delegateJSON, "old-spiffe": placeholderJSON} {
		require.NoError(t, s.client.Set(ctx, redisKey(s.keyPrefix, KeyTypeClient, id), raw, 0).Err())
	}

	t.Run("delegate reconciles and comes back stamped", func(t *testing.T) {
		t.Parallel()
		delegate := testDelegateClient(t, "old-delegate", "secret", []string{"a"}, []string{"https://mcp.example"})
		require.NoError(t, s.ReconcileConfiguredClient(ctx, delegate))

		got, err := s.GetClient(ctx, "old-delegate")
		require.NoError(t, err)
		assert.True(t, registration.IsConfigured(got))
	})

	t.Run("placeholder reconciles and stays reserved", func(t *testing.T) {
		t.Parallel()
		require.NoError(t, s.ReconcileConfiguredClient(ctx, placeholder))

		got, err := s.GetClient(ctx, "old-spiffe")
		require.NoError(t, err)
		assert.True(t, isReservedPlaceholder(got))
		assert.False(t, registration.IsConfigured(got), "a reserved row is never wrapped")
	})
}

func TestConfiguredStamp_MemoryPlaceholderReconciledTwice(t *testing.T) {
	t.Parallel()

	s := NewMemoryStorage()
	t.Cleanup(func() { _ = s.Close() })
	placeholder := testPlaceholder(t, "spiffe-twice", "identity", []string{"a"}, []string{"https://r.example"})

	require.NoError(t, s.ReconcileConfiguredClient(t.Context(), placeholder))
	require.NoError(t, s.ReconcileConfiguredClient(t.Context(), placeholder))

	got, err := s.GetClient(t.Context(), "spiffe-twice")
	require.NoError(t, err)
	assert.True(t, isReservedPlaceholder(got))
	assert.False(t, registration.IsConfigured(got))
}

func TestConfiguredStamp_RedisPublicRowWithTTLIsNotDCRIssued(t *testing.T) {
	t.Parallel()

	s, mr := newTestRedisStorage(t)
	t.Cleanup(func() { _ = s.Close() })
	ctx := t.Context()

	require.NoError(t, s.ReconcileConfiguredClient(ctx,
		&fosite.DefaultClient{ID: stampTestClientID, Public: true, RedirectURIs: []string{"http://127.0.0.1/cb"}}))
	mr.SetTTL(redisKey(s.keyPrefix, KeyTypeClient, stampTestClientID), time.Hour)

	got, err := s.GetClient(ctx, stampTestClientID)
	require.NoError(t, err)
	assert.True(t, registration.IsConfigured(got))
	assert.False(t, registration.DCRIssued(got), "the legacy public+TTL compensation must not apply to a stamped row")
}

func TestConfiguredStamp_MemoryClearedByOtherWritesAndExpiry(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		// clear removes or replaces the live row; it takes s.mu itself.
		clear func(s *MemoryStorage)
		// wantGone is set when the row itself must be absent afterwards.
		wantGone bool
	}{
		{
			name: "expiry removes the row and the stamp",
			clear: func(s *MemoryStorage) {
				s.mu.Lock()
				s.dcrClientExpiry[stampTestClientID] = time.Now().Add(-time.Minute)
				s.mu.Unlock()
				s.cleanupExpired()
			},
			wantGone: true,
		},
		{
			name: "overwrite with an expiring client clears the stamp",
			clear: func(s *MemoryStorage) {
				s.mu.Lock()
				defer s.mu.Unlock()
				s.putExpiringClientLocked(stampTestClientID,
					registration.MarkDCRIssued(&fosite.DefaultClient{ID: stampTestClientID, Public: true}),
					time.Now().Add(time.Hour))
			},
		},
		{
			name: "overwrite with a permanent client clears the stamp",
			clear: func(s *MemoryStorage) {
				s.mu.Lock()
				defer s.mu.Unlock()
				s.putPermanentClientLocked(stampTestClientID, &fosite.DefaultClient{ID: stampTestClientID})
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s := NewMemoryStorage()
			t.Cleanup(func() { _ = s.Close() })
			require.NoError(t, s.ReconcileConfiguredClient(t.Context(),
				&fosite.DefaultClient{ID: stampTestClientID}))
			got, err := s.GetClient(t.Context(), stampTestClientID)
			require.NoError(t, err)
			require.True(t, registration.IsConfigured(got))

			tt.clear(s)
			s.mu.RLock()
			_, stamped := s.configured[stampTestClientID]
			s.mu.RUnlock()
			assert.False(t, stamped)

			got, err = s.GetClient(t.Context(), stampTestClientID)
			if tt.wantGone {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.False(t, registration.IsConfigured(got))
		})
	}
}

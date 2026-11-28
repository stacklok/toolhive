// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package authserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ory/fosite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/authserver/server/handlers"
	"github.com/stacklok/toolhive/pkg/authserver/storage"
	"github.com/stacklok/toolhive/pkg/authserver/storage/mocks"
	"github.com/stacklok/toolhive/pkg/authserver/upstream"
	upstreammocks "github.com/stacklok/toolhive/pkg/authserver/upstream/mocks"
)

func TestUpstreamTokenRefresher_IssuerBinding(t *testing.T) {
	t.Parallel()

	t.Run("refuses refresh without calling new IdP when stored issuer mismatches", func(t *testing.T) {
		t.Parallel()
		ctx := context.Background()

		idpA := newCountingIdP(t, http.StatusOK, refreshOKBody)
		idpB := newCountingIdP(t, http.StatusOK, refreshOKBody)
		provA := idpA.provider(t)
		provB := idpB.provider(t)

		stor := storage.NewMemoryStorage()
		t.Cleanup(func() { _ = stor.Close() })

		expired := &storage.UpstreamTokens{
			ProviderID:       "default",
			Issuer:           upstream.ProviderIssuer(provA),
			AccessToken:      "access-a",
			RefreshToken:     "refresh-secret-from-a",
			ExpiresAt:        time.Now().Add(-time.Minute),
			SessionExpiresAt: time.Now().Add(time.Hour),
			UserID:           "user-1",
			UpstreamSubject:  "sub-1",
			ClientID:         testClientID,
		}
		require.NotEmpty(t, expired.Issuer)
		require.NoError(t, stor.StoreUpstreamTokens(ctx, "sess-1", "default", expired))

		// Reconfigure "default" to point at IdP B.
		refresher := newUpstreamTokenRefresher(
			[]handlers.NamedUpstream{{Name: "default", Provider: provB}},
			stor,
			time.Hour,
		)

		refreshed, err := refresher.RefreshAndStore(ctx, "sess-1", expired)
		require.ErrorIs(t, err, storage.ErrIssuerMismatch)
		assert.Nil(t, refreshed)
		assert.Equal(t, int32(0), idpB.calls.Load(), "must never disclose IdP A refresh token to IdP B")

		// Also reject when the caller passes an empty-issuer copy but storage
		// already holds the IdP A bound row loaded inside singleflight.
		unstampedCallerCopy := *expired
		unstampedCallerCopy.Issuer = ""
		_, err = refresher.RefreshAndStore(ctx, "sess-1", &unstampedCallerCopy)
		require.ErrorIs(t, err, storage.ErrIssuerMismatch)
		assert.Equal(t, int32(0), idpB.calls.Load())
	})

	t.Run("stamps legacy empty-issuer row on first match and rejects later repoint", func(t *testing.T) {
		t.Parallel()
		ctx := context.Background()

		idpA := newCountingIdP(t, http.StatusOK, refreshOKBody)
		idpB := newCountingIdP(t, http.StatusOK, refreshOKBody)
		provA := idpA.provider(t)
		provB := idpB.provider(t)

		stor := storage.NewMemoryStorage()
		t.Cleanup(func() { _ = stor.Close() })

		legacy := &storage.UpstreamTokens{
			ProviderID:       "default",
			Issuer:           "", // Legacy pre-upgrade row.
			AccessToken:      "legacy-access",
			RefreshToken:     "legacy-refresh",
			ExpiresAt:        time.Now().Add(-time.Minute),
			SessionExpiresAt: time.Now().Add(time.Hour),
			UserID:           "user-1",
			UpstreamSubject:  "sub-1",
			ClientID:         testClientID,
		}
		require.NoError(t, stor.StoreUpstreamTokens(ctx, "sess-legacy", "default", legacy))

		// First refresh under IdP A succeeds and stamps IdP A's issuer.
		refresherA := newUpstreamTokenRefresher(
			[]handlers.NamedUpstream{{Name: "default", Provider: provA}},
			stor,
			time.Hour,
		)
		refreshed, err := refresherA.RefreshAndStore(ctx, "sess-legacy", legacy)
		require.NoError(t, err)
		require.NotNil(t, refreshed)
		assert.Equal(t, upstream.ProviderIssuer(provA), refreshed.Issuer)
		assert.Equal(t, int32(1), idpA.calls.Load())

		persisted, err := stor.GetUpstreamTokens(ctx, "sess-legacy", "default")
		require.NoError(t, err)
		assert.Equal(t, upstream.ProviderIssuer(provA), persisted.Issuer)

		// Expire the newly stored row and repoint "default" to IdP B.
		persisted.ExpiresAt = time.Now().Add(-time.Minute)
		require.NoError(t, stor.StoreUpstreamTokens(ctx, "sess-legacy", "default", persisted))

		refresherB := newUpstreamTokenRefresher(
			[]handlers.NamedUpstream{{Name: "default", Provider: provB}},
			stor,
			time.Hour,
		)
		_, err = refresherB.RefreshAndStore(ctx, "sess-legacy", persisted)
		require.ErrorIs(t, err, storage.ErrIssuerMismatch)
		assert.Equal(t, int32(0), idpB.calls.Load(), "stamped row must not be sent to repointed IdP B")
	})
}

func TestUpstreamRefreshGuard_IssuerMismatch(t *testing.T) {
	t.Parallel()

	t.Run("refuses expired row with mismatched issuer without invoking refresher or revoking grant", func(t *testing.T) {
		t.Parallel()
		ctx := context.Background()

		stor := storage.NewMemoryStorage()
		t.Cleanup(func() { _ = stor.Close() })

		row := guardTestRow(time.Now().Add(-time.Minute), "rt-from-a")
		row.Issuer = "https://idp-a.example.com"
		require.NoError(t, stor.StoreUpstreamTokens(ctx, guardTestTSID, "idp", row))

		req := guardTestRequest(guardTestTSID, "refresh_token")
		require.NoError(t, stor.CreateRefreshTokenSession(ctx, "rt-sig", "at-sig", req))
		require.NoError(t, stor.CreateAccessTokenSession(ctx, "at-sig", req))

		refresher := mocks.NewMockUpstreamTokenRefresher(gomock.NewController(t))
		// No RefreshAndStore expectation: guard must short-circuit before calling refresher.
		guard, err := newUpstreamRefreshGuard(
			stor,
			refresher,
			[]string{"idp"},
			withRefreshGuardIssuers(map[string]string{"idp": "https://idp-b.example.com"}),
		)
		require.NoError(t, err)

		err = guard.HandleTokenEndpointRequest(ctx, req)
		require.ErrorIs(t, err, fosite.ErrInvalidGrant)

		_, rtErr := stor.GetRefreshTokenSession(ctx, "rt-sig", nil)
		assert.NoError(t, rtErr, "issuer-mismatch refusal must not revoke across rolling deploys")
	})

	t.Run("refuses when refresher returns ErrIssuerMismatch instead of treating it as transient", func(t *testing.T) {
		t.Parallel()
		ctx := context.Background()

		stor := storage.NewMemoryStorage()
		t.Cleanup(func() { _ = stor.Close() })

		row := guardTestRow(time.Now().Add(-time.Minute), "rt-from-a")
		require.NoError(t, stor.StoreUpstreamTokens(ctx, guardTestTSID, "idp", row))

		req := guardTestRequest(guardTestTSID, "refresh_token")
		refresher := mocks.NewMockUpstreamTokenRefresher(gomock.NewController(t))
		refresher.EXPECT().
			RefreshAndStore(gomock.Any(), guardTestTSID, gomock.Any()).
			Return(nil, storage.ErrIssuerMismatch)

		guard, err := newUpstreamRefreshGuard(stor, refresher, []string{"idp"})
		require.NoError(t, err)

		err = guard.HandleTokenEndpointRequest(ctx, req)
		require.ErrorIs(t, err, fosite.ErrInvalidGrant)
	})
}

func TestProviderIssuer(t *testing.T) {
	t.Parallel()

	t.Run("pure OAuth2 provider reports TokenEndpoint", func(t *testing.T) {
		t.Parallel()
		p, err := upstream.NewOAuth2Provider(&upstream.OAuth2Config{
			CommonOAuthConfig: upstream.CommonOAuthConfig{
				ClientID:    "client",
				RedirectURI: "http://localhost/callback",
			},
			AuthorizationEndpoint: "https://oauth.example.com/authorize",
			TokenEndpoint:         "https://oauth.example.com/token",
		})
		require.NoError(t, err)
		assert.Equal(t, "https://oauth.example.com/token", upstream.ProviderIssuer(p))
	})

	t.Run("OIDC provider reports configured OIDC issuer rather than token endpoint", func(t *testing.T) {
		t.Parallel()
		var srv *httptest.Server
		srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"issuer":                                srv.URL,
				"authorization_endpoint":                srv.URL + "/oauth2/authorize",
				"token_endpoint":                        srv.URL + "/oauth2/token",
				"jwks_uri":                              srv.URL + "/oauth2/jwks",
				"response_types_supported":              []string{"code"},
				"subject_types_supported":               []string{"public"},
				"id_token_signing_alg_values_supported": []string{"RS256"},
			})
		}))
		t.Cleanup(srv.Close)

		p, err := upstream.NewOIDCProvider(context.Background(), &upstream.OIDCConfig{
			CommonOAuthConfig: upstream.CommonOAuthConfig{
				ClientID:    "client",
				RedirectURI: "http://localhost/callback",
			},
			Issuer:            srv.URL,
			AllowPrivateIPs:   true,
			InsecureAllowHTTP: true,
		})
		require.NoError(t, err)
		assert.Equal(t, srv.URL, upstream.ProviderIssuer(p))
	})

	t.Run("plain mock without IssuerIdentifier returns empty", func(t *testing.T) {
		t.Parallel()
		mock := upstreammocks.NewMockOAuth2Provider(gomock.NewController(t))
		assert.Empty(t, upstream.ProviderIssuer(mock))
	})
}

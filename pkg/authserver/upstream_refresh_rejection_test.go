// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package authserver

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/auth/oautherr"
	"github.com/stacklok/toolhive/pkg/auth/upstreamtoken"
	"github.com/stacklok/toolhive/pkg/authserver/storage"
	"github.com/stacklok/toolhive/pkg/authserver/upstream"
)

// TestUpstreamRefreshRejection_ReachesServiceCallers verifies that an IdP's
// invalid_grant answer to an upstream refresh survives the provider, the
// refresher and the upstream token service, so both GetValidTokens and
// GetAllUpstreamCredentials callers can tell it from a transient failure. The
// token endpoint's *oauth2.RetrieveError survives the same path, so the
// oautherr predicates classify every other answer too.
func TestUpstreamRefreshRejection_ReachesServiceCallers(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		tokenEndpoint    http.HandlerFunc
		closeBeforeUse   bool
		wantInvalidGrant bool
		wantPermanent    bool
	}{
		{
			name: "invalid_grant is definitive",
			tokenEndpoint: func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"error":"invalid_grant","error_description":"refresh token revoked"}`))
			},
			wantInvalidGrant: true,
			wantPermanent:    true,
		},
		{
			name: "other 4xx error code is not invalid_grant",
			tokenEndpoint: func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"error":"invalid_client"}`))
			},
			wantPermanent: true,
		},
		{
			name: "5xx is transient",
			tokenEndpoint: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusServiceUnavailable)
			},
		},
		{
			name: "429 is transient",
			tokenEndpoint: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusTooManyRequests)
			},
		},
		{
			name: "4xx without an OAuth error code is transient",
			tokenEndpoint: func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/html")
				w.WriteHeader(http.StatusForbidden)
				_, _ = w.Write([]byte(`<html>blocked by WAF</html>`))
			},
		},
		{
			name:           "network error is transient",
			tokenEndpoint:  func(http.ResponseWriter, *http.Request) {},
			closeBeforeUse: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()

			idp := httptest.NewServer(tt.tokenEndpoint)
			t.Cleanup(idp.Close)
			tokenURL := idp.URL + "/token"
			if tt.closeBeforeUse {
				// Reserve a loopback port nothing listens on so the dial fails.
				l, err := net.Listen("tcp", "127.0.0.1:0")
				require.NoError(t, err)
				tokenURL = "http://" + l.Addr().String() + "/token"
				require.NoError(t, l.Close())
			}

			provider, err := upstream.NewOAuth2Provider(&upstream.OAuth2Config{
				CommonOAuthConfig: upstream.CommonOAuthConfig{
					ClientID:    "client",
					RedirectURI: "http://localhost/callback",
				},
				AuthorizationEndpoint: idp.URL + "/authorize",
				TokenEndpoint:         tokenURL,
			})
			require.NoError(t, err)

			stor := storage.NewMemoryStorage()
			t.Cleanup(func() { _ = stor.Close() })
			require.NoError(t, stor.StoreUpstreamTokens(ctx, "session", "idp", &storage.UpstreamTokens{
				ProviderID:       "idp",
				AccessToken:      "expired-access",
				RefreshToken:     "upstream-refresh",
				ExpiresAt:        time.Now().Add(-time.Minute),
				SessionExpiresAt: time.Now().Add(time.Hour),
				UserID:           "user",
				UpstreamSubject:  "subject",
				ClientID:         testClientID,
			}))

			refresher := &upstreamTokenRefresher{
				providers:            map[string]upstream.OAuth2Provider{"idp": provider},
				storage:              stor,
				refreshTokenLifespan: time.Hour,
			}
			svc := upstreamtoken.NewInProcessService(stor, refresher)

			_, err = svc.GetValidTokens(ctx, "session", "idp")
			require.ErrorIs(t, err, upstreamtoken.ErrRefreshFailed)
			assert.Equal(t, tt.wantInvalidGrant, errors.Is(err, upstream.ErrGrantRejected), "GetValidTokens: %v", err)
			assert.Equal(t, tt.wantInvalidGrant, oautherr.IsRejectedRefreshGrant(err), "GetValidTokens: %v", err)
			assert.Equal(t, tt.wantPermanent, oautherr.IsPermanentCredentialError(err), "GetValidTokens: %v", err)
			assert.NotContains(t, err.Error(), "blocked by WAF")

			creds, failed, err := svc.GetAllUpstreamCredentials(ctx, "session")
			require.NoError(t, err)
			assert.Empty(t, creds)
			require.Contains(t, failed, "idp")
			require.ErrorIs(t, failed["idp"], upstreamtoken.ErrRefreshFailed)
			assert.Equal(t, tt.wantInvalidGrant, errors.Is(failed["idp"], upstream.ErrGrantRejected),
				"GetAllUpstreamCredentials: %v", failed["idp"])
			assert.Equal(t, tt.wantInvalidGrant, oautherr.IsRejectedRefreshGrant(failed["idp"]),
				"GetAllUpstreamCredentials: %v", failed["idp"])
			assert.Equal(t, tt.wantPermanent, oautherr.IsPermanentCredentialError(failed["idp"]),
				"GetAllUpstreamCredentials: %v", failed["idp"])
			assert.NotContains(t, failed["idp"].Error(), "blocked by WAF")
		})
	}
}

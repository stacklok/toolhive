// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package authserver_test

import (
	"context"
	"crypto/rand"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/authserver"
	servercrypto "github.com/stacklok/toolhive/pkg/authserver/server/crypto"
	"github.com/stacklok/toolhive/pkg/authserver/server/keys"
	"github.com/stacklok/toolhive/pkg/authserver/storage"
	"github.com/stacklok/toolhive/pkg/authserver/upstream"
	"github.com/stacklok/toolhive/pkg/authserver/upstreamfilter"
	"github.com/stacklok/toolhive/test/integration/authserver/helpers"
)

type embedderFilter func(context.Context, upstreamfilter.FilterPrincipal, []string) ([]string, error)

func (f embedderFilter) FilterUpstreams(
	ctx context.Context, principal upstreamfilter.FilterPrincipal, configured []string,
) ([]string, error) {
	return f(ctx, principal, configured)
}

var _ upstreamfilter.UpstreamFilter = embedderFilter(nil)

func TestEmbedderUpstreamFilter(t *testing.T) {
	t.Parallel()

	for _, fail := range []bool{false, true} {
		name := "select no optional upstreams"
		if fail {
			name = "filter error fails authorization"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			idp := helpers.NewMockUpstreamIDP(t)
			httpServer := httptest.NewUnstartedServer(nil)
			issuer := "http://" + httpServer.Listener.Addr().String()
			secret := make([]byte, 32)
			_, err := rand.Read(secret)
			require.NoError(t, err)
			var calls atomic.Int32
			cfg := authserver.Config{
				Issuer: issuer, KeyProvider: keys.NewGeneratingProvider(keys.DefaultAlgorithm),
				HMACSecrets:      servercrypto.NewHMACSecrets(secret),
				AllowedAudiences: []string{cfgAudienceA},
				UpstreamFilter: embedderFilter(func(_ context.Context, principal upstreamfilter.FilterPrincipal, configured []string) ([]string, error) {
					calls.Add(1)
					assert.NotEmpty(t, principal.GetSubject())
					assert.Equal(t, principal.GetSubject(), principal.GetCanonicalUserID(), "synthetic identity uses mapped subject")
					assert.Nil(t, principal.GetClaims(), "OAuth2 synthetic identity must not manufacture claims")
					assert.Equal(t, []string{"optional"}, configured)
					if fail {
						return nil, errors.New("embedder denied authorization")
					}
					return nil, nil
				}),
			}
			for _, upstreamName := range []string{"first", "optional"} {
				cfg.Upstreams = append(cfg.Upstreams, authserver.UpstreamConfig{
					Name: upstreamName, Type: authserver.UpstreamProviderTypeOAuth2,
					OAuth2Config: &upstream.OAuth2Config{
						CommonOAuthConfig: upstream.CommonOAuthConfig{
							ClientID: "test-client-id", RedirectURI: issuer + "/oauth/callback",
						},
						AuthorizationEndpoint: idp.URL() + "/authorize",
						TokenEndpoint:         idp.URL() + "/token",
					},
				})
			}
			srv, err := authserver.New(t.Context(), cfg, storage.NewMemoryStorage())
			require.NoError(t, err)
			t.Cleanup(func() { assert.NoError(t, srv.Close()) })
			httpServer.Config.Handler = srv.Handler()
			httpServer.Start()
			t.Cleanup(httpServer.Close)

			clientID := registerPublicClientViaDCR(t, httpServer.URL, "external-filter-client")
			browser := helpers.NewOAuthClient(httpServer.URL)
			page, err := browser.StartAuthorization(map[string][]string{
				"client_id": {clientID}, "redirect_uri": {cfgTUIRedirectURI}, "response_type": {"code"},
				"scope": {"openid profile"}, "state": {"filter-state"}, "resource": {cfgAudienceA},
				"code_challenge":        {servercrypto.ComputePKCEChallenge(servercrypto.GeneratePKCEVerifier())},
				"code_challenge_method": {"S256"},
			})
			require.NoError(t, err)
			require.Equal(t, http.StatusOK, page.StatusCode)
			approved, err := browser.ApproveConsent(page, httpServer.URL)
			require.NoError(t, err)
			require.NoError(t, page.Body.Close())
			require.Equal(t, http.StatusSeeOther, approved.StatusCode)
			location, err := approved.Location()
			require.NoError(t, err)
			require.NoError(t, approved.Body.Close())
			callback, err := browser.Callback("mock-auth-code", location.Query().Get("state"))
			require.NoError(t, err)
			require.NoError(t, callback.Body.Close())
			require.Equal(t, http.StatusSeeOther, callback.StatusCode)
			location, err = callback.Location()
			require.NoError(t, err)
			assert.Equal(t, int32(1), calls.Load())
			assert.Equal(t, "filter-state", location.Query().Get("state"))
			if fail {
				assert.Equal(t, "server_error", location.Query().Get("error"))
				assert.Empty(t, location.Query().Get("code"))
			} else {
				assert.Empty(t, location.Query().Get("error"))
				assert.NotEmpty(t, location.Query().Get("code"), "optional upstream was skipped")
			}
		})
	}
}

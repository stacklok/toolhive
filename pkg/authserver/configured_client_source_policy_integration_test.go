// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package authserver_test

import (
	"net/http"
	"net/url"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/authserver"
	"github.com/stacklok/toolhive/pkg/authserver/storage"
	"github.com/stacklok/toolhive/pkg/oauthproto"
	"github.com/stacklok/toolhive/test/integration/authserver/helpers"
)

const cfgTUIRedirectURI = "http://127.0.0.1:8123/callback"

const cfgAudienceB = "https://b.test.local"

// registerPublicClientViaDCR registers a public client the way a TUI does at
// runtime and returns its client_id.
func registerPublicClientViaDCR(t *testing.T, serverURL, name string) string {
	t.Helper()
	registered, status, err := helpers.NewOAuthClient(serverURL).RegisterClient(map[string]any{
		"client_name": name, "redirect_uris": []string{cfgTUIRedirectURI},
		"token_endpoint_auth_method": "none", "grant_types": []string{"authorization_code", "refresh_token"},
		"response_types": []string{"code"},
	})
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, status, registered)
	clientID, ok := registered["client_id"].(string)
	require.True(t, ok, "client_id missing in %v", registered)
	return clientID
}

// publicClientAccessToken logs a public client in and returns its access token.
func publicClientAccessToken(t *testing.T, serverURL, clientID string) string {
	t.Helper()
	code, verifier := loginWith(t, serverURL, clientID, cfgTUIRedirectURI, cfgAudienceA, "openid profile")
	tokens := postToken(t, serverURL, clientAuth{}, url.Values{
		"grant_type": {oauthproto.GrantTypeAuthorizationCode}, "client_id": {clientID}, "code": {code},
		"redirect_uri": {cfgTUIRedirectURI}, "code_verifier": {verifier},
	})
	require.Equal(t, http.StatusOK, tokens.status, tokens.body)
	return tokens.str(t, "access_token")
}

// TestTokenExchangeSourcePolicyWithRealClientLogins runs the source policy
// against real logins. The TUI registers through DCR, so its client_id exists
// only after a server has run: register on a first server, then restart over
// the same Redis with a broker that names it. The DCR rows must still be
// admitted after the restart.
func TestTokenExchangeSourcePolicyWithRealClientLogins(t *testing.T) {
	t.Parallel()

	redisServer := miniredis.RunT(t)
	bootstrap, _, _, stopBootstrap := startEmbeddedAuthServer(t, func(cfg *authserver.RunConfig, _ string) {
		configuredClients()(cfg)
	}, newRedisStorage(redisServer))
	tuiID := registerPublicClientViaDCR(t, bootstrap.URL, "tui")
	otherID := registerPublicClientViaDCR(t, bootstrap.URL, "other")
	stopBootstrap()

	broker := secretClient(t, cfgBrokerID, nil)
	broker.Audiences = []string{cfgAudienceA, cfgAudienceB}
	broker.TokenExchange = &authserver.DelegateClientTokenExchange{
		SelfIssuedSubjectTokens: authserver.SelfIssuedSubjectTokensRestricted,
		AllowedSourceClients:    []string{tuiID}, APIAudience: cfgAudienceB,
	}
	clients := configuredClients(broker)
	srv, _, embedded, _ := startEmbeddedAuthServer(t, func(cfg *authserver.RunConfig, _ string) {
		clients(cfg)
		cfg.AllowedAudiences = []string{cfgAudienceA, cfgAudienceB}
	}, newRedisStorage(redisServer))

	auth := clientAuth{clientID: cfgBrokerID, secret: cfgClientSecret}
	exchange := func(subjectToken, resource string) tokenResult {
		return postToken(t, srv.URL, auth, url.Values{
			"grant_type":         {oauthproto.GrantTypeTokenExchange},
			"subject_token":      {subjectToken},
			"subject_token_type": {oauthproto.TokenTypeAccessToken},
			"scope":              {"openid profile"}, "resource": {resource},
		})
	}

	tuiToken := publicClientAccessToken(t, srv.URL, tuiID)
	otherToken := publicClientAccessToken(t, srv.URL, otherID)

	listed := exchange(tuiToken, cfgAudienceA)
	require.Equal(t, http.StatusOK, listed.status, listed.body)

	apiAudience := exchange(tuiToken, cfgAudienceB)
	require.Equal(t, http.StatusOK, apiAudience.status, "api_audience needs no subject coverage: %v", apiAudience.body)
	assert.Equal(t, []any{cfgAudienceB}, accessTokenClaims(t, embedded, apiAudience.str(t, "access_token"))["aud"])

	unlisted := exchange(otherToken, cfgAudienceA)
	assert.Equal(t, http.StatusBadRequest, unlisted.status, unlisted.body)
	assert.Equal(t, "invalid_grant", unlisted.body["error"])
}

// TestTokenExchangeSelfIssuedSubjectTokensModes runs each trust mode against a
// token issued to an unrelated client: an explicit any keeps blanket trust,
// and restricted with no source clients accepts only the client's own tokens.
func TestTokenExchangeSelfIssuedSubjectTokensModes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		mode       authserver.SelfIssuedSubjectTokens
		wantStatus int
		wantError  string
	}{
		{name: "explicit any accepts another client's token", mode: authserver.SelfIssuedSubjectTokensAny,
			wantStatus: http.StatusOK},
		{name: "restricted without source clients refuses it", mode: authserver.SelfIssuedSubjectTokensRestricted,
			wantStatus: http.StatusBadRequest, wantError: "invalid_grant"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			broker := secretClient(t, cfgBrokerID, []string{oauthproto.GrantTypeTokenExchange})
			broker.TokenExchange = &authserver.DelegateClientTokenExchange{SelfIssuedSubjectTokens: tt.mode}
			clients := configuredClients(broker)
			srv, _, _, _ := startEmbeddedAuthServer(t, func(cfg *authserver.RunConfig, _ string) {
				clients(cfg)
			}, storage.NewMemoryStorage())

			otherID := registerPublicClientViaDCR(t, srv.URL, "other")
			otherToken := publicClientAccessToken(t, srv.URL, otherID)

			result := postToken(t, srv.URL, clientAuth{clientID: cfgBrokerID, secret: cfgClientSecret}, url.Values{
				"grant_type":         {oauthproto.GrantTypeTokenExchange},
				"subject_token":      {otherToken},
				"subject_token_type": {oauthproto.TokenTypeAccessToken},
				"scope":              {"openid profile"}, "resource": {cfgAudienceA},
			})
			require.Equal(t, tt.wantStatus, result.status, result.body)
			if tt.wantError != "" {
				assert.Equal(t, tt.wantError, result.body["error"])
			}
		})
	}
}

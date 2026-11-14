// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package upstream

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/oauth2"

	"github.com/stacklok/toolhive/pkg/oauthproto"
)

// clientOnTokenRequest records which client a token request authenticated as.
type clientOnTokenRequest struct {
	mu                sync.Mutex
	basicID, formID   string
	basicSecret, form string
}

func (c *clientOnTokenRequest) handler(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	c.mu.Lock()
	c.basicID, c.basicSecret, _ = r.BasicAuth()
	c.formID, c.form = r.PostForm.Get("client_id"), r.PostForm.Get("client_secret")
	c.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(testTokenResponse{AccessToken: "at", TokenType: "Bearer", ExpiresIn: 3600})
}

func clientIDInAuthorizationURL(t *testing.T, p interface {
	AuthorizationURL(string, string, ...AuthorizationOption) (string, error)
}) string {
	t.Helper()
	raw, err := p.AuthorizationURL("state-0123456789", "")
	require.NoError(t, err)
	parsed, err := url.Parse(raw)
	require.NoError(t, err)
	return parsed.Query().Get("client_id")
}

func TestBaseOAuth2Provider_UpdateClientCredentials(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	mock := newMockOAuth2Server()
	t.Cleanup(mock.Close)
	seen := &clientOnTokenRequest{}
	mock.tokenHandler = seen.handler

	provider, err := NewOAuth2Provider(&OAuth2Config{
		CommonOAuthConfig: CommonOAuthConfig{
			ClientID:     "first-client",
			ClientSecret: "first-secret",
			RedirectURI:  "http://localhost:8080/callback",
		},
		AuthorizationEndpoint: mock.URL + "/authorize",
		TokenEndpoint:         mock.URL + "/token",
	})
	require.NoError(t, err)
	require.Equal(t, "first-client", clientIDInAuthorizationURL(t, provider))

	require.NoError(t, provider.UpdateClientCredentials(ClientCredentials{
		ClientID:                "second-client",
		ClientSecret:            "second-secret",
		TokenEndpointAuthMethod: oauthproto.TokenEndpointAuthMethodClientSecretBasic,
	}))
	assert.Equal(t, "second-client", clientIDInAuthorizationURL(t, provider))
	_, err = provider.exchangeCodeForTokens(ctx, "code", "")
	require.NoError(t, err)
	assert.Equal(t, "second-client", seen.basicID, "the negotiated auth method must apply to the new client")
	assert.Equal(t, "second-secret", seen.basicSecret)
	_, err = provider.RefreshTokens(ctx, "refresh-token", "")
	require.NoError(t, err)
	assert.Equal(t, "second-client", seen.basicID)

	for name, creds := range map[string]ClientCredentials{
		"unsupported auth method": {ClientID: "third-client", TokenEndpointAuthMethod: "private_key_jwt"},
		"empty client_id":         {ClientSecret: "secret"},
	} {
		require.Error(t, provider.UpdateClientCredentials(creds), name)
		assert.Equal(t, "second-client", clientIDInAuthorizationURL(t, provider), name)
	}

	// Endpoints in the update replace the provider's; requests go there.
	moved := newMockOAuth2Server()
	t.Cleanup(moved.Close)
	seenMoved := &clientOnTokenRequest{}
	moved.tokenHandler = seenMoved.handler
	require.NoError(t, provider.UpdateClientCredentials(ClientCredentials{
		ClientID:              "third-client",
		AuthorizationEndpoint: moved.URL + "/authorize",
		TokenEndpoint:         moved.URL + "/token",
	}))
	raw, err := provider.AuthorizationURL("state-0123456789", "")
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(raw, moved.URL+"/authorize?"), raw)
	_, err = provider.exchangeCodeForTokens(ctx, "code", "")
	require.NoError(t, err)
	assert.Equal(t, "third-client", seenMoved.formID)
}

func TestBaseOAuth2Provider_UpdateClientCredentialsWhileServing(t *testing.T) {
	t.Parallel()
	provider, err := NewOAuth2Provider(&OAuth2Config{
		CommonOAuthConfig:     CommonOAuthConfig{ClientID: "client-0", RedirectURI: "http://localhost:8080/callback"},
		AuthorizationEndpoint: "https://idp.example.com/authorize",
		TokenEndpoint:         "https://idp.example.com/token",
	})
	require.NoError(t, err)

	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 200 {
				_, err := provider.AuthorizationURL("state-0123456789", "")
				assert.NoError(t, err)
			}
		}()
	}
	for i := range 200 {
		require.NoError(t, provider.UpdateClientCredentials(ClientCredentials{ClientID: "client-" + string(rune('a'+i%26))}))
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("authorization URL builders did not finish")
	}
}

func TestOIDCProvider_UpdateClientCredentials(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	mock := newMockOIDCServer(t)
	t.Cleanup(mock.Close)
	seen := &clientOnTokenRequest{}
	mock.tokenHandler = seen.handler

	provider, err := NewOIDCProvider(ctx, &OIDCConfig{
		CommonOAuthConfig: CommonOAuthConfig{
			ClientID:     "first-client",
			ClientSecret: "first-secret",
			RedirectURI:  testRedirectURI,
			Scopes:       []string{"openid"},
		},
		Issuer:            mock.issuer,
		InsecureAllowHTTP: true,
		AllowPrivateIPs:   true,
	})
	require.NoError(t, err)

	require.NoError(t, provider.UpdateClientCredentials(ClientCredentials{
		ClientID:                "second-client",
		ClientSecret:            "second-secret",
		TokenEndpointAuthMethod: oauthproto.TokenEndpointAuthMethodClientSecretBasic,
	}))
	assert.Equal(t, "second-client", clientIDInAuthorizationURL(t, provider))

	// The verifier follows the client: an ID token for the new client_id
	// verifies, one for the previous client_id does not.
	expiry := time.Now().Add(time.Hour)
	_, err = provider.validateIDToken(ctx, provider.client.Load(), mock.signIDToken("second-client", "user-1", "", expiry), "")
	require.NoError(t, err)
	_, err = provider.validateIDToken(ctx, provider.client.Load(), mock.signIDToken("first-client", "user-1", "", expiry), "")
	require.Error(t, err)

	// OIDC providers keep sending credentials in the body, and keep their
	// discovered endpoints.
	require.NoError(t, provider.UpdateClientCredentials(ClientCredentials{
		ClientID:                "second-client",
		ClientSecret:            "second-secret",
		TokenEndpointAuthMethod: oauthproto.TokenEndpointAuthMethodClientSecretBasic,
		TokenEndpoint:           "https://elsewhere.example.com/token",
	}))
	assert.Equal(t, oauth2.AuthStyleInParams, provider.client.Load().oauth2.Endpoint.AuthStyle)
	assert.Equal(t, mock.issuer+"/token", provider.client.Load().oauth2.Endpoint.TokenURL)
	_, err = provider.exchangeCodeForTokens(ctx, "code", "")
	require.NoError(t, err)
	assert.Equal(t, "second-client", seen.formID)
	assert.Equal(t, "second-secret", seen.form)
	assert.Empty(t, seen.basicID)
}

// TestOIDCProvider_UpdateDuringTokenRequestKeepsOneClient holds the token
// response open while the client is replaced. The upstream issues the ID
// token to the client that made the request, so the sign-in or refresh must
// check it with that client's verifier, not the replacement's.
func TestOIDCProvider_UpdateDuringTokenRequestKeepsOneClient(t *testing.T) {
	t.Parallel()
	for name, call := range map[string]func(context.Context, *OIDCProviderImpl) error{
		"code exchange": func(ctx context.Context, p *OIDCProviderImpl) error {
			_, err := p.ExchangeCodeForIdentity(ctx, "code", "", "")
			return err
		},
		"refresh": func(ctx context.Context, p *OIDCProviderImpl) error {
			_, err := p.RefreshTokens(ctx, "refresh-token", "user-1")
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			mock := newMockOIDCServer(t)
			t.Cleanup(mock.Close)
			reached, release := make(chan struct{}), make(chan struct{})
			mock.tokenHandler = func(w http.ResponseWriter, r *http.Request) {
				_ = r.ParseForm()
				close(reached)
				<-release
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(testTokenResponse{
					AccessToken: "at", TokenType: "Bearer", ExpiresIn: 3600, RefreshToken: "rotated",
					IDToken: mock.signIDToken(r.PostForm.Get("client_id"), "user-1", "", time.Now().Add(time.Hour)),
				})
			}
			provider, err := NewOIDCProvider(ctx, &OIDCConfig{
				CommonOAuthConfig: CommonOAuthConfig{
					ClientID: "first-client", RedirectURI: testRedirectURI, Scopes: []string{"openid"},
				},
				Issuer:            mock.issuer,
				InsecureAllowHTTP: true,
				AllowPrivateIPs:   true,
			})
			require.NoError(t, err)

			result := make(chan error, 1)
			go func() { result <- call(ctx, provider) }()
			select {
			case <-reached:
			case <-time.After(5 * time.Second):
				t.Fatal("the token request never reached the upstream")
			}
			require.NoError(t, provider.UpdateClientCredentials(ClientCredentials{ClientID: "second-client"}))
			close(release)
			select {
			case err := <-result:
				require.NoError(t, err, "a request that started under the first client must finish with it")
			case <-time.After(5 * time.Second):
				t.Fatal("the token request did not return")
			}
		})
	}
}

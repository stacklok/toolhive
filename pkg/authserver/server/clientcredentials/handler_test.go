// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package clientcredentials

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	josev3 "github.com/go-jose/go-jose/v3"
	"github.com/ory/fosite"
	"github.com/ory/fosite/compose"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/authserver/server"
	servercrypto "github.com/stacklok/toolhive/pkg/authserver/server/crypto"
	"github.com/stacklok/toolhive/pkg/authserver/server/registration"
	"github.com/stacklok/toolhive/pkg/authserver/server/session"
	spiffeauth "github.com/stacklok/toolhive/pkg/authserver/spiffe"
	"github.com/stacklok/toolhive/pkg/authserver/storage"
	"github.com/stacklok/toolhive/pkg/oauthproto"
)

type recordingHandler struct{ called bool }

func (*recordingHandler) CanHandleTokenEndpointRequest(context.Context, fosite.AccessRequester) bool {
	return true
}
func (*recordingHandler) CanSkipClientAuth(context.Context, fosite.AccessRequester) bool {
	return false
}
func (h *recordingHandler) HandleTokenEndpointRequest(context.Context, fosite.AccessRequester) error {
	h.called = true
	return nil
}
func (*recordingHandler) PopulateTokenEndpointResponse(context.Context, fosite.AccessRequester, fosite.AccessResponder) error {
	return nil
}

func TestHandlerCanHandleExactClientCredentialsGrant(t *testing.T) {
	t.Parallel()

	handler := &Handler{}
	for _, tt := range []struct {
		name   string
		grants fosite.Arguments
		want   bool
	}{
		{name: "exact client credentials", grants: fosite.Arguments{oauthproto.GrantTypeClientCredentials}, want: true},
		{name: "client credentials plus another grant", grants: fosite.Arguments{oauthproto.GrantTypeClientCredentials, oauthproto.GrantTypeTokenExchange}},
		{name: "token exchange", grants: fosite.Arguments{oauthproto.GrantTypeTokenExchange}},
		{name: "no grants"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			req := fosite.NewAccessRequest(&session.Session{})
			req.GrantTypes = tt.grants
			assert.Equal(t, tt.want, handler.CanHandleTokenEndpointRequest(context.Background(), req))
			assert.False(t, handler.CanSkipClientAuth(context.Background(), req))
		})
	}
}

func TestHandlerHandleTokenEndpointRequest(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		grants    fosite.Arguments
		client    fosite.Client
		form      url.Values
		scopes    fosite.Arguments
		wantError error
		check     func(*testing.T, *fosite.AccessRequest)
	}{
		{name: "exact grant creates SPIFFE subject session", grants: fosite.Arguments{oauthproto.GrantTypeClientCredentials}, client: authenticatedClient(t, []string{oauthproto.GrantTypeClientCredentials}), form: url.Values{"resource": {"https://resource.example.com"}}, scopes: fosite.Arguments{"openid"}, check: func(t *testing.T, req *fosite.AccessRequest) {
			t.Helper()
			sess, ok := req.GetSession().(*session.Session)
			require.True(t, ok)
			assert.Equal(t, "spiffe://example.org/workload/concrete", sess.JWTClaims.Subject)
			assert.Equal(t, "client", sess.JWTClaims.Extra[session.ClientIDClaimKey])
			assert.Equal(t, fosite.Arguments{"openid"}, req.GetGrantedScopes())
			assert.Equal(t, fosite.Arguments{"https://resource.example.com"}, req.GetGrantedAudience())
		}},
		{name: "omitted scope grants none", grants: fosite.Arguments{oauthproto.GrantTypeClientCredentials}, client: authenticatedClient(t, []string{oauthproto.GrantTypeClientCredentials}), form: url.Values{"resource": {"https://resource.example.com"}}, check: func(t *testing.T, req *fosite.AccessRequest) { t.Helper(); assert.Empty(t, req.GetGrantedScopes()) }},
		{name: "excess scope is invalid scope", grants: fosite.Arguments{oauthproto.GrantTypeClientCredentials}, client: authenticatedClient(t, []string{oauthproto.GrantTypeClientCredentials}), form: url.Values{"resource": {"https://resource.example.com"}}, scopes: fosite.Arguments{"admin"}, wantError: fosite.ErrInvalidScope},
		{name: "ordinary confidential client is rejected", grants: fosite.Arguments{oauthproto.GrantTypeClientCredentials}, client: &fosite.DefaultClient{ID: "client", Public: false}, form: url.Values{"resource": {"https://resource.example.com"}}, wantError: fosite.ErrInvalidGrant},
		{name: "unwrapped static SPIFFE client is rejected", grants: fosite.Arguments{oauthproto.GrantTypeClientCredentials}, client: staticClient(t, []string{oauthproto.GrantTypeClientCredentials}), form: url.Values{"resource": {"https://resource.example.com"}}, wantError: fosite.ErrInvalidGrant},
		{name: "association grant is rejected", grants: fosite.Arguments{oauthproto.GrantTypeClientCredentials}, client: authenticatedClient(t, []string{oauthproto.GrantTypeTokenExchange}), form: url.Values{"resource": {"https://resource.example.com"}}, wantError: fosite.ErrUnauthorizedClient},
		{name: "other grant is declined", grants: fosite.Arguments{oauthproto.GrantTypeTokenExchange}, client: authenticatedClient(t, []string{oauthproto.GrantTypeClientCredentials}), form: url.Values{"resource": {"https://resource.example.com"}}, wantError: fosite.ErrUnknownRequest},
		{name: "missing resource is invalid target", grants: fosite.Arguments{oauthproto.GrantTypeClientCredentials}, client: authenticatedClient(t, []string{oauthproto.GrantTypeClientCredentials}), wantError: errInvalidTarget()},
		{name: "empty resource is invalid target", grants: fosite.Arguments{oauthproto.GrantTypeClientCredentials}, client: authenticatedClient(t, []string{oauthproto.GrantTypeClientCredentials}), form: url.Values{"resource": {""}}, wantError: errInvalidTarget()},
		{name: "duplicate resource is invalid target", grants: fosite.Arguments{oauthproto.GrantTypeClientCredentials}, client: authenticatedClient(t, []string{oauthproto.GrantTypeClientCredentials}), form: url.Values{"resource": {"https://resource.example.com", "https://resource.example.com"}}, wantError: errInvalidTarget()},
		{name: "audience only is invalid target", grants: fosite.Arguments{oauthproto.GrantTypeClientCredentials}, client: authenticatedClient(t, []string{oauthproto.GrantTypeClientCredentials}), form: url.Values{"audience": {"https://resource.example.com"}}, wantError: errInvalidTarget()},
		{name: "unauthorized resource is invalid target", grants: fosite.Arguments{oauthproto.GrantTypeClientCredentials}, client: authenticatedClient(t, []string{oauthproto.GrantTypeClientCredentials}), form: url.Values{"resource": {"https://other.example.com"}}, wantError: errInvalidTarget()},
		{name: "malformed resource is invalid target", grants: fosite.Arguments{oauthproto.GrantTypeClientCredentials}, client: authenticatedClient(t, []string{oauthproto.GrantTypeClientCredentials}), form: url.Values{"resource": {"relative"}}, wantError: errInvalidTarget()},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			stock := &recordingHandler{}
			req := fosite.NewAccessRequest(&session.Session{})
			req.GrantTypes, req.Client, req.Form, req.RequestedScope = tt.grants, tt.client, tt.form, tt.scopes
			err := (&Handler{stock: stock}).HandleTokenEndpointRequest(context.Background(), req)
			if tt.wantError != nil {
				require.ErrorIs(t, err, tt.wantError)
				assert.False(t, stock.called)
				return
			}
			require.NoError(t, err)
			assert.True(t, stock.called)
			tt.check(t, req)
		})
	}
}

func errInvalidTarget() error { return server.ErrInvalidTarget }

func staticClient(t *testing.T, grants []string) *registration.SPIFFEClient {
	t.Helper()
	client, err := registration.NewSPIFFEClient("client", grants, []string{"openid"}, []string{"https://resource.example.com"}, nil, false)
	require.NoError(t, err)
	return client
}

func authenticatedClient(t *testing.T, grants []string) *registration.AuthenticatedSPIFFEClient {
	t.Helper()
	client := staticClient(t, grants)
	principal := spiffeauth.NewNormalizedSPIFFEPrincipal("client", "spiffe://example.org/workload/concrete", "example.org", spiffeauth.SPIFFEAuthenticationMethodJWT, spiffeauth.NewSPIFFEAuthorizationPolicy(grants, []string{"openid"}, []string{"https://resource.example.com"}, nil, false))
	authenticated, err := registration.NewAuthenticatedSPIFFEClient(client, principal)
	require.NoError(t, err)
	return authenticated
}

// TestHandlerProviderIssuesEquivalentSPIFFEClientCredentialsTokens drives Fosite's
// access-request and access-response flow with each authenticated SPIFFE carrier.
// It deliberately begins after the credential-specific authentication strategy:
// TLS peer-certificate extraction and JWT-SVID signature validation are exercised
// by the X.509 and JWT auth-arm tests in package server. What remains untested
// here is a live TLS listener or HTTP JWT assertion transport path; this test
// proves the common resolved-carrier → custom handler → Fosite issuance path.
func TestHandlerProviderIssuesEquivalentSPIFFEClientCredentialsTokens(t *testing.T) {
	t.Parallel()

	const (
		clientID   = "spiffe-client"
		spiffeID   = "spiffe://example.org/workload/concrete"
		resource   = "https://resource.example.com"
		issuer     = "https://auth.example.com"
		scopeValue = "openid profile"
	)
	requestedScopes := []string{"openid", "profile"}

	for _, method := range []spiffeauth.SPIFFEAuthenticationMethod{
		spiffeauth.SPIFFEAuthenticationMethodX509,
		spiffeauth.SPIFFEAuthenticationMethodJWT,
	} {
		t.Run(string(method), func(t *testing.T) {
			t.Parallel()

			provider, config := newSPIFFEClientCredentialsProvider(t, issuer)
			carrier := newAuthenticatedCarrier(t, clientID, spiffeID, method, requestedScopes, resource)
			config.ClientAuthenticationStrategy = func(context.Context, *http.Request, url.Values) (fosite.Client, error) {
				return carrier, nil
			}

			form := url.Values{
				"client_id":  {clientID},
				"grant_type": {oauthproto.GrantTypeClientCredentials},
				"resource":   {resource},
				"scope":      {scopeValue},
			}
			req := httptest.NewRequest(http.MethodPost, issuer+"/oauth/token", strings.NewReader(form.Encode()))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

			accessRequest, err := provider.NewAccessRequest(context.Background(), req, session.New("", "", "", session.UserClaims{}))
			require.NoError(t, err)
			assert.Equal(t, fosite.Arguments(requestedScopes), accessRequest.GetGrantedScopes())
			assert.Equal(t, fosite.Arguments{resource}, accessRequest.GetGrantedAudience())

			issuedSession, ok := accessRequest.GetSession().(*session.Session)
			require.True(t, ok)
			assert.Equal(t, spiffeID, issuedSession.JWTClaims.Subject)
			assert.Equal(t, clientID, issuedSession.JWTClaims.Extra[session.ClientIDClaimKey])

			response, err := provider.NewAccessResponse(context.Background(), accessRequest)
			require.NoError(t, err)
			claims := accessTokenClaims(t, response.GetAccessToken())
			assert.Equal(t, spiffeID, claims["sub"])
			assert.Equal(t, clientID, claims[session.ClientIDClaimKey])
			assert.Equal(t, requestedScopes, claimScopes(t, claims["scope"]))
			assert.Equal(t, []string{resource}, claimAudience(t, claims["aud"]))
		})
	}
}

func newSPIFFEClientCredentialsProvider(t *testing.T, issuer string) (fosite.OAuth2Provider, *server.AuthorizationServerConfig) {
	t.Helper()

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	config, err := server.NewAuthorizationServerConfig(&server.AuthorizationServerParams{
		Issuer:               issuer,
		AccessTokenLifespan:  time.Hour,
		RefreshTokenLifespan: 24 * time.Hour,
		AuthCodeLifespan:     time.Minute,
		HMACSecrets:          servercrypto.NewHMACSecrets([]byte("test-secret-with-32-bytes-long!!")),
		SigningKeyID:         "test-key",
		SigningKeyAlgorithm:  "RS256",
		SigningKey:           key,
		AllowedAudiences:     []string{"https://resource.example.com"},
	})
	require.NoError(t, err)

	store := storage.NewMemoryStorage()
	t.Cleanup(func() { _ = store.Close() })
	signingKey := &josev3.JSONWebKey{Key: key, KeyID: "test-key", Algorithm: "RS256", Use: "sig"}
	strategy := &compose.CommonStrategy{CoreStrategy: compose.NewOAuth2JWTStrategy(
		func(context.Context) (interface{}, error) { return signingKey, nil },
		compose.NewOAuth2HMACStrategy(config.Config), config.Config,
	)}
	provider, err := server.NewAuthorizationServer(config, store, strategy, Factory())
	require.NoError(t, err)
	return provider, config
}

func newAuthenticatedCarrier(
	t *testing.T, clientID, spiffeID string, method spiffeauth.SPIFFEAuthenticationMethod, scopes []string, resource string,
) *registration.AuthenticatedSPIFFEClient {
	t.Helper()

	client, err := registration.NewSPIFFEClient(clientID, []string{oauthproto.GrantTypeClientCredentials}, scopes, []string{resource}, nil, false)
	require.NoError(t, err)
	principal := spiffeauth.NewNormalizedSPIFFEPrincipal(
		clientID, spiffeID, "example.org", method,
		spiffeauth.NewSPIFFEAuthorizationPolicy([]string{oauthproto.GrantTypeClientCredentials}, scopes, []string{resource}, nil, false),
	)
	carrier, err := registration.NewAuthenticatedSPIFFEClient(client, principal)
	require.NoError(t, err)
	return carrier
}

func accessTokenClaims(t *testing.T, token string) map[string]any {
	t.Helper()

	parts := strings.Split(token, ".")
	require.Len(t, parts, 3)
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	require.NoError(t, err)
	claims := make(map[string]any)
	require.NoError(t, json.Unmarshal(payload, &claims))
	return claims
}

func claimScopes(t *testing.T, claim any) []string {
	t.Helper()

	switch scopes := claim.(type) {
	case string:
		return strings.Fields(scopes)
	case []any:
		values := make([]string, len(scopes))
		for i, value := range scopes {
			var ok bool
			values[i], ok = value.(string)
			require.True(t, ok, "scope claim entry must be a string")
		}
		return values
	default:
		require.Failf(t, "unexpected scope claim", "type %T", claim)
		return nil
	}
}

func claimAudience(t *testing.T, claim any) []string {
	t.Helper()

	switch audience := claim.(type) {
	case string:
		return []string{audience}
	case []any:
		values := make([]string, len(audience))
		for i, value := range audience {
			var ok bool
			values[i], ok = value.(string)
			require.True(t, ok, "audience claim entry must be a string")
		}
		return values
	default:
		require.Failf(t, "unexpected audience claim", "type %T", claim)
		return nil
	}
}

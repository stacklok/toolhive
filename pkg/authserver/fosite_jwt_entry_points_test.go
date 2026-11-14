// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package authserver_test

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/ory/fosite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/authserver"
	servercrypto "github.com/stacklok/toolhive/pkg/authserver/server/crypto"
	"github.com/stacklok/toolhive/pkg/oauthproto"
)

// unsafeJWSHint is part of the hint fositejose.IsUnsafeJWS rejections carry.
// fosite's own errors never contain it, so finding it in a response shows the
// guard answered before fosite parsed the token.
const unsafeJWSHint = "compact JWS with a JSON header and payload"

// TestFositeJWTEntryPointsRefuseUnsafeTokens drives every entry point where an
// untrusted JWT reaches fosite's go-jose v3 parser through a running server.
// The client is private_key_jwt, so it has the registered keys fosite needs
// before it parses a client assertion or a request object. Its claims nest
// one level past encoding/json's limit, far below the depth that overflows
// go-jose v3, so a regression fails the test instead of crashing it.
func TestFositeJWTEntryPointsRefuseUnsafeTokens(t *testing.T) {
	t.Parallel()

	enc := base64.RawURLEncoding
	nestedClaims := `{"iss":"` + cfgBrokerID + `","sub":"` + cfgBrokerID + `","x":` +
		strings.Repeat("[", 10000) + strings.Repeat("]", 10000) + `}`
	unsafeJWT := enc.EncodeToString([]byte(`{"alg":"ES256","kid":"broker-key"}`)) + "." +
		enc.EncodeToString([]byte(nestedClaims)) + ".c2ln"

	for _, backend := range []string{"memory", "redis"} {
		t.Run(backend, func(t *testing.T) {
			t.Parallel()
			jwksFile := filepath.Join(t.TempDir(), "broker.jwks")
			writeJWKS(t, jwksFile, newAssertionKey(t, "broker-key"))
			srv, _, _, _ := startEmbeddedAuthServer(t, func(cfg *authserver.RunConfig, _ string) {
				configuredClients(brokerClient(jwksFile))(cfg)
			}, newStorageForBackend(t, backend))

			t.Run("client assertion at the token endpoint", func(t *testing.T) {
				t.Parallel()
				result := postToken(t, srv.URL, clientAuth{}, url.Values{
					"grant_type": {oauthproto.GrantTypeAuthorizationCode}, "code": {"code"},
					"redirect_uri": {cfgRedirectURI}, "client_id": {cfgBrokerID},
					"client_assertion_type": {cfgAssertionType}, "client_assertion": {unsafeJWT},
				})
				assert.Equal(t, http.StatusUnauthorized, result.status, result.body)
				assert.Equal(t, "invalid_client", result.body["error"])
				assert.Contains(t, result.body["error_description"], unsafeJWSHint)
			})

			t.Run("request object at the authorize endpoint", func(t *testing.T) {
				t.Parallel()
				status, body := authorizeError(t, srv.URL, url.Values{"request": {unsafeJWT}})
				assert.Equal(t, http.StatusBadRequest, status, body)
				assert.Equal(t, "invalid_request_object", body.Get("error"))
				assert.Contains(t, body.Get("error_description"), unsafeJWSHint)
			})

			// fosite fetches a request_uri only when the client registered it,
			// and no ToolHive client model can carry registered request URIs.
			t.Run("request_uri is refused without a fetch", func(t *testing.T) {
				t.Parallel()
				var fetches atomic.Int32
				requestURIServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					fetches.Add(1)
					_, _ = io.WriteString(w, unsafeJWT)
				}))
				t.Cleanup(requestURIServer.Close)

				status, body := authorizeError(t, srv.URL, url.Values{"request_uri": {requestURIServer.URL}})
				assert.Equal(t, http.StatusBadRequest, status, body)
				assert.Equal(t, "invalid_request_uri", body.Get("error"))
				assert.Zero(t, fetches.Load())
			})
		})
	}
}

// TestFositeJWTParsingInventory pins the routes and fosite handlers through
// which an untrusted JWT can reach fosite's go-jose v3 parser. Client
// assertions (every route that authenticates a client) and request objects
// passed by value at /oauth/authorize are guarded by fositejose.IsUnsafeJWS.
// Introspection, revocation, pushed authorization and fosite's own assertion
// grants would parse tokens the guard never sees, so a new route or handler
// fails here and must be checked for a JWT fosite parses before it is added.
// The config enables device flow, token exchange and the JWT-bearer grant, so
// every optional route and handler is present.
func TestFositeJWTParsingInventory(t *testing.T) {
	t.Parallel()

	cfg, stor := removedClientConfig(t)
	srv, err := authserver.New(t.Context(), cfg, stor)
	require.NoError(t, err)
	t.Cleanup(func() { _ = srv.Close() })
	router, ok := srv.Handler().(chi.Routes)
	require.True(t, ok, "server handler is %T, not a chi router", srv.Handler())
	var routes []string
	require.NoError(t, chi.Walk(router, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		routes = append(routes, method+" "+route)
		return nil
	}))
	assert.ElementsMatch(t, []string{
		"GET /oauth/authorize", "POST /oauth/consent", "GET /oauth/callback", "POST /oauth/token",
		"POST /oauth/register", "POST /oauth/device_authorization", "GET /oauth/device", "POST /oauth/device",
		"POST /oauth/device/confirm", "GET /.well-known/jwks.json",
		"GET /.well-known/oauth-authorization-server", "GET /.well-known/oauth-authorization-server/*",
		"GET /.well-known/openid-configuration", "GET /.well-known/openid-configuration/*",
	}, routes, "routes changed: a route that hands fosite a JWT must refuse fositejose.IsUnsafeJWS tokens first")

	providerCfg, providerStor := removedClientConfig(t)
	t.Cleanup(func() { _ = providerStor.Close() })
	built, err := authserver.BuildProviderForTest(t.Context(), providerCfg, providerStor)
	require.NoError(t, err)
	provider, ok := built.(*fosite.Fosite)
	require.True(t, ok)
	ctx := t.Context()
	typesOf := func(handlers any) []string {
		var names []string
		v := reflect.ValueOf(handlers)
		for i := range v.Len() {
			names = append(names, v.Index(i).Elem().Type().String())
		}
		return names
	}
	const msg = "fosite handlers changed: a handler that parses a JWT with fosite must refuse fositejose.IsUnsafeJWS tokens first"
	assert.ElementsMatch(t, []string{"*oauth2.AuthorizeExplicitGrantHandler", "*pkce.Handler"},
		typesOf(provider.Config.GetAuthorizeEndpointHandlers(ctx)), msg)
	assert.ElementsMatch(t, []string{
		"*oauth2.AuthorizeExplicitGrantHandler", "*oauth2.RefreshTokenGrantHandler", "*pkce.Handler",
		"*tokenexchange.Handler", "*tokenexchange.RefreshTokenHandler", "*tokenexchange.JWTBearerHandler",
		"*tokenexchange.IDJAGHandler", "*deviceflow.Handler", "*authserver.refreshGrantValidators",
	}, typesOf(provider.Config.GetTokenEndpointHandlers(ctx)), msg)
	assert.Empty(t, provider.Config.GetTokenIntrospectionHandlers(ctx), msg)
	assert.Empty(t, provider.Config.GetRevocationHandlers(ctx), msg)
	par, ok := provider.Config.(fosite.PushedAuthorizeRequestHandlersProvider)
	require.True(t, ok, "provider config %T does not expose its pushed authorization handlers", provider.Config)
	assert.Empty(t, par.GetPushedAuthorizeEndpointHandlers(ctx), msg)
}

// authorizeError sends the broker's authorization request with extra
// parameters, without following redirects, and returns the status and the
// error parameters from the redirect or the JSON body.
func authorizeError(t *testing.T, serverURL string, extra url.Values) (int, url.Values) {
	t.Helper()
	query := url.Values{
		"client_id": {cfgBrokerID}, "redirect_uri": {cfgRedirectURI}, "response_type": {"code"},
		"scope": {"openid profile"}, "state": {"probe-state"}, "resource": {cfgAudienceA},
		"code_challenge":        {servercrypto.ComputePKCEChallenge(servercrypto.GeneratePKCEVerifier())},
		"code_challenge_method": {"S256"},
	}
	for k, v := range extra {
		query[k] = v
	}
	response, err := (&http.Client{
		Timeout:       10 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}).Get(serverURL + "/oauth/authorize?" + query.Encode())
	require.NoError(t, err)
	defer func() {
		_, _ = io.Copy(io.Discard, response.Body)
		require.NoError(t, response.Body.Close())
	}()
	if location, err := response.Location(); err == nil {
		return response.StatusCode, location.Query()
	}
	var body map[string]string
	require.NoError(t, json.NewDecoder(response.Body).Decode(&body))
	values := url.Values{}
	for k, v := range body {
		values.Set(k, v)
	}
	return response.StatusCode, values
}

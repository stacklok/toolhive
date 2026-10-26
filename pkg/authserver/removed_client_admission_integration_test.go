// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package authserver_test

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/go-chi/chi/v5"
	"github.com/ory/fosite"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/authserver"
	authserverrunner "github.com/stacklok/toolhive/pkg/authserver/runner"
	servercrypto "github.com/stacklok/toolhive/pkg/authserver/server/crypto"
	"github.com/stacklok/toolhive/pkg/authserver/server/keys"
	"github.com/stacklok/toolhive/pkg/authserver/server/registration"
	"github.com/stacklok/toolhive/pkg/authserver/server/tokenexchange"
	"github.com/stacklok/toolhive/pkg/authserver/storage"
	"github.com/stacklok/toolhive/pkg/authserver/upstream"
	"github.com/stacklok/toolhive/pkg/oauthproto"
	"github.com/stacklok/toolhive/test/integration/authserver/helpers"
)

// This harness owns the wiring: a real server on Redis, restarted without a
// client, must reject that client on every route that resolves one. The
// admission decision is owned by the storage tests, and what each handler does
// before it rejects by the handler tests. A new client-bearing route must add a
// removalProbe (or, for a route that never resolves a client, an entry in
// removedClientExemptRoutes); TestRemovedClientProbeCoverage fails otherwise.

const (
	removedClientKeyPrefix   = "test:removed-client:auth:"
	removedClientSurvivorID  = "surviving-delegate"
	removedClientSurvivorPwd = "surviving-delegate-secret-well-above-minimum-length"
	removedClientRedirectURI = "http://127.0.0.1:9/callback"
	removedClientUpstream    = "test-upstream"
)

// removedClientKind says how a fixture's row got into storage and what the
// restart does to it.
type removedClientKind int

const (
	// kindDelegate is a configured delegate: declared on the first start only.
	// Served before the restart, refused after.
	kindDelegate removedClientKind = iota
	// kindEmbedder is a client an embedder writes with RegisterClient and never
	// declares. Served before and after the restart.
	kindEmbedder
	// kindLegacy is a delegate row as a binary that predates the configured
	// stamp wrote it, put into Redis as raw JSON and never declared. Refused
	// before and after.
	kindLegacy
)

// removedClientFixture is a client row that is in storage across both server
// starts. Only the kindDelegate fixture is removed from configuration.
type removedClientFixture struct {
	name, id, secret string
	kind             removedClientKind
}

func (f removedClientFixture) servedBefore() bool { return f.kind != kindLegacy }
func (f removedClientFixture) servedAfter() bool  { return f.kind == kindEmbedder }

// authCapable reports whether the client can run the authorization-code and
// device flows, so "served" can be shown beyond the token endpoint.
func (f removedClientFixture) authCapable() bool { return f.kind == kindEmbedder }

var removedClientFixtures = []removedClientFixture{
	{name: "delegate", id: "removed-delegate", secret: "removed-delegate-secret-well-above-minimum-length"},
	{name: "embedder", id: "embedder-client", secret: "embedder-client-secret-well-above-minimum-length", kind: kindEmbedder},
	{name: "legacy", id: "removed-legacy", secret: "removed-legacy-secret-well-above-minimum-length", kind: kindLegacy},
}

// removalProbe exercises one client-bearing route. before runs on the first
// start and may seed state; after runs on the second start, once the delegate
// has left the configuration, and must show each fixture served or refused as
// its kind says. A probe with once set runs a single time with a zero fixture;
// otherwise it runs for the delegate, and for every other fixture too when
// forAll is set.
type removalProbe struct {
	name   string
	covers []string
	once   bool
	forAll bool
	before func(t *testing.T, w *removedClientWorld, f removedClientFixture) any
	after  func(t *testing.T, w *removedClientWorld, f removedClientFixture, state any)
}

// removedClientExemptRoutes lists the routes no probe covers, with the reason
// each is left out.
var removedClientExemptRoutes = map[string]string{
	"GET /.well-known/jwks.json":                    "discovery: no client",
	"GET /.well-known/oauth-authorization-server":   "discovery: no client",
	"GET /.well-known/oauth-authorization-server/*": "discovery: no client",
	"GET /.well-known/openid-configuration":         "discovery: no client",
	"GET /.well-known/openid-configuration/*":       "discovery: no client",
	"GET /oauth/device":                             "verification form: only renders the form, resolves no client",
	"GET /oauth/callback":                           "pending authorization: client resolved when the request was created",
	"POST /oauth/consent":                           "pending authorization: client resolved when the request was created",
	"POST /oauth/device":                            "pending device request: client resolved at device authorization",
	"POST /oauth/device/confirm":                    "pending device request: client resolved at device authorization",
}

var removalProbes = []removalProbe{
	{
		name: "authorize", covers: []string{"GET /oauth/authorize"}, forAll: true,
		before: func(t *testing.T, w *removedClientWorld, f removedClientFixture) any {
			t.Helper()
			assertAuthorize(t, w, f, f.servedBefore())
			return nil
		},
		after: func(t *testing.T, w *removedClientWorld, f removedClientFixture, _ any) {
			t.Helper()
			assertAuthorize(t, w, f, f.servedAfter())
		},
	},
	{
		name: "device_authorization", covers: []string{"POST /oauth/device_authorization"},
		before: func(t *testing.T, w *removedClientWorld, f removedClientFixture) any {
			t.Helper()
			resp := w.post(t, "/oauth/device_authorization", url.Values{"client_id": {f.id}, "client_secret": {f.secret}})
			assert.Equal(t, "unauthorized_client", resp.body["error"], "a delegate has no device grant: %s", resp.raw)
			return nil
		},
		after: func(t *testing.T, w *removedClientWorld, f removedClientFixture, _ any) {
			t.Helper()
			assertInvalidClient401(t, w.post(t, "/oauth/device_authorization", url.Values{"client_id": {f.id}, "client_secret": {f.secret}}))
		},
	},
	{
		// fosite authenticates the client before any grant handler runs, so one
		// probe stands for every grant (see TestRemovedClientHandlerTypes).
		name: "token endpoint", covers: []string{"POST /oauth/token"}, forAll: true,
		before: func(t *testing.T, w *removedClientWorld, f removedClientFixture) any {
			t.Helper()
			assertTokenEndpoint(t, w, f, f.servedBefore())
			return nil
		},
		after: func(t *testing.T, w *removedClientWorld, f removedClientFixture, _ any) {
			t.Helper()
			assertTokenEndpoint(t, w, f, f.servedAfter())
		},
	},
	{
		name: "surviving delegate still exchanges", once: true,
		before: func(t *testing.T, w *removedClientWorld, _ removedClientFixture) any {
			t.Helper()
			require.Equal(t, http.StatusOK, w.tokenExchange(t).status)
			return nil
		},
		after: func(t *testing.T, w *removedClientWorld, _ removedClientFixture, _ any) {
			t.Helper()
			resp := w.tokenExchange(t)
			require.Equal(t, http.StatusOK, resp.status, "a configured client must survive the restart: %s", resp.raw)
		},
	},
	{
		name: "registered client still authorizes", once: true, covers: []string{"POST /oauth/register"},
		before: func(t *testing.T, w *removedClientWorld, _ removedClientFixture) any {
			t.Helper()
			resp := w.send(t, http.MethodPost, "/oauth/register", strings.NewReader(
				`{"redirect_uris":["`+removedClientRedirectURI+`"],"token_endpoint_auth_method":"none",`+
					`"grant_types":["authorization_code"],"response_types":["code"],"scope":"scope-a"}`),
				func(r *http.Request) { r.Header.Set("Content-Type", "application/json") })
			require.Equal(t, http.StatusCreated, resp.status, resp.raw)
			return resp.body["client_id"]
		},
		after: func(t *testing.T, w *removedClientWorld, _ removedClientFixture, state any) {
			t.Helper()
			id, ok := state.(string)
			require.True(t, ok, "before state is %T, not a client_id string", state)
			resp := w.authorize(t, id)
			assert.Equal(t, http.StatusOK, resp.status, "a registered client must reach the consent page: %s", resp.raw)
		},
	},
}

// assertAuthorize checks the authorization endpoint: a served client that can
// authorize reaches the consent page; a refused one gets invalid_client and no
// code. A served delegate cannot authorize, so nothing is asserted for it.
func assertAuthorize(t *testing.T, w *removedClientWorld, f removedClientFixture, served bool) {
	t.Helper()
	resp := w.authorize(t, f.id)
	if served {
		if f.authCapable() {
			assert.Equal(t, http.StatusOK, resp.status, "a served client must reach the consent page: %s", resp.raw)
		}
		return
	}
	location, err := url.Parse(resp.header.Get("Location"))
	require.NoError(t, err)
	assert.Empty(t, location.Query().Get("code"), "no authorization code may be issued")
	code, _ := resp.body["error"].(string)
	if code == "" {
		code = location.Query().Get("error")
	}
	assert.Equal(t, "invalid_client", code, resp.raw)
}

// assertTokenEndpoint checks client authentication at the token endpoint: a
// served client gets a grant error for the empty exchange, a refused one 401.
func assertTokenEndpoint(t *testing.T, w *removedClientWorld, f removedClientFixture, served bool) {
	t.Helper()
	resp := w.tokenRequest(t, f)
	if served {
		assert.NotEqual(t, http.StatusUnauthorized, resp.status, "a served client must authenticate: %s", resp.raw)
		return
	}
	assertInvalidClient401(t, resp)
}

func TestRemovedClientsAreNotServedAfterRestart(t *testing.T) {
	t.Parallel()

	w := newRemovedClientWorld(t)
	first := w.start(t, removedClientFixtures)

	state := map[string]any{}
	passed := map[string]bool{}
	forEachProbe := func(phase string, run func(t *testing.T, p removalProbe, f removedClientFixture, key string)) {
		for _, p := range removalProbes {
			fixtures := removedClientFixtures
			if p.once {
				fixtures = []removedClientFixture{{}}
			}
			for _, f := range fixtures {
				if f.kind != kindDelegate && !p.forAll {
					continue
				}
				key := p.name + "/" + f.id
				passed[phase+"/"+key] = t.Run(strings.Trim(phase+"/"+p.name+"/"+f.name, "/"), func(t *testing.T) {
					run(t, p, f, key)
				})
			}
		}
	}

	forEachProbe("before", func(t *testing.T, p removalProbe, f removedClientFixture, key string) {
		t.Helper()
		state[key] = p.before(t, w, f)
	})
	first()
	w.start(t, nil)
	forEachProbe("after", func(t *testing.T, p removalProbe, f removedClientFixture, key string) {
		t.Helper()
		if !passed["before/"+key] {
			t.Skip("the before step did not succeed")
		}
		p.after(t, w, f, state[key])
	})
}

// TestRemovedClientProbeCoverage pins that every route has a probe or an
// explicit exemption, in both directions.
func TestRemovedClientProbeCoverage(t *testing.T) {
	t.Parallel()

	covered := map[string]bool{}
	for _, p := range removalProbes {
		for _, c := range p.covers {
			covered[c] = true
		}
	}
	cfg, stor := removedClientConfig(t)
	srv, err := authserver.New(t.Context(), cfg, stor)
	require.NoError(t, err)
	t.Cleanup(func() { _ = srv.Close() })
	router, ok := srv.Handler().(chi.Routes)
	require.True(t, ok, "server handler is %T, not a chi router", srv.Handler())

	var walked []string
	require.NoError(t, chi.Walk(router, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		walked = append(walked, method+" "+route)
		return nil
	}))
	for _, route := range walked {
		_, isExempt := removedClientExemptRoutes[route]
		assert.Truef(t, covered[route] != isExempt, "route %q must be covered by exactly one of a probe or the exempt list", route)
	}
	for route := range removedClientExemptRoutes {
		assert.Contains(t, walked, route, "stale exempt route")
	}
	for route := range covered {
		assert.Contains(t, walked, route, "probe covers a route the server does not serve")
	}
}

// TestRemovedClientHandlerTypes is what lets a single token-endpoint probe stand
// for every grant. Fosite drops the client authentication error only when a
// handler that handles the request returns true from CanSkipClientAuth, so two
// properties are pinned: without credentials only the plain JWT-bearer handler
// may skip, and with credentials no handler may skip. It also pins the set of
// authorize and token handler types so a new grant has to be considered here.
// The provider comes from the same parameter construction newServer uses.
func TestRemovedClientHandlerTypes(t *testing.T) {
	t.Parallel()

	cfg, stor := removedClientConfig(t)
	t.Cleanup(func() { _ = stor.Close() })
	built, err := authserver.BuildProviderForTest(t.Context(), cfg, stor)
	require.NoError(t, err)
	provider, ok := built.(*fosite.Fosite)
	require.True(t, ok)

	const jwtBearerHandler = "*tokenexchange.JWTBearerHandler"
	grants := []string{
		"authorization_code", "refresh_token", oauthproto.GrantTypeTokenExchange, oauthproto.GrantTypeJWTBearer,
		"urn:ietf:params:oauth:grant-type:device_code", "client_credentials", "password",
	}
	ctx := t.Context()
	var gotToken, gotAuthorize []string
	for _, h := range provider.Config.GetTokenEndpointHandlers(ctx) {
		name := reflect.TypeOf(h).String()
		gotToken = append(gotToken, name)
		skips := func(withCreds bool) bool {
			return slices.ContainsFunc(grants, func(grant string) bool {
				req := fosite.NewAccessRequest(nil)
				req.Form = url.Values{"grant_type": {grant}, "assertion": {"a.b.c"}}
				httpReq := httptest.NewRequest(http.MethodPost, "/oauth/token", nil)
				if withCreds {
					req.Form.Set("client_id", removedClientSurvivorID)
					httpReq.SetBasicAuth(removedClientSurvivorID, removedClientSurvivorPwd)
				}
				req.GrantTypes = fosite.Arguments{grant}
				return h.CanSkipClientAuth(context.WithValue(ctx, fosite.RequestContextKey, httpReq), req)
			})
		}
		assert.Equalf(t, name == jwtBearerHandler, skips(false), "%s: only the JWT-bearer handler may skip client authentication", name)
		assert.Falsef(t, skips(true), "%s: no handler may skip client authentication when credentials are present", name)
	}
	for _, h := range provider.Config.GetAuthorizeEndpointHandlers(ctx) {
		gotAuthorize = append(gotAuthorize, reflect.TypeOf(h).String())
	}
	assert.ElementsMatch(t, []string{
		"*oauth2.AuthorizeExplicitGrantHandler", "*oauth2.RefreshTokenGrantHandler", "*pkce.Handler", "*tokenexchange.Handler",
		jwtBearerHandler, "*tokenexchange.IDJAGHandler", "*deviceflow.Handler",
		"*authserver.refreshGrantValidators",
	}, gotToken, "token endpoint handlers changed: decide whether the new one authenticates the client")
	assert.ElementsMatch(t, []string{"*oauth2.AuthorizeExplicitGrantHandler", "*pkce.Handler"}, gotAuthorize,
		"authorize endpoint handlers changed: add a probe for the new one")
}

// removedClientConfig is an in-memory config with device flow, a trusted issuer
// and a delegate client, so every grant handler is registered. The caller owns
// the storage (authserver.New takes ownership of it).
func removedClientConfig(t *testing.T) (authserver.Config, storage.Storage) {
	t.Helper()
	secret := make([]byte, 32)
	_, err := rand.Read(secret)
	require.NoError(t, err)
	mockUpstream := helpers.NewMockUpstreamIDP(t)
	return authserver.Config{
		Issuer:      "http://127.0.0.1:1",
		KeyProvider: keys.NewGeneratingProvider(keys.DefaultAlgorithm),
		HMACSecrets: servercrypto.NewHMACSecrets(secret),
		Upstreams: []authserver.UpstreamConfig{{
			Name: removedClientUpstream, Type: authserver.UpstreamProviderTypeOAuth2,
			OAuth2Config: &upstream.OAuth2Config{
				CommonOAuthConfig: upstream.CommonOAuthConfig{
					ClientID: "upstream-client", RedirectURI: "http://127.0.0.1:1/oauth/callback",
				},
				AuthorizationEndpoint: mockUpstream.URL() + "/authorize",
				TokenEndpoint:         mockUpstream.URL() + "/token",
			},
		}},
		ScopesSupported:   []string{"scope-a"},
		AllowedAudiences:  []string{delegateAudience},
		DeviceFlowEnabled: true,
		InsecureAllowConfidentialOverLoopbackHTTP: true,
		RefreshGrantHandlers:                      []fosite.TokenEndpointHandler{struct{ fosite.TokenEndpointHandler }{}},
		TrustedIssuers: []tokenexchange.TrustedIssuer{{
			IssuerURL: "https://external-issuer.example.com", ExpectedAudience: delegateAudience,
			JWKSURL: "http://127.0.0.1:1/jwks", InsecureAllowHTTP: true, AllowPrivateIPs: true,
			ActorClaim: "azp", AllowedActors: []string{"external-subject"}, AllowedDelegateClients: []string{"*"},
			JWTBearerGrant: &tokenexchange.JWTBearerGrantPolicy{
				MaxAssertionAge: "10m",
				SubjectBindings: []tokenexchange.JWTBearerSubjectBinding{{
					Subject: "external-subject", AllowedResources: []string{delegateAudience},
				}},
			},
		}},
		DelegateClients: []authserver.DelegateClient{{
			ClientID: removedClientSurvivorID, ClientSecret: removedClientSurvivorPwd,
			Scopes: []string{"scope-a"}, Audiences: []string{delegateAudience},
		}},
	}, storage.NewMemoryStorage()
}

// removedClientWorld holds what survives a restart (the Redis and the
// upstream) plus the currently running server.
type removedClientWorld struct {
	mr        *miniredis.Miniredis
	base      *storage.RedisStorage
	upstream  *helpers.MockUpstreamIDP
	baseURL   string
	exchanges atomic.Int32
	embedded  *authserverrunner.EmbeddedAuthServer
	http      *http.Client
}

func newRemovedClientWorld(t *testing.T) *removedClientWorld {
	t.Helper()
	mr := miniredis.RunT(t)
	rc := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rc.Close() })
	w := &removedClientWorld{
		mr:   mr,
		base: storage.NewRedisStorageWithClient(rc, removedClientKeyPrefix),
		http: &http.Client{
			Timeout:       10 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}
	w.upstream = helpers.NewMockUpstreamIDP(t, func(m *helpers.MockUpstreamIDP) {
		m.TokenHandler = func(rw http.ResponseWriter, _ *http.Request) {
			w.exchanges.Add(1)
			rw.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(rw).Encode(map[string]any{"access_token": "mock", "token_type": "Bearer", "expires_in": 3600})
		}
	})
	return w
}

// start boots a server on a fresh listener with the survivor and the given
// fixtures configured, replacing the previous one. The returned function stops it.
func (w *removedClientWorld) start(t *testing.T, fixtures []removedClientFixture) (stop func()) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	w.baseURL = "http://" + listener.Addr().String()

	cfg := helpers.NewTestAuthServerConfig(t, w.upstream.URL(), helpers.WithScopesSupported([]string{"scope-a"}))
	cfg.Issuer = w.baseURL
	cfg.Upstreams[0].Name = removedClientUpstream
	cfg.Upstreams[0].OAuth2Config.RedirectURI = w.baseURL + "/oauth/callback"
	cfg.AllowedAudiences = []string{delegateAudience}
	cfg.InsecureAllowConfidentialOverLoopbackHTTP = true
	cfg.DeviceFlowEnabled = true
	cfg.Storage = &storage.RunConfig{
		Type:        string(storage.TypeRedis),
		RedisConfig: &storage.RedisRunConfig{Addr: w.mr.Addr(), KeyPrefix: removedClientKeyPrefix},
	}

	delegates := map[string]string{removedClientSurvivorID: removedClientSurvivorPwd}
	for _, f := range fixtures {
		switch f.kind {
		case kindDelegate:
			delegates[f.id] = f.secret
		case kindEmbedder:
			hashed, err := registration.SHA256Hasher.Hash(t.Context(), []byte(f.secret))
			require.NoError(t, err)
			err = w.base.RegisterClient(t.Context(), &fosite.DefaultClient{
				ID: f.id, Secret: hashed, RedirectURIs: []string{removedClientRedirectURI},
				ResponseTypes: []string{"code"}, GrantTypes: []string{"authorization_code", "refresh_token"},
				Scopes: []string{"scope-a"}, Audience: []string{delegateAudience},
			})
			if !errors.Is(err, storage.ErrAlreadyExists) {
				require.NoError(t, err)
			}
		case kindLegacy:
			w.writeLegacyDelegateRow(t, f)
		}
	}
	dir := t.TempDir()
	for id, secret := range delegates {
		file := filepath.Join(dir, id)
		require.NoError(t, os.WriteFile(file, []byte(secret), 0o600))
		cfg.DelegateClients = append(cfg.DelegateClients, authserver.DelegateClientRunConfig{
			ClientID: id, ClientSecretFile: file, Scopes: []string{"scope-a"}, Audiences: []string{delegateAudience},
		})
	}

	embedded, err := authserverrunner.NewEmbeddedAuthServer(t.Context(), cfg)
	require.NoError(t, err)
	server := httptest.NewUnstartedServer(embedded.Handler())
	server.Listener = listener
	server.Start()
	w.embedded = embedded

	var once sync.Once
	stop = func() {
		once.Do(func() {
			server.Close()
			assert.NoError(t, embedded.Close())
		})
	}
	t.Cleanup(stop)
	return stop
}

// writeLegacyDelegateRow writes the row a binary that predates the configured
// stamp wrote for a delegate: confidential, hashed secret, token-exchange only,
// defaulted response types, no stamp and no token endpoint auth method. It goes
// in as raw JSON so the current writer cannot add the stamp.
func (w *removedClientWorld) writeLegacyDelegateRow(t *testing.T, f removedClientFixture) {
	t.Helper()
	hashed, err := registration.SHA256Hasher.Hash(t.Context(), []byte(f.secret))
	require.NoError(t, err)
	raw, err := json.Marshal(map[string]any{
		"id": f.id, "secret": hashed, "redirect_uris": nil,
		"grant_types":    []string{oauthproto.GrantTypeTokenExchange},
		"response_types": []string{"code"},
		"scopes":         []string{"scope-a"}, "audience": []string{delegateAudience},
		"public": false,
	})
	require.NoError(t, err)
	w.mr.Set(removedClientKeyPrefix+storage.KeyTypeClient+":"+f.id, string(raw))
}

type removedClientResponse struct {
	status int
	header http.Header
	raw    string
	body   map[string]any
}

// send issues one request against the running server; opts adjust it first.
func (w *removedClientWorld) send(
	t *testing.T, method, path string, body io.Reader, opts ...func(*http.Request),
) removedClientResponse {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), method, w.baseURL+path, body)
	require.NoError(t, err)
	for _, opt := range opts {
		opt(req)
	}
	resp, err := w.http.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	out := removedClientResponse{status: resp.StatusCode, header: resp.Header, raw: string(raw)}
	_ = json.Unmarshal(raw, &out.body)
	return out
}

func (w *removedClientWorld) post(t *testing.T, path string, form url.Values, opts ...func(*http.Request)) removedClientResponse {
	t.Helper()
	form1 := func(r *http.Request) { r.Header.Set("Content-Type", "application/x-www-form-urlencoded") }
	return w.send(t, http.MethodPost, path, strings.NewReader(form.Encode()), append([]func(*http.Request){form1}, opts...)...)
}

func basicAuth(id, secret string) func(*http.Request) {
	return func(r *http.Request) { r.SetBasicAuth(id, secret) }
}

func (w *removedClientWorld) authorize(t *testing.T, clientID string) removedClientResponse {
	t.Helper()
	sum := sha256.Sum256([]byte("removed-client-pkce-verifier-0123456789-0123456789"))
	q := url.Values{
		"response_type": {"code"}, "client_id": {clientID}, "redirect_uri": {removedClientRedirectURI},
		"scope": {"scope-a"}, "state": {"state-value-0123456789"}, "resource": {delegateAudience},
		"code_challenge": {base64.RawURLEncoding.EncodeToString(sum[:])}, "code_challenge_method": {"S256"},
	}
	return w.send(t, http.MethodGet, "/oauth/authorize?"+q.Encode(), nil)
}

// tokenExchange performs a token exchange as the surviving delegate.
func (w *removedClientWorld) tokenExchange(t *testing.T) removedClientResponse {
	t.Helper()
	return w.post(t, "/oauth/token", url.Values{
		"grant_type":         {oauthproto.GrantTypeTokenExchange},
		"subject_token":      {signedSubjectTokenWithScopes(t, w.embedded, w.baseURL, removedClientSurvivorID, "scope-a")},
		"subject_token_type": {oauthproto.TokenTypeAccessToken},
		"scope":              {"scope-a"},
		"audience":           {delegateAudience},
	}, basicAuth(removedClientSurvivorID, removedClientSurvivorPwd))
}

// tokenRequest authenticates as the fixture at the token endpoint. A served
// client gets a grant error for this empty exchange, never invalid_client.
func (w *removedClientWorld) tokenRequest(t *testing.T, f removedClientFixture) removedClientResponse {
	t.Helper()
	return w.post(t, "/oauth/token", url.Values{"grant_type": {oauthproto.GrantTypeTokenExchange}}, basicAuth(f.id, f.secret))
}

func assertInvalidClient401(t *testing.T, resp removedClientResponse) {
	t.Helper()
	assert.Equal(t, http.StatusUnauthorized, resp.status, resp.raw)
	assert.Equal(t, "invalid_client", resp.body["error"], resp.raw)
	assert.Empty(t, resp.body["access_token"], "no token may be issued")
}

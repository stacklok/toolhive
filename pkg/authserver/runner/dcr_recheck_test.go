// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/auth/dcr"
	"github.com/stacklok/toolhive/pkg/authserver"
	"github.com/stacklok/toolhive/pkg/authserver/server/registration"
	"github.com/stacklok/toolhive/pkg/authserver/storage"
	"github.com/stacklok/toolhive/pkg/oauthproto"
)

// TestEmbeddedAuthServer_DCRRecheckRecoversWithoutRestart runs two replicas
// of a downstream auth server against one Redis, both holding the client the
// upstream later evicts. With RegistrationProbeInterval set, one re-check on
// the first replica replaces the registration and switches its running
// provider; one re-check on the second adopts the replaced row. Neither
// replica restarts.
func TestEmbeddedAuthServer_DCRRecheckRecoversWithoutRestart(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	upstream, upstreamRedis, upstreamStore := newDCRUpstream(t)
	downstreamRedis := miniredis.RunT(t)
	downstream := newIndirectServer(t)

	newStore := func() *storage.RedisStorage {
		return storage.NewRedisStorageWithClient(
			redis.NewClient(&redis.Options{Addr: downstreamRedis.Addr()}), "downstream:")
	}
	sessionStore := newStore()
	client, err := registration.New(registration.Config{
		ID:                      sessionClientID,
		Secret:                  "session-client-secret-0123456789abcdef",
		RedirectURIs:            []string{sessionCallback},
		TokenEndpointAuthMethod: oauthproto.TokenEndpointAuthMethodClientSecretBasic,
		GrantTypes:              []string{oauthproto.GrantTypeAuthorizationCode, oauthproto.GrantTypeRefreshToken},
		ResponseTypes:           []string{oauthproto.ResponseTypeCode},
		Scopes:                  []string{"openid", "offline_access"},
		Audience:                []string{downstream.URL},
	})
	require.NoError(t, err)
	require.NoError(t, sessionStore.RegisterClient(ctx, client))

	boot := func() *EmbeddedAuthServer {
		t.Helper()
		as, err := NewEmbeddedAuthServerWithStorage(ctx, &authserver.RunConfig{
			SchemaVersion:    authserver.CurrentSchemaVersion,
			Issuer:           downstream.URL,
			AllowedAudiences: []string{downstream.URL},
			Upstreams: []authserver.UpstreamRunConfig{{
				Name: "chained-upstream",
				Type: authserver.UpstreamProviderTypeOAuth2,
				OAuth2Config: &authserver.OAuth2UpstreamRunConfig{
					AuthorizationEndpoint: upstream.URL + "/oauth/authorize",
					TokenEndpoint:         upstream.URL + "/oauth/token",
					RedirectURI:           downstream.URL + "/oauth/callback",
					Scopes:                []string{"openid", "offline_access"},
					DCRConfig: &authserver.DCRUpstreamConfig{
						DiscoveryURL:              upstream.URL + "/.well-known/oauth-authorization-server",
						RegistrationProbeInterval: "1m",
					},
				},
			}},
		}, newStore())
		require.NoError(t, err)
		t.Cleanup(func() { _ = as.Close() })
		require.Len(t, as.dcrRechecks, 1)
		return as
	}
	authorizeOn := func(as *EmbeddedAuthServer) string {
		t.Helper()
		downstream.set(as.Handler())
		resp, presented, _ := authorizeThroughDownstream(t, downstream.URL, upstream.URL+"/oauth/authorize")
		requireUpstreamAcceptedClient(t, resp, "replica presented client "+presented)
		return presented
	}

	first, second := boot(), boot()
	registered := authorizeOn(first)
	require.Equal(t, registered, authorizeOn(second), "both replicas must share one registration")

	upstreamRedis.FastForward(storage.DefaultDCRClientTTL + time.Hour)
	_, err = upstreamStore.GetClient(ctx, registered)
	require.Error(t, err, "the upstream must have evicted the idle DCR client")

	first.dcrRechecks[0].tick(ctx, first.server)
	replaced := authorizeOn(first)
	require.NotEqual(t, registered, replaced, "the re-check must replace the evicted client in place")

	second.dcrRechecks[0].tick(ctx, second.server)
	require.Equal(t, replaced, authorizeOn(second), "the second replica must adopt the replaced registration")

	// A re-check against a registration the upstream still knows changes nothing.
	first.dcrRechecks[0].tick(ctx, first.server)
	require.Equal(t, replaced, authorizeOn(first))
}

func TestNewDCRRecheck(t *testing.T) {
	t.Parallel()
	req := &dcr.Request{Issuer: "https://as.example.com"}
	res := &dcr.Resolution{ClientID: "c"}
	runConfig := func(interval string) *authserver.UpstreamRunConfig {
		return &authserver.UpstreamRunConfig{Name: "up", OAuth2Config: &authserver.OAuth2UpstreamRunConfig{
			TokenEndpoint: "https://idp.example.com/token",
			DCRConfig: &authserver.DCRUpstreamConfig{
				RegistrationProbeInterval: interval,
				InitialAccessTokenFile:    "/var/run/iat",
			},
		}}
	}
	store := newMemoryDCRStore(t)

	r, err := newDCRRecheck(runConfig("5m"), req, res, store)
	require.NoError(t, err)
	require.NotNil(t, r)
	assert.Equal(t, 5*time.Minute, r.interval)
	assert.Equal(t, "up", r.upstream)
	assert.Same(t, res, r.current)
	assert.True(t, r.req.Periodic, "the loop's request marks periodic re-checks")
	assert.False(t, req.Periodic, "the caller's request is not modified")
	assert.Equal(t, "/var/run/iat", r.initialAccessTokenFile)
	assert.True(t, r.adoptAuthorizationEndpoint, "an endpoint the run config left empty follows the registration")
	assert.False(t, r.adoptTokenEndpoint, "an endpoint the run config set stays")

	oidc := &authserver.UpstreamRunConfig{OIDCConfig: &authserver.OIDCUpstreamRunConfig{
		DCRConfig: &authserver.DCRUpstreamConfig{RegistrationProbeInterval: "2m"},
	}}
	r, err = newDCRRecheck(oidc, req, res, store)
	require.NoError(t, err)
	require.NotNil(t, r)
	assert.Equal(t, 2*time.Minute, r.interval)
	assert.False(t, r.adoptAuthorizationEndpoint || r.adoptTokenEndpoint, "OIDC endpoints come from discovery")

	for name, tc := range map[string]struct {
		rc  *authserver.UpstreamRunConfig
		req *dcr.Request
		res *dcr.Resolution
	}{
		"no interval":   {runConfig(""), req, res},
		"no DCR config": {&authserver.UpstreamRunConfig{OAuth2Config: &authserver.OAuth2UpstreamRunConfig{}}, req, res},
		"no request":    {runConfig("5m"), nil, res},
		"no resolution": {runConfig("5m"), req, nil},
	} {
		r, err := newDCRRecheck(tc.rc, tc.req, tc.res, store)
		require.NoError(t, err, name)
		assert.Nil(t, r, name)
	}

	_, err = newDCRRecheck(runConfig("30s"), req, res, store)
	require.ErrorContains(t, err, "at least 1m0s")
}

// failingDCRStore fails every read, so ResolveCredentials errors.
type failingDCRStore struct {
	dcr.CredentialStore
	gets atomic.Int32
}

func (s *failingDCRStore) Get(context.Context, dcr.Key) (*dcr.Resolution, bool, error) {
	s.gets.Add(1)
	return nil, false, errors.New("store unavailable")
}

// serverWithoutUpdates is an authserver.Server without the
// UpstreamCredentialsUpdater capability.
type serverWithoutUpdates struct{ authserver.Server }

func TestDCRRecheckTick_KeepsCurrentClientOnFailure(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	req := &dcr.Request{
		Issuer:               "https://as.example.com",
		RegistrationEndpoint: "https://idp.example.com/register",
		RedirectURI:          "https://as.example.com/oauth/callback",
		RegistrationProbe:    dcr.ProbeNone,
	}
	current := &dcr.Resolution{ClientID: "current"}

	// The store fails: nothing is switched.
	failing := &failingDCRStore{}
	r := &dcrRecheck{upstream: "up", req: req, store: failing, interval: time.Minute, current: current}
	r.tick(ctx, serverWithoutUpdates{})
	assert.EqualValues(t, 1, failing.gets.Load())
	assert.Same(t, current, r.current)

	// The initial access token file cannot be read: the tick stops before
	// resolving anything.
	r.initialAccessTokenFile = filepath.Join(t.TempDir(), "missing")
	r.tick(ctx, serverWithoutUpdates{})
	assert.EqualValues(t, 1, failing.gets.Load())
	assert.Same(t, current, r.current)

	// The stored row names another client, but the server cannot switch:
	// the re-check keeps reporting the client the provider still holds.
	store := newMemoryDCRStore(t)
	key := dcr.Key{
		Issuer:      req.Issuer,
		UpstreamID:  req.RegistrationEndpoint,
		RedirectURI: req.RedirectURI,
		ScopesHash:  storage.ScopesHash(nil),
	}
	_, err := store.PutIfAbsent(ctx, key, &dcr.Resolution{
		ClientID:              "replaced",
		RedirectURI:           req.RedirectURI,
		AuthorizationEndpoint: "https://idp.example.com/authorize",
		TokenEndpoint:         "https://idp.example.com/token",
	})
	require.NoError(t, err)
	r = &dcrRecheck{upstream: "up", req: req, store: store, interval: time.Minute, current: current}
	r.tick(ctx, serverWithoutUpdates{})
	assert.Same(t, current, r.current)
}

func TestStartDCRRechecks_StopsLoops(t *testing.T) {
	t.Parallel()
	failing := &failingDCRStore{}
	r := &dcrRecheck{
		upstream: "up",
		req:      &dcr.Request{Issuer: "https://as.example.com", RegistrationEndpoint: "https://idp.example.com/register"},
		store:    failing,
		interval: 10 * time.Millisecond,
		current:  &dcr.Resolution{ClientID: "current"},
	}
	stop := startDCRRechecks(serverWithoutUpdates{}, []*dcrRecheck{r})
	require.Eventually(t, func() bool { return failing.gets.Load() >= 2 }, 5*time.Second, 5*time.Millisecond)
	stopped := make(chan struct{})
	go func() { stop(); close(stopped) }()
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("stop did not return")
	}
	after := failing.gets.Load()
	time.Sleep(50 * time.Millisecond)
	assert.Equal(t, after, failing.gets.Load(), "no tick may run after stop returns")

	startDCRRechecks(serverWithoutUpdates{}, nil)()
}

// resettingUpstream is an OAuth 2.0 authorization server with dynamic client
// registration whose reset forgets every registered client and moves its
// token endpoint, as an upstream that rebuilds its client store and paths
// on a redeploy.
type resettingUpstream struct {
	*httptest.Server
	mu         sync.Mutex
	generation int
	clients    map[string]string // client_id -> client_secret
	issued     int
	// registrationTokens are the bearer initial access tokens presented,
	// in order; exchanges are the token endpoint paths of successful
	// code redemptions, in order.
	registrationTokens []string
	exchanges          []string
}

func newResettingUpstream(t *testing.T) *resettingUpstream {
	t.Helper()
	u := &resettingUpstream{generation: 1, clients: map[string]string{}}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/oauth-authorization-server", func(w http.ResponseWriter, _ *http.Request) {
		u.mu.Lock()
		tokenPath := fmt.Sprintf("/token-v%d", u.generation)
		u.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(oauthproto.AuthorizationServerMetadata{
			Issuer:                            u.URL,
			AuthorizationEndpoint:             u.URL + "/authorize",
			TokenEndpoint:                     u.URL + tokenPath,
			RegistrationEndpoint:              u.URL + "/register",
			TokenEndpointAuthMethodsSupported: []string{oauthproto.TokenEndpointAuthMethodClientSecretBasic},
			CodeChallengeMethodsSupported:     []string{"S256"},
			ScopesSupported:                   []string{"openid"},
		})
	})
	mux.HandleFunc("/register", func(w http.ResponseWriter, r *http.Request) {
		var req oauthproto.DynamicClientRegistrationRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		u.mu.Lock()
		u.registrationTokens = append(u.registrationTokens, strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		u.issued++
		id, secret := fmt.Sprintf("client-%d", u.issued), fmt.Sprintf("secret-%d", u.issued)
		u.clients[id] = secret
		u.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(oauthproto.DynamicClientRegistrationResponse{
			ClientID: id, ClientSecret: secret, TokenEndpointAuthMethod: req.TokenEndpointAuthMethod,
		})
	})
	mux.HandleFunc("/authorize", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		u.mu.Lock()
		_, known := u.clients[q.Get("client_id")]
		u.mu.Unlock()
		if !known {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"error":"invalid_client"}`)
			return
		}
		back := url.Values{"code": {"code-for-" + q.Get("client_id")}, "state": {q.Get("state")}}
		http.Redirect(w, r, q.Get("redirect_uri")+"?"+back.Encode(), http.StatusFound)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/token-v") {
			http.NotFound(w, r)
			return
		}
		_ = r.ParseForm()
		id, secret, _ := r.BasicAuth()
		u.mu.Lock()
		defer u.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if want, known := u.clients[id]; !known || want != secret {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(w, `{"error":"invalid_client"}`)
			return
		}
		if r.URL.Path != fmt.Sprintf("/token-v%d", u.generation) || r.PostForm.Get("code") != "code-for-"+id {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"error":"invalid_grant"}`)
			return
		}
		u.exchanges = append(u.exchanges, r.URL.Path)
		_, _ = io.WriteString(w, `{"access_token":"at-`+id+`","token_type":"Bearer","expires_in":3600}`)
	})
	u.Server = httptest.NewServer(mux)
	t.Cleanup(u.Close)
	return u
}

func (u *resettingUpstream) reset() {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.clients = map[string]string{}
	u.generation++
}

// TestEmbeddedAuthServer_DCRRecheckFollowsMovedTokenEndpoint has the
// upstream forget its clients and move its token endpoint. The re-check
// registers again with the initial access token currently on disk and
// switches the provider to the new client and the new token endpoint, so a
// full sign-in, code redemption included, succeeds without a restart.
func TestEmbeddedAuthServer_DCRRecheckFollowsMovedTokenEndpoint(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	up := newResettingUpstream(t)
	downstream := newIndirectServer(t)
	tokenFile := filepath.Join(t.TempDir(), "initial-access-token")
	require.NoError(t, os.WriteFile(tokenFile, []byte("iat-1"), 0o600))

	store := storage.NewRedisStorageWithClient(
		redis.NewClient(&redis.Options{Addr: miniredis.RunT(t).Addr()}), "downstream:")
	client, err := registration.New(registration.Config{
		ID:                      sessionClientID,
		Secret:                  "session-client-secret-0123456789abcdef",
		RedirectURIs:            []string{sessionCallback},
		TokenEndpointAuthMethod: oauthproto.TokenEndpointAuthMethodClientSecretBasic,
		GrantTypes:              []string{oauthproto.GrantTypeAuthorizationCode, oauthproto.GrantTypeRefreshToken},
		ResponseTypes:           []string{oauthproto.ResponseTypeCode},
		Scopes:                  []string{"openid", "offline_access"},
		Audience:                []string{downstream.URL},
	})
	require.NoError(t, err)
	require.NoError(t, store.RegisterClient(ctx, client))
	as, err := NewEmbeddedAuthServerWithStorage(ctx, &authserver.RunConfig{
		SchemaVersion:    authserver.CurrentSchemaVersion,
		Issuer:           downstream.URL,
		AllowedAudiences: []string{downstream.URL},
		Upstreams: []authserver.UpstreamRunConfig{{
			Name: "resetting-upstream",
			Type: authserver.UpstreamProviderTypeOAuth2,
			OAuth2Config: &authserver.OAuth2UpstreamRunConfig{
				// Endpoints left to discovery, so the registration supplies them.
				RedirectURI: downstream.URL + "/oauth/callback",
				Scopes:      []string{"openid"},
				DCRConfig: &authserver.DCRUpstreamConfig{
					DiscoveryURL:              up.URL + "/.well-known/oauth-authorization-server",
					InitialAccessTokenFile:    tokenFile,
					RegistrationProbeInterval: "1m",
				},
			},
		}},
	}, store)
	require.NoError(t, err)
	t.Cleanup(func() { _ = as.Close() })
	downstream.set(as.Handler())
	require.Len(t, as.dcrRechecks, 1)

	// signIn runs a whole authorization through the downstream, upstream
	// code redemption included, and requires a code for the session client.
	signIn := func() {
		t.Helper()
		toUpstream, _, browser := authorizeThroughDownstream(t, downstream.URL, up.URL+"/authorize")
		body, _ := io.ReadAll(toUpstream.Body)
		require.Equal(t, http.StatusFound, toUpstream.StatusCode, "upstream must accept the client: %s", body)
		callback, err := browser.Get(toUpstream.Header.Get("Location"))
		require.NoError(t, err)
		page, _ := io.ReadAll(callback.Body)
		_ = callback.Body.Close()
		require.Contains(t, []int{http.StatusFound, http.StatusSeeOther}, callback.StatusCode,
			"downstream callback must redeem the code: %s", page)
		back, err := url.Parse(callback.Header.Get("Location"))
		require.NoError(t, err)
		require.True(t, strings.HasPrefix(back.String(), sessionCallback+"?"),
			"downstream must return to the session client, got %q", back)
		require.Empty(t, back.Query().Get("error"), "downstream returned an error to the session client: %s", back)
		require.NotEmpty(t, back.Query().Get("code"), "downstream must issue a code to the session client: %s", back)
	}

	signIn()
	require.Equal(t, []string{"/token-v1"}, up.exchanges)

	up.reset()
	require.NoError(t, os.WriteFile(tokenFile, []byte("iat-2"), 0o600))
	as.dcrRechecks[0].tick(ctx, as.server)

	signIn()
	assert.Equal(t, []string{"/token-v1", "/token-v2"}, up.exchanges,
		"the code redemption after recovery must reach the moved token endpoint")
	assert.Equal(t, []string{"iat-1", "iat-2"}, up.registrationTokens,
		"the re-registration must present the initial access token currently on disk")
}

func TestJitterInterval(t *testing.T) {
	t.Parallel()
	for range 1000 {
		d := jitterInterval(10 * time.Minute)
		require.GreaterOrEqual(t, d, 9*time.Minute)
		require.Less(t, d, 11*time.Minute)
	}
	assert.Equal(t, time.Duration(3), jitterInterval(3))
}

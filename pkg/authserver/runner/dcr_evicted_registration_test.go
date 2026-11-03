// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package runner

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"html"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/auth/dcr"
	"github.com/stacklok/toolhive/pkg/authserver"
	"github.com/stacklok/toolhive/pkg/authserver/server/registration"
	"github.com/stacklok/toolhive/pkg/authserver/storage"
	"github.com/stacklok/toolhive/pkg/oauthproto"
)

// TestEmbeddedAuthServer_ChainedDCRRecoversFromEvictedRegistration chains two
// ToolHive auth servers: the downstream registers itself on the upstream via
// DCR and persists that registration in its own Redis. The upstream stores
// DCR-issued clients with DefaultDCRClientTTL, renewed only by a token
// exchange, while advertising client_secret_expires_at=0, so an idle
// registration is evicted upstream but kept in the downstream's DCR cache.
// After a restart the downstream must notice and register again instead of
// sending every authorization to the upstream with a client_id it no longer
// knows (#6821).
func TestEmbeddedAuthServer_ChainedDCRRecoversFromEvictedRegistration(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	upstream, upstreamRedis, upstreamStore := newDCRUpstream(t)

	// The downstream persists its state, including the DCR cache, in its own
	// Redis, which survives a downstream restart.
	downstreamRedis := miniredis.RunT(t)
	downstream := newIndirectServer(t)
	downstreamConfig := func() *authserver.RunConfig {
		return &authserver.RunConfig{
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
						DiscoveryURL: upstream.URL + "/.well-known/oauth-authorization-server",
					},
				},
			}},
		}
	}
	bootDownstream := func(registerSessionClient bool) *EmbeddedAuthServer {
		t.Helper()
		store := storage.NewRedisStorageWithClient(
			redis.NewClient(&redis.Options{Addr: downstreamRedis.Addr()}), "downstream:")
		if registerSessionClient {
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
		}
		as, err := NewEmbeddedAuthServerWithStorage(ctx, downstreamConfig(), store)
		require.NoError(t, err)
		downstream.set(as.Handler())
		return as
	}

	authorize := func() (*http.Response, string) {
		t.Helper()
		resp, presented, _ := authorizeThroughDownstream(t, downstream.URL, upstream.URL+"/oauth/authorize")
		return resp, presented
	}
	requireUpstreamAccepted := func(resp *http.Response, msg string) {
		t.Helper()
		requireUpstreamAcceptedClient(t, resp, msg)
	}

	// First boot: the downstream registers on the upstream and the upstream
	// accepts that client.
	first := bootDownstream(true)
	resp, registered := authorize()
	require.NotEmpty(t, registered)
	requireUpstreamAccepted(resp, "a fresh registration must be accepted")
	require.NoError(t, first.Close())

	// Nobody completes a token exchange for longer than the upstream's TTL,
	// so the upstream evicts the idle registration.
	upstreamRedis.FastForward(storage.DefaultDCRClientTTL + time.Hour)
	_, err := upstreamStore.GetClient(ctx, registered)
	require.Error(t, err, "the upstream must have evicted the idle DCR client")

	// The downstream restarts against the same persistent Redis. Its next
	// authorization must reach the upstream with a client the upstream knows.
	second := bootDownstream(false)
	t.Cleanup(func() { _ = second.Close() })
	resp, presented := authorize()
	requireUpstreamAccepted(resp, "after the upstream evicted client "+registered+
		", the restarted downstream presented client "+presented)
}

// TestDCRResolver_PublicClientRecoversFromEvictedRegistration is the
// public-client counterpart of the test above. The embedded auth server
// always registers a confidential client, so this drives the resolver the
// way the CLI flow does (PublicClient, token_endpoint_auth_method=none)
// against the same ToolHive upstream. The probe then carries no client
// secret, and detecting the eviction relies on the upstream looking the
// client up before it validates the authorization code.
func TestDCRResolver_PublicClientRecoversFromEvictedRegistration(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	upstream, upstreamRedis, upstreamStore := newDCRUpstream(t)

	downstreamRedis := miniredis.RunT(t)
	cache := dcr.NewStorageBackedStore(storage.NewRedisStorageWithClient(
		redis.NewClient(&redis.Options{Addr: downstreamRedis.Addr()}), "downstream:"))
	req := &dcr.Request{
		Issuer:       "https://downstream.example.com",
		Scopes:       []string{"openid", "offline_access"},
		DiscoveryURL: upstream.URL + "/.well-known/oauth-authorization-server",
		PublicClient: true,
	}

	registered, err := dcr.ResolveCredentials(ctx, req, cache)
	require.NoError(t, err)
	require.Equal(t, oauthproto.TokenEndpointAuthMethodNone, registered.TokenEndpointAuthMethod)
	_, err = upstreamStore.GetClient(ctx, registered.ClientID)
	require.NoError(t, err, "the upstream must know the registered public client")

	// While the upstream still knows the client, the cached one is kept.
	kept, err := dcr.ResolveCredentials(ctx, req, cache)
	require.NoError(t, err)
	require.Equal(t, registered.ClientID, kept.ClientID)

	upstreamRedis.FastForward(storage.DefaultDCRClientTTL + time.Hour)
	_, err = upstreamStore.GetClient(ctx, registered.ClientID)
	require.Error(t, err, "the upstream must have evicted the idle DCR client")

	recovered, err := dcr.ResolveCredentials(ctx, req, cache)
	require.NoError(t, err)
	require.NotEqual(t, registered.ClientID, recovered.ClientID,
		"the resolver must replace the public client the upstream evicted")
	_, err = upstreamStore.GetClient(ctx, recovered.ClientID)
	require.NoError(t, err, "the upstream must know the replacement client")
}

// The downstream's own session client, registered in its storage so a test
// can start authorizations on it.
const (
	sessionClientID = "session-client"
	sessionCallback = "https://client.example.com/callback"
)

// consentHandle finds the handle on a ToolHive auth server's consent page.
var consentHandle = regexp.MustCompile(`name="handle" value="([^"]+)"`)

// authorizeThroughDownstream starts a session authorization on the
// downstream, approves the consent page, and follows the downstream's
// redirect to the upstream's authorization endpoint, returning the
// upstream's response, the client_id the downstream presented there, and the
// browser (cookie jar included) for following the flow further.
func authorizeThroughDownstream(
	t *testing.T, downstreamURL, upstreamAuthorizeURL string,
) (*http.Response, string, *http.Client) {
	t.Helper()
	ctx := context.Background()
	jar, err := cookiejar.New(nil)
	require.NoError(t, err)
	noRedirect := &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	challenge := sha256.Sum256([]byte("session-code-verifier-0123456789abcdefghijklmnop"))
	query := url.Values{
		"client_id":             {sessionClientID},
		"redirect_uri":          {sessionCallback},
		"response_type":         {"code"},
		"scope":                 {"openid offline_access"},
		"state":                 {"session-state-0123456789"},
		"code_challenge":        {base64.RawURLEncoding.EncodeToString(challenge[:])},
		"code_challenge_method": {"S256"},
	}
	consentPage, err := noRedirect.Get(downstreamURL + "/oauth/authorize?" + query.Encode())
	require.NoError(t, err)
	page, _ := io.ReadAll(consentPage.Body)
	_ = consentPage.Body.Close()
	require.Equal(t, http.StatusOK, consentPage.StatusCode, "downstream must accept its own session client: %s", page)
	match := consentHandle.FindSubmatch(page)
	require.Len(t, match, 2, "downstream must render its consent page")

	approve, err := http.NewRequestWithContext(ctx, http.MethodPost, downstreamURL+"/oauth/consent",
		strings.NewReader(url.Values{"handle": {html.UnescapeString(string(match[1]))}, "decision": {"approve"}}.Encode()))
	require.NoError(t, err)
	approve.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	approve.Header.Set("Origin", downstreamURL)
	toDownstream, err := noRedirect.Do(approve)
	require.NoError(t, err)
	approved, _ := io.ReadAll(toDownstream.Body)
	_ = toDownstream.Body.Close()
	require.Equal(t, http.StatusSeeOther, toDownstream.StatusCode, "downstream must redirect upstream after consent: %s", approved)
	location := toDownstream.Header.Get("Location")
	require.True(t, strings.HasPrefix(location, upstreamAuthorizeURL+"?"),
		"downstream must redirect to the upstream, got %q", location)
	parsed, err := url.Parse(location)
	require.NoError(t, err)

	toUpstream, err := noRedirect.Get(location)
	require.NoError(t, err)
	t.Cleanup(func() { _ = toUpstream.Body.Close() })
	return toUpstream, parsed.Query().Get("client_id"), noRedirect
}

// requireUpstreamAcceptedClient asserts the upstream accepted the presented
// client: a known client gets the upstream's own consent page, an unknown
// one is refused with invalid_client before consent.
func requireUpstreamAcceptedClient(t *testing.T, resp *http.Response, msg string) {
	t.Helper()
	body, _ := io.ReadAll(resp.Body)
	require.Equal(t, http.StatusOK, resp.StatusCode, "%s; upstream answered %d: %s", msg, resp.StatusCode, body)
	require.Regexp(t, consentHandle, string(body), "%s; upstream must render its consent page", msg)
}

// newDCRUpstream starts a ToolHive auth server that accepts dynamic client
// registration and stores its clients in its own miniredis, so a test can
// fast-forward past DefaultDCRClientTTL. Its own upstream IdP is never
// reached by these tests.
func newDCRUpstream(t *testing.T) (*indirectServer, *miniredis.Miniredis, *storage.RedisStorage) {
	t.Helper()
	idp, _ := newMockAuthorizationServer(t)

	upstreamRedis := miniredis.RunT(t)
	upstreamStore := storage.NewRedisStorageWithClient(
		redis.NewClient(&redis.Options{Addr: upstreamRedis.Addr()}), "upstream:")
	upstream := newIndirectServer(t)
	upstreamAS, err := NewEmbeddedAuthServerWithStorage(context.Background(), &authserver.RunConfig{
		SchemaVersion:    authserver.CurrentSchemaVersion,
		Issuer:           upstream.URL,
		AllowedAudiences: []string{upstream.URL + "/mcp"},
		Upstreams: []authserver.UpstreamRunConfig{{
			Name: "idp",
			Type: authserver.UpstreamProviderTypeOAuth2,
			OAuth2Config: &authserver.OAuth2UpstreamRunConfig{
				AuthorizationEndpoint: idp.URL + "/authorize",
				TokenEndpoint:         idp.URL + "/token",
				ClientID:              "upstream-at-idp",
				RedirectURI:           upstream.URL + "/oauth/callback",
			},
		}},
	}, upstreamStore)
	require.NoError(t, err)
	t.Cleanup(func() { _ = upstreamAS.Close() })
	upstream.set(upstreamAS.Handler())
	return upstream, upstreamRedis, upstreamStore
}

// indirectServer is an httptest server whose handler is installed after the
// server's URL is known, so an auth server can use that URL as its issuer.
type indirectServer struct {
	*httptest.Server
	handler atomic.Pointer[http.Handler]
}

func newIndirectServer(t *testing.T) *indirectServer {
	t.Helper()
	s := &indirectServer{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := s.handler.Load()
		if h == nil {
			http.Error(w, "handler not installed", http.StatusServiceUnavailable)
			return
		}
		(*h).ServeHTTP(w, r)
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *indirectServer) set(h http.Handler) { s.handler.Store(&h) }

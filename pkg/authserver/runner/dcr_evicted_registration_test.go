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

	// The upstream's own IdP. The test stops at the upstream's consent page,
	// so the IdP is never reached.
	idp, _ := newMockAuthorizationServer(t)

	upstreamRedis := miniredis.RunT(t)
	upstreamStore := storage.NewRedisStorageWithClient(
		redis.NewClient(&redis.Options{Addr: upstreamRedis.Addr()}), "upstream:")
	upstream := newIndirectServer(t)
	upstreamAS, err := NewEmbeddedAuthServerWithStorage(ctx, &authserver.RunConfig{
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

	// The downstream persists its state, including the DCR cache, in its own
	// Redis, which survives a downstream restart.
	downstreamRedis := miniredis.RunT(t)
	downstream := newIndirectServer(t)
	const (
		sessionClientID = "session-client"
		sessionCallback = "https://client.example.com/callback"
	)
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

	// authorize starts a session authorization on the downstream, approves the
	// consent page, and follows the downstream's redirect to the upstream,
	// returning the upstream's response and the client_id the downstream
	// presented there.
	consentHandle := regexp.MustCompile(`name="handle" value="([^"]+)"`)
	authorize := func() (*http.Response, string) {
		t.Helper()
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
		consentPage, err := noRedirect.Get(downstream.URL + "/oauth/authorize?" + query.Encode())
		require.NoError(t, err)
		page, _ := io.ReadAll(consentPage.Body)
		_ = consentPage.Body.Close()
		require.Equal(t, http.StatusOK, consentPage.StatusCode, "downstream must accept its own session client: %s", page)
		match := consentHandle.FindSubmatch(page)
		require.Len(t, match, 2, "downstream must render its consent page")

		approve, err := http.NewRequestWithContext(ctx, http.MethodPost, downstream.URL+"/oauth/consent",
			strings.NewReader(url.Values{"handle": {html.UnescapeString(string(match[1]))}, "decision": {"approve"}}.Encode()))
		require.NoError(t, err)
		approve.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		approve.Header.Set("Origin", downstream.URL)
		toDownstream, err := noRedirect.Do(approve)
		require.NoError(t, err)
		approved, _ := io.ReadAll(toDownstream.Body)
		_ = toDownstream.Body.Close()
		require.Equal(t, http.StatusSeeOther, toDownstream.StatusCode, "downstream must redirect upstream after consent: %s", approved)
		location := toDownstream.Header.Get("Location")
		require.True(t, strings.HasPrefix(location, upstream.URL+"/oauth/authorize?"),
			"downstream must redirect to the upstream, got %q", location)
		parsed, err := url.Parse(location)
		require.NoError(t, err)

		toUpstream, err := noRedirect.Get(location)
		require.NoError(t, err)
		t.Cleanup(func() { _ = toUpstream.Body.Close() })
		return toUpstream, parsed.Query().Get("client_id")
	}
	// The upstream accepts a known client by rendering its own consent page;
	// an unknown client is refused with invalid_client before consent.
	requireUpstreamAccepted := func(resp *http.Response, msg string) {
		t.Helper()
		body, _ := io.ReadAll(resp.Body)
		require.Equal(t, http.StatusOK, resp.StatusCode, "%s; upstream answered %d: %s", msg, resp.StatusCode, body)
		require.Regexp(t, consentHandle, string(body), "%s; upstream must render its consent page", msg)
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
	_, err = upstreamStore.GetClient(ctx, registered)
	require.Error(t, err, "the upstream must have evicted the idle DCR client")

	// The downstream restarts against the same persistent Redis. Its next
	// authorization must reach the upstream with a client the upstream knows.
	second := bootDownstream(false)
	t.Cleanup(func() { _ = second.Close() })
	resp, presented := authorize()
	requireUpstreamAccepted(resp, "after the upstream evicted client "+registered+
		", the restarted downstream presented client "+presented)
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

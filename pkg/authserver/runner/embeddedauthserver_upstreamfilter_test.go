// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package runner

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/oauth2-proxy/mockoidc"
	"github.com/ory/fosite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/authserver"
	"github.com/stacklok/toolhive/pkg/authserver/server/registration"
	"github.com/stacklok/toolhive/pkg/authserver/storage"
	"github.com/stacklok/toolhive/pkg/authserver/upstreamfilter"
)

const (
	filterTestIssuer      = "https://auth.example.com"
	filterTestClientID    = "filter-test-client"
	filterTestRedirectURI = "https://client.example.com/callback"
	filterTestAudience    = "https://mcp.example.com"
)

// startFilterUpstream runs a mockoidc instance standing in for one upstream
// IdP and queues a user carrying the given groups. mockoidc only copies
// MockUser.Groups into the ID token when the "groups" scope is requested, so
// every upstream config below asks for it.
func startFilterUpstream(t *testing.T, name string, groups []string) *mockoidc.MockOIDC {
	t.Helper()
	m, err := mockoidc.Run()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, m.Shutdown()) })
	m.QueueUser(&mockoidc.MockUser{
		Subject:           name + "-subject",
		Email:             name + "@example.com",
		PreferredUsername: name + "-user",
		Groups:            groups,
	})
	return m
}

// newFilterRunConfig builds a RunConfig whose upstreams are the given mockoidc
// instances, in order. The first is the mandatory identity leg.
func newFilterRunConfig(
	t *testing.T,
	filter *upstreamfilter.GroupBasedFilterConfig,
	names []string,
	ms []*mockoidc.MockOIDC,
) *authserver.RunConfig {
	t.Helper()
	secretDir := t.TempDir()
	upstreams := make([]authserver.UpstreamRunConfig, 0, len(ms))
	for i, m := range ms {
		cfg := m.Config()
		upstreams = append(upstreams, authserver.UpstreamRunConfig{
			Name: names[i],
			Type: authserver.UpstreamProviderTypeOIDC,
			OIDCConfig: &authserver.OIDCUpstreamRunConfig{
				IssuerURL:        m.Issuer(),
				ClientID:         cfg.ClientID,
				ClientSecretFile: writeUpstreamSecret(t, secretDir, names[i], cfg.ClientSecret),
				RedirectURI:      filterTestIssuer + "/oauth/callback",
				Scopes:           []string{"openid", "groups"},
			},
		})
	}
	return &authserver.RunConfig{
		SchemaVersion:     authserver.CurrentSchemaVersion,
		Issuer:            filterTestIssuer,
		Upstreams:         upstreams,
		UpstreamFilter:    filter,
		ScopesSupported:   []string{"openid", "profile", "offline_access"},
		AllowedAudiences:  []string{filterTestAudience},
		InsecureAllowHTTP: true,
	}
}

// writeUpstreamSecret stores an upstream client secret in a file. RunConfig
// takes secrets by reference, and a file keeps these tests parallel-safe where
// an environment variable would not be.
func writeUpstreamSecret(t *testing.T, dir, name, secret string) string {
	t.Helper()
	path := filepath.Join(dir, name+".secret")
	require.NoError(t, os.WriteFile(path, []byte(secret), 0o600))
	return path
}

// newFilterTestServer builds the auth server from the RunConfig — exercising
// the RunConfig -> buildUpstreamFilter -> resolvedCfg wiring this test exists
// for — and serves it over httptest.
func newFilterTestServer(
	t *testing.T,
	filter *upstreamfilter.GroupBasedFilterConfig,
	names []string,
	ms []*mockoidc.MockOIDC,
) *httptest.Server {
	t.Helper()
	ctx := context.Background()

	stor := storage.NewMemoryStorage()
	eas, err := NewEmbeddedAuthServerWithStorage(ctx, newFilterRunConfig(t, filter, names, ms), stor)
	require.NoError(t, err)

	require.NoError(t, stor.RegisterClient(ctx, &fosite.DefaultClient{
		ID:            filterTestClientID,
		RedirectURIs:  []string{filterTestRedirectURI},
		ResponseTypes: []string{"code"},
		GrantTypes:    []string{"authorization_code", "refresh_token"},
		Scopes:        registration.DefaultScopes,
		Audience:      []string{filterTestAudience},
		Public:        true,
	}))

	srv := httptest.NewServer(eas.Handler())
	t.Cleanup(func() {
		srv.Close()
		require.NoError(t, eas.Close())
	})
	return srv
}

// filterNoRedirectClient returns a client that surfaces each redirect instead
// of following it, so the test can inspect which upstream the chain picks.
func filterNoRedirectClient() *http.Client {
	return &http.Client{
		Timeout: 10 * time.Second,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// filterGetWithCookies issues a GET carrying cookies verbatim. /oauth/callback
// is bound to the browser that started the flow by a cookie set on the
// redirect to the upstream, so a hand-walked flow must carry it itself.
func filterGetWithCookies(
	t *testing.T, client *http.Client, rawURL string, cookies []*http.Cookie,
) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	require.NoError(t, err)
	for _, c := range cookies {
		req.AddCookie(c)
	}
	resp, err := client.Do(req)
	require.NoError(t, err)
	return resp
}

// filterApproveConsent posts an approval for the consent page the authorize
// endpoint serves before the first upstream redirect.
func filterApproveConsent(
	t *testing.T, client *http.Client, serverURL string, resp *http.Response,
) *http.Response {
	t.Helper()
	require.Equal(t, http.StatusOK, resp.StatusCode, "expected consent page")
	page, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	cookies := resp.Cookies()
	resp.Body.Close()

	match := regexp.MustCompile(`name="handle" value="([^"]+)"`).FindSubmatch(page)
	require.Len(t, match, 2, "consent handle missing")
	form := url.Values{"handle": {string(match[1])}, "decision": {"approve"}}
	req, err := http.NewRequest(http.MethodPost, serverURL+"/oauth/consent", strings.NewReader(form.Encode()))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", filterTestIssuer)
	for _, c := range cookies {
		req.AddCookie(c)
	}
	approved, err := client.Do(req)
	require.NoError(t, err)
	return approved
}

// walkChain drives the authorization chain leg by leg and reports the host of
// every upstream authorize endpoint it was redirected to, in order, together
// with the response that ended the walk — a 303 back to the client once the
// chain is satisfied, or whatever the callback returned instead.
//
// The per-leg return is the point: the filter runs on the first leg's
// callback, so the hosts collected here are exactly the chain it selected.
func walkChain(t *testing.T, serverURL, challenge, clientState string) ([]string, *http.Response) {
	t.Helper()
	client := filterNoRedirectClient()
	parsedServerURL, err := url.Parse(serverURL)
	require.NoError(t, err)

	authorizeURL := serverURL + "/oauth/authorize?" + url.Values{
		"client_id":             {filterTestClientID},
		"redirect_uri":          {filterTestRedirectURI},
		"state":                 {clientState},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
		"response_type":         {"code"},
		"scope":                 {"openid"},
	}.Encode()

	resp, err := client.Get(authorizeURL)
	require.NoError(t, err)
	resp = filterApproveConsent(t, client, serverURL, resp)
	require.Equal(t, http.StatusSeeOther, resp.StatusCode, "expected redirect to first upstream")
	next, err := resp.Location()
	require.NoError(t, err)
	cookies := resp.Cookies()
	resp.Body.Close()

	var visited []string
	for range maxChainLegs {
		visited = append(visited, next.Host)

		upstreamResp, err := client.Get(next.String())
		require.NoError(t, err)
		require.Equal(t, http.StatusFound, upstreamResp.StatusCode, "expected upstream to redirect to callback")
		callback, err := upstreamResp.Location()
		require.NoError(t, err)
		upstreamResp.Body.Close()
		callback.Scheme = parsedServerURL.Scheme
		callback.Host = parsedServerURL.Host

		resp = filterGetWithCookies(t, client, callback.String(), cookies)
		if resp.StatusCode != http.StatusFound {
			return visited, resp
		}
		// Another upstream leg: the callback mints a fresh binding cookie.
		next, err = resp.Location()
		require.NoError(t, err)
		cookies = resp.Cookies()
		resp.Body.Close()
	}
	t.Fatalf("chain did not finish within %d legs", maxChainLegs)
	return visited, nil
}

const maxChainLegs = 5

// TestIntegration_RunConfigUpstreamFilter_UnsetWalksFullChain is the control
// case: with no filter in the RunConfig every configured upstream is visited,
// which is the behavior that existed before the filter hook.
func TestIntegration_RunConfigUpstreamFilter_UnsetWalksFullChain(t *testing.T) {
	t.Parallel()

	okta := startFilterUpstream(t, "okta", []string{"viewer"})
	slack := startFilterUpstream(t, "slack", nil)
	salesforce := startFilterUpstream(t, "salesforce", nil)

	names := []string{"okta", "slack", "salesforce"}
	ms := []*mockoidc.MockOIDC{okta, slack, salesforce}
	srv := newFilterTestServer(t, nil, names, ms)

	visited, final := walkChain(t, srv.URL, "test-challenge", "client-state")
	defer final.Body.Close()

	assert.Equal(t, []string{hostOf(t, okta), hostOf(t, slack), hostOf(t, salesforce)}, visited)
	assert.Equal(t, http.StatusSeeOther, final.StatusCode)
	loc, err := final.Location()
	require.NoError(t, err)
	assert.Equal(t, filterTestRedirectURI, loc.Scheme+"://"+loc.Host+loc.Path)
	assert.NotEmpty(t, loc.Query().Get("code"))
	assert.Equal(t, "client-state", loc.Query().Get("state"))
}

func hostOf(t *testing.T, m *mockoidc.MockOIDC) string {
	t.Helper()
	u, err := url.Parse(m.AuthorizationEndpoint())
	require.NoError(t, err)
	return u.Host
}

// TestIntegration_RunConfigUpstreamFilter_NarrowsChain proves the filter the
// RunConfig asks for is the one the callback handler actually runs: a viewer
// is redirected through okta then slack, and never to salesforce. Server
// construction still discovers salesforce; what the filter removes is the
// authorization redirect.
func TestIntegration_RunConfigUpstreamFilter_NarrowsChain(t *testing.T) {
	t.Parallel()

	okta := startFilterUpstream(t, "okta", []string{"viewer"})
	slack := startFilterUpstream(t, "slack", nil)
	salesforce := startFilterUpstream(t, "salesforce", nil)

	names := []string{"okta", "slack", "salesforce"}
	ms := []*mockoidc.MockOIDC{okta, slack, salesforce}
	filter := &upstreamfilter.GroupBasedFilterConfig{
		Rules: []upstreamfilter.GroupRule{
			{Groups: []string{"viewer"}, UpstreamProviders: []string{"slack"}},
		},
	}
	srv := newFilterTestServer(t, filter, names, ms)

	visited, final := walkChain(t, srv.URL, "test-challenge", "client-state")
	defer final.Body.Close()

	assert.Equal(t, []string{hostOf(t, okta), hostOf(t, slack)}, visited)
	assert.NotContains(t, visited, hostOf(t, salesforce),
		"the user must never be redirected to salesforce")
	assert.Equal(t, http.StatusSeeOther, final.StatusCode)
	loc, err := final.Location()
	require.NoError(t, err)
	assert.Equal(t, filterTestRedirectURI, loc.Scheme+"://"+loc.Host+loc.Path)
	assert.NotEmpty(t, loc.Query().Get("code"), "authorization must complete on the narrowed chain")
	assert.Equal(t, "client-state", loc.Query().Get("state"))
}

// TestIntegration_RunConfigUpstreamFilter_MissingClaimFails covers the
// fail-closed contract through the wiring: an identity without the configured
// claim stops the authorization on the first leg's callback.
func TestIntegration_RunConfigUpstreamFilter_MissingClaimFails(t *testing.T) {
	t.Parallel()

	okta := startFilterUpstream(t, "okta", nil)
	slack := startFilterUpstream(t, "slack", nil)

	names := []string{"okta", "slack"}
	ms := []*mockoidc.MockOIDC{okta, slack}
	filter := &upstreamfilter.GroupBasedFilterConfig{
		Rules: []upstreamfilter.GroupRule{
			{Groups: []string{"viewer"}, UpstreamProviders: []string{"slack"}},
		},
		DefaultUpstreams: []string{"slack"},
	}
	srv := newFilterTestServer(t, filter, names, ms)

	visited, final := walkChain(t, srv.URL, "test-challenge", "client-state")
	defer final.Body.Close()

	assert.Equal(t, []string{hostOf(t, okta)}, visited, "the chain must stop on the first leg")
	require.Equal(t, http.StatusSeeOther, final.StatusCode)
	loc, err := final.Location()
	require.NoError(t, err)
	assert.Equal(t, "server_error", loc.Query().Get("error"))
	assert.Contains(t, loc.Query().Get("error_description"), "failed to determine authorization chain")
	assert.Empty(t, loc.Query().Get("code"), "no authorization code may be issued")
	assert.Equal(t, "client-state", loc.Query().Get("state"))
}

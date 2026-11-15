// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package authserver_test

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4/jwt"
	"github.com/oauth2-proxy/mockoidc"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/authserver"
	"github.com/stacklok/toolhive/pkg/authserver/claimnames"
	"github.com/stacklok/toolhive/pkg/authserver/server"
	servercrypto "github.com/stacklok/toolhive/pkg/authserver/server/crypto"
	"github.com/stacklok/toolhive/pkg/oauthproto"
	"github.com/stacklok/toolhive/test/integration/authserver/helpers"
)

// requestIDJAG presents a grant token and asks for an ID-JAG for target and
// xrtIDJAGResource.
func (e *xrtEnv) requestIDJAG(t *testing.T, auth clientAuth, grant, target string) tokenResult {
	t.Helper()
	return e.requestIDJAGWithHeader(t, auth, grant, target, nil)
}

func (e *xrtEnv) requestIDJAGWithHeader(
	t *testing.T, auth clientAuth, grant, target string, header http.Header,
) tokenResult {
	t.Helper()
	return postTokenWithHeader(t, e.srv.URL, auth, idJAGForm(grant, target), header)
}

// requestIDJAGForm posts form as given, for requests that change or drop what
// requestIDJAG sets.
func (e *xrtEnv) requestIDJAGForm(t *testing.T, auth clientAuth, form url.Values) tokenResult {
	t.Helper()
	return postToken(t, e.srv.URL, auth, form)
}

// idJAGForm is the form of a valid ID-JAG request for target and xrtIDJAGResource.
func idJAGForm(grant, target string) url.Values {
	return url.Values{
		"grant_type":           {oauthproto.GrantTypeTokenExchange},
		"subject_token":        {grant},
		"subject_token_type":   {oauthproto.TokenTypeRefreshToken},
		"requested_token_type": {oauthproto.TokenTypeIDJAG},
		"audience":             {target},
		"resource":             {xrtIDJAGResource},
	}
}

// idJAGGrant returns a grant token owned by auth that allows ID-JAGs, from a
// login-shaped access token.
func (e *xrtEnv) idJAGGrant(t *testing.T, auth clientAuth) string {
	t.Helper()
	subject := e.mint(t, nil)
	res := e.requestRefreshToken(t, auth, subject, func(f url.Values) {
		f.Set("authorization_details", "["+xrtEntry(oauthproto.TokenTypeIDJAG, nil)+"]")
	})
	require.Equal(t, http.StatusOK, res.status, res.body)
	return res.str(t, "access_token")
}

// decodeIDJAG returns the JOSE header and the claims of a compact JWT without
// verifying it. Numbers stay json.Number, so a fractional time shows.
func decodeIDJAG(t *testing.T, token string) (map[string]any, map[string]any) {
	t.Helper()
	parts := strings.Split(token, ".")
	require.Len(t, parts, 3)
	decode := func(segment string) map[string]any {
		raw, err := base64.RawURLEncoding.DecodeString(segment)
		require.NoError(t, err)
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.UseNumber()
		var out map[string]any
		require.NoError(t, decoder.Decode(&out))
		return out
	}
	return decode(parts[0]), decode(parts[1])
}

func intClaim(t *testing.T, claims map[string]any, name string) int64 {
	t.Helper()
	number, ok := claims[name].(json.Number)
	require.True(t, ok, "%s is a JSON number: %v", name, claims[name])
	value, err := number.Int64()
	require.NoError(t, err, "%s is an integer", name)
	return value
}

// assertMintedIDJAG checks a minted ID-JAG response and returns the token's
// claims. The token is for target and xrtIDJAGResource, for the user "user-1";
// extraClaims names the claims the target adds to the base set.
func assertMintedIDJAG(t *testing.T, env *xrtEnv, res tokenResult, target string, extraClaims ...string) map[string]any {
	t.Helper()
	require.Equal(t, http.StatusOK, res.status, res.body)
	assert.ElementsMatch(t, []string{"access_token", "issued_token_type", "token_type", "expires_in"},
		slices.Collect(maps.Keys(res.body)), "the response members are exactly these")
	assert.Equal(t, oauthproto.TokenTypeIDJAG, res.body["issued_token_type"])
	assert.Equal(t, "N_A", res.body["token_type"])
	assert.Equal(t, "no-store", res.header.Get("Cache-Control"))

	header, claims := decodeIDJAG(t, res.str(t, "access_token"))
	assert.Equal(t, oauthproto.JWTTypeIDJAG, header["typ"])
	assert.Equal(t, env.issuer, claims["iss"])
	assert.Equal(t, "user-1", claims["sub"])
	assert.Equal(t, target, claims["aud"])
	assert.Equal(t, xrtTargetClientID, claims["client_id"])
	assert.Equal(t, xrtIDJAGResource, claims["resource"])
	assert.EqualValues(t, xrtIDJAGLifetime, intClaim(t, claims, "exp")-intClaim(t, claims, "iat"))
	base := []string{"iss", "sub", "aud", "client_id", "jti", "iat", "exp", "resource"}
	assert.ElementsMatch(t, append(base, extraClaims...), slices.Collect(maps.Keys(claims)), "the claims are exactly these")
	return claims
}

// TestMintIDJAGFromExchangeGrant mints ID-JAGs from exchange grants over HTTP,
// on both storage backends. Subtests on one server run serially, because
// postToken derives the client assertion's jti from the clock.
func TestMintIDJAGFromExchangeGrant(t *testing.T) {
	t.Parallel()
	for _, backend := range []string{"memory", "redis"} {
		t.Run(backend, func(t *testing.T) {
			t.Parallel()
			runIDJAGGrantFlows(t, startXRTServer(t, backend))
		})
	}
}

func runIDJAGGrantFlows(t *testing.T, env *xrtEnv) {
	t.Helper()
	upstream := map[string]any{"iss": "https://idp.example", "claim": "oid", "value": "obj-1"}

	t.Run("a top-level grant mints an ID-JAG with the golden claims and response", func(t *testing.T) {
		grant := env.idJAGGrant(t, env.broker)
		res := env.requestIDJAG(t, env.broker, grant, xrtTargetPlain)
		assertMintedIDJAG(t, env, res, xrtTargetPlain)
	})

	t.Run("a child grant mints with the claims it copied from its parent", func(t *testing.T) {
		parent := env.mint(t, func(_ *jwt.Claims, x map[string]any) { x[claimnames.UpstreamSubjectKey] = upstream })
		parentGrant := env.requestRefreshToken(t, env.broker, parent, nil)
		require.Equal(t, http.StatusOK, parentGrant.status, parentGrant.body)
		child := env.derive(t, env.broker, parentGrant.str(t, "access_token"), oauthproto.TokenTypeIDJAG, nil)
		require.Equal(t, http.StatusOK, child.status, child.body)

		res := env.requestIDJAG(t, env.broker, child.str(t, "access_token"), xrtTargetUpstream)
		claims := assertMintedIDJAG(t, env, res, xrtTargetUpstream, claimnames.UpstreamSubjectKey)
		assert.Equal(t, upstream, claims[claimnames.UpstreamSubjectKey])
	})

	t.Run("a DPoP header on a valid request is refused", func(t *testing.T) {
		grant := env.idJAGGrant(t, env.broker)
		res := env.requestIDJAGWithHeader(t, env.broker, grant, xrtTargetPlain, http.Header{"Dpop": {"proof"}})
		requireError(t, res, "invalid_request", server.ReasonDPoPNotSupported)
		assert.NotContains(t, res.body, "access_token")
	})

	targetRefusals := []struct {
		name   string
		mutate func(url.Values)
		code   string
	}{
		{name: "an unknown audience", code: "invalid_target",
			mutate: func(f url.Values) { f.Set("audience", "https://unknown.example.net") }},
		{name: "a resource the target does not list", code: "invalid_target",
			mutate: func(f url.Values) { f.Set("resource", "https://other.example.net/mcp") }},
		{name: "a missing resource", code: "invalid_target",
			mutate: func(f url.Values) { f.Del("resource") }},
		{name: "a repeated audience", code: "invalid_request",
			mutate: func(f url.Values) { f.Add("audience", xrtTargetPlain) }},
	}
	for _, tt := range targetRefusals {
		t.Run(tt.name+" is refused", func(t *testing.T) {
			form := idJAGForm(env.idJAGGrant(t, env.broker), xrtTargetPlain)
			tt.mutate(form)
			res := env.requestIDJAGForm(t, env.broker, form)
			require.Equal(t, http.StatusBadRequest, res.status, res.body)
			assert.Equal(t, tt.code, res.body["error"])
			assert.NotContains(t, res.body, "access_token")
		})
	}
}

// TestMintIDJAGClaimsFromRealLogin proves that upstream_subject and sub_id in a
// minted ID-JAG come from a real login: an upstream OIDC login through
// /oauth/authorize and the callback, the source app's access token, a grant
// exchanged from it, and the ID-JAG minted from that grant. Nothing is written
// to storage by the test. The upstream is mockoidc, an OIDC provider, because
// only a verified OIDC identity is complete enough to be recorded.
func TestMintIDJAGClaimsFromRealLogin(t *testing.T) {
	t.Parallel()
	t.Run("memory", func(t *testing.T) {
		t.Parallel()
		oidc := startRealLoginUpstream(t)
		env := startXRTServerWith(t, "memory", func(cfg *authserver.RunConfig, issuer string) {
			cfg.Upstreams = []authserver.UpstreamRunConfig{{
				Name: "mockoidc",
				Type: authserver.UpstreamProviderTypeOIDC,
				OIDCConfig: &authserver.OIDCUpstreamRunConfig{
					IssuerURL: oidc.Issuer(), ClientID: oidc.Config().ClientID,
					ClientSecretFile:  writeSecretFile(t, oidc.Config().ClientSecret),
					RedirectURI:       issuer + "/oauth/callback",
					Scopes:            []string{"openid", "profile", "email"},
					InsecureAllowHTTP: true, AllowPrivateIPs: true,
				},
			}}
		})

		login := realLoginAccessToken(t, env)
		_, loginClaims := decodeIDJAG(t, login)
		wantUpstream := map[string]any{"iss": oidc.Issuer(), "claim": "sub", "value": realLoginUpstreamSubject}
		wantSubID := map[string]any{"format": "iss_sub", "iss": oidc.Issuer(), "sub": realLoginUpstreamSubject}
		require.Equal(t, wantUpstream, loginClaims[claimnames.UpstreamSubjectKey], "the login recorded upstream_subject")
		require.Equal(t, wantSubID, loginClaims["sub_id"], "the login recorded sub_id")

		grant := env.requestRefreshToken(t, env.broker, login, func(f url.Values) {
			f.Set("authorization_details", "["+xrtEntry(oauthproto.TokenTypeIDJAG, nil)+"]")
		})
		require.Equal(t, http.StatusOK, grant.status, grant.body)
		res := env.requestIDJAG(t, env.broker, grant.str(t, "access_token"), xrtTargetBoth)
		require.Equal(t, http.StatusOK, res.status, res.body)

		_, claims := decodeIDJAG(t, res.str(t, "access_token"))
		assert.Equal(t, loginClaims["sub"], claims["sub"], "sub is the login's local subject")
		assert.Equal(t, wantUpstream, claims[claimnames.UpstreamSubjectKey])
		assert.Equal(t, wantSubID, claims["sub_id"])
	})
}

// realLoginAccessToken logs a user in to the source app through the upstream
// and returns the access token for the broker's API audience.
func realLoginAccessToken(t *testing.T, env *xrtEnv) string {
	t.Helper()
	locationOf := func(response *http.Response, status int) *url.URL {
		t.Helper()
		defer func() { require.NoError(t, response.Body.Close()) }()
		require.Equal(t, status, response.StatusCode)
		location, err := response.Location()
		require.NoError(t, err)
		return location
	}
	browser := helpers.NewOAuthClient(env.srv.URL)
	verifier := servercrypto.GeneratePKCEVerifier()
	page, err := browser.StartAuthorization(url.Values{
		"client_id": {xrtSourceApp}, "redirect_uri": {cfgRedirectURI}, "response_type": {"code"},
		"scope": {"openid profile offline_access"}, "state": {"login-state"}, "resource": {cfgAudienceA},
		"code_challenge":        {servercrypto.ComputePKCEChallenge(verifier)},
		"code_challenge_method": {"S256"},
	})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, page.StatusCode)
	approved, err := browser.ApproveConsent(page, env.srv.URL)
	require.NoError(t, err)
	require.NoError(t, page.Body.Close())
	upstreamAuthorize := locationOf(approved, http.StatusSeeOther)

	upstreamResponse, err := (&http.Client{
		Timeout:       10 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}).Get(upstreamAuthorize.String())
	require.NoError(t, err)
	upstreamCallback := locationOf(upstreamResponse, http.StatusFound)

	callback, err := browser.Callback(upstreamCallback.Query().Get("code"), upstreamCallback.Query().Get("state"))
	require.NoError(t, err)
	done := locationOf(callback, http.StatusSeeOther)
	require.Equal(t, "login-state", done.Query().Get("state"))

	tokens := postToken(t, env.srv.URL, clientAuth{}, url.Values{
		"grant_type": {oauthproto.GrantTypeAuthorizationCode}, "code": {done.Query().Get("code")},
		"redirect_uri": {cfgRedirectURI}, "code_verifier": {verifier}, "client_id": {xrtSourceApp},
		"resource": {cfgAudienceA},
	})
	require.Equal(t, http.StatusOK, tokens.status, tokens.body)
	return tokens.str(t, "access_token")
}

// realLoginUpstreamSubject is the subject mockoidc asserts for the queued user.
const realLoginUpstreamSubject = "idjag-real-login-user"

// startRealLoginUpstream starts mockoidc with one queued user.
func startRealLoginUpstream(t *testing.T) *mockoidc.MockOIDC {
	t.Helper()
	m, err := mockoidc.Run()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, m.Shutdown()) })
	m.QueueUser(&mockoidc.MockUser{Subject: realLoginUpstreamSubject, Email: "idjag-user@example.com"})
	return m
}

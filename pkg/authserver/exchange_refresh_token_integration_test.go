// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package authserver_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	"github.com/ory/fosite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/authserver"
	authserverrunner "github.com/stacklok/toolhive/pkg/authserver/runner"
	"github.com/stacklok/toolhive/pkg/authserver/server"
	"github.com/stacklok/toolhive/pkg/authserver/server/session"
	"github.com/stacklok/toolhive/pkg/authserver/server/tokenexchange"
	"github.com/stacklok/toolhive/pkg/authserver/storage"
	"github.com/stacklok/toolhive/pkg/oauthproto"
	"github.com/stacklok/toolhive/test/integration/authserver/helpers"
)

const (
	xrtGrantType       = "https://toolhive.dev/exchange_grant"
	xrtIDJAGOnlyClient = "configured-idjag-only"
	xrtSourceApp       = "dcr-app"
	xrtOtherApp        = "other-app"
	xrtOtherAudience   = "https://other.test.local"
	xrtIDJAGCap        = 12 * time.Hour
	xrtRefreshCap      = 720 * time.Hour
)

// xrtEnv is an embedded auth server with a broker client that may exchange
// login tokens for refresh tokens, plus clients that may not.
type xrtEnv struct {
	srv       *httptest.Server
	issuer    string
	embedded  *authserverrunner.EmbeddedAuthServer
	store     storage.Storage
	brokerKey assertionKey
	broker    clientAuth
	secretIn  clientAuth // configured-idjag-only
	exchange  clientAuth // configured-exchange-only, no exchange_refresh_tokens
	noCode    clientAuth // configured-no-code, for the JWT-bearer grant
	idpKey    *rsa.PrivateKey
}

func xrtClients(t *testing.T, jwksFile string) []authserver.DelegateClientRunConfig {
	t.Helper()
	broker := brokerClient(jwksFile)
	broker.TokenExchange = &authserver.DelegateClientTokenExchange{
		SelfIssuedSubjectTokens: authserver.SelfIssuedSubjectTokensRestricted,
		APIAudience:             cfgAudienceA,
		AllowedSourceClients:    []string{xrtSourceApp},
		ExchangeRefreshTokens: &authserver.DelegateClientExchangeRefreshTokens{
			IDJAGMaxLifetime: xrtIDJAGCap.String(), RefreshTokenMaxLifetime: xrtRefreshCap.String(),
		},
	}
	idJAGOnly := secretClient(t, xrtIDJAGOnlyClient, []string{oauthproto.GrantTypeTokenExchange})
	idJAGOnly.TokenExchange = &authserver.DelegateClientTokenExchange{
		SelfIssuedSubjectTokens: authserver.SelfIssuedSubjectTokensRestricted,
		APIAudience:             cfgAudienceA,
		AllowedSourceClients:    []string{xrtSourceApp},
		ExchangeRefreshTokens: &authserver.DelegateClientExchangeRefreshTokens{
			IDJAGMaxLifetime: xrtIDJAGCap.String(),
		},
	}
	return []authserver.DelegateClientRunConfig{
		broker,
		idJAGOnly,
		secretClient(t, cfgExchangeOnly, []string{oauthproto.GrantTypeTokenExchange}),
		secretClient(t, cfgNoCodeID, []string{oauthproto.GrantTypeTokenExchange, oauthproto.GrantTypeJWTBearer}),
	}
}

func startXRTServer(t *testing.T, backend string) *xrtEnv {
	t.Helper()
	brokerKey := newAssertionKey(t, "broker-key")
	jwksFile := filepath.Join(t.TempDir(), "broker.jwks")
	writeJWKS(t, jwksFile, brokerKey)

	idpKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	jwksServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{
			Key: &idpKey.PublicKey, KeyID: "idp-key", Use: "sig", Algorithm: string(jose.RS256),
		}}})
	}))
	t.Cleanup(jwksServer.Close)

	clients := configuredClients(xrtClients(t, jwksFile)...)
	stor := newStorageForBackend(t, backend)
	srv, issuer, embedded, _ := startEmbeddedAuthServer(t, func(cfg *authserver.RunConfig, issuer string) {
		clients(cfg)
		cfg.AllowedAudiences = []string{cfgAudienceA, xrtOtherAudience}
		cfg.InboundGrants.JWTBearer = &authserver.JWTBearerInboundGrantRunConfig{
			IssuerPolicies: []authserver.JWTBearerIssuerPolicyRunConfig{{
				IssuerRef: "mock-idp", MaxAssertionAge: "5m",
				SubjectBindings: []tokenexchange.JWTBearerSubjectBinding{{
					Subject: cfgSubject, AllowedResources: []string{cfgAudienceA},
				}},
				AcceptedAudiences: []string{issuer + "/oauth/token"},
			}},
		}
		cfg.TrustedIssuers = []tokenexchange.TrustedIssuer{{
			Name: "mock-idp", IssuerURL: cfgIDJAGIssuer, JWKSURL: jwksServer.URL,
			InsecureAllowHTTP: true, AllowPrivateIPs: true,
		}}
	}, stor)
	return &xrtEnv{
		srv: srv, issuer: issuer, embedded: embedded, store: stor, brokerKey: brokerKey, idpKey: idpKey,
		broker:   clientAuth{clientID: cfgBrokerID, key: &brokerKey},
		secretIn: clientAuth{clientID: xrtIDJAGOnlyClient, secret: cfgClientSecret},
		exchange: clientAuth{clientID: cfgExchangeOnly, secret: cfgClientSecret},
		noCode:   clientAuth{clientID: cfgNoCodeID, secret: cfgClientSecret},
	}
}

// mint signs a self-issued token shaped like a login access token issued to
// an allowed source client; mutate edits the claims to build a token that should be refused.
func (e *xrtEnv) mint(t *testing.T, mutate func(*jwt.Claims, map[string]any)) string {
	t.Helper()
	signingKey, err := e.embedded.KeyProvider().SigningKey(context.Background())
	require.NoError(t, err)
	signer, err := jose.NewSigner(
		jose.SigningKey{Algorithm: jose.SignatureAlgorithm(signingKey.Algorithm), Key: signingKey.Key},
		(&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", signingKey.KeyID))
	require.NoError(t, err)
	jti := make([]byte, 12)
	_, err = rand.Read(jti)
	require.NoError(t, err)
	now := time.Now()
	std := jwt.Claims{
		Issuer: e.issuer, Subject: "user-1", Audience: jwt.Audience{cfgAudienceA}, ID: hex.EncodeToString(jti),
		Expiry: jwt.NewNumericDate(now.Add(30 * time.Minute)), IssuedAt: jwt.NewNumericDate(now),
	}
	extra := map[string]any{"client_id": xrtSourceApp, "scope": "openid profile"}
	if mutate != nil {
		mutate(&std, extra)
	}
	token, err := jwt.Signed(signer).Claims(std).Claims(extra).Serialize()
	require.NoError(t, err)
	return token
}

func xrtEntry(tokenType string, expiresAt *int64) string {
	entry := fmt.Sprintf(`{"type":%q,"issuable_token_types":[%q]`, xrtGrantType, tokenType)
	if expiresAt != nil {
		entry += fmt.Sprintf(`,"expires_at":%d`, *expiresAt)
	}
	return entry + "}"
}

// exchange requests a refresh token for subject. mutate edits the form.
func (e *xrtEnv) requestRefreshToken(
	t *testing.T, auth clientAuth, subject string, mutate func(url.Values),
) tokenResult {
	t.Helper()
	form := url.Values{
		"grant_type":            {oauthproto.GrantTypeTokenExchange},
		"subject_token":         {subject},
		"subject_token_type":    {oauthproto.TokenTypeAccessToken},
		"requested_token_type":  {oauthproto.TokenTypeRefreshToken},
		"authorization_details": {"[" + xrtEntry(oauthproto.TokenTypeRefreshToken, nil) + "]"},
	}
	if mutate != nil {
		mutate(form)
	}
	return postToken(t, e.srv.URL, auth, form)
}

// tokenSignature is the signature segment of a token, which keys its grant.
func tokenSignature(token string) string { return token[strings.LastIndex(token, ".")+1:] }

// grant reads the stored grant behind a refresh token.
func (e *xrtEnv) grant(t *testing.T, token string) *storage.ExchangeGrant {
	t.Helper()
	grants, ok := storage.Unwrap(e.store).(storage.ExchangeGrantStorage)
	require.True(t, ok)
	g, err := grants.GetExchangeGrant(t.Context(), tokenSignature(token))
	require.NoError(t, err)
	return g
}

// assertJSONEqual compares the decoded values, not the bytes, so key order does
// not matter.
func assertJSONEqual(t *testing.T, want map[string]any, got string) {
	t.Helper()
	var decoded map[string]any
	require.NoError(t, json.Unmarshal([]byte(got), &decoded), got)
	assert.Equal(t, want, decoded)
}

// hinted is the error_description fosite builds from a base error and a hint.
func hinted(base *fosite.RFC6749Error, hint string) string {
	return base.WithHint(hint).GetDescription()
}

// requireError asserts a 400 token response with the given error and
// error_description.
func requireError(t *testing.T, res tokenResult, code, description string) {
	t.Helper()
	require.Equal(t, http.StatusBadRequest, res.status, res.body)
	assert.Equal(t, code, res.body["error"])
	got, _ := res.body["error_description"].(string)
	assert.Equal(t, description, got)
}

func TestExchangeRefreshTokensThroughEmbeddedAuthServer(t *testing.T) {
	t.Parallel()
	for _, backend := range []string{"memory", "redis"} {
		t.Run(backend, func(t *testing.T) {
			t.Parallel()
			runExchangeRefreshTokenGrantFlows(t, startXRTServer(t, backend))
		})
	}
	// Every refusal fails before any store access, so the backend cannot change
	// the outcome. Refusals that touch the store (replay, and not consuming the
	// subject token on refusal) run on both backends above.
	t.Run("refusals", func(t *testing.T) {
		t.Parallel()
		runExchangeRefreshTokenRefusals(t, startXRTServer(t, "memory"))
	})
}

// runExchangeRefreshTokenGrantFlows runs the subtests that read or write the
// exchange-grant store, so they run on every storage backend.
func runExchangeRefreshTokenGrantFlows(t *testing.T, env *xrtEnv) {
	t.Helper()
	t.Run("source client token yields a refresh token with the specified response", func(t *testing.T) {
		before := time.Now()
		res := env.requestRefreshToken(t, env.broker, env.mint(t, nil), nil)
		after := time.Now()
		require.Equal(t, http.StatusOK, res.status, res.body)
		assertRefreshTokenResponse(t, res, oauthproto.TokenTypeRefreshToken, before, after, xrtRefreshCap)
	})

	t.Run("token response shape and echo", func(t *testing.T) {
		far := time.Now().Add(2000 * time.Hour).Unix()
		near := time.Now().Add(time.Hour).Unix()
		for name, tc := range map[string]struct {
			tokenType string
			expiresAt *int64
			wantCap   time.Duration
			wantExact *int64
		}{
			"absent expires_at is filled with the cap":      {oauthproto.TokenTypeRefreshToken, nil, xrtRefreshCap, nil},
			"expires_at above the cap is capped":            {oauthproto.TokenTypeRefreshToken, &far, xrtRefreshCap, nil},
			"id-jag grant is capped by its own cap":         {oauthproto.TokenTypeIDJAG, &far, xrtIDJAGCap, nil},
			"expires_at below the cap is kept as requested": {oauthproto.TokenTypeIDJAG, &near, xrtIDJAGCap, &near},
		} {
			t.Run(name, func(t *testing.T) {
				before := time.Now()
				res := env.requestRefreshToken(t, env.broker, env.mint(t, nil), func(f url.Values) {
					f.Set("authorization_details", "["+xrtEntry(tc.tokenType, tc.expiresAt)+"]")
				})
				after := time.Now()
				require.Equal(t, http.StatusOK, res.status, res.body)
				entry := assertRefreshTokenResponse(t, res, tc.tokenType, before, after, tc.wantCap)
				if tc.wantExact != nil {
					assert.EqualValues(t, *tc.wantExact, entry["expires_at"])
				}
				delete(entry, "expires_at")
				assert.Equal(t, map[string]any{
					"type": xrtGrantType, "issuable_token_types": []any{tc.tokenType},
				}, entry, "the echo equals the request except the granted expires_at")
			})
		}
	})

	t.Run("the grant records sub_id and upstream_subject", func(t *testing.T) {
		subID := map[string]any{"format": "iss_sub", "iss": env.issuer, "sub": "user-1"}
		upstream := map[string]any{"iss": "https://idp.example", "claim": "oid", "value": "obj-1"}
		subject := env.mint(t, func(_ *jwt.Claims, x map[string]any) {
			x["sub_id"] = subID
			x[session.UpstreamSubjectClaimKey] = upstream
		})
		res := env.requestRefreshToken(t, env.broker, subject, nil)
		require.Equal(t, http.StatusOK, res.status, res.body)
		grant := env.grant(t, res.str(t, "access_token"))
		assertJSONEqual(t, subID, grant.SubjectIdentifier)
		assertJSONEqual(t, upstream, grant.UpstreamSubject)
	})

	t.Run("a token without either claim still gets a grant with both fields empty", func(t *testing.T) {
		res := env.requestRefreshToken(t, env.broker, env.mint(t, nil), nil)
		require.Equal(t, http.StatusOK, res.status, res.body)
		grant := env.grant(t, res.str(t, "access_token"))
		assert.Empty(t, grant.SubjectIdentifier)
		assert.Empty(t, grant.UpstreamSubject)
	})

	t.Run("a refused request does not consume the subject token", func(t *testing.T) {
		token := env.mint(t, nil)
		refused := env.requestRefreshToken(t, env.broker, token, func(f url.Values) { f.Set("scope", "openid") })
		require.Equal(t, http.StatusBadRequest, refused.status, refused.body)
		assert.Equal(t, http.StatusOK, env.requestRefreshToken(t, env.broker, token, nil).status)
	})

	t.Run("replayed subject token is refused as token_used", func(t *testing.T) {
		token := env.mint(t, nil)
		require.Equal(t, http.StatusOK, env.requestRefreshToken(t, env.broker, token, nil).status)
		requireError(t, env.requestRefreshToken(t, env.broker, token, nil), "invalid_request", "token_used")
	})

	t.Run("concurrent exchanges of one subject token produce one grant", func(t *testing.T) {
		token := env.mint(t, nil)
		form := url.Values{
			"grant_type":            {oauthproto.GrantTypeTokenExchange},
			"subject_token":         {token},
			"subject_token_type":    {oauthproto.TokenTypeAccessToken},
			"requested_token_type":  {oauthproto.TokenTypeRefreshToken},
			"authorization_details": {"[" + xrtEntry(oauthproto.TokenTypeIDJAG, nil) + "]"},
		}
		const workers = 8
		type outcome struct {
			status int
			body   map[string]any
			err    error
		}
		results := make([]outcome, workers)
		start := make(chan struct{})
		var wg sync.WaitGroup
		for i := range workers {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				results[i].status, results[i].body, results[i].err = postFormWithSecret(
					env.srv.URL, xrtIDJAGOnlyClient, cfgClientSecret, form)
			}()
		}
		close(start)
		wg.Wait()
		ok := 0
		var winner string
		for _, res := range results {
			require.NoError(t, res.err)
			if res.status == http.StatusOK {
				ok++
				winner, _ = res.body["access_token"].(string)
				continue
			}
			assert.Equal(t, http.StatusBadRequest, res.status, res.body)
			assert.Equal(t, "invalid_request", res.body["error"], res.body)
			assert.Equal(t, "token_used", res.body["error_description"], res.body)
		}
		assert.Equal(t, 1, ok, "exactly one exchange succeeds")
		require.NotEmpty(t, winner, "the winning response carries a refresh token")
		assert.NotNil(t, env.grant(t, winner), "the winning token resolves to a stored grant")
	})

	t.Run("the issued token is not a login refresh token", func(t *testing.T) {
		issued := env.requestRefreshToken(t, env.broker, env.mint(t, nil), nil)
		require.Equal(t, http.StatusOK, issued.status, issued.body)
		res := postToken(t, env.srv.URL, env.broker, url.Values{
			"grant_type": {oauthproto.GrantTypeRefreshToken}, "refresh_token": {issued.str(t, "access_token")},
		})
		assert.Equal(t, http.StatusBadRequest, res.status, res.body)
		assert.Equal(t, "invalid_grant", res.body["error"])
		assert.NotContains(t, res.body, "access_token", "nothing is issued")
	})
}

// runExchangeRefreshTokenRefusals runs the subtests that refuse a request before
// anything is stored, so one backend is enough.
func runExchangeRefreshTokenRefusals(t *testing.T, env *xrtEnv) {
	t.Helper()
	t.Run("routing", func(t *testing.T) {
		// subject_token_type=refresh_token is claimed by the refresh-token handler,
		// which has no derive path yet and refuses it.
		res := env.requestRefreshToken(t, env.broker, "irrelevant", func(f url.Values) {
			f.Set("subject_token_type", oauthproto.TokenTypeRefreshToken)
			f.Set("requested_token_type", oauthproto.TokenTypeIDJAG)
			f.Del("authorization_details")
		})
		requireError(t, res, "invalid_request", hinted(fosite.ErrInvalidRequest, fmt.Sprintf(
			"The 'subject_token_type' value %q is not supported. Use %q or %q.",
			oauthproto.TokenTypeRefreshToken, oauthproto.TokenTypeAccessToken, oauthproto.TokenTypeJWT)))

		// A plain access-token exchange still reaches the original handler.
		plain := postToken(t, env.srv.URL, env.broker, url.Values{
			"grant_type":         {oauthproto.GrantTypeTokenExchange},
			"subject_token":      {env.mint(t, nil)},
			"subject_token_type": {oauthproto.TokenTypeAccessToken},
			"scope":              {"openid profile"}, "audience": {cfgAudienceA},
		})
		require.Equal(t, http.StatusOK, plain.status, plain.body)
		assert.Equal(t, oauthproto.TokenTypeAccessToken, plain.body["issued_token_type"])
		assert.Equal(t, "bearer", plain.body["token_type"])
	})

	t.Run("client without exchange_refresh_tokens gets the unsupported-type error", func(t *testing.T) {
		res := env.requestRefreshToken(t, env.exchange, env.mint(t, nil), nil)
		requireError(t, res, "invalid_request",
			unsupportedTypeDescription("requested_token_type", oauthproto.TokenTypeRefreshToken))
	})

	t.Run("subject token refusals", func(t *testing.T) {
		for _, tc := range []struct {
			name        string
			mutate      func(*jwt.Claims, map[string]any)
			description string
		}{
			{"delegated token (act claim)", func(_ *jwt.Claims, x map[string]any) { x["act"] = map[string]any{"sub": cfgBrokerID} }, "subject_token_invalid"},
			{"external issuer claim", func(_ *jwt.Claims, x map[string]any) { x["external_issuer"] = cfgIDJAGIssuer }, "subject_token_invalid"},
			{"external subject with a hash", func(c *jwt.Claims, _ map[string]any) { c.Subject = cfgIDJAGIssuer + "#" + cfgSubject }, "subject_token_invalid"},
			{"no upstream session claim", func(_ *jwt.Claims, x map[string]any) { x["https://toolhive.dev/no_upstream_session"] = true }, "subject_token_invalid"},
			{"missing client_id", func(_ *jwt.Claims, x map[string]any) { delete(x, "client_id") }, "subject_token_invalid"},
			{"missing jti", func(c *jwt.Claims, _ map[string]any) { c.ID = "" }, "subject_token_invalid"},
			{"audience without the API audience", func(c *jwt.Claims, _ map[string]any) { c.Audience = jwt.Audience{xrtOtherAudience} }, "subject_token_audience"},
			{"issued to the exchanging client", func(_ *jwt.Claims, x map[string]any) { x["client_id"] = cfgBrokerID }, "subject_token_source_client"},
			{"source client not allowed", func(_ *jwt.Claims, x map[string]any) { x["client_id"] = xrtOtherApp }, "subject_token_source_client"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				requireError(t, env.requestRefreshToken(t, env.broker, env.mint(t, tc.mutate), nil),
					"invalid_request", tc.description)
			})
		}

		t.Run("ID token", func(t *testing.T) {
			token := env.mint(t, func(_ *jwt.Claims, x map[string]any) { x["at_hash"] = "abc" })
			requireError(t, env.requestRefreshToken(t, env.broker, token, nil), "invalid_request", "subject_token_invalid")
		})
		t.Run("not a token", func(t *testing.T) {
			requireError(t, env.requestRefreshToken(t, env.broker, "garbage", nil), "invalid_request", "subject_token_invalid")
		})

		for name, typ := range map[string]string{"JWT-bearer-issued token": "JWT", "ID-JAG-issued token": "oauth-id-jag+jwt"} {
			t.Run(name, func(t *testing.T) {
				assertion := signAssertionOfType(t, env.idpKey, typ, env.issuer, cfgNoCodeID, "jti-"+strings.ReplaceAll(typ, "+", "-")+"-"+t.Name())
				issued := postToken(t, env.srv.URL, env.noCode, url.Values{
					"grant_type": {oauthproto.GrantTypeJWTBearer}, "assertion": {assertion}, "resource": {cfgAudienceA},
				})
				require.Equal(t, http.StatusOK, issued.status, issued.body)
				requireError(t, env.requestRefreshToken(t, env.broker, issued.str(t, "access_token"), nil),
					"invalid_request", "subject_token_invalid")
			})
		}
	})

	t.Run("request refusals", func(t *testing.T) {
		details := func(raw string) func(url.Values) {
			return func(f url.Values) { f.Set("authorization_details", raw) }
		}
		refreshEntry := xrtEntry(oauthproto.TokenTypeRefreshToken, nil)
		past := time.Now().Add(-time.Hour).Unix()
		invalidDetails := "The authorization_details parameter is invalid."
		targetRefused := hinted(server.ErrInvalidTarget,
			"The 'resource' and 'audience' parameters are not supported when requesting a refresh token.")
		for _, tc := range []struct {
			name        string
			mutate      func(url.Values)
			auth        *clientAuth
			code        string
			description string
		}{
			{name: "actor_token", mutate: func(f url.Values) {
				f.Set("actor_token", "x")
				f.Set("actor_token_type", oauthproto.TokenTypeAccessToken)
			},
				code: "invalid_request", description: hinted(fosite.ErrInvalidRequest, "The 'actor_token' parameter is not supported when requesting a refresh token.")},
			{name: "actor_token_type alone", mutate: func(f url.Values) { f.Set("actor_token_type", oauthproto.TokenTypeAccessToken) },
				code: "invalid_request", description: hinted(fosite.ErrInvalidRequest, "The 'actor_token' parameter is not supported when requesting a refresh token.")},
			{name: "scope", mutate: func(f url.Values) { f.Set("scope", "openid") },
				code: "invalid_scope", description: hinted(fosite.ErrInvalidScope, "The 'scope' parameter is not supported when requesting a refresh token.")},
			{name: "resource", mutate: func(f url.Values) { f.Set("resource", cfgAudienceA) },
				code: "invalid_target", description: targetRefused},
			{name: "audience", mutate: func(f url.Values) { f.Set("audience", cfgAudienceA) },
				code: "invalid_target", description: targetRefused},
			{name: "subject_token_type jwt", mutate: func(f url.Values) { f.Set("subject_token_type", oauthproto.TokenTypeJWT) },
				code: "invalid_request", description: hinted(fosite.ErrInvalidRequest, fmt.Sprintf("The 'subject_token_type' value %q is not supported when requesting a refresh token. Use %q.", oauthproto.TokenTypeJWT, oauthproto.TokenTypeAccessToken))},
			{name: "absent authorization_details", mutate: func(f url.Values) { f.Del("authorization_details") },
				code: "invalid_request", description: "The authorization_details parameter is required."},
			{name: "empty authorization_details", mutate: details(""),
				code: "invalid_request", description: "The authorization_details parameter is required."},
			{name: "empty array", mutate: details("[]"),
				code: "invalid_authorization_details", description: "single_entry_required"},
			{name: "repeated authorization_details", mutate: func(f url.Values) {
				f["authorization_details"] = []string{"[" + refreshEntry + "]", "[" + refreshEntry + "]"}
			},
				code: "invalid_request", description: "The authorization_details parameter must not be repeated."},
			{name: "two entries", mutate: details("[" + refreshEntry + "," + refreshEntry + "]"),
				code: "invalid_authorization_details", description: "single_entry_required"},
			{name: "entry with no type", mutate: details(`[{"issuable_token_types":["` + oauthproto.TokenTypeRefreshToken + `"]}]`),
				code: "invalid_authorization_details", description: invalidDetails},
			{name: "malformed JSON", mutate: details("{"),
				code: "invalid_authorization_details", description: invalidDetails},
			{name: "unknown member", mutate: details(`[{"type":"` + xrtGrantType + `","issuable_token_types":["` + oauthproto.TokenTypeRefreshToken + `"],"locations":["https://a"]}]`),
				code: "invalid_authorization_details", description: "malformed_entry"},
			{name: "two issuable types", mutate: details(`[{"type":"` + xrtGrantType + `","issuable_token_types":["` + oauthproto.TokenTypeRefreshToken + `","` + oauthproto.TokenTypeIDJAG + `"]}]`),
				code: "invalid_authorization_details", description: "invalid_issuable_token_types"},
			{name: "expires_at in the past", mutate: details("[" + xrtEntry(oauthproto.TokenTypeRefreshToken, &past) + "]"),
				code: "invalid_authorization_details", description: "invalid_expires_at"},
			{name: "fractional expires_at", mutate: details(`[{"type":"` + xrtGrantType + `","issuable_token_types":["` + oauthproto.TokenTypeRefreshToken + `"],"expires_at":1.5}]`),
				code: "invalid_authorization_details", description: "invalid_expires_at"},
			{name: "type not configured for the client", mutate: details("[" + refreshEntry + "]"), auth: &env.secretIn,
				code: "invalid_authorization_details", description: "token_type_not_permitted"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				auth := env.broker
				if tc.auth != nil {
					auth = *tc.auth
				}
				requireError(t, env.requestRefreshToken(t, auth, env.mint(t, nil), tc.mutate), tc.code, tc.description)
			})
		}
	})

	t.Run("a delegated token is refused as a subject", func(t *testing.T) {
		delegated := postToken(t, env.srv.URL, env.broker, url.Values{
			"grant_type":         {oauthproto.GrantTypeTokenExchange},
			"subject_token":      {env.mint(t, nil)},
			"subject_token_type": {oauthproto.TokenTypeAccessToken},
			"scope":              {"openid profile"}, "audience": {cfgAudienceA},
		})
		require.Equal(t, http.StatusOK, delegated.status, delegated.body)
		res := env.requestRefreshToken(t, env.broker, delegated.str(t, "access_token"), nil)
		requireError(t, res, "invalid_request", "subject_token_invalid")
	})
}

// postFormWithSecret posts a token request with HTTP Basic client credentials.
// Unlike postToken it reports failures as an error, so goroutines can use it.
func postFormWithSecret(serverURL, clientID, secret string, form url.Values) (int, map[string]any, error) {
	request, err := http.NewRequest(http.MethodPost, serverURL+"/oauth/token", strings.NewReader(form.Encode()))
	if err != nil {
		return 0, nil, err
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.SetBasicAuth(clientID, secret)
	response, err := (&http.Client{Timeout: 10 * time.Second}).Do(request)
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = response.Body.Close() }()
	var body map[string]any
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		return 0, nil, err
	}
	return response.StatusCode, body, nil
}

// unsupportedTypeDescription is the error_description of a token type the
// client or request cannot use.
func unsupportedTypeDescription(param, value string) string {
	return hinted(fosite.ErrInvalidRequest, fmt.Sprintf(
		"The '%s' value %q is not supported for this client. The access token type %q is always supported.",
		param, value, oauthproto.TokenTypeAccessToken))
}

// assertRefreshTokenResponse checks the RFC 8693 response of an issued refresh
// token and returns the echoed entry. expires_at must equal the whole second of
// the response time plus expires_in, and respect the cap.
func assertRefreshTokenResponse(
	t *testing.T, res tokenResult, tokenType string, before, after time.Time, limit time.Duration,
) map[string]any {
	t.Helper()
	assert.NotEmpty(t, res.str(t, "access_token"))
	assert.Equal(t, "N_A", res.str(t, "token_type"))
	assert.Equal(t, oauthproto.TokenTypeRefreshToken, res.str(t, "issued_token_type"))
	assert.NotContains(t, res.body, "refresh_token")
	assert.NotContains(t, res.body, "scope")
	assert.Equal(t, "no-store", res.header.Get("Cache-Control"))
	assert.Equal(t, "no-cache", res.header.Get("Pragma"))

	expiresIn, ok := res.body["expires_in"].(float64)
	require.True(t, ok, "expires_in is a number: %v", res.body)
	details, ok := res.body["authorization_details"].([]any)
	require.True(t, ok, "authorization_details is an array: %v", res.body)
	require.Len(t, details, 1)
	entry, ok := details[0].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, xrtGrantType, entry["type"])
	assert.Equal(t, []any{tokenType}, entry["issuable_token_types"])
	expiresAt, ok := entry["expires_at"].(float64)
	require.True(t, ok, "the echo carries the granted expires_at: %v", entry)
	assert.GreaterOrEqual(t, int64(expiresAt), before.Unix()+int64(expiresIn))
	assert.LessOrEqual(t, int64(expiresAt), after.Unix()+int64(expiresIn))
	assert.LessOrEqual(t, int64(expiresAt), after.Add(limit).Unix(), "the cap holds")
	return entry
}

// TestExchangeRefreshTokensRequireTheStorageCapability starts a server over a
// store that hides ExchangeGrantStorage. Startup must fail when a client
// enables the feature, and succeed when none does.
func TestExchangeRefreshTokensRequireTheStorageCapability(t *testing.T) {
	t.Parallel()

	// newHidden wraps a fresh memory store so the exchange-grant capability is
	// not visible through storage.Unwrap. Each start owns its store: the
	// constructor closes it on failure and EmbeddedAuthServer.Close on success.
	newHidden := func() storage.Storage {
		memory := storage.NewMemoryStorage()
		return struct {
			storage.Storage
			storage.DCRCredentialStore
		}{memory, memory}
	}
	_, hasCapability := storage.Unwrap(newHidden()).(storage.ExchangeGrantStorage)
	require.False(t, hasCapability, "the decorator hides the capability")

	start := func(clients []authserver.DelegateClientRunConfig) error {
		upstream := helpers.NewMockUpstreamIDP(t)
		cfg := helpers.NewTestAuthServerConfig(t, upstream.URL())
		cfg.Issuer = "http://127.0.0.1:9"
		cfg.Upstreams[0].OAuth2Config.RedirectURI = cfg.Issuer + "/oauth/callback"
		cfg.InsecureAllowConfidentialOverLoopbackHTTP = true
		configuredClients(clients...)(cfg)
		embedded, err := authserverrunner.NewEmbeddedAuthServerWithStorage(t.Context(), cfg, newHidden())
		if err == nil {
			assert.NoError(t, embedded.Close())
		}
		return err
	}

	jwksFile := filepath.Join(t.TempDir(), "broker.jwks")
	writeJWKS(t, jwksFile, newAssertionKey(t, "broker-key"))
	err := start(xrtClients(t, jwksFile))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "storage.ExchangeGrantStorage")
	assert.Contains(t, err.Error(), cfgBrokerID)

	plain := []authserver.DelegateClientRunConfig{secretClient(t, cfgExchangeOnly, []string{oauthproto.GrantTypeTokenExchange})}
	assert.NoError(t, start(plain), "no client enables the feature")
}

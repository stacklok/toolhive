// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package authserver_test

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/authserver"
	authserverrunner "github.com/stacklok/toolhive/pkg/authserver/runner"
	"github.com/stacklok/toolhive/pkg/oauthproto"
	"github.com/stacklok/toolhive/test/integration/authserver/helpers"
)

const (
	delegateClientID       = "configured-delegate-client"
	delegateClientSecret   = "configured-delegate-secret-well-above-minimum-length"
	delegateAudience       = "https://delegate.test.local"
	ungrantedScope         = "email"
	ungrantedAudience      = "https://other-resource.test.local"
	delegatedSubject       = "delegated-subject"
	tokenExchangeGrantType = "urn:ietf:params:oauth:grant-type:token-exchange"
)

// TestConfiguredDelegateClientTokenExchange proves configured static clients are
// registered through RunConfig and runner startup before serving RFC 8693 requests.
func TestConfiguredDelegateClientTokenExchange(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		authenticate  func(*http.Request)
		mutateRequest func(url.Values)
		wantStatus    int
		wantError     string
	}{
		{
			name: "client_secret_basic",
			authenticate: func(req *http.Request) {
				req.SetBasicAuth(delegateClientID, delegateClientSecret)
			},
			wantStatus: http.StatusOK,
		},
		{
			name:         "client_secret_post",
			authenticate: func(_ *http.Request) {},
			wantStatus:   http.StatusOK,
		},
		{
			// fosite treats a request with no credentials presented at all (neither
			// Basic auth nor a client_id/client_secret form pair) as a malformed
			// request, not an authentication failure -- 401 invalid_client is reserved
			// for credentials that were presented but are wrong (see "invalid_secret"
			// below).
			name:         "missing_credentials",
			authenticate: func(_ *http.Request) {},
			mutateRequest: func(values url.Values) {
				values.Del("client_id")
				values.Del("client_secret")
			},
			wantStatus: http.StatusBadRequest,
			wantError:  "invalid_request",
		},
		{
			name:         "invalid_secret",
			authenticate: func(_ *http.Request) {},
			mutateRequest: func(values url.Values) {
				values.Set("client_secret", "wrong-secret")
			},
			wantStatus: http.StatusUnauthorized,
			wantError:  "invalid_client",
		},
		{
			name:         "global_but_ungranted_scope",
			authenticate: func(_ *http.Request) {},
			mutateRequest: func(values url.Values) {
				values.Set("scope", ungrantedScope)
			},
			wantStatus: http.StatusBadRequest,
			wantError:  "invalid_scope",
		},
		{
			name:         "global_but_ungranted_audience",
			authenticate: func(_ *http.Request) {},
			mutateRequest: func(values url.Values) {
				values.Set("audience", ungrantedAudience)
			},
			wantStatus: http.StatusBadRequest,
			wantError:  "invalid_request",
		},
		{
			name:         "id_token_subject_token_type_rejected",
			authenticate: func(_ *http.Request) {},
			mutateRequest: func(values url.Values) {
				values.Set("subject_token_type", oauthproto.TokenTypeIDToken)
			},
			wantStatus: http.StatusBadRequest,
			wantError:  "invalid_request",
		},
	}

	t.Run("discovery advertises token exchange and client secret methods", func(t *testing.T) {
		server, _, _ := startConfiguredDelegateAuthServer(t)
		assertConfiguredDelegateDiscovery(t, server.URL)
	})

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			server, issuer, embedded := startConfiguredDelegateAuthServer(t)
			subjectToken := signedDelegateSubjectToken(t, embedded, issuer)
			values := url.Values{
				"grant_type":         {tokenExchangeGrantType},
				"subject_token":      {subjectToken},
				"subject_token_type": {oauthproto.TokenTypeAccessToken},
				"scope":              {"openid profile"},
				"audience":           {delegateAudience},
				"client_id":          {delegateClientID},
				"client_secret":      {delegateClientSecret},
			}
			if tt.name == "client_secret_basic" {
				values.Del("client_id")
				values.Del("client_secret")
			}
			if tt.mutateRequest != nil {
				tt.mutateRequest(values)
			}

			request, err := http.NewRequest(http.MethodPost, server.URL+"/oauth/token", strings.NewReader(values.Encode()))
			require.NoError(t, err)
			request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			tt.authenticate(request)

			response, err := (&http.Client{Timeout: 10 * time.Second}).Do(request)
			require.NoError(t, err)
			t.Cleanup(func() {
				_, _ = io.Copy(io.Discard, response.Body)
				require.NoError(t, response.Body.Close())
			})

			var body map[string]any
			require.NoError(t, json.NewDecoder(response.Body).Decode(&body))
			require.Equal(t, tt.wantStatus, response.StatusCode, "token response: %v", body)
			if tt.wantError != "" {
				assert.Equal(t, tt.wantError, body["error"])
				return
			}

			assert.Equal(t, oauthproto.TokenTypeAccessToken, body["issued_token_type"])
			accessToken, ok := body["access_token"].(string)
			require.True(t, ok)
			verifyDelegatedToken(t, embedded, accessToken, issuer)
		})
	}
}

// TestConfiguredDelegateClientTokenExchange_WithActorToken proves that an
// explicit RFC 8693 actor_token composes correctly with the configured
// delegate-client relaxation instead of being tested in isolation from it.
//
// The discriminating case is the mismatched-actor_token one: a configured
// delegate client's blanket trust to exchange ANY self-issued subject token
// (see docs/arch/17-token-exchange-delegation.md, "Delegate clients and
// self-issued token exchange") must never be interpretable as also relaxing
// the actor_token binding check in resolveActorIdentity. The subject token
// deliberately uses a client_id the delegate relaxation WOULD otherwise
// excuse, so a wrongly-successful response here proves actor identity
// resolution was skipped, not merely that delegation consent was lenient.
func TestConfiguredDelegateClientTokenExchange_WithActorToken(t *testing.T) {
	t.Parallel()

	const originalClientID = "original-non-delegate-client"

	tests := []struct {
		name             string
		subjectClientID  string // client_id claim baked into the subject token
		actorTokenClient string // client_id claim of the actor_token (binding check)
		actorTokenSub    string // sub claim of the actor_token (asserted actor identity)
		wantStatus       int
		wantError        string
	}{
		{
			// RFC 8693 §2.2.2 requires invalid_request when an actor token is
			// unacceptable based on policy, including a client-ID binding mismatch.
			name:             "mismatched actor_token rejected before delegation consent is reached",
			subjectClientID:  originalClientID,
			actorTokenClient: "someone-elses-client",
			actorTokenSub:    "someone-else",
			wantStatus:       http.StatusBadRequest,
			wantError:        "invalid_request",
		},
		{
			name:             "matching actor_token still succeeds via the delegate-client relaxation",
			subjectClientID:  originalClientID,
			actorTokenClient: delegateClientID,
			actorTokenSub:    delegateClientID,
			wantStatus:       http.StatusOK,
		},
		{
			// Delegate status must not change behavior when the relaxation isn't
			// needed: the subject token's client_id already matches the
			// authenticated client, so this succeeds on ordinary client_id
			// binding, and verifyDelegatedToken's act.sub assertion below proves
			// it produces the identical act shape either way.
			name:             "actor_token present but relaxation unneeded still succeeds",
			subjectClientID:  delegateClientID,
			actorTokenClient: delegateClientID,
			actorTokenSub:    delegateClientID,
			wantStatus:       http.StatusOK,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			server, issuer, embedded := startConfiguredDelegateAuthServer(t)
			subjectToken := signedSubjectTokenForClient(t, embedded, issuer, tt.subjectClientID)
			actorToken := signedActorToken(t, embedded, issuer, tt.actorTokenClient, tt.actorTokenSub)

			values := url.Values{
				"grant_type":         {tokenExchangeGrantType},
				"subject_token":      {subjectToken},
				"subject_token_type": {oauthproto.TokenTypeAccessToken},
				"actor_token":        {actorToken},
				"actor_token_type":   {oauthproto.TokenTypeAccessToken},
				"scope":              {"openid profile"},
				"audience":           {delegateAudience},
				"client_id":          {delegateClientID},
				"client_secret":      {delegateClientSecret},
			}

			request, err := http.NewRequest(http.MethodPost, server.URL+"/oauth/token", strings.NewReader(values.Encode()))
			require.NoError(t, err)
			request.Header.Set("Content-Type", "application/x-www-form-urlencoded")

			response, err := (&http.Client{Timeout: 10 * time.Second}).Do(request)
			require.NoError(t, err)
			t.Cleanup(func() {
				_, _ = io.Copy(io.Discard, response.Body)
				require.NoError(t, response.Body.Close())
			})

			var body map[string]any
			require.NoError(t, json.NewDecoder(response.Body).Decode(&body))
			require.Equal(t, tt.wantStatus, response.StatusCode, "token response: %v", body)
			if tt.wantError != "" {
				assert.Equal(t, tt.wantError, body["error"])
				return
			}

			accessToken, ok := body["access_token"].(string)
			require.True(t, ok)
			// verifyDelegatedToken asserts act.sub == delegateClientID, which
			// here also confirms actor_token's own claims never leak into act:
			// RFC 8693 §4.1's act.sub must name the current actor precisely
			// because downstream access-control decisions key off it.
			verifyDelegatedToken(t, embedded, accessToken, issuer)
		})
	}
}

func assertConfiguredDelegateDiscovery(t *testing.T, serverURL string) {
	t.Helper()

	wantAuthMethods := []string{
		oauthproto.TokenEndpointAuthMethodNone,
		oauthproto.TokenEndpointAuthMethodClientSecretBasic,
		oauthproto.TokenEndpointAuthMethodClientSecretPost,
	}
	for _, endpoint := range []string{
		"/.well-known/oauth-authorization-server",
		"/.well-known/openid-configuration",
	} {
		response, err := (&http.Client{Timeout: 10 * time.Second}).Get(serverURL + endpoint)
		require.NoError(t, err)
		func() {
			defer func() {
				_, _ = io.Copy(io.Discard, response.Body)
				require.NoError(t, response.Body.Close())
			}()

			var metadata oauthproto.AuthorizationServerMetadata
			require.NoError(t, json.NewDecoder(response.Body).Decode(&metadata))
			require.Equal(t, http.StatusOK, response.StatusCode)
			assert.Contains(t, metadata.GrantTypesSupported, tokenExchangeGrantType)
			assert.Equal(t, wantAuthMethods, metadata.TokenEndpointAuthMethodsSupported)
		}()
	}
}

func startConfiguredDelegateAuthServer(t *testing.T) (*httptest.Server, string, *authserverrunner.EmbeddedAuthServer) {
	t.Helper()

	upstream := helpers.NewMockUpstreamIDP(t)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	issuer := "http://" + listener.Addr().String()

	secretFile := t.TempDir() + "/delegate-client-secret"
	require.NoError(t, os.WriteFile(secretFile, []byte(delegateClientSecret), 0o600))

	cfg := helpers.NewTestAuthServerConfig(t, upstream.URL(), helpers.WithScopesSupported([]string{"openid", "profile", ungrantedScope}))
	cfg.Issuer = issuer
	cfg.Upstreams[0].OAuth2Config.RedirectURI = issuer + "/oauth/callback"
	cfg.AllowedAudiences = []string{delegateAudience, ungrantedAudience}
	cfg.InsecureAllowConfidentialOverLoopbackHTTP = true
	cfg.DelegateClients = []authserver.DelegateClientRunConfig{{
		ClientID:         delegateClientID,
		ClientSecretFile: secretFile,
		Scopes:           []string{"openid", "profile"},
		Audiences:        []string{delegateAudience},
	}}

	embedded, err := authserverrunner.NewEmbeddedAuthServer(t.Context(), cfg)
	require.NoError(t, err)

	server := httptest.NewUnstartedServer(embedded.Handler())
	server.Listener = listener
	server.Start()
	t.Cleanup(func() {
		server.Close()
		require.NoError(t, embedded.Close())
	})

	return server, issuer, embedded
}

func signedDelegateSubjectToken(
	t *testing.T,
	embedded *authserverrunner.EmbeddedAuthServer,
	issuer string,
) string {
	t.Helper()
	return signedSubjectTokenForClient(t, embedded, issuer, delegateClientID)
}

// signedSubjectTokenForClient mints a self-issued subject token whose
// "client_id" claim is the given client — used to exercise the configured
// delegate-client relaxation, which requires a subject token originally
// obtained by a DIFFERENT client than the one performing the exchange.
func signedSubjectTokenForClient(
	t *testing.T,
	embedded *authserverrunner.EmbeddedAuthServer,
	issuer, clientID string,
) string {
	t.Helper()

	signingKey, err := embedded.KeyProvider().SigningKey(context.Background())
	require.NoError(t, err)
	signer, err := jose.NewSigner(
		jose.SigningKey{Algorithm: jose.SignatureAlgorithm(signingKey.Algorithm), Key: signingKey.Key},
		(&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", signingKey.KeyID),
	)
	require.NoError(t, err)

	now := time.Now()
	token, err := jwt.Signed(signer).Claims(jwt.Claims{
		Issuer:   issuer,
		Subject:  delegatedSubject,
		Audience: jwt.Audience{delegateAudience},
		Expiry:   jwt.NewNumericDate(now.Add(30 * time.Minute)),
		IssuedAt: jwt.NewNumericDate(now),
	}).Claims(map[string]any{
		"client_id": clientID,
		"scope":     "openid profile",
	}).Serialize()
	require.NoError(t, err)
	return token
}

// signedActorToken mints a self-issued RFC 8693 actor_token with the given
// "client_id" and "sub" claims. resolveActorIdentity requires "client_id" to
// equal the authenticated client's ID (the client-ID binding check) or the
// exchange is rejected before delegation consent is ever consulted; "sub" is
// the asserted actor identity, which flows into the
// delegated token's act.sub and may legitimately differ from "client_id" —
// see TestConfiguredDelegateClientTokenExchange_WithActorToken.
func signedActorToken(
	t *testing.T,
	embedded *authserverrunner.EmbeddedAuthServer,
	issuer, tokenClientID, sub string,
) string {
	t.Helper()

	signingKey, err := embedded.KeyProvider().SigningKey(context.Background())
	require.NoError(t, err)
	signer, err := jose.NewSigner(
		jose.SigningKey{Algorithm: jose.SignatureAlgorithm(signingKey.Algorithm), Key: signingKey.Key},
		(&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", signingKey.KeyID),
	)
	require.NoError(t, err)

	now := time.Now()
	token, err := jwt.Signed(signer).Claims(jwt.Claims{
		Issuer:   issuer,
		Subject:  sub,
		Audience: jwt.Audience{delegateAudience},
		Expiry:   jwt.NewNumericDate(now.Add(time.Hour)),
		IssuedAt: jwt.NewNumericDate(now),
	}).Claims(map[string]any{
		"client_id": tokenClientID,
	}).Serialize()
	require.NoError(t, err)
	return token
}

func verifyDelegatedToken(t *testing.T, embedded *authserverrunner.EmbeddedAuthServer, token, issuer string) {
	t.Helper()

	signingKey, err := embedded.KeyProvider().SigningKey(context.Background())
	require.NoError(t, err)
	parsed, err := jwt.ParseSigned(token, []jose.SignatureAlgorithm{jose.SignatureAlgorithm(signingKey.Algorithm)})
	require.NoError(t, err)

	var claims map[string]any
	require.NoError(t, parsed.Claims(signingKey.Key.Public(), &claims))
	assert.Equal(t, delegatedSubject, claims["sub"])
	assert.Equal(t, issuer, claims["iss"])
	assert.Equal(t, delegateClientID, claims["client_id"])
	assert.Equal(t, []any{delegateAudience}, claims["aud"])
	assert.Equal(t, []any{"openid", "profile"}, claims["scp"])
	act, ok := claims["act"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, delegateClientID, act["sub"])
}

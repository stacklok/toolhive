// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package authserver_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	josev3 "github.com/go-jose/go-jose/v3"
	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/authserver"
	authserverrunner "github.com/stacklok/toolhive/pkg/authserver/runner"
	servercrypto "github.com/stacklok/toolhive/pkg/authserver/server/crypto"
	"github.com/stacklok/toolhive/pkg/authserver/server/tokenexchange"
	"github.com/stacklok/toolhive/pkg/authserver/storage"
	"github.com/stacklok/toolhive/pkg/oauthproto"
	"github.com/stacklok/toolhive/test/integration/authserver/helpers"
)

const (
	cfgAudienceA     = delegateAudience
	cfgRedirectURI   = "https://broker.example.com/callback"
	cfgIDJAGIssuer   = "https://idp.example.com"
	cfgSubject       = "testuser@example.com"
	cfgClientSecret  = "configured-client-secret-well-above-minimum-length"
	cfgBrokerID      = "configured-broker"
	cfgNoCodeID      = "configured-no-code"
	cfgExchangeOnly  = "configured-exchange-only"
	cfgLegacyID      = "legacy-delegate"
	cfgAssertionType = "urn:ietf:params:oauth:client-assertion-type:jwt-bearer"
)

// pinSigningKeys makes the server keep the same signing key and HMAC secret
// across restarts, which the default ephemeral ones do not.
func pinSigningKeys(t *testing.T) func(cfg *authserver.RunConfig) {
	t.Helper()
	keyDir := t.TempDir()
	signingKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	encoded, err := x509.MarshalECPrivateKey(signingKey)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(keyDir, "signing-key.pem"),
		pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: encoded}), 0o600))
	hmacFile := writeSecretFile(t, "stable-hmac-secret-value-longer-than-32-bytes")
	return func(cfg *authserver.RunConfig) {
		cfg.SigningKeyConfig = &authserver.SigningKeyRunConfig{KeyDir: keyDir, SigningKeyFile: "signing-key.pem"}
		cfg.HMACSecretFiles = []string{hmacFile}
	}
}

type assertionKey struct {
	private *ecdsa.PrivateKey
	keyID   string
}

func newAssertionKey(t *testing.T, keyID string) assertionKey {
	t.Helper()
	private, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	return assertionKey{private: private, keyID: keyID}
}

// writeJWKS writes the public half of keys to path as a JWKS document.
func writeJWKS(t *testing.T, path string, keys ...assertionKey) {
	t.Helper()
	set := josev3.JSONWebKeySet{}
	for _, k := range keys {
		set.Keys = append(set.Keys, josev3.JSONWebKey{
			Key: &k.private.PublicKey, KeyID: k.keyID, Use: "sig", Algorithm: "ES256",
		})
	}
	data, err := json.Marshal(set)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, data, 0o600))
}

func writeSecretFile(t *testing.T, secret string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "secret")
	require.NoError(t, os.WriteFile(path, []byte(secret), 0o600))
	return path
}

// clientAuth authenticates a token request with a secret (HTTP Basic) or a
// private_key_jwt assertion.
type clientAuth struct {
	clientID string
	secret   string
	key      *assertionKey
	// emptyBasic sends HTTP Basic with an empty secret.
	emptyBasic bool
}

type tokenResult struct {
	status int
	body   map[string]any
}

func (r tokenResult) str(t *testing.T, key string) string {
	t.Helper()
	value, ok := r.body[key].(string)
	require.True(t, ok, "%s missing in %v", key, r.body)
	return value
}

func postToken(t *testing.T, serverURL string, auth clientAuth, values url.Values) tokenResult {
	t.Helper()
	form := url.Values{}
	for k, v := range values {
		form[k] = append([]string(nil), v...)
	}
	if auth.key != nil {
		signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.ES256, Key: auth.key.private},
			(&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", auth.key.keyID))
		require.NoError(t, err)
		now := time.Now()
		assertion, err := jwt.Signed(signer).Claims(jwt.Claims{
			Issuer: auth.clientID, Subject: auth.clientID, Audience: jwt.Audience{serverURL},
			ID: fmt.Sprintf("jti-%d", time.Now().UnixNano()), IssuedAt: jwt.NewNumericDate(now),
			Expiry: jwt.NewNumericDate(now.Add(time.Minute)),
		}).Serialize()
		require.NoError(t, err)
		form.Set("client_id", auth.clientID)
		form.Set("client_assertion_type", cfgAssertionType)
		form.Set("client_assertion", assertion)
	}
	request, err := http.NewRequest(http.MethodPost, serverURL+"/oauth/token", strings.NewReader(form.Encode()))
	require.NoError(t, err)
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if auth.secret != "" || auth.emptyBasic {
		request.SetBasicAuth(auth.clientID, auth.secret)
	}
	response, err := (&http.Client{Timeout: 10 * time.Second}).Do(request)
	require.NoError(t, err)
	defer func() {
		_, _ = io.Copy(io.Discard, response.Body)
		require.NoError(t, response.Body.Close())
	}()
	var body map[string]any
	require.NoError(t, json.NewDecoder(response.Body).Decode(&body))
	return tokenResult{status: response.StatusCode, body: body}
}

// authorizeProbe sends an authorization request without following redirects and
// returns the status and the "error" the server reports, if any.
func authorizeProbe(t *testing.T, serverURL, clientID string) (int, string) {
	t.Helper()
	query := url.Values{
		"client_id": {clientID}, "redirect_uri": {cfgRedirectURI}, "response_type": {"code"},
		"scope": {"openid profile"}, "state": {"probe-state"}, "resource": {cfgAudienceA},
		"code_challenge":        {servercrypto.ComputePKCEChallenge(servercrypto.GeneratePKCEVerifier())},
		"code_challenge_method": {"S256"},
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
	if response.StatusCode >= http.StatusMultipleChoices && response.StatusCode < http.StatusBadRequest {
		location, err := response.Location()
		require.NoError(t, err)
		return response.StatusCode, location.Query().Get("error")
	}
	var body map[string]any
	if json.NewDecoder(response.Body).Decode(&body) == nil {
		code, _ := body["error"].(string)
		return response.StatusCode, code
	}
	return response.StatusCode, ""
}

func newRedisStorage(mr *miniredis.Miniredis) storage.Storage {
	return storage.NewRedisStorageWithClient(redis.NewClient(&redis.Options{Addr: mr.Addr()}), "cfgclient:{test}:")
}

func newStorageForBackend(t *testing.T, backend string) storage.Storage {
	t.Helper()
	if backend == "redis" {
		return newRedisStorage(miniredis.RunT(t))
	}
	return storage.NewMemoryStorage()
}

// configuredClients configures the scopes, the one allowed audience and the
// given delegate clients.
func configuredClients(clients ...authserver.DelegateClientRunConfig) func(*authserver.RunConfig) {
	return func(cfg *authserver.RunConfig) {
		cfg.ScopesSupported = []string{"openid", "profile", "offline_access"}
		cfg.AllowedAudiences = []string{cfgAudienceA}
		cfg.InboundGrants = &authserver.InboundGrantsRunConfig{
			TokenExchange: &authserver.TokenExchangeInboundGrantRunConfig{DelegateClients: clients},
		}
	}
}

func brokerClient(jwksFile string) authserver.DelegateClientRunConfig {
	return authserver.DelegateClientRunConfig{
		ClientID:                    cfgBrokerID,
		TokenEndpointAuthMethod:     oauthproto.TokenEndpointAuthMethodPrivateKeyJWT,
		JWKSFile:                    jwksFile,
		TokenEndpointAuthSigningAlg: "ES256",
		GrantTypes: []string{
			oauthproto.GrantTypeAuthorizationCode, oauthproto.GrantTypeRefreshToken, oauthproto.GrantTypeTokenExchange,
		},
		RedirectURIs:  []string{cfgRedirectURI},
		Scopes:        []string{"openid", "profile", "offline_access"},
		Audiences:     []string{cfgAudienceA},
		TokenExchange: anySelfIssuedSubjectTokens(),
	}
}

// secretClient builds a client-secret delegate client. A client with grants
// must declare its trust mode, so it gets an explicit any, which keeps the
// blanket trust these tests were written against.
func secretClient(t *testing.T, id string, grants []string) authserver.DelegateClientRunConfig {
	t.Helper()
	client := authserver.DelegateClientRunConfig{
		ClientID: id, ClientSecretFile: writeSecretFile(t, cfgClientSecret), GrantTypes: grants,
		Scopes: []string{"openid", "profile"}, Audiences: []string{cfgAudienceA},
	}
	if len(grants) > 0 {
		client.TokenExchange = anySelfIssuedSubjectTokens()
	}
	return client
}

func anySelfIssuedSubjectTokens() *authserver.DelegateClientTokenExchange {
	return &authserver.DelegateClientTokenExchange{
		SelfIssuedSubjectTokens: authserver.SelfIssuedSubjectTokensAny,
	}
}

// login logs the configured broker client in.
func login(t *testing.T, serverURL string) (string, string) {
	t.Helper()
	return loginWith(t, serverURL, cfgBrokerID, cfgRedirectURI, cfgAudienceA, "openid profile offline_access")
}

// loginWith drives authorize, consent and the upstream callback for the given
// client, and returns the authorization code with the PKCE verifier that
// redeems it. The mock upstream is not visited: its code is fixed, so the
// callback is driven directly.
func loginWith(t *testing.T, serverURL, clientID, redirectURI, resource, scope string) (string, string) {
	t.Helper()
	locationOf := func(response *http.Response) *url.URL {
		t.Helper()
		defer func() { require.NoError(t, response.Body.Close()) }()
		require.Equal(t, http.StatusSeeOther, response.StatusCode)
		location, err := response.Location()
		require.NoError(t, err)
		return location
	}

	browser := helpers.NewOAuthClient(serverURL)
	verifier := servercrypto.GeneratePKCEVerifier()
	page, err := browser.StartAuthorization(url.Values{
		"client_id": {clientID}, "redirect_uri": {redirectURI}, "response_type": {"code"},
		"scope": {scope}, "state": {"login-state"}, "resource": {resource},
		"code_challenge":        {servercrypto.ComputePKCEChallenge(verifier)},
		"code_challenge_method": {"S256"},
	})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, page.StatusCode)
	approved, err := browser.ApproveConsent(page, serverURL)
	require.NoError(t, err)
	require.NoError(t, page.Body.Close())
	upstreamState := locationOf(approved).Query().Get("state")

	callback, err := browser.Callback("mock-auth-code", upstreamState)
	require.NoError(t, err)
	done := locationOf(callback)
	require.Equal(t, "login-state", done.Query().Get("state"))
	code := done.Query().Get("code")
	require.NotEmpty(t, code)
	return code, verifier
}

func TestConfiguredClientsThroughEmbeddedAuthServer(t *testing.T) {
	t.Parallel()

	for _, backend := range []string{"memory", "redis"} {
		t.Run(backend, func(t *testing.T) {
			t.Parallel()
			runConfiguredClientFlows(t, backend)
		})
	}
}

func runConfiguredClientFlows(t *testing.T, backend string) {
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

	clients := configuredClients(
		brokerClient(jwksFile),
		secretClient(t, cfgNoCodeID, []string{oauthproto.GrantTypeTokenExchange, oauthproto.GrantTypeJWTBearer}),
		secretClient(t, cfgExchangeOnly, []string{oauthproto.GrantTypeTokenExchange}),
		secretClient(t, cfgLegacyID, nil),
	)
	srv, issuer, embedded, _ := startEmbeddedAuthServer(t, func(cfg *authserver.RunConfig, issuer string) {
		clients(cfg)
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
	}, newStorageForBackend(t, backend))

	t.Run("discovery advertises private_key_jwt", func(t *testing.T) {
		response, err := http.Get(srv.URL + "/.well-known/oauth-authorization-server")
		require.NoError(t, err)
		defer func() { require.NoError(t, response.Body.Close()) }()
		var metadata oauthproto.AuthorizationServerMetadata
		require.NoError(t, json.NewDecoder(response.Body).Decode(&metadata))
		assert.Contains(t, metadata.TokenEndpointAuthMethodsSupported, oauthproto.TokenEndpointAuthMethodPrivateKeyJWT)
		assert.Contains(t, metadata.TokenEndpointAuthSigningAlgValuesSupported, "ES256")
	})

	t.Run("authorize fails before upstream login without authorization_code", func(t *testing.T) {
		for _, clientID := range []string{cfgNoCodeID, cfgExchangeOnly} {
			status, code := authorizeProbe(t, srv.URL, clientID)
			assert.Equal(t, http.StatusUnauthorized, status, clientID)
			assert.Equal(t, "invalid_client", code, clientID)
		}
	})

	t.Run("private_key_jwt client logs in and refreshes", func(t *testing.T) {
		code, verifier := login(t, srv.URL)
		auth := clientAuth{clientID: cfgBrokerID, key: &brokerKey}
		tokens := postToken(t, srv.URL, auth, url.Values{
			"grant_type": {oauthproto.GrantTypeAuthorizationCode}, "code": {code},
			"redirect_uri": {cfgRedirectURI}, "code_verifier": {verifier},
		})
		require.Equal(t, http.StatusOK, tokens.status, tokens.body)
		assert.Equal(t, []any{cfgAudienceA}, accessTokenClaims(t, embedded, tokens.str(t, "access_token"))["aud"])

		refreshed := postToken(t, srv.URL, auth, url.Values{
			"grant_type": {oauthproto.GrantTypeRefreshToken}, "refresh_token": {tokens.str(t, "refresh_token")},
		})
		require.Equal(t, http.StatusOK, refreshed.status, refreshed.body)
		assert.Equal(t, []any{cfgAudienceA}, accessTokenClaims(t, embedded, refreshed.str(t, "access_token"))["aud"])
	})

	t.Run("ID-JAG honors explicit grants only", func(t *testing.T) {
		redeem := func(clientID, jti string) tokenResult {
			assertion := signIDJAGAssertion(t, idpKey, issuer, clientID, jti)
			return postToken(t, srv.URL, clientAuth{clientID: clientID, secret: cfgClientSecret}, url.Values{
				"grant_type": {oauthproto.GrantTypeJWTBearer}, "assertion": {assertion}, "resource": {cfgAudienceA},
			})
		}
		refused := redeem(cfgExchangeOnly, "jti-exchange-only")
		assert.Equal(t, http.StatusBadRequest, refused.status, refused.body)
		assert.Equal(t, "unauthorized_client", refused.body["error"])

		explicit := redeem(cfgNoCodeID, "jti-explicit")
		assert.Equal(t, http.StatusOK, explicit.status, explicit.body)

		legacy := redeem(cfgLegacyID, "jti-legacy")
		assert.Equal(t, http.StatusOK, legacy.status, legacy.body)
	})

	t.Run("private_key_jwt client cannot authenticate without its assertion", func(t *testing.T) {
		for name, auth := range map[string]clientAuth{
			"client_id only":          {clientID: cfgBrokerID},
			"basic with empty secret": {clientID: cfgBrokerID, emptyBasic: true},
		} {
			result := postToken(t, srv.URL, auth, url.Values{
				"grant_type":         {oauthproto.GrantTypeTokenExchange},
				"client_id":          {cfgBrokerID},
				"subject_token":      {signedSubjectTokenWithScopes(t, embedded, issuer, cfgBrokerID, "openid profile")},
				"subject_token_type": {oauthproto.TokenTypeAccessToken},
				"scope":              {"openid profile"}, "audience": {cfgAudienceA},
			})
			assert.Equal(t, http.StatusUnauthorized, result.status, "%s: %v", name, result.body)
			assert.Equal(t, "invalid_client", result.body["error"], name)
		}
	})
}

func TestConfiguredLoginClientConsentExemptStartsOnBothBackends(t *testing.T) {
	t.Parallel()

	for _, backend := range []string{"memory", "redis"} {
		t.Run(backend, func(t *testing.T) {
			t.Parallel()
			jwksFile := filepath.Join(t.TempDir(), "broker.jwks")
			writeJWKS(t, jwksFile, newAssertionKey(t, "broker-key"))

			clients := configuredClients(brokerClient(jwksFile))
			srv, _, _, _ := startEmbeddedAuthServer(t, func(cfg *authserver.RunConfig, _ string) {
				clients(cfg)
				// Consent exemption requires an HTTPS issuer.
				cfg.Issuer = "https://auth.example.com"
				cfg.Upstreams[0].OAuth2Config.RedirectURI = cfg.Issuer + "/oauth/callback"
			}, newStorageForBackend(t, backend),
				authserverrunner.WithConsentExemptClientIDs([]string{cfgBrokerID}))

			status, code := authorizeProbe(t, srv.URL, cfgBrokerID)
			assert.Contains(t, []int{http.StatusFound, http.StatusSeeOther}, status,
				"an exempt client is redirected to upstream login instead of seeing the consent page")
			assert.Empty(t, code)
		})
	}
}

// TestConfiguredPrivateKeyJWTClientKeepsIDAcrossKeyRotation covers rotating the
// JWKS file between restarts over shared storage.
func TestConfiguredPrivateKeyJWTClientKeepsIDAcrossKeyRotation(t *testing.T) {
	t.Parallel()

	pin := pinSigningKeys(t)
	mr := miniredis.RunT(t)
	oldKey, newKey := newAssertionKey(t, "old-key"), newAssertionKey(t, "new-key")
	jwksFile := filepath.Join(t.TempDir(), "broker.jwks")

	type running struct {
		url      string
		issuer   string
		embedded *authserverrunner.EmbeddedAuthServer
		stop     func()
	}
	start := func() running {
		clients := configuredClients(brokerClient(jwksFile))
		srv, issuer, embedded, stop := startEmbeddedAuthServer(t, func(cfg *authserver.RunConfig, _ string) {
			clients(cfg)
			pin(cfg)
		}, newRedisStorage(mr))
		return running{url: srv.URL, issuer: issuer, embedded: embedded, stop: stop}
	}
	exchange := func(srv running, key assertionKey) tokenResult {
		return postToken(t, srv.url, clientAuth{clientID: cfgBrokerID, key: &key}, url.Values{
			"grant_type":         {oauthproto.GrantTypeTokenExchange},
			"subject_token":      {signedSubjectTokenWithScopes(t, srv.embedded, srv.issuer, cfgBrokerID, "openid profile")},
			"subject_token_type": {oauthproto.TokenTypeAccessToken},
			"scope":              {"openid profile"}, "audience": {cfgAudienceA},
		})
	}

	writeJWKS(t, jwksFile, oldKey)
	srv := start()
	require.Equal(t, http.StatusOK, exchange(srv, oldKey).status)
	srv.stop()

	writeJWKS(t, jwksFile, newKey)
	srv = start()
	assert.Equal(t, http.StatusOK, exchange(srv, newKey).status, "new key is accepted under the same client_id")
	refused := exchange(srv, oldKey)
	assert.Equal(t, http.StatusBadRequest, refused.status, refused.body)
	assert.Contains(t, refused.body["error_description"], "old-key", "the refused key is reported by its kid")
}

func accessTokenClaims(t *testing.T, embedded *authserverrunner.EmbeddedAuthServer, raw string) map[string]any {
	t.Helper()
	signingKey, err := embedded.KeyProvider().SigningKey(t.Context())
	require.NoError(t, err)
	parsed, err := jwt.ParseSigned(raw, []jose.SignatureAlgorithm{jose.SignatureAlgorithm(signingKey.Algorithm)})
	require.NoError(t, err)
	var claims map[string]any
	require.NoError(t, parsed.Claims(signingKey.Key.Public(), &claims))
	return claims
}

func signIDJAGAssertion(t *testing.T, key *rsa.PrivateKey, issuer, clientID, jti string) string {
	t.Helper()
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: key},
		(&jose.SignerOptions{}).WithType("oauth-id-jag+jwt").WithHeader("kid", "idp-key"))
	require.NoError(t, err)
	now := time.Now()
	assertion, err := jwt.Signed(signer).Claims(jwt.Claims{
		Issuer: cfgIDJAGIssuer, Subject: cfgSubject, Audience: jwt.Audience{issuer + "/oauth/token"},
		ID: jti, IssuedAt: jwt.NewNumericDate(now), Expiry: jwt.NewNumericDate(now.Add(2 * time.Minute)),
	}).Claims(map[string]any{"client_id": clientID}).Serialize()
	require.NoError(t, err)
	return assertion
}

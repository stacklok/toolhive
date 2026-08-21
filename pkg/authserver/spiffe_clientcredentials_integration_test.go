// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package authserver

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	"github.com/spiffe/go-spiffe/v2/bundle/spiffebundle"
	"github.com/spiffe/go-spiffe/v2/federation"
	"github.com/spiffe/go-spiffe/v2/spiffeid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/auth"
	servercrypto "github.com/stacklok/toolhive/pkg/authserver/server/crypto"
	spiffeauth "github.com/stacklok/toolhive/pkg/authserver/spiffe"
	"github.com/stacklok/toolhive/pkg/authserver/storage"
	"github.com/stacklok/toolhive/pkg/authserver/upstream"
	"github.com/stacklok/toolhive/pkg/authz/authorizers"
	cedarauth "github.com/stacklok/toolhive/pkg/authz/authorizers/cedar"
	"github.com/stacklok/toolhive/pkg/oauthproto"
)

const (
	spiffeClientCredentialsClientID = "spiffe-client"
	spiffeClientCredentialsID       = "spiffe://example.org/workload/client"
	spiffeClientCredentialsResource = "https://mcp.example.com"
	spiffeClientCredentialsLifetime = 7 * time.Minute
)

type spiffeClientCredentialsFixture struct {
	server     *httptest.Server
	client     *http.Client
	jwtKey     crypto.Signer
	privateKey *rsa.PrivateKey
}

// TestIntegration_SPIFFEClientCredentialsAuthenticationArms drives /oauth/token
// through the installed production SPIFFE authentication dispatcher. The X.509
// case presents an SVID over a TLS client-certificate connection; the JWT case
// sends a signed JWT-SVID assertion. Both use the same configured association.
func TestIntegration_SPIFFEClientCredentialsAuthenticationArms(t *testing.T) {
	t.Parallel()

	fixture := newSPIFFEClientCredentialsFixture(t)
	assertSPIFFEClientCredentialsDiscovery(t, fixture.client, fixture.server.URL)
	forms := []struct {
		name string
		form func(*testing.T) url.Values
	}{
		{
			name: "X509 SVID over mTLS",
			form: func(*testing.T) url.Values {
				return spiffeClientCredentialsForm()
			},
		},
		{
			name: "JWT SVID assertion",
			form: func(t *testing.T) url.Values {
				t.Helper()
				form := spiffeClientCredentialsForm()
				form.Set("client_assertion_type", spiffeauth.SPIFFEJWTAssertionType)
				form.Set("client_assertion", signedSPIFFEJWTAssertion(t, fixture.jwtKey))
				return form
			},
		},
	}

	for _, tt := range forms {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			response := postSPIFFEClientCredentials(t, fixture.client, fixture.server.URL, tt.form(t))
			defer response.Body.Close()

			tokenResponse := parseTokenResponse(t, response)
			require.Equal(t, http.StatusOK, response.StatusCode, "token request should succeed: %v", tokenResponse)
			accessToken, ok := tokenResponse["access_token"].(string)
			require.True(t, ok)

			claims := decodeSPIFFEClientCredentialsToken(t, accessToken, fixture.privateKey.Public())
			assert.Equal(t, spiffeClientCredentialsID, claims["sub"])
			assert.Equal(t, spiffeClientCredentialsClientID, claims["client_id"])
			assert.Equal(t, []any{"openid", "profile"}, claims["scp"])
			assert.Equal(t, []any{spiffeClientCredentialsResource}, claims["aud"])
			assertTokenLifetime(t, claims, spiffeClientCredentialsLifetime)
		})
	}
}

func TestIntegration_SPIFFETokenExchangeAuthenticationArms(t *testing.T) {
	t.Parallel()

	fixture := newSPIFFEClientCredentialsFixture(t)
	cedarAuthorizer, err := cedarauth.NewCedarAuthorizer(cedarauth.ConfigOptions{
		Policies:     []string{`permit(principal, action == Action::"call_tool", resource == Tool::"delegate-tool") when { context.claim_act.sub == "spiffe://example.org/workload/client" };`},
		EntitiesJSON: `[]`,
	}, "")
	require.NoError(t, err)
	forms := []struct {
		name string
		form func(*testing.T) url.Values
	}{
		{name: "X509 SVID", form: func(t *testing.T) url.Values {
			t.Helper()
			return spiffeTokenExchangeForm(t, fixture.privateKey)
		}},
		{name: "JWT SVID", form: func(t *testing.T) url.Values {
			t.Helper()
			form := spiffeTokenExchangeForm(t, fixture.privateKey)
			form.Set("client_assertion_type", spiffeauth.SPIFFEJWTAssertionType)
			form.Set("client_assertion", signedSPIFFEJWTAssertion(t, fixture.jwtKey))
			return form
		}},
	}

	for _, tt := range forms {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			response := postSPIFFEClientCredentials(t, fixture.client, fixture.server.URL, tt.form(t))
			defer response.Body.Close()
			body := parseTokenResponse(t, response)
			require.Equal(t, http.StatusOK, response.StatusCode, "%v", body)
			accessToken, ok := body["access_token"].(string)
			require.True(t, ok)
			claims := decodeSPIFFEClientCredentialsToken(t, accessToken, fixture.privateKey.Public())
			assert.Equal(t, "delegated-user", claims["sub"])
			assert.Equal(t, spiffeClientCredentialsClientID, claims["client_id"])
			assert.Equal(t, map[string]any{"iss": testIssuer, "sub": spiffeClientCredentialsID}, claims["act"])
			assert.Equal(t, []any{"openid"}, claims["scp"])
			assert.Equal(t, []any{spiffeClientCredentialsResource}, claims["aud"])
			assert.Equal(t, oauthproto.TokenTypeAccessToken, body["issued_token_type"])
			assertTokenLifetime(t, claims, 15*time.Minute)
			assert.NotContains(t, claims, "authentication_method")
			identity := &auth.Identity{PrincipalInfo: auth.PrincipalInfo{Subject: "delegated-user", Claims: claims}}
			authorized, err := cedarAuthorizer.AuthorizeWithJWTClaims(
				auth.WithIdentity(context.Background(), identity),
				authorizers.MCPFeatureTool,
				authorizers.MCPOperationCall,
				"delegate-tool",
				nil,
			)
			require.NoError(t, err)
			assert.True(t, authorized)
		})
	}

	for _, clientAuth := range []string{"Basic c3BpZmZlLWNsaWVudDphbnktc2VjcmV0", ""} {
		name := "client secret basic"
		if clientAuth == "" {
			name = "client secret post"
		}
		t.Run(name+" without SVID is rejected for reserved ID", func(t *testing.T) {
			form := spiffeTokenExchangeForm(t, fixture.privateKey)
			if clientAuth == "" {
				form.Set("client_secret", "any-secret")
			}
			request, err := http.NewRequest(http.MethodPost, fixture.server.URL+"/oauth/token", strings.NewReader(form.Encode()))
			require.NoError(t, err)
			request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			if clientAuth != "" {
				request.Header.Set("Authorization", clientAuth)
			}
			response, err := fixture.client.Do(request)
			require.NoError(t, err)
			defer response.Body.Close()
			body := parseTokenResponse(t, response)
			assert.Equal(t, http.StatusUnauthorized, response.StatusCode)
			assert.Equal(t, "invalid_client", body["error"])
		})
	}
}

func spiffeTokenExchangeForm(t *testing.T, key crypto.Signer) url.Values {
	t.Helper()
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: key}, nil)
	require.NoError(t, err)
	subjectToken, err := jwt.Signed(signer).Claims(jwt.Claims{Issuer: testIssuer, Subject: "delegated-user", Audience: jwt.Audience{spiffeClientCredentialsResource}, Expiry: jwt.NewNumericDate(time.Now().Add(time.Hour)), IssuedAt: jwt.NewNumericDate(time.Now())}).Claims(map[string]any{"client_id": spiffeClientCredentialsClientID, "scope": "openid"}).Serialize()
	require.NoError(t, err)
	return url.Values{"grant_type": {oauthproto.GrantTypeTokenExchange}, "client_id": {spiffeClientCredentialsClientID}, "subject_token": {subjectToken}, "subject_token_type": {oauthproto.TokenTypeAccessToken}, "resource": {spiffeClientCredentialsResource}, "scope": {"openid"}}
}

func TestNewRejectsDelegateClientIDReservedBySPIFFEAssociation(t *testing.T) {
	t.Parallel()

	trust := newSPIFFEClientCredentialsTrust(t)
	registry, err := NewSPIFFEBundleRegistry(trust)
	require.NoError(t, err)
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	secret := make([]byte, 32)
	_, err = rand.Read(secret)
	require.NoError(t, err)
	config := Config{
		Issuer: testIssuer, KeyProvider: &testKeyProvider{key: privateKey}, HMACSecrets: servercrypto.NewHMACSecrets(secret),
		AccessTokenLifespan: time.Hour, RefreshTokenLifespan: time.Hour, AuthCodeLifespan: time.Minute,
		ScopesSupported: []string{"openid", "profile"}, AllowedAudiences: []string{spiffeClientCredentialsResource},
		Upstreams:   []UpstreamConfig{{Name: "default", Type: UpstreamProviderTypeOAuth2, OAuth2Config: &upstream.OAuth2Config{CommonOAuthConfig: upstream.CommonOAuthConfig{ClientID: "upstream-client", RedirectURI: "https://example.com/callback"}, AuthorizationEndpoint: "https://idp.example.com/authorize", TokenEndpoint: "https://idp.example.com/token"}}},
		SPIFFETrust: trust, SPIFFEBundleRegistry: registry, InsecureAllowConfidentialOverLoopbackHTTP: true,
		DelegateClients: []DelegateClient{{
			ClientID: spiffeClientCredentialsClientID, ClientSecret: strings.Repeat("x", 32),
			Scopes: []string{"openid"}, Audiences: []string{spiffeClientCredentialsResource},
		}},
	}

	server, err := New(context.Background(), config, storage.NewMemoryStorage())
	require.Error(t, err)
	assert.Nil(t, server)
	assert.ErrorIs(t, err, storage.ErrAlreadyExists)
	assert.Contains(t, err.Error(), "reserved for a static SPIFFE client")
}

// TestIntegration_SPIFFEClientCredentialsRejectsInvalidJWTAssertion proves the
// token endpoint rejects an actual malformed SPIFFE credential before issuing a
// client-credentials token.
func TestIntegration_SPIFFEClientCredentialsRejectsInvalidJWTAssertion(t *testing.T) {
	t.Parallel()

	fixture := newSPIFFEClientCredentialsFixture(t)
	form := spiffeClientCredentialsForm()
	form.Set("client_assertion_type", spiffeauth.SPIFFEJWTAssertionType)
	form.Set("client_assertion", "not-a-jwt")

	response := postSPIFFEClientCredentials(t, fixture.client, fixture.server.URL, form)
	defer response.Body.Close()

	var body map[string]any
	require.NoError(t, json.NewDecoder(response.Body).Decode(&body))
	assert.Equal(t, http.StatusUnauthorized, response.StatusCode)
	assert.Equal(t, "invalid_client", body["error"])
}

func assertSPIFFEClientCredentialsDiscovery(t *testing.T, client *http.Client, serverURL string) {
	t.Helper()

	wantMethods := []string{
		oauthproto.TokenEndpointAuthMethodNone,
		oauthproto.TokenEndpointAuthMethodSPIFFEX509,
		oauthproto.TokenEndpointAuthMethodSPIFFEJWT,
	}
	wantGrants := []string{
		oauthproto.GrantTypeAuthorizationCode,
		oauthproto.GrantTypeRefreshToken,
		oauthproto.GrantTypeTokenExchange,
		oauthproto.GrantTypeClientCredentials,
	}
	for _, path := range []string{oauthproto.WellKnownOAuthServerPath, oauthproto.WellKnownOIDCPath} {
		response, err := client.Get(serverURL + path)
		require.NoError(t, err)
		defer response.Body.Close()
		require.Equal(t, http.StatusOK, response.StatusCode)

		var metadata struct {
			TokenEndpointAuthMethodsSupported []string `json:"token_endpoint_auth_methods_supported"`
			GrantTypesSupported               []string `json:"grant_types_supported"`
		}
		require.NoError(t, json.NewDecoder(response.Body).Decode(&metadata))
		assert.Equal(t, wantMethods, metadata.TokenEndpointAuthMethodsSupported, path)
		assert.Equal(t, wantGrants, metadata.GrantTypesSupported, path)
	}
}

func newSPIFFEClientCredentialsFixture(t *testing.T) *spiffeClientCredentialsFixture {
	t.Helper()

	id := spiffeid.RequireFromString(spiffeClientCredentialsID)
	ca, _, clientCertificate, clientKey, serverCertificate := newSPIFFEX509SVID(t, id)
	jwtKey := newSPIFFEJWTSigningKey(t)
	bundle := spiffebundle.New(id.TrustDomain())
	bundle.AddX509Authority(ca)
	require.NoError(t, bundle.AddJWTAuthority("test-jwt-key", jwtKey.Public()))

	bundleServer := newSPIFFEBundleEndpoint(t, id.TrustDomain(), bundle)
	trust := newSPIFFEClientCredentialsTrust(t)
	registry, err := NewSPIFFEBundleRegistry(trust)
	require.NoError(t, err)
	endpointURL, err := url.Parse(bundleServer.URL)
	require.NoError(t, err)
	registry.endpointSources[0].endpoint = *endpointURL
	registry.endpointSources[0].client = bundleServer.Client()

	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	secret := make([]byte, 32)
	_, err = rand.Read(secret)
	require.NoError(t, err)
	store := storage.NewMemoryStorage()
	config := Config{
		Issuer:               testIssuer,
		KeyProvider:          &testKeyProvider{key: privateKey},
		HMACSecrets:          servercrypto.NewHMACSecrets(secret),
		AccessTokenLifespan:  spiffeClientCredentialsLifetime,
		RefreshTokenLifespan: time.Hour,
		AuthCodeLifespan:     time.Minute,
		ScopesSupported:      []string{"openid", "profile"},
		AllowedAudiences:     []string{spiffeClientCredentialsResource},
		Upstreams: []UpstreamConfig{{
			Name: "default", Type: UpstreamProviderTypeOAuth2,
			OAuth2Config: &upstream.OAuth2Config{CommonOAuthConfig: upstream.CommonOAuthConfig{
				ClientID: "upstream-client", RedirectURI: "https://example.com/callback",
			}, AuthorizationEndpoint: "https://idp.example.com/authorize", TokenEndpoint: "https://idp.example.com/token"},
		}},
		SPIFFETrust:          trust,
		SPIFFEBundleRegistry: registry,
	}
	authServer, err := New(context.Background(), config, store)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, authServer.Close()) })

	server := httptest.NewUnstartedServer(spiffeauth.Middleware(authServer.Handler()))
	server.TLS = &tls.Config{
		Certificates: []tls.Certificate{serverCertificate},
		ClientAuth:   tls.RequestClientCert,
		MinVersion:   tls.VersionTLS12,
	}
	server.StartTLS()
	t.Cleanup(server.Close)

	clientTLS := server.Client().Transport.(*http.Transport).TLSClientConfig.Clone()
	roots := x509.NewCertPool()
	roots.AddCert(ca)
	clientTLS.RootCAs = roots
	clientTLS.Certificates = []tls.Certificate{{Certificate: [][]byte{clientCertificate.Raw}, PrivateKey: clientKey}}
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: clientTLS}, Timeout: 10 * time.Second}
	t.Cleanup(client.CloseIdleConnections)

	return &spiffeClientCredentialsFixture{server: server, client: client, jwtKey: jwtKey, privateKey: privateKey}
}

func newSPIFFEClientCredentialsTrust(t *testing.T) *SPIFFETrustConfig {
	t.Helper()

	trust, err := NewSPIFFETrustConfig(
		[]SPIFFETrustDomainRunConfig{{
			Name: "example", TrustDomain: "example.org",
			Methods:      []SPIFFEAuthenticationMethod{SPIFFEAuthenticationMethodX509, SPIFFEAuthenticationMethodJWT},
			BundleSource: SPIFFEBundleSourceRunConfig{Type: SPIFFEBundleSourceTypeEndpoint, Endpoint: &SPIFFEBundleEndpointSourceRunConfig{URL: "https://bundles.example.org"}},
		}},
		&InboundGrantsRunConfig{SPIFFEClientAuth: []SPIFFEClientAuthRunConfig{{
			TrustDomainRef: "example", Principal: spiffeClientCredentialsID, ClientID: spiffeClientCredentialsClientID,
			Methods:   []SPIFFEAuthenticationMethod{SPIFFEAuthenticationMethodX509, SPIFFEAuthenticationMethodJWT},
			Resources: []string{spiffeClientCredentialsResource}, Audiences: []string{"https://token-audience.example.com"},
			Scopes: []string{"openid", "profile"}, GrantTypes: []string{oauthproto.GrantTypeClientCredentials, oauthproto.GrantTypeTokenExchange},
			TokenExchange: &SPIFFETokenExchangeRunConfig{Enabled: true},
		}}},
		[]string{"openid", "profile"}, []string{spiffeClientCredentialsResource, "https://token-audience.example.com"},
	)
	require.NoError(t, err)
	return trust
}

func newSPIFFEBundleEndpoint(t *testing.T, trustDomain spiffeid.TrustDomain, bundle *spiffebundle.Bundle) *httptest.Server {
	t.Helper()

	handler, err := federation.NewHandler(trustDomain, &testSPIFFEBundleSource{bundle: bundle})
	require.NoError(t, err)
	server := httptest.NewTLSServer(handler)
	t.Cleanup(server.Close)
	return server
}

func newSPIFFEX509SVID(t *testing.T, id spiffeid.ID) (*x509.Certificate, *rsa.PrivateKey, *x509.Certificate, *rsa.PrivateKey, tls.Certificate) {
	t.Helper()

	caKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	clientKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	now := time.Now()
	caTemplate := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "SPIFFE test CA"}, NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, caKey.Public(), caKey)
	require.NoError(t, err)
	ca, err := x509.ParseCertificate(caDER)
	require.NoError(t, err)
	clientDER, err := x509.CreateCertificate(rand.Reader, &x509.Certificate{SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "SPIFFE workload"}, NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}, URIs: []*url.URL{id.URL()}}, ca, clientKey.Public(), caKey)
	require.NoError(t, err)
	clientCertificate, err := x509.ParseCertificate(clientDER)
	require.NoError(t, err)
	serverKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	serverDER, err := x509.CreateCertificate(rand.Reader, &x509.Certificate{SerialNumber: big.NewInt(3), Subject: pkix.Name{CommonName: "test server"}, NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}}, ca, serverKey.Public(), caKey)
	require.NoError(t, err)
	return ca, caKey, clientCertificate, clientKey, tls.Certificate{Certificate: [][]byte{serverDER, ca.Raw}, PrivateKey: serverKey}
}

// newSPIFFEJWTSigningKey returns an ECDSA P-256 key. The algorithm is not
// arbitrary: jwtsvid.ParseAndValidate accepts only RS*/ES*/PS* per the SPIFFE
// JWT-SVID specification, so an Ed25519 key would be rejected before any claim
// is examined and the arm would fail with an opaque invalid_client.
func newSPIFFEJWTSigningKey(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	return key
}

func spiffeClientCredentialsForm() url.Values {
	return url.Values{"grant_type": {oauthproto.GrantTypeClientCredentials}, "client_id": {spiffeClientCredentialsClientID}, "resource": {spiffeClientCredentialsResource}, "scope": {"openid profile"}}
}

func signedSPIFFEJWTAssertion(t *testing.T, key crypto.Signer) string {
	t.Helper()

	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.ES256, Key: jose.JSONWebKey{Key: key, KeyID: "test-jwt-key"}}, (&jose.SignerOptions{}).WithType("JWT"))
	require.NoError(t, err)
	assertion, err := jwt.Signed(signer).Claims(jwt.Claims{Issuer: "example.org", Subject: spiffeClientCredentialsID, Audience: []string{testIssuer}, Expiry: jwt.NewNumericDate(time.Now().Add(time.Hour)), NotBefore: jwt.NewNumericDate(time.Now().Add(-time.Minute)), IssuedAt: jwt.NewNumericDate(time.Now())}).Serialize()
	require.NoError(t, err)
	return assertion
}

func postSPIFFEClientCredentials(t *testing.T, client *http.Client, serverURL string, form url.Values) *http.Response {
	t.Helper()

	request, err := http.NewRequest(http.MethodPost, serverURL+"/oauth/token", strings.NewReader(form.Encode()))
	require.NoError(t, err)
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := client.Do(request)
	require.NoError(t, err)
	return response
}

func decodeSPIFFEClientCredentialsToken(t *testing.T, token string, publicKey crypto.PublicKey) map[string]any {
	t.Helper()

	parsed, err := jwt.ParseSigned(token, []jose.SignatureAlgorithm{jose.RS256})
	require.NoError(t, err)
	claims := make(map[string]any)
	require.NoError(t, parsed.Claims(publicKey, &claims))
	return claims
}

func assertTokenLifetime(t *testing.T, claims map[string]any, lifetime time.Duration) {
	t.Helper()

	issuedAt, ok := claims["iat"].(float64)
	require.True(t, ok)
	expiresAt, ok := claims["exp"].(float64)
	require.True(t, ok)
	assert.InDelta(t, lifetime.Seconds(), expiresAt-issuedAt, 2)
}

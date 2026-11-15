// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package authserver

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/authserver/claimnames"
	servercrypto "github.com/stacklok/toolhive/pkg/authserver/server/crypto"
	spiffeauth "github.com/stacklok/toolhive/pkg/authserver/spiffe"
	"github.com/stacklok/toolhive/pkg/authserver/storage"
	"github.com/stacklok/toolhive/pkg/oauthproto"
)

const (
	ccTestResource = "https://resource.example.com"
	ccTestAudience = "https://audience.example.com"
	ccJWTSPIFFEID  = "spiffe://example.org/ns/default/jwt-agent"
	ccX509SPIFFEID = "spiffe://example.org/ns/default/x509-agent"
	ccJWTClientID  = "spiffe-jwt-client"
	ccX509ClientID = "spiffe-x509-client"
	ccDelegateID   = "delegate-client"
	ccDelegateSec  = "delegate-secret-value-0123456789abcdef"
)

// ccFixture is an auth server built through newServer, so the SPIFFE storage
// decorator, association registry, client resolver and bundle source are the
// production ones.
type ccFixture struct {
	handler http.Handler
	// tokenKey verifies access tokens; jwtKey signs JWT-SVIDs; leaf is an
	// X.509-SVID for ccX509SPIFFEID.
	tokenKey *rsa.PrivateKey
	jwtKey   *ecdsa.PrivateKey
	leaf     *x509.Certificate
}

func newCCFixture(t *testing.T) *ccFixture {
	t.Helper()

	jwtKey := mustCCECKey(t)
	caKey := mustCCECKey(t)
	now := time.Now()
	caTemplate := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "test CA"},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	require.NoError(t, err)
	ca, err := x509.ParseCertificate(caDER)
	require.NoError(t, err)
	spiffeURL, err := url.Parse(ccX509SPIFFEID)
	require.NoError(t, err)
	leafKey := mustCCECKey(t)
	leafDER, err := x509.CreateCertificate(rand.Reader, &x509.Certificate{
		SerialNumber:          big.NewInt(2),
		Subject:               pkix.Name{CommonName: "test workload"},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		URIs:                  []*url.URL{spiffeURL},
	}, ca, &leafKey.PublicKey, caKey)
	require.NoError(t, err)
	leaf, err := x509.ParseCertificate(leafDER)
	require.NoError(t, err)

	bundleJSON, err := json.Marshal(map[string]any{
		"spiffe_sequence": 1,
		"keys": []jose.JSONWebKey{
			{Key: &jwtKey.PublicKey, KeyID: "jwt-key", Use: "jwt-svid"},
			{Key: &caKey.PublicKey, KeyID: "x509-key", Use: "x509-svid", Certificates: []*x509.Certificate{ca}},
		},
	})
	require.NoError(t, err)
	dir, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	bundlePath := filepath.Join(dir, "bundle.json")
	require.NoError(t, os.WriteFile(bundlePath, bundleJSON, 0o600))

	association := func(clientID, principal string, method SPIFFEAuthenticationMethod) SPIFFEClientAuthRunConfig {
		return SPIFFEClientAuthRunConfig{
			TrustDomainRef:   "production",
			PrincipalPattern: principal,
			ClientID:         clientID,
			Methods:          []SPIFFEAuthenticationMethod{method},
			GrantTypes:       []string{oauthproto.GrantTypeClientCredentials},
			Scopes:           []string{"openid"},
			Resources:        []string{ccTestResource},
			Audiences:        []string{ccTestAudience},
		}
	}
	trust, err := NewSPIFFETrustConfig([]SPIFFETrustDomainRunConfig{{
		Name:        "production",
		TrustDomain: "example.org",
		Methods:     []SPIFFEAuthenticationMethod{SPIFFEAuthenticationMethodX509, SPIFFEAuthenticationMethodJWT},
		BundleSource: SPIFFEBundleSourceRunConfig{
			Type: SPIFFEBundleSourceTypeFile,
			File: &SPIFFEFileBundleSourceRunConfig{Path: bundlePath},
		},
	}}, &InboundGrantsRunConfig{SPIFFEClientAuth: []SPIFFEClientAuthRunConfig{
		association(ccJWTClientID, ccJWTSPIFFEID, SPIFFEAuthenticationMethodJWT),
		association(ccX509ClientID, ccX509SPIFFEID, SPIFFEAuthenticationMethodX509),
	}}, []string{"openid"}, []string{ccTestResource, ccTestAudience})
	require.NoError(t, err)

	secret := make([]byte, 32)
	_, err = rand.Read(secret)
	require.NoError(t, err)
	tokenKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	stor := storage.NewMemoryStorage()
	t.Cleanup(func() { _ = stor.Close() })
	srv, err := newServer(context.Background(), Config{
		Issuer:               testIssuer,
		KeyProvider:          &testKeyProvider{key: tokenKey},
		HMACSecrets:          servercrypto.NewHMACSecrets(secret),
		AccessTokenLifespan:  time.Hour,
		RefreshTokenLifespan: 24 * time.Hour,
		AuthCodeLifespan:     10 * time.Minute,
		ScopesSupported:      []string{"openid"},
		AllowedAudiences:     []string{ccTestResource, ccTestAudience},
		SPIFFETrust:          trust,
		// The issuer is a plain-HTTP loopback URL, and both the delegate client's
		// secret and the JWT-SVID association are confidential credentials.
		InsecureAllowConfidentialOverLoopbackHTTP: true,
		DelegateClients: []DelegateClient{{
			ClientID:     ccDelegateID,
			ClientSecret: ccDelegateSec,
			Scopes:       []string{"openid"},
			Audiences:    []string{ccTestAudience},
		}},
	}, stor)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, srv.Close()) })

	return &ccFixture{handler: srv.Handler(), tokenKey: tokenKey, jwtKey: jwtKey, leaf: leaf}
}

func mustCCECKey(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	return key
}

func (f *ccFixture) jwtSVID(t *testing.T, spiffeID string) string {
	t.Helper()
	signer, err := jose.NewSigner(
		jose.SigningKey{Algorithm: jose.ES256, Key: f.jwtKey},
		(&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", "jwt-key"),
	)
	require.NoError(t, err)
	token, err := jwt.Signed(signer).Claims(jwt.Claims{
		Subject:  spiffeID,
		Audience: jwt.Audience{testIssuer},
		Expiry:   jwt.NewNumericDate(time.Now().Add(time.Minute)),
	}).Serialize()
	require.NoError(t, err)
	return token
}

func (f *ccFixture) postToken(t *testing.T, form url.Values, peer *x509.Certificate) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, testIssuer+"/oauth/token", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if peer != nil {
		req.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{peer}}
	}
	rec := httptest.NewRecorder()
	f.handler.ServeHTTP(rec, req)
	return rec
}

func (f *ccFixture) accessTokenClaims(t *testing.T, raw string) map[string]any {
	t.Helper()
	parsed, err := jwt.ParseSigned(raw, []jose.SignatureAlgorithm{jose.RS256})
	require.NoError(t, err)
	var claims map[string]any
	require.NoError(t, parsed.Claims(f.tokenKey.Public(), &claims))
	return claims
}

func decodeCCBody(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body), rec.Body.String())
	return body
}

// TestSPIFFEClientCredentialsThroughNewServer drives the grant through newServer
// with the real SPIFFE storage decorator and resolver. Stored SPIFFE clients are
// wrapped by the decorator; a resolver that asserts the bare type rejects every
// SPIFFE request, which only a test on this path can catch.
func TestSPIFFEClientCredentialsThroughNewServer(t *testing.T) {
	t.Parallel()
	f := newCCFixture(t)

	tests := []struct {
		name      string
		form      func(t *testing.T) url.Values
		peer      *x509.Certificate
		wantCode  int
		wantError string
		wantSub   string
		wantCID   string
	}{
		{
			name: "JWT-SVID issues an access token for the SPIFFE ID",
			form: func(t *testing.T) url.Values {
				t.Helper()
				return url.Values{
					"grant_type":            {oauthproto.GrantTypeClientCredentials},
					"client_assertion_type": {spiffeauth.SPIFFEJWTAssertionType},
					"client_assertion":      {f.jwtSVID(t, ccJWTSPIFFEID)},
					"resource":              {ccTestResource},
					"scope":                 {"openid"},
				}
			},
			wantCode: http.StatusOK,
			wantSub:  ccJWTSPIFFEID,
			wantCID:  ccJWTClientID,
		},
		{
			name: "X.509-SVID issues an access token for the SPIFFE ID",
			form: func(*testing.T) url.Values {
				return url.Values{
					"grant_type": {oauthproto.GrantTypeClientCredentials},
					"client_id":  {ccX509ClientID},
					"resource":   {ccTestResource},
					"scope":      {"openid"},
				}
			},
			peer:     f.leaf,
			wantCode: http.StatusOK,
			wantSub:  ccX509SPIFFEID,
			wantCID:  ccX509ClientID,
		},
		{
			name: "delegate client with a secret is unauthorized_client",
			form: func(*testing.T) url.Values {
				return url.Values{
					"grant_type":    {oauthproto.GrantTypeClientCredentials},
					"client_id":     {ccDelegateID},
					"client_secret": {ccDelegateSec},
					"resource":      {ccTestAudience},
				}
			},
			wantCode:  http.StatusBadRequest,
			wantError: "unauthorized_client",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			rec := f.postToken(t, tt.form(t), tt.peer)
			require.Equal(t, tt.wantCode, rec.Code, rec.Body.String())
			body := decodeCCBody(t, rec)
			if tt.wantError != "" {
				assert.Equal(t, tt.wantError, body["error"])
				assert.NotContains(t, body, "access_token")
				return
			}

			assert.NotContains(t, body, "refresh_token")
			raw, ok := body["access_token"].(string)
			require.True(t, ok, rec.Body.String())
			claims := f.accessTokenClaims(t, raw)
			assert.Equal(t, tt.wantSub, claims["sub"])
			assert.Equal(t, tt.wantCID, claims["client_id"])
			assert.Equal(t, []any{ccTestResource}, claims["aud"], "aud is the requested resource, not the association audiences")
			assert.Equal(t, []any{"openid"}, claims["scp"])
			assert.NotContains(t, claims, "scope")
			assert.Equal(t, true, claims[claimnames.NoUpstreamSessionKey])
		})
	}
}

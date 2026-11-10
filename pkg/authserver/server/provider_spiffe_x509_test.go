// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	"github.com/ory/fosite"
	"github.com/spiffe/go-spiffe/v2/bundle/x509bundle"
	"github.com/spiffe/go-spiffe/v2/spiffeid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	servercrypto "github.com/stacklok/toolhive/pkg/authserver/server/crypto"
	"github.com/stacklok/toolhive/pkg/authserver/server/registration"
	spiffeauth "github.com/stacklok/toolhive/pkg/authserver/spiffe"
)

// TestNewAuthorizationServerSPIFFEX509ClientAuthentication pins that the
// provider built by NewAuthorizationServer authenticates X.509-SVID clients
// against config.SPIFFEX509BundleSource, not against a nil or unrelated pool.
func TestNewAuthorizationServerSPIFFEX509ClientAuthentication(t *testing.T) {
	t.Parallel()

	spiffeID := spiffeid.RequireFromString("spiffe://example.org/workload/my-service")
	withUsages := func(template *x509.Certificate) {
		template.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth}
	}
	trustedLeaf, trustedSource := newTestX509SVID(t, spiffeID, withUsages)
	// Signed by a second CA that is never placed in trustedSource.
	otherCALeaf, _ := newTestX509SVID(t, spiffeID, withUsages)
	signingKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	resolvedClient := &fosite.DefaultClient{ID: "spiffe-client"}

	tests := []struct {
		name        string
		certificate *x509.Certificate
		wantClient  fosite.Client
		wantErrIs   error
	}{
		{
			name:        "leaf signed by configured bundle resolves client",
			certificate: trustedLeaf,
			wantClient:  resolvedClient,
		},
		{
			name:        "leaf signed by CA outside configured bundle fails closed",
			certificate: otherCALeaf,
			wantErrIs:   fosite.ErrInvalidClient,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			resolverCalls := 0
			resolver := func(context.Context, string, string, spiffeauth.SPIFFEAuthenticationMethod) (fosite.Client, error) {
				resolverCalls++
				return resolvedClient, nil
			}
			provider := buildSPIFFEX509TestProvider(t, signingKey, trustedSource, &mockStorage{}, resolver)

			req := httptest.NewRequest(http.MethodPost, "/oauth/token", nil)
			req.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{tt.certificate}}

			authenticator, ok := provider.(interface {
				AuthenticateClient(context.Context, *http.Request, url.Values) (fosite.Client, error)
			})
			require.True(t, ok, "provider must expose AuthenticateClient")
			client, err := authenticator.AuthenticateClient(
				req.Context(), req, url.Values{"client_id": {"spiffe-client"}},
			)

			if tt.wantErrIs != nil {
				require.ErrorIs(t, err, tt.wantErrIs)
				assert.Nil(t, client)
				assert.Zero(t, resolverCalls, "resolver must not run for an unverified leaf")
				return
			}
			require.NoError(t, err)
			assert.Same(t, tt.wantClient, client)
			assert.Equal(t, 1, resolverCalls)
		})
	}
}

func newSPIFFEX509TestConfig(t *testing.T, signingKey *rsa.PrivateKey) *AuthorizationServerConfig {
	t.Helper()

	config, err := NewAuthorizationServerConfig(&AuthorizationServerParams{
		Issuer:               "https://auth.example.com",
		AccessTokenLifespan:  time.Hour,
		RefreshTokenLifespan: 24 * time.Hour,
		AuthCodeLifespan:     10 * time.Minute,
		HMACSecrets:          servercrypto.NewHMACSecrets([]byte("test-secret-with-32-bytes-long!!")),
		SigningKeyID:         "key-1",
		SigningKeyAlgorithm:  "RS256",
		SigningKey:           signingKey,
	})
	require.NoError(t, err)
	return config
}

// buildSPIFFEX509TestProvider constructs the provider through NewAuthorizationServer.
func buildSPIFFEX509TestProvider(
	t *testing.T,
	signingKey *rsa.PrivateKey,
	bundleSource x509bundle.Source,
	store *mockStorage,
	resolver SPIFFEClientResolver,
	opts ...func(*AuthorizationServerConfig),
) fosite.OAuth2Provider {
	t.Helper()

	config := newSPIFFEX509TestConfig(t, signingKey)
	config.SPIFFEClientResolver = resolver
	config.SPIFFEX509BundleSource = bundleSource
	config.SPIFFEX509ClientMatcher = matchClientID("spiffe-client")

	for _, opt := range opts {
		opt(config)
	}
	provider, err := NewAuthorizationServer(config, store, nil)
	require.NoError(t, err)
	return provider
}

// TestNewAuthorizationServerRequiresX509MatcherWithX509Source pins that a
// resolver and X.509 bundle source without a matcher is rejected.
func TestNewAuthorizationServerRequiresX509MatcherWithX509Source(t *testing.T) {
	t.Parallel()

	signingKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	config := newSPIFFEX509TestConfig(t, signingKey)
	config.SPIFFEClientResolver = stubResolver
	config.SPIFFEX509BundleSource = x509bundle.NewSet()

	_, err = NewAuthorizationServer(config, &mockStorage{}, nil)
	require.ErrorContains(t, err, "X.509 client matcher")

	config.SPIFFEX509ClientMatcher = matchClientID("spiffe-client")
	_, err = NewAuthorizationServer(config, &mockStorage{}, nil)
	require.NoError(t, err)
}

// TestStaticSPIFFEClientCannotAuthenticateByDefaultStrategy pins that a
// SPIFFE static client reaching the default strategy never authenticates. The
// confidential control client proves the harness can authenticate at all.
func TestStaticSPIFFEClientCannotAuthenticateByDefaultStrategy(t *testing.T) {
	t.Parallel()

	signingKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	spiffeClient, err := registration.NewSPIFFEClient(
		"spiffe-client", []string{"openid"}, []string{"https://mcp.example.com"}, nil,
	)
	require.NoError(t, err)
	hashed, err := registration.SHA256Hasher.Hash(context.Background(), []byte("control-secret"))
	require.NoError(t, err)
	control := &fosite.DefaultClient{ID: "control-client", Secret: hashed}
	store := &mockStorage{clients: map[string]fosite.Client{"spiffe-client": spiffeClient, "control-client": control}}
	// The matcher is false so every request below reaches the default strategy.
	provider := buildSPIFFEX509TestProvider(t, signingKey, x509bundle.NewSet(), store, stubResolver,
		func(c *AuthorizationServerConfig) { c.SPIFFEX509ClientMatcher = matchClientID() })
	authenticator, ok := provider.(interface {
		AuthenticateClient(context.Context, *http.Request, url.Values) (fosite.Client, error)
	})
	require.True(t, ok, "provider must expose AuthenticateClient")

	basic := func(id, secret string) string {
		return "Basic " + base64.StdEncoding.EncodeToString([]byte(id+":"+secret))
	}
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: signingKey}, nil)
	require.NoError(t, err)
	now := time.Now()
	assertion, err := jwt.Signed(signer).Claims(jwt.Claims{
		Issuer: "spiffe-client", Subject: "spiffe-client", Audience: jwt.Audience{testIssuer + "/oauth/token"},
		ID: "assertion-jti", IssuedAt: jwt.NewNumericDate(now), Expiry: jwt.NewNumericDate(now.Add(time.Minute)),
	}).Serialize()
	require.NoError(t, err)
	tests := []struct {
		name      string
		authorize string
		form      url.Values
		wantOK    bool
	}{
		{name: "empty client secret", form: url.Values{"client_id": {"spiffe-client"}, "client_secret": {""}}},
		{name: "arbitrary client secret", form: url.Values{"client_id": {"spiffe-client"}, "client_secret": {"guess"}}},
		{name: "auth method none", form: url.Values{"client_id": {"spiffe-client"}}},
		// The keyfunc rejects a non-OpenIDConnectClient with invalid_request, which
		// mapClientAssertionError remaps to invalid_client; this row does not
		// reach a signature or claims check.
		{
			name: "private_key_jwt",
			form: url.Values{
				"client_id":             {"spiffe-client"},
				"client_assertion_type": {"urn:ietf:params:oauth:client-assertion-type:jwt-bearer"},
				"client_assertion":      {assertion},
			},
		},
		{name: "Basic with empty secret", authorize: basic("spiffe-client", ""), form: url.Values{}},
		{name: "Basic with arbitrary secret", authorize: basic("spiffe-client", "guess"), form: url.Values{}},
		{
			name: "positive control: confidential client with its secret",
			form: url.Values{"client_id": {"control-client"}, "client_secret": {"control-secret"}}, wantOK: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			req := httptest.NewRequest(http.MethodPost, "/oauth/token", nil)
			if tt.authorize != "" {
				req.Header.Set("Authorization", tt.authorize)
			}
			client, err := authenticator.AuthenticateClient(req.Context(), req, tt.form)
			if tt.wantOK {
				require.NoError(t, err)
				assert.Equal(t, "control-client", client.GetID())
				return
			}
			require.ErrorIs(t, err, fosite.ErrInvalidClient)
			assert.Nil(t, client)
		})
	}
}

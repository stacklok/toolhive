// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	"github.com/ory/fosite"
	"github.com/spiffe/go-spiffe/v2/bundle/jwtbundle"
	"github.com/spiffe/go-spiffe/v2/spiffeid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	spiffeauth "github.com/stacklok/toolhive/pkg/authserver/spiffe"
)

const testIssuer = "https://auth.example.com"

// stubResolver is a SPIFFEClientResolver that is never called by these tests;
// it exists only to make "resolver configured" distinguishable from "resolver
// nil" for the client-authentication strategy under test.
func stubResolver(context.Context, string, string, spiffeauth.SPIFFEAuthenticationMethod) (fosite.Client, error) {
	panic("not called")
}

// matchClientID returns a SPIFFEX509ClientMatcher that matches only the given
// client IDs.
func matchClientID(ids ...string) SPIFFEX509ClientMatcher {
	return func(clientID string) bool { return slices.Contains(ids, clientID) }
}

func TestSPIFFEClientAuthenticationStrategy(t *testing.T) {
	t.Parallel()

	spiffeID := spiffeid.RequireFromString("spiffe://example.org/workload/my-service")
	spiffeLeaf, _ := newTestX509SVID(t, spiffeID, nil)
	nonSPIFFELeaf := &x509.Certificate{URIs: []*url.URL{{Scheme: "https", Host: "example.org", Path: "/workload"}}}
	defaultClient := &fosite.DefaultClient{ID: "default-client"}
	defaultErr := errors.New("default strategy error")
	x509Client := matchClientID("x509-client")

	tests := []struct {
		name            string
		leaf            *x509.Certificate
		emptyTLS        bool
		form            url.Values
		authorize       string
		resolver        SPIFFEClientResolver
		x509Client      SPIFFEX509ClientMatcher
		wantErr         string
		wantErrIs       error
		wantDefaultCall bool
	}{
		{
			name:       "SPIFFE JWT assertion type takes precedence over an X.509 client_id and SVID leaf",
			leaf:       spiffeLeaf,
			resolver:   stubResolver,
			x509Client: x509Client,
			form: url.Values{
				"client_assertion_type": {spiffeauth.SPIFFEJWTAssertionType},
				"client_id":             {"x509-client"},
			},
			wantErr: fosite.ErrInvalidRequest.HintField,
		},
		{
			name:            "non-SPIFFE client certificate delegates to default strategy",
			leaf:            nonSPIFFELeaf,
			resolver:        stubResolver,
			form:            url.Values{"client_id": {"client"}},
			wantDefaultCall: true,
		},
		{
			name:            "TLS connection without peer certificates delegates to default strategy",
			emptyTLS:        true,
			resolver:        stubResolver,
			form:            url.Values{"client_id": {"client"}},
			wantDefaultCall: true,
		},
		{
			name:     "SPIFFE JWT assertion is detected when not the first value, then rejected as duplicated",
			resolver: stubResolver,
			form: url.Values{
				"client_assertion_type": {
					"urn:ietf:params:oauth:client-assertion-type:jwt-bearer",
					spiffeauth.SPIFFEJWTAssertionType,
				},
			},
			wantErr: fosite.ErrInvalidRequest.HintField,
		},
		{
			name:     "RFC 7523 assertion delegates to default strategy",
			resolver: stubResolver,
			form: url.Values{
				"client_assertion_type": {"urn:ietf:params:oauth:client-assertion-type:jwt-bearer"},
			},
			wantDefaultCall: true,
		},
		{
			name:            "requests without SPIFFE credentials delegate to default strategy",
			resolver:        stubResolver,
			form:            url.Values{"client_id": {"client"}},
			wantDefaultCall: true,
		},
		{
			name: "nil resolver with SPIFFE leaf and SPIFFE JWT assertion type delegates to default strategy",
			leaf: spiffeLeaf,
			form: url.Values{
				"client_assertion_type": {spiffeauth.SPIFFEJWTAssertionType},
			},
			resolver:        nil,
			wantDefaultCall: true,
		},
		{
			name:            "nil resolver with SPIFFE leaf and an X.509 client_id delegates to default strategy",
			leaf:            spiffeLeaf,
			x509Client:      x509Client,
			form:            url.Values{"client_id": {"x509-client"}},
			wantDefaultCall: true,
		},
		{
			name:            "public client with an incidental SVID uses the default strategy",
			leaf:            spiffeLeaf,
			resolver:        stubResolver,
			x509Client:      x509Client,
			form:            url.Values{"client_id": {"public-client"}},
			wantDefaultCall: true,
		},
		{
			name:            "Basic carrying an X.509 client's ID with an SVID uses the default strategy",
			leaf:            spiffeLeaf,
			resolver:        stubResolver,
			x509Client:      x509Client,
			authorize:       "Basic eDUwOS1jbGllbnQ6",
			form:            url.Values{},
			wantDefaultCall: true,
		},
		{
			name:       "duplicated client_id with one X.509 client is rejected",
			leaf:       spiffeLeaf,
			resolver:   stubResolver,
			x509Client: x509Client,
			form:       url.Values{"client_id": {"delegate", "x509-client"}},
			wantErrIs:  fosite.ErrInvalidRequest,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			defaultCalled := false
			strategy := newSPIFFEClientAuthenticationStrategy(func(_ context.Context, _ *http.Request, _ url.Values) (fosite.Client, error) {
				defaultCalled = true
				return defaultClient, defaultErr
			}, testIssuer, nil, nil, tt.resolver, tt.x509Client)

			req := httptest.NewRequest("POST", "/oauth/token", nil)
			req.TLS = tlsStateWithLeaf(tt.leaf)
			if tt.emptyTLS {
				req.TLS = &tls.ConnectionState{}
			}
			if tt.authorize != "" {
				req.Header.Set("Authorization", tt.authorize)
			}
			client, err := strategy(context.Background(), req, tt.form)

			assert.Equal(t, tt.wantDefaultCall, defaultCalled)
			if tt.wantErrIs != nil {
				require.ErrorIs(t, err, tt.wantErrIs)
				assert.Nil(t, client)
				return
			}
			if tt.wantErr != "" {
				require.Error(t, err)
				var rfcErr *fosite.RFC6749Error
				require.ErrorAs(t, err, &rfcErr)
				assert.Equal(t, tt.wantErr, rfcErr.HintField)
				if assertion := tt.form.Get("client_assertion"); assertion != "" {
					assert.NotContains(t, rfcErr.HintField, assertion)
				}
				assert.Nil(t, client)
				return
			}

			assert.ErrorIs(t, err, defaultErr)
			assert.Same(t, defaultClient, client)
		})
	}
}

func TestSPIFFEJWTClientAuthentication(t *testing.T) {
	t.Parallel()

	id := spiffeid.RequireFromString("spiffe://example.org/workload/my-service")
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)

	validToken := signedJWT(t, jose.RS256, key, "key-1", "JWT", standardClaims(id, []string{testIssuer}))
	source := jwtbundle.NewSet(jwtbundle.FromJWTAuthorities(id.TrustDomain(), map[string]crypto.PublicKey{"key-1": key.Public()}))
	client := &fosite.DefaultClient{ID: "client"}
	incidentalLeaf, _ := newTestX509SVID(t, id, nil)

	tests := []struct {
		name           string
		form           url.Values
		authorize      string
		leaf           *x509.Certificate
		source         jwtbundle.Source
		resolverErr    error
		resolverClient fosite.Client
		wantErr        error
		wantCall       bool
	}{
		{name: "valid JWT-SVID without jti", form: jwtForm(validToken), source: source, wantCall: true},
		{name: "valid JWT-SVID with an incidental SPIFFE leaf", form: jwtForm(validToken), leaf: incidentalLeaf, source: source, wantCall: true},
		{name: "valid JWT-SVID with omitted client ID", form: url.Values{"client_assertion_type": {spiffeauth.SPIFFEJWTAssertionType}, "client_assertion": {validToken}}, source: source, wantCall: true},
		{name: "missing assertion", form: url.Values{"client_assertion_type": {spiffeauth.SPIFFEJWTAssertionType}, "client_id": {"client"}}, source: source, wantErr: fosite.ErrInvalidRequest},
		{name: "empty assertion", form: url.Values{"client_assertion_type": {spiffeauth.SPIFFEJWTAssertionType}, "client_assertion": {""}, "client_id": {"client"}}, source: source, wantErr: fosite.ErrInvalidRequest},
		{name: "duplicate assertion type", form: url.Values{"client_assertion_type": {spiffeauth.SPIFFEJWTAssertionType, spiffeauth.SPIFFEJWTAssertionType}, "client_assertion": {validToken}, "client_id": {"client"}}, source: source, wantErr: fosite.ErrInvalidRequest},
		{name: "empty client ID", form: url.Values{"client_assertion_type": {spiffeauth.SPIFFEJWTAssertionType}, "client_assertion": {validToken}, "client_id": {""}}, source: source, wantErr: fosite.ErrInvalidRequest},
		{name: "whitespace client ID", form: url.Values{"client_assertion_type": {spiffeauth.SPIFFEJWTAssertionType}, "client_assertion": {validToken}, "client_id": {" \t"}}, source: source, wantErr: fosite.ErrInvalidRequest},
		{name: "whitespace assertion", form: url.Values{"client_assertion_type": {spiffeauth.SPIFFEJWTAssertionType}, "client_assertion": {" \t"}, "client_id": {"client"}}, source: source, wantErr: fosite.ErrInvalidRequest},
		{name: "whitespace assertion type", form: url.Values{"client_assertion_type": {spiffeauth.SPIFFEJWTAssertionType, " \t"}, "client_assertion": {validToken}, "client_id": {"client"}}, source: source, wantErr: fosite.ErrInvalidRequest},
		{name: "duplicate client ID", form: url.Values{"client_assertion_type": {spiffeauth.SPIFFEJWTAssertionType}, "client_assertion": {validToken}, "client_id": {"client", "client"}}, source: source, wantErr: fosite.ErrInvalidRequest},
		// A client_id padded with whitespace is passed to the resolver exactly
		// as received (no implicit normalization); it is then rejected because
		// it does not exactly match the resolved client's ID. Silently
		// trimming it before the match would let a whitespace-decorated
		// client_id impersonate the canonical one.
		{name: "client ID is not normalized", form: url.Values{"client_assertion_type": {spiffeauth.SPIFFEJWTAssertionType}, "client_assertion": {validToken}, "client_id": {" client "}}, source: source, wantErr: fosite.ErrInvalidClient, wantCall: true},
		{name: "Basic authorization with empty credentials", form: jwtForm(validToken), authorize: "Basic", source: source, wantErr: fosite.ErrInvalidClient},
		{name: "malformed Basic authorization", form: jwtForm(validToken), authorize: "Basic not-base64", source: source, wantErr: fosite.ErrInvalidClient},
		{name: "case-insensitive Basic authorization", form: jwtForm(validToken), authorize: "basic credentials", source: source, wantErr: fosite.ErrInvalidClient},
		{name: "client secret key", form: func() url.Values { f := jwtForm(validToken); f["client_secret"] = []string{""}; return f }(), source: source, wantErr: fosite.ErrInvalidClient},
		{name: "maximum-sized assertion", form: func() url.Values {
			f := jwtForm(validToken)
			f["client_assertion"] = []string{strings.Repeat("a", 16*1024)}
			return f
		}(), source: source, wantErr: fosite.ErrInvalidClient},
		{name: "oversized assertion", form: func() url.Values {
			f := jwtForm(validToken)
			f["client_assertion"] = []string{strings.Repeat("a", 16*1024+1)}
			return f
		}(), source: source, wantErr: fosite.ErrInvalidClient},
		{name: "malformed token", form: jwtForm("not-a-jwt"), source: source, wantErr: fosite.ErrInvalidClient},
		{name: "wrong signature", form: jwtForm(signedJWT(t, jose.RS256, mustRSAKey(t), "key-1", "JWT", standardClaims(id, []string{testIssuer}))), source: source, wantErr: fosite.ErrInvalidClient},
		{name: "missing key ID", form: jwtForm(signedJWT(t, jose.RS256, key, "", "JWT", standardClaims(id, []string{testIssuer}))), source: source, wantErr: fosite.ErrInvalidClient},
		{name: "invalid type", form: jwtForm(signedJWT(t, jose.RS256, key, "key-1", "not-jwt", standardClaims(id, []string{testIssuer}))), source: source, wantErr: fosite.ErrInvalidClient},
		{name: "wrong audience", form: jwtForm(signedJWT(t, jose.RS256, key, "key-1", "JWT", standardClaims(id, []string{"wrong"}))), source: source, wantErr: fosite.ErrInvalidClient},
		{name: "iss claim is not required and a mismatched one is ignored", form: jwtForm(signedJWT(t, jose.RS256, key, "key-1", "JWT", func() jwt.Claims {
			claims := standardClaims(id, []string{testIssuer})
			claims.Issuer = "example.org"
			return claims
		}())), source: source, wantCall: true},
		{name: "multiple audience", form: jwtForm(signedJWT(t, jose.RS256, key, "key-1", "JWT", standardClaims(id, []string{testIssuer, "other"}))), source: source, wantErr: fosite.ErrInvalidClient},
		{name: "validity comfortably within six minutes", form: jwtForm(signedJWT(t, jose.RS256, key, "key-1", "JWT", claimsExpiringIn(id, 5*time.Minute))), source: source, wantCall: true},
		{name: "validity comfortably beyond six minutes", form: jwtForm(signedJWT(t, jose.RS256, key, "key-1", "JWT", claimsExpiringIn(id, 7*time.Minute))), source: source, wantErr: fosite.ErrInvalidClient},
		{name: "expired token", form: jwtForm(signedJWT(t, jose.RS256, key, "key-1", "JWT", expiredClaims(id))), source: source, wantErr: fosite.ErrInvalidClient},
		{name: "wrong trust domain", form: jwtForm(signedJWT(t, jose.RS256, key, "key-1", "JWT", standardClaims(spiffeid.RequireFromString("spiffe://other.org/workload"), []string{testIssuer}))), source: source, wantErr: fosite.ErrInvalidClient},
		{name: "resolver rejection", form: jwtForm(validToken), source: source, resolverErr: errors.New("association denied"), wantErr: fosite.ErrInvalidClient, wantCall: true},
		{name: "resolver returns different client", form: jwtForm(validToken), source: source, resolverClient: &fosite.DefaultClient{ID: "other-client"}, wantErr: fosite.ErrInvalidClient, wantCall: true},
		{name: "no JWT bundle source configured", form: jwtForm(validToken), source: nil, wantErr: fosite.ErrInvalidClient},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			called := false
			strategy := newSPIFFEClientAuthenticationStrategy(
				func(context.Context, *http.Request, url.Values) (fosite.Client, error) {
					t.Fatal("default strategy called")
					return nil, nil
				},
				testIssuer, tt.source, nil,
				func(_ context.Context, gotSPIFFEID, clientID string, method spiffeauth.SPIFFEAuthenticationMethod) (fosite.Client, error) {
					called = true
					assert.Equal(t, id.String(), gotSPIFFEID)
					assert.Equal(t, spiffeauth.SPIFFEAuthenticationMethodJWT, method)
					assert.Equal(t, tt.form.Get("client_id"), clientID)
					var resolvedClient fosite.Client = client
					if tt.resolverClient != nil {
						resolvedClient = tt.resolverClient
					}
					return resolvedClient, tt.resolverErr
				},
				nil,
			)
			req := httptest.NewRequest(http.MethodPost, "/oauth/token", nil)
			req.TLS = tlsStateWithLeaf(tt.leaf)
			if tt.authorize != "" {
				req.Header.Set("Authorization", tt.authorize)
			}
			got, err := strategy(context.Background(), req, tt.form)
			assert.Equal(t, tt.wantCall, called)
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				assert.Nil(t, got)
				if assertion := tt.form.Get("client_assertion"); assertion != "" {
					assert.NotContains(t, err.Error(), assertion)
				}
				return
			}
			require.NoError(t, err)
			assert.Same(t, client, got)
		})
	}
}

func TestSPIFFEJWTAssertionSizeLimit(t *testing.T) {
	t.Parallel()

	request := httptest.NewRequest(http.MethodPost, "/oauth/token", nil)
	form := url.Values{}
	assert.False(t, rejectedSPIFFEJWTRequest(request, form, strings.Repeat("a", 16*1024)))
	assert.True(t, rejectedSPIFFEJWTRequest(request, form, strings.Repeat("a", 16*1024+1)))
}

func TestSPIFFEJWTClientAuthenticationAlgorithms(t *testing.T) {
	t.Parallel()

	id := spiffeid.RequireFromString("spiffe://example.org/workload/my-service")
	keys := []struct {
		name string
		alg  jose.SignatureAlgorithm
		key  crypto.Signer
	}{
		{"RS256", jose.RS256, mustRSAKey(t)},
		{"ES256", jose.ES256, mustECDSAKey(t)},
		{"PS256", jose.PS256, mustRSAKey(t)},
	}
	for _, tt := range keys {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			token := signedJWT(t, tt.alg, tt.key, "key", "JWT", standardClaims(id, []string{testIssuer}))
			source := jwtbundle.NewSet(jwtbundle.FromJWTAuthorities(id.TrustDomain(), map[string]crypto.PublicKey{"key": tt.key.Public()}))
			defaultCalled := false
			strategy := newSPIFFEClientAuthenticationStrategy(
				func(context.Context, *http.Request, url.Values) (fosite.Client, error) {
					defaultCalled = true
					return nil, nil
				},
				testIssuer, source, nil,
				func(_ context.Context, gotSPIFFEID, _ string, method spiffeauth.SPIFFEAuthenticationMethod) (fosite.Client, error) {
					assert.Equal(t, id.String(), gotSPIFFEID)
					assert.Equal(t, spiffeauth.SPIFFEAuthenticationMethodJWT, method)
					return &fosite.DefaultClient{ID: "client"}, nil
				},
				nil,
			)

			got, err := strategy(context.Background(), httptest.NewRequest(http.MethodPost, "/", nil), jwtForm(token))
			require.NoError(t, err)
			assert.NotNil(t, got)
			assert.False(t, defaultCalled)
		})
	}
}

func TestSPIFFEJWTDispatchRejectsDuplicateAssertionType(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		form         url.Values
		wantDelegate bool
	}{
		{
			name: "SPIFFE then non-SPIFFE",
			form: url.Values{"client_assertion_type": {
				spiffeauth.SPIFFEJWTAssertionType,
				"urn:ietf:params:oauth:client-assertion-type:jwt-bearer",
			}},
		},
		{
			name: "non-SPIFFE then SPIFFE",
			form: url.Values{"client_assertion_type": {
				"urn:ietf:params:oauth:client-assertion-type:jwt-bearer",
				spiffeauth.SPIFFEJWTAssertionType,
			}},
		},
		{
			name: "identical non-SPIFFE duplicates",
			form: url.Values{"client_assertion_type": {
				"urn:ietf:params:oauth:client-assertion-type:jwt-bearer",
				"urn:ietf:params:oauth:client-assertion-type:jwt-bearer",
			}},
			wantDelegate: true,
		},
		{
			name:         "one non-SPIFFE assertion type",
			form:         url.Values{"client_assertion_type": {"urn:ietf:params:oauth:client-assertion-type:jwt-bearer"}},
			wantDelegate: true,
		},
		{
			name:         "absent assertion type",
			form:         url.Values{"client_id": {"client"}},
			wantDelegate: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			called := false
			strategy := newSPIFFEClientAuthenticationStrategy(func(context.Context, *http.Request, url.Values) (fosite.Client, error) {
				called = true
				return nil, errors.New("default")
			}, testIssuer, nil, nil, stubResolver, nil)
			_, err := strategy(context.Background(), httptest.NewRequest(http.MethodPost, "/", nil), tt.form)
			assert.Equal(t, tt.wantDelegate, called)
			if tt.wantDelegate {
				assert.EqualError(t, err, "default")
				return
			}
			require.ErrorIs(t, err, fosite.ErrInvalidRequest)
		})
	}
}

func TestSPIFFEJWTDispatchDelegatesWithoutResolverEvenWithDuplicateAssertionType(t *testing.T) {
	t.Parallel()

	defaultCalled := false
	strategy := newSPIFFEClientAuthenticationStrategy(func(context.Context, *http.Request, url.Values) (fosite.Client, error) {
		defaultCalled = true
		return nil, nil
	}, testIssuer, nil, nil, nil, nil)
	_, err := strategy(context.Background(), httptest.NewRequest(http.MethodPost, "/", nil), url.Values{
		"client_assertion_type": {spiffeauth.SPIFFEJWTAssertionType, spiffeauth.SPIFFEJWTAssertionType},
	})
	require.NoError(t, err)
	assert.True(t, defaultCalled)
}

func tlsStateWithLeaf(leaf *x509.Certificate) *tls.ConnectionState {
	if leaf == nil {
		return nil
	}
	return &tls.ConnectionState{PeerCertificates: []*x509.Certificate{leaf}}
}

func jwtForm(token string) url.Values {
	return url.Values{"client_assertion_type": {spiffeauth.SPIFFEJWTAssertionType}, "client_assertion": {token}, "client_id": {"client"}}
}
func standardClaims(id spiffeid.ID, audience []string) jwt.Claims {
	return jwt.Claims{
		Issuer:    id.TrustDomain().IDString(),
		Subject:   id.String(),
		Audience:  audience,
		Expiry:    jwt.NewNumericDate(time.Now().Add(2 * time.Minute)),
		NotBefore: jwt.NewNumericDate(time.Now().Add(-time.Minute)),
		IssuedAt:  jwt.NewNumericDate(time.Now()),
	}
}
func claimsExpiringIn(id spiffeid.ID, validity time.Duration) jwt.Claims {
	claims := standardClaims(id, []string{testIssuer})
	claims.Expiry = jwt.NewNumericDate(time.Now().Add(validity))
	return claims
}
func expiredClaims(id spiffeid.ID) jwt.Claims {
	c := standardClaims(id, []string{testIssuer})
	c.Expiry = jwt.NewNumericDate(time.Now().Add(-2 * time.Minute))
	return c
}
func mustRSAKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	return key
}
func mustECDSAKey(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	return key
}
func signedJWT(t *testing.T, alg jose.SignatureAlgorithm, key crypto.Signer, kid, typ string, claims jwt.Claims) string {
	t.Helper()
	options := (&jose.SignerOptions{}).WithType(jose.ContentType(typ))
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: alg, Key: jose.JSONWebKey{Key: key, KeyID: kid}}, options)
	require.NoError(t, err)
	token, err := jwt.Signed(signer).Claims(claims).Serialize()
	require.NoError(t, err)
	return token
}

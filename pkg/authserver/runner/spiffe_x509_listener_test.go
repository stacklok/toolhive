// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package runner

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"fmt"
	"math/big"
	"net"
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
	"github.com/spiffe/go-spiffe/v2/bundle/spiffebundle"
	"github.com/spiffe/go-spiffe/v2/spiffeid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/authserver"
	"github.com/stacklok/toolhive/pkg/authserver/server/tokenexchange"
	spiffeauth "github.com/stacklok/toolhive/pkg/authserver/spiffe"
	"github.com/stacklok/toolhive/pkg/oauthproto"
)

const (
	spiffeX509TestTrustDomain = "example.org"
	spiffeX509TestPrincipal   = "spiffe://example.org/ns/default/agent"
	spiffeX509TestClientID    = "spiffe-client"
	spiffeX509TestIssuer      = "https://auth.example.com"

	spiffeX509TestOtherPrincipal = "spiffe://example.org/ns/other/agent"
	spiffeX509TestOtherClientID  = "other-spiffe-client"
)

// spiffeTestAuthority is a test trust root with an X.509 authority and a JWT
// authority (jwtKey, published as jwtKeyID).
type spiffeTestAuthority struct {
	cert     *x509.Certificate
	key      *ecdsa.PrivateKey
	jwtKey   *ecdsa.PrivateKey
	jwtKeyID string
}

func newSPIFFETestAuthority(t *testing.T, name string) spiffeTestAuthority {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(time.Now().UnixNano()),
		Subject:               pkix.Name{CommonName: name},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	require.NoError(t, err)
	cert, err := x509.ParseCertificate(der)
	require.NoError(t, err)
	jwtKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	return spiffeTestAuthority{cert: cert, key: key, jwtKey: jwtKey, jwtKeyID: "jwt-" + name}
}

// issueSVID returns an X.509-SVID leaf for spiffeX509TestPrincipal signed by the authority.
func (a spiffeTestAuthority) issueSVID(t *testing.T) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	id, err := url.Parse(spiffeX509TestPrincipal)
	require.NoError(t, err)
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(time.Now().UnixNano()),
		Subject:               pkix.Name{CommonName: "workload"},
		URIs:                  []*url.URL{id},
		NotBefore:             time.Now().Add(-time.Minute),
		NotAfter:              time.Now().Add(time.Hour),
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, a.cert, &key.PublicKey, a.key)
	require.NoError(t, err)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

// writeSPIFFEBundleFile writes a trust bundle holding the authorities of the
// given CAs.
func writeSPIFFEBundleFile(t *testing.T, path string, authorities ...spiffeTestAuthority) {
	t.Helper()
	bundle := spiffebundle.New(spiffeid.RequireTrustDomainFromString(spiffeX509TestTrustDomain))
	for _, a := range authorities {
		bundle.AddX509Authority(a.cert)
		require.NoError(t, bundle.AddJWTAuthority(a.jwtKeyID, a.jwtKey.Public()))
	}
	bundle.SetSequenceNumber(1)
	doc, err := bundle.Marshal()
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, doc, 0o600))
}

// TestEmbeddedAuthServer_SPIFFEX509ClientAuthOverTLS drives real TLS
// handshakes carrying X.509-SVID client certificates through the listener of
// an embedded auth server into /oauth/token.
//
// The requests carry no subject_token, so a client that authenticates
// successfully still fails at the grant level with 400 invalid_request. That
// grant-level error proves client authentication succeeded; a 401
// invalid_client means client authentication failed (untrusted SVID or no
// association for the client_id). Complete exchanges are in
// TestEmbeddedAuthServer_SPIFFETokenExchangeOverTLS.
//
// Bundle rotation is covered by TestReloadSPIFFEBundleFileRotatesAndRetainsLastKnownGood
// (pkg/authserver/spiffe_bundle_source_test.go) and
// TestSPIFFEX509ClientAuthenticationFollowsBundleRotation
// (pkg/authserver/server/spiffe_client_auth_x509_test.go); the strategy re-verifies against the
// live x509bundle.Source on every request, and the 30s poll interval is not overridable from tests.
func TestEmbeddedAuthServer_SPIFFEX509ClientAuthOverTLS(t *testing.T) {
	t.Parallel()

	trusted := newSPIFFETestAuthority(t, "ca-1")
	untrusted := newSPIFFETestAuthority(t, "ca-2")
	tokenURL, roots, _ := startSPIFFEX509TLSServer(t, trusted, func(cfg *authserver.RunConfig) {
		cfg.InboundGrants.SPIFFEClientAuth = append(cfg.InboundGrants.SPIFFEClientAuth, authserver.SPIFFEClientAuthRunConfig{
			TrustDomainRef:   "production",
			PrincipalPattern: spiffeX509TestOtherPrincipal,
			ClientID:         spiffeX509TestOtherClientID,
			Methods:          []authserver.SPIFFEAuthenticationMethod{authserver.SPIFFEAuthenticationMethodX509},
			Scopes:           []string{"openid"},
			Audiences:        []string{"https://mcp.example.com"},
			GrantTypes:       []string{authserver.SPIFFEGrantTypeTokenExchange},
		})
	})

	postToken := func(t *testing.T, svid tls.Certificate, clientID string) (int, string) {
		t.Helper()
		status, body := postTokenOverTLS(t, tokenURL, roots, &svid, "", url.Values{
			"grant_type": {"urn:ietf:params:oauth:grant-type:token-exchange"},
			"client_id":  {clientID},
		})
		errCode, _ := body["error"].(string)
		return status, errCode
	}

	t.Run("leaf from trusted authority authenticates", func(t *testing.T) {
		t.Parallel()
		svid := trusted.issueSVID(t)
		status, body := postTokenOverTLS(t, tokenURL, roots, &svid, "", url.Values{
			"grant_type": {"urn:ietf:params:oauth:grant-type:token-exchange"},
			"client_id":  {spiffeX509TestClientID},
		})
		assert.Equal(t, http.StatusBadRequest, status)
		assert.Equal(t, "invalid_request", body["error"])
		assert.Contains(t, body["error_description"], "subject_token",
			"the grant handler, not client authentication, rejected the request")
	})

	t.Run("trusted leaf presented with another association's client_id is rejected", func(t *testing.T) {
		t.Parallel()
		status, errCode := postToken(t, trusted.issueSVID(t), spiffeX509TestOtherClientID)
		assert.Equal(t, http.StatusUnauthorized, status)
		assert.Equal(t, "invalid_client", errCode)
	})

	t.Run("leaf from untrusted authority is rejected", func(t *testing.T) {
		t.Parallel()
		status, errCode := postToken(t, untrusted.issueSVID(t), spiffeX509TestClientID)
		assert.Equal(t, http.StatusUnauthorized, status)
		assert.Equal(t, "invalid_client", errCode)
	})
}

// TestEmbeddedAuthServer_SPIFFEClientCannotRedeemIDJAG proves a SPIFFE
// association client, which is restricted to token exchange, is refused when it
// redeems an ID-JAG through the jwt-bearer grant, while a delegate client that
// lists jwt-bearer redeems the same kind of assertion over the same listener.
func TestEmbeddedAuthServer_SPIFFEClientCannotRedeemIDJAG(t *testing.T) {
	t.Parallel()

	const (
		issuer         = "https://auth.example.com"
		idpIssuer      = "https://idp.example.com"
		idpSubject     = "testuser@example.com"
		audience       = "https://mcp.example.com"
		delegateID     = "jwt-bearer-delegate"
		delegateSecret = "delegate-secret-value-that-is-long-enough-for-32-chars"
	)

	idpKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	jwksServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{
			Key: &idpKey.PublicKey, KeyID: "idp-key", Use: "sig", Algorithm: string(jose.ES256),
		}}})
	}))
	t.Cleanup(jwksServer.Close)

	secretFile := filepath.Join(t.TempDir(), "delegate-secret")
	require.NoError(t, os.WriteFile(secretFile, []byte(delegateSecret), 0o600))

	trusted := newSPIFFETestAuthority(t, "ca-1")
	tokenURL, roots, _ := startSPIFFEX509TLSServer(t, trusted, func(cfg *authserver.RunConfig) {
		// SPIFFE clients carry a resource allowlist (server.ValidateIDJAGClientAudience);
		// without it the redemption would stop at invalid_target before the grant
		// gate matters.
		cfg.InboundGrants.SPIFFEClientAuth[0].Resources = []string{audience}
		cfg.InboundGrants.JWTBearer = &authserver.JWTBearerInboundGrantRunConfig{
			IssuerPolicies: []authserver.JWTBearerIssuerPolicyRunConfig{{
				IssuerRef:       "mock-idp",
				MaxAssertionAge: "5m",
				SubjectBindings: []tokenexchange.JWTBearerSubjectBinding{{
					Subject: idpSubject, AllowedResources: []string{audience},
				}},
				AcceptedAudiences: []string{issuer + "/oauth/token"},
			}},
		}
		cfg.InboundGrants.TokenExchange = &authserver.TokenExchangeInboundGrantRunConfig{
			DelegateClients: []authserver.DelegateClientRunConfig{{
				ClientID:         delegateID,
				ClientSecretFile: secretFile,
				GrantTypes:       []string{oauthproto.GrantTypeTokenExchange, oauthproto.GrantTypeJWTBearer},
				Scopes:           []string{"openid"},
				Audiences:        []string{audience},
				TokenExchange: &authserver.DelegateClientTokenExchange{
					SelfIssuedSubjectTokens: authserver.SelfIssuedSubjectTokensAny,
				},
			}},
		}
		cfg.TrustedIssuers = []tokenexchange.TrustedIssuer{{
			Name: "mock-idp", IssuerURL: idpIssuer, JWKSURL: jwksServer.URL,
			InsecureAllowHTTP: true, AllowPrivateIPs: true,
		}}
	})

	signIDJAG := func(t *testing.T, clientID, jti string) string {
		t.Helper()
		signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.ES256, Key: idpKey},
			(&jose.SignerOptions{}).WithType(jose.ContentType(oauthproto.JWTTypeIDJAG)).WithHeader("kid", "idp-key"))
		require.NoError(t, err)
		now := time.Now()
		assertion, err := jwt.Signed(signer).Claims(jwt.Claims{
			Issuer: idpIssuer, Subject: idpSubject, Audience: jwt.Audience{issuer + "/oauth/token"},
			ID: jti, IssuedAt: jwt.NewNumericDate(now), Expiry: jwt.NewNumericDate(now.Add(2 * time.Minute)),
		}).Claims(map[string]any{"client_id": clientID}).Serialize()
		require.NoError(t, err)
		return assertion
	}
	redeemForm := func(t *testing.T, clientID, jti string) url.Values {
		t.Helper()
		return url.Values{
			"grant_type": {oauthproto.GrantTypeJWTBearer},
			"assertion":  {signIDJAG(t, clientID, jti)},
			"resource":   {audience},
			"client_id":  {clientID},
		}
	}

	t.Run("delegate client listing jwt-bearer redeems the ID-JAG", func(t *testing.T) {
		t.Parallel()
		status, body := postTokenOverTLS(t, tokenURL, roots, nil, delegateID+":"+delegateSecret,
			redeemForm(t, delegateID, "jti-delegate"))
		assert.Equal(t, http.StatusOK, status, "%v", body)
		assert.NotEmpty(t, body["access_token"])
	})

	t.Run("SPIFFE association client is refused as an unauthorized client", func(t *testing.T) {
		t.Parallel()
		svid := trusted.issueSVID(t)
		status, body := postTokenOverTLS(t, tokenURL, roots, &svid, "",
			redeemForm(t, spiffeX509TestClientID, "jti-spiffe"))
		assert.Equal(t, http.StatusBadRequest, status, "%v", body)
		assert.Equal(t, "unauthorized_client", body["error"])
	})
}

func signSPIFFESubjectToken(
	t *testing.T, srv *EmbeddedAuthServer, clientID, scope string, mayAct map[string]any, audiences ...string,
) string {
	t.Helper()
	signingKey, err := srv.KeyProvider().SigningKey(context.Background())
	require.NoError(t, err)
	signer, err := jose.NewSigner(
		jose.SigningKey{Algorithm: jose.SignatureAlgorithm(signingKey.Algorithm), Key: signingKey.Key},
		(&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", signingKey.KeyID),
	)
	require.NoError(t, err)
	now := time.Now()
	extra := map[string]any{"client_id": clientID, "scope": scope}
	if mayAct != nil {
		extra["may_act"] = mayAct
	}
	token, err := jwt.Signed(signer).Claims(jwt.Claims{
		Issuer: spiffeX509TestIssuer, Subject: "test-user", Audience: jwt.Audience(audiences),
		Expiry: jwt.NewNumericDate(now.Add(30 * time.Minute)), IssuedAt: jwt.NewNumericDate(now),
	}).Claims(extra).Serialize()
	require.NoError(t, err)
	return token
}

// TestEmbeddedAuthServer_SPIFFEOnlyTokenExchangeOverTLS completes an X.509
// exchange with no upstream, delegate client or trusted issuer configured.
func TestEmbeddedAuthServer_SPIFFEOnlyTokenExchangeOverTLS(t *testing.T) {
	t.Parallel()

	const resource = "https://mcp.example.com"
	trusted := newSPIFFETestAuthority(t, "ca-1")
	tokenURL, roots, srv := startSPIFFEX509TLSServer(t, trusted, func(cfg *authserver.RunConfig) {
		cfg.Upstreams = nil
		cfg.InboundGrants.SPIFFEClientAuth[0].Resources = []string{resource}
	})

	svid := trusted.issueSVID(t)
	status, body := postTokenOverTLS(t, tokenURL, roots, &svid, "", url.Values{
		"grant_type":         {oauthproto.GrantTypeTokenExchange},
		"subject_token":      {signSPIFFESubjectToken(t, srv, spiffeX509TestClientID, "openid", nil, resource)},
		"subject_token_type": {oauthproto.TokenTypeAccessToken},
		"client_id":          {spiffeX509TestClientID},
		"scope":              {"openid"},
		"resource":           {resource},
	})
	require.Equal(t, http.StatusOK, status, "token response: %v", body)
	assert.NotEmpty(t, body["access_token"])
}

// TestEmbeddedAuthServer_SPIFFETokenExchangeOverTLS completes RFC 8693
// exchanges over the TLS listener with X.509-SVID and JWT-SVID client
// authentication, checking the issued token and the policy and consent limits.
func TestEmbeddedAuthServer_SPIFFETokenExchangeOverTLS(t *testing.T) {
	t.Parallel()

	const (
		issuer         = "https://auth.example.com"
		resource       = "https://mcp.example.com"
		otherAudience  = "https://other.example.com"
		delegateID     = "plain-delegate"
		delegateSecret = "delegate-secret-value-that-is-long-enough-for-32-chars"
	)

	secretFile := filepath.Join(t.TempDir(), "delegate-secret")
	require.NoError(t, os.WriteFile(secretFile, []byte(delegateSecret), 0o600))

	trusted := newSPIFFETestAuthority(t, "ca-1")
	tokenURL, roots, srv := startSPIFFEX509TLSServer(t, trusted, func(cfg *authserver.RunConfig) {
		cfg.ScopesSupported = []string{"openid", "profile"}
		cfg.SPIFFETrustDomains[0].Methods = []authserver.SPIFFEAuthenticationMethod{
			authserver.SPIFFEAuthenticationMethodX509, authserver.SPIFFEAuthenticationMethodJWT,
		}
		association := &cfg.InboundGrants.SPIFFEClientAuth[0]
		association.Methods = cfg.SPIFFETrustDomains[0].Methods
		association.Resources = []string{resource}
		cfg.InboundGrants.TokenExchange = &authserver.TokenExchangeInboundGrantRunConfig{
			DelegateClients: []authserver.DelegateClientRunConfig{{
				ClientID:         delegateID,
				ClientSecretFile: secretFile,
				Scopes:           []string{"openid"},
				Audiences:        []string{resource},
				TokenExchange: &authserver.DelegateClientTokenExchange{
					SelfIssuedSubjectTokens: authserver.SelfIssuedSubjectTokensAny,
				},
			}},
		}
	})

	subjectToken := func(t *testing.T, clientID, scope string, mayAct map[string]any, audiences ...string) string {
		t.Helper()
		return signSPIFFESubjectToken(t, srv, clientID, scope, mayAct, append([]string{resource}, audiences...)...)
	}
	// jwtSVID mints a five-minute JWT-SVID, inside the validator's six-minute cap.
	jwtSVID := func(t *testing.T) string {
		t.Helper()
		signer, err := jose.NewSigner(
			jose.SigningKey{Algorithm: jose.ES256, Key: jose.JSONWebKey{Key: trusted.jwtKey, KeyID: trusted.jwtKeyID}},
			(&jose.SignerOptions{}).WithType("JWT"),
		)
		require.NoError(t, err)
		now := time.Now()
		token, err := jwt.Signed(signer).Claims(jwt.Claims{
			Subject: spiffeX509TestPrincipal, Audience: jwt.Audience{issuer},
			Expiry: jwt.NewNumericDate(now.Add(5 * time.Minute)), IssuedAt: jwt.NewNumericDate(now),
			NotBefore: jwt.NewNumericDate(now.Add(-time.Minute)),
		}).Serialize()
		require.NoError(t, err)
		return token
	}
	exchangeForm := func(token, clientID string, extra url.Values) url.Values {
		form := url.Values{
			"grant_type":         {oauthproto.GrantTypeTokenExchange},
			"subject_token":      {token},
			"subject_token_type": {oauthproto.TokenTypeAccessToken},
			"client_id":          {clientID},
			"scope":              {"openid"},
			"resource":           {resource},
		}
		for key, values := range extra {
			form[key] = values
		}
		return form
	}
	issuedClaims := func(t *testing.T, accessToken string) map[string]any {
		t.Helper()
		signingKey, err := srv.KeyProvider().SigningKey(context.Background())
		require.NoError(t, err)
		parsed, err := jwt.ParseSigned(accessToken, []jose.SignatureAlgorithm{jose.SignatureAlgorithm(signingKey.Algorithm)})
		require.NoError(t, err)
		var claims map[string]any
		require.NoError(t, parsed.Claims(signingKey.Key.Public(), &claims))
		return claims
	}

	type request struct {
		svid  *tls.Certificate
		basic string
		form  url.Values
	}
	tests := []struct {
		name       string
		build      func(t *testing.T) request
		wantStatus int
		wantError  string
		wantDesc   string
		wantIssued string
	}{
		{
			name: "X.509-SVID exchange issues a token bound to the association",
			build: func(t *testing.T) request {
				t.Helper()
				svid := trusted.issueSVID(t)
				return request{svid: &svid, form: exchangeForm(
					subjectToken(t, spiffeX509TestClientID, "openid", nil), spiffeX509TestClientID, nil)}
			},
			wantStatus: http.StatusOK,
			wantIssued: spiffeX509TestClientID,
		},
		{
			name: "JWT-SVID exchange without a client certificate issues the same token",
			build: func(t *testing.T) request {
				t.Helper()
				return request{form: exchangeForm(
					subjectToken(t, spiffeX509TestClientID, "openid", nil), spiffeX509TestClientID, url.Values{
						"client_assertion_type": {spiffeauth.SPIFFEJWTAssertionType},
						"client_assertion":      {jwtSVID(t)},
					})}
			},
			wantStatus: http.StatusOK,
			wantIssued: spiffeX509TestClientID,
		},
		{
			name: "scope outside the association scopes is invalid_scope",
			build: func(t *testing.T) request {
				t.Helper()
				svid := trusted.issueSVID(t)
				return request{svid: &svid, form: exchangeForm(
					subjectToken(t, spiffeX509TestClientID, "openid profile", nil), spiffeX509TestClientID,
					url.Values{"scope": {"profile"}})}
			},
			wantStatus: http.StatusBadRequest,
			wantError:  "invalid_scope",
		},
		{
			name: "audience outside the association audiences is invalid_target",
			build: func(t *testing.T) request {
				t.Helper()
				svid := trusted.issueSVID(t)
				// The subject token covers otherAudience: only the association refuses it.
				form := exchangeForm(subjectToken(t, spiffeX509TestClientID, "openid", nil, otherAudience), spiffeX509TestClientID,
					url.Values{"audience": {otherAudience}})
				form.Del("resource")
				return request{svid: &svid, form: form}
			},
			wantStatus: http.StatusBadRequest,
			wantError:  "invalid_target",
			wantDesc:   "not permitted to request audience",
		},
		{
			name: "subject token issued to another client without may_act is invalid_grant",
			build: func(t *testing.T) request {
				t.Helper()
				svid := trusted.issueSVID(t)
				return request{svid: &svid, form: exchangeForm(
					subjectToken(t, "other-client", "openid", nil), spiffeX509TestClientID, nil)}
			},
			wantStatus: http.StatusBadRequest,
			wantError:  "invalid_grant",
		},
		{
			name: "subject token naming the SPIFFE client in may_act is accepted",
			build: func(t *testing.T) request {
				t.Helper()
				svid := trusted.issueSVID(t)
				return request{svid: &svid, form: exchangeForm(
					subjectToken(t, "other-client", "openid", map[string]any{"sub": spiffeX509TestClientID}),
					spiffeX509TestClientID, nil)}
			},
			wantStatus: http.StatusOK,
			wantIssued: spiffeX509TestClientID,
		},
		{
			name: "client_secret_post delegate with an incidental SVID authenticates normally",
			build: func(t *testing.T) request {
				t.Helper()
				svid := trusted.issueSVID(t)
				return request{svid: &svid, form: exchangeForm(
					subjectToken(t, "other-client", "openid", nil), delegateID,
					url.Values{"client_secret": {delegateSecret}})}
			},
			wantStatus: http.StatusOK,
			wantIssued: delegateID,
		},
		{
			name: "HTTP Basic delegate with an incidental SVID authenticates normally",
			build: func(t *testing.T) request {
				t.Helper()
				svid := trusted.issueSVID(t)
				form := exchangeForm(subjectToken(t, "other-client", "openid", nil), delegateID, nil)
				form.Del("client_id")
				return request{svid: &svid, basic: delegateID + ":" + delegateSecret, form: form}
			},
			wantStatus: http.StatusOK,
			wantIssued: delegateID,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			req := tt.build(t)
			status, body := postTokenOverTLS(t, tokenURL, roots, req.svid, req.basic, req.form)
			require.Equal(t, tt.wantStatus, status, "token response: %v", body)
			if tt.wantError != "" {
				assert.Equal(t, tt.wantError, body["error"])
				if tt.wantDesc != "" {
					assert.Contains(t, body["error_description"], tt.wantDesc)
				}
				assert.NotContains(t, body, "access_token")
				return
			}

			assert.Equal(t, oauthproto.TokenTypeAccessToken, body["issued_token_type"])
			accessToken, ok := body["access_token"].(string)
			require.True(t, ok, "token response: %v", body)
			claims := issuedClaims(t, accessToken)
			assert.Equal(t, tt.wantIssued, claims["client_id"])
			assert.Equal(t, []any{"openid"}, claims["scp"])
			assert.Equal(t, []any{resource}, claims["aud"])
			act, ok := claims["act"].(map[string]any)
			require.True(t, ok, "claims: %v", claims)
			assert.Equal(t, tt.wantIssued, act["sub"])
		})
	}
}

// startSPIFFEX509TLSServer starts an embedded auth server whose TLS listener
// trusts X.509-SVIDs issued by trusted for spiffeX509TestClientID, optionally
// adjusted by configure, and returns its token endpoint URL and the root pool
// that trusts the listener certificate. The server is returned for minting
// subject tokens.
func startSPIFFEX509TLSServer(
	t *testing.T, trusted spiffeTestAuthority, configure func(*authserver.RunConfig),
) (string, *x509.CertPool, *EmbeddedAuthServer) {
	t.Helper()

	bundlePath := filepath.Join(t.TempDir(), "bundle.json")
	writeSPIFFEBundleFile(t, bundlePath, trusted)

	listenerPair := makeTestCertificate(t, "auth.example.com")
	certFile, keyFile := writeTestCertificate(t, t.TempDir(), "listener", listenerPair)
	cfg := &authserver.RunConfig{
		TLSListener:      &authserver.TLSListenerRunConfig{CertFile: certFile, KeyFile: keyFile},
		SchemaVersion:    authserver.CurrentSchemaVersion,
		Issuer:           "https://auth.example.com",
		ScopesSupported:  []string{"openid"},
		AllowedAudiences: []string{"https://mcp.example.com"},
		Upstreams: []authserver.UpstreamRunConfig{{
			Name: "static-upstream",
			Type: authserver.UpstreamProviderTypeOAuth2,
			OAuth2Config: &authserver.OAuth2UpstreamRunConfig{
				AuthorizationEndpoint: "https://upstream.example.com/authorize",
				TokenEndpoint:         "https://upstream.example.com/token",
				ClientID:              "test-client-id",
				RedirectURI:           "https://auth.example.com/oauth/callback",
			},
		}},
		SPIFFETrustDomains: []authserver.SPIFFETrustDomainRunConfig{{
			Name:        "production",
			TrustDomain: spiffeX509TestTrustDomain,
			Methods:     []authserver.SPIFFEAuthenticationMethod{authserver.SPIFFEAuthenticationMethodX509},
			BundleSource: authserver.SPIFFEBundleSourceRunConfig{
				Type: authserver.SPIFFEBundleSourceTypeFile,
				File: &authserver.SPIFFEFileBundleSourceRunConfig{Path: bundlePath},
			},
		}},
		InboundGrants: &authserver.InboundGrantsRunConfig{
			SPIFFEClientAuth: []authserver.SPIFFEClientAuthRunConfig{{
				TrustDomainRef:   "production",
				PrincipalPattern: spiffeX509TestPrincipal,
				ClientID:         spiffeX509TestClientID,
				Methods:          []authserver.SPIFFEAuthenticationMethod{authserver.SPIFFEAuthenticationMethodX509},
				Scopes:           []string{"openid"},
				Audiences:        []string{"https://mcp.example.com"},
				GrantTypes:       []string{authserver.SPIFFEGrantTypeTokenExchange},
			}},
		},
	}
	if configure != nil {
		configure(cfg)
	}

	srv, err := NewEmbeddedAuthServer(context.Background(), cfg, WithListenerHost("127.0.0.1"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, srv.Close()) })
	require.NotNil(t, srv.tlsListener)
	srv.tlsListener.port = 0
	require.NoError(t, srv.Start())
	addr := srv.tlsListener.listener.Addr().(*net.TCPAddr)

	roots := x509.NewCertPool()
	roots.AddCert(listenerPair.cert)
	return fmt.Sprintf("https://127.0.0.1:%d/oauth/token", addr.Port), roots, srv
}

// postTokenOverTLS posts form to tokenURL over a fresh TLS handshake. svid, when
// non-nil, is presented as the client certificate; basicAuth, when non-empty,
// is "id:secret" sent as HTTP Basic credentials. It returns the status and the
// decoded JSON body.
func postTokenOverTLS(
	t *testing.T, tokenURL string, roots *x509.CertPool, svid *tls.Certificate, basicAuth string, form url.Values,
) (int, map[string]any) {
	t.Helper()

	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots}
	if svid != nil {
		// Always present the certificate, bypassing the client's own CA-hint
		// filtering, so the server decides.
		tlsConfig.GetClientCertificate = func(*tls.CertificateRequestInfo) (*tls.Certificate, error) {
			return svid, nil
		}
	}
	// Fresh transport per request so every call performs a new handshake.
	transport := &http.Transport{DisableKeepAlives: true, TLSClientConfig: tlsConfig}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 5 * time.Second}

	req, err := http.NewRequest(http.MethodPost, tokenURL, strings.NewReader(form.Encode()))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if id, secret, ok := strings.Cut(basicAuth, ":"); ok {
		req.SetBasicAuth(id, secret)
	}
	resp, err := client.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	var body map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	return resp.StatusCode, body
}

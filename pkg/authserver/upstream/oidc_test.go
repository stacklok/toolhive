// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package upstream

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v3"
	"github.com/go-jose/go-jose/v3/jwt"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	oauthproto "github.com/stacklok/toolhive/pkg/oauth"
)

const (
	testClientID      = "test-client-id"
	testClientSecret  = "test-client-secret"
	testRedirectURI   = "http://localhost:8080/callback"
	testIssuer        = "https://example.com"
	testAuthEndpoint  = "https://example.com/authorize"
	testTokenEndpoint = "https://example.com/token"
	testJWKSURI       = "https://example.com/jwks"
	testUserinfoURL   = "https://example.com/userinfo"
)

// mockOIDCServer creates a mock OIDC server for testing.
type mockOIDCServer struct {
	*httptest.Server
	issuer       string
	privateKey   *rsa.PrivateKey
	keyID        string
	tokenHandler func(w http.ResponseWriter, r *http.Request)
}

func newMockOIDCServer(t *testing.T) *mockOIDCServer {
	t.Helper()

	// Generate RSA key pair for signing JWTs
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)

	mock := &mockOIDCServer{
		privateKey: privateKey,
		keyID:      "test-key-1",
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", mock.handleDiscovery)
	mux.HandleFunc("/authorize", mock.handleAuthorize)
	mux.HandleFunc("/token", mock.handleToken)
	mux.HandleFunc("/userinfo", mock.handleUserInfo)
	mux.HandleFunc("/jwks", mock.handleJWKS)

	mock.Server = httptest.NewServer(mux)
	mock.issuer = mock.URL

	return mock
}

func (m *mockOIDCServer) handleDiscovery(w http.ResponseWriter, _ *http.Request) {
	doc := map[string]any{
		"issuer":                                m.issuer,
		"authorization_endpoint":                m.issuer + "/authorize",
		"token_endpoint":                        m.issuer + "/token",
		"userinfo_endpoint":                     m.issuer + "/userinfo",
		"jwks_uri":                              m.issuer + "/jwks",
		"code_challenge_methods_supported":      []string{"S256"},
		"response_types_supported":              []string{"code"},
		"subject_types_supported":               []string{"public"},
		"id_token_signing_alg_values_supported": []string{"RS256"},
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(doc); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (*mockOIDCServer) handleAuthorize(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
}

func (m *mockOIDCServer) handleToken(w http.ResponseWriter, r *http.Request) {
	if m.tokenHandler != nil {
		m.tokenHandler(w, r)
		return
	}

	// Default: return tokens (without ID token for foundation tests)
	resp := testTokenResponse{
		AccessToken:  "test-access-token",
		TokenType:    "Bearer",
		RefreshToken: "test-refresh-token",
		ExpiresIn:    3600,
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (*mockOIDCServer) handleUserInfo(w http.ResponseWriter, r *http.Request) {
	// Check for Authorization header
	auth := r.Header.Get("Authorization")
	if auth == "" || len(auth) < 8 {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}

	resp := map[string]any{
		"sub":   "user-123",
		"name":  "Test User",
		"email": "test@example.com",
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (m *mockOIDCServer) handleJWKS(w http.ResponseWriter, _ *http.Request) {
	// Return JWKS with public key
	jwks := map[string]any{
		"keys": []map[string]any{
			{
				"kty": "RSA",
				"kid": m.keyID,
				"use": "sig",
				"alg": "RS256",
				"n":   base64.RawURLEncoding.EncodeToString(m.privateKey.N.Bytes()),
				"e":   base64.RawURLEncoding.EncodeToString(big.NewInt(int64(m.privateKey.E)).Bytes()),
			},
		},
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(jwks); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// signIDToken creates a signed JWT ID token.
//
//nolint:unparam // subject parameter kept for test flexibility
func (m *mockOIDCServer) signIDToken(audience, subject, nonce string, expiry time.Time) string {
	signingKey := jose.SigningKey{Algorithm: jose.RS256, Key: m.privateKey}
	signer, err := jose.NewSigner(signingKey, (&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", m.keyID))
	if err != nil {
		panic(err)
	}

	claims := map[string]any{
		"iss": m.issuer,
		"sub": subject,
		"aud": audience,
		"exp": expiry.Unix(),
		"iat": time.Now().Unix(),
	}
	if nonce != "" {
		claims["nonce"] = nonce
	}

	token, err := jwt.Signed(signer).Claims(claims).CompactSerialize()
	if err != nil {
		panic(err)
	}

	return token
}

func TestNewOIDCProvider(t *testing.T) {
	t.Parallel()

	// Table-driven tests for config validation errors (no server needed)
	t.Run("config validation errors", func(t *testing.T) {
		t.Parallel()
		tests := []struct {
			name    string
			config  *OIDCConfig
			wantErr string
		}{
			{"nil config", nil, "config is required"},
			{"missing issuer", &OIDCConfig{
				CommonOAuthConfig: CommonOAuthConfig{
					ClientID:     testClientID,
					ClientSecret: testClientSecret,
					RedirectURI:  testRedirectURI,
				},
				Issuer: "",
			}, "issuer is required"},
			{"invalid issuer URL", &OIDCConfig{
				CommonOAuthConfig: CommonOAuthConfig{
					ClientID:     testClientID,
					ClientSecret: testClientSecret,
					RedirectURI:  testRedirectURI,
				},
				Issuer: "not-a-valid-url\x00",
			}, "invalid issuer URL"},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				t.Parallel()
				_, err := NewOIDCProvider(context.Background(), tt.config)
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
			})
		}
	})

	t.Run("valid config creates provider successfully", func(t *testing.T) {
		t.Parallel()

		mock := newMockOIDCServer(t)
		t.Cleanup(mock.Close)

		config := &OIDCConfig{
			CommonOAuthConfig: CommonOAuthConfig{
				ClientID:     testClientID,
				ClientSecret: testClientSecret,
				RedirectURI:  testRedirectURI,
				Scopes:       []string{"openid", "profile", "email"},
			},
			Issuer: mock.issuer,
		}

		ctx := context.Background()
		provider, err := NewOIDCProvider(ctx, config)
		require.NoError(t, err)
		require.NotNil(t, provider)
		assert.Equal(t, ProviderTypeOIDC, provider.Type())
	})

	t.Run("discovery failure returns error", func(t *testing.T) {
		t.Parallel()

		// Server that returns 404 for discovery
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNotFound)
		}))
		t.Cleanup(server.Close)

		config := &OIDCConfig{
			CommonOAuthConfig: CommonOAuthConfig{
				ClientID:     testClientID,
				ClientSecret: testClientSecret,
				RedirectURI:  testRedirectURI,
			},
			Issuer: server.URL,
		}

		ctx := context.Background()
		_, err := NewOIDCProvider(ctx, config)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed to discover OIDC endpoints")
	})

	t.Run("issuer mismatch returns error", func(t *testing.T) {
		t.Parallel()

		// Server that returns a different issuer
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			doc := map[string]any{
				"issuer":                 "https://wrong-issuer.example.com",
				"authorization_endpoint": "https://wrong-issuer.example.com/authorize",
				"token_endpoint":         "https://wrong-issuer.example.com/token",
				"jwks_uri":               "https://wrong-issuer.example.com/jwks",
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(doc)
		}))
		t.Cleanup(server.Close)

		config := &OIDCConfig{
			CommonOAuthConfig: CommonOAuthConfig{
				ClientID:     testClientID,
				ClientSecret: testClientSecret,
				RedirectURI:  testRedirectURI,
			},
			Issuer: server.URL,
		}

		ctx := context.Background()
		_, err := NewOIDCProvider(ctx, config)
		require.Error(t, err)
		// go-oidc validates issuer mismatch
		assert.Contains(t, err.Error(), "issuer")
	})

	t.Run("default scopes when not specified", func(t *testing.T) {
		t.Parallel()

		mock := newMockOIDCServer(t)
		t.Cleanup(mock.Close)

		config := &OIDCConfig{
			CommonOAuthConfig: CommonOAuthConfig{
				ClientID:     testClientID,
				ClientSecret: testClientSecret,
				RedirectURI:  testRedirectURI,
				// No scopes specified
			},
			Issuer: mock.issuer,
		}

		ctx := context.Background()
		provider, err := NewOIDCProvider(ctx, config)
		require.NoError(t, err)
		require.NotNil(t, provider)

		// Verify by checking the authorization URL includes default scopes
		// Uses embedded BaseOAuth2Provider's method
		authURL, err := provider.AuthorizationURL("test-state", "")
		require.NoError(t, err)
		parsed, err := url.Parse(authURL)
		require.NoError(t, err)
		scope := parsed.Query().Get("scope")
		assert.Contains(t, scope, "openid")
		assert.Contains(t, scope, "profile")
		assert.Contains(t, scope, "email")
	})

	t.Run("with custom HTTP client", func(t *testing.T) {
		t.Parallel()

		mock := newMockOIDCServer(t)
		t.Cleanup(mock.Close)

		config := &OIDCConfig{
			CommonOAuthConfig: CommonOAuthConfig{
				ClientID:     testClientID,
				ClientSecret: testClientSecret,
				RedirectURI:  testRedirectURI,
			},
			Issuer: mock.issuer,
		}

		customClient := &http.Client{Timeout: 5 * time.Second}

		ctx := context.Background()
		provider, err := NewOIDCProvider(ctx, config, WithHTTPClient(customClient))
		require.NoError(t, err)
		require.NotNil(t, provider)
	})

	t.Run("with force consent screen", func(t *testing.T) {
		t.Parallel()

		mock := newMockOIDCServer(t)
		t.Cleanup(mock.Close)

		config := &OIDCConfig{
			CommonOAuthConfig: CommonOAuthConfig{
				ClientID:     testClientID,
				ClientSecret: testClientSecret,
				RedirectURI:  testRedirectURI,
			},
			Issuer: mock.issuer,
		}

		ctx := context.Background()
		provider, err := NewOIDCProvider(ctx, config, WithForceConsentScreen(true))
		require.NoError(t, err)
		require.NotNil(t, provider)
		// Force consent screen is tested in commit 2 with AuthorizationURL tests
	})
}

func TestValidateDiscoveryDocument(t *testing.T) {
	t.Parallel()

	// Note: issuer mismatch is validated by go-oidc's NewProvider() before
	// validateDiscoveryDocument is called, so we don't test it here.
	tests := []struct {
		name    string
		modify  func(*oauthproto.OIDCDiscoveryDocument)
		wantErr string
	}{
		{"valid document", nil, ""},
		{"missing authorization endpoint", func(d *oauthproto.OIDCDiscoveryDocument) { d.AuthorizationEndpoint = "" }, "missing authorization_endpoint"},
		{"missing token endpoint", func(d *oauthproto.OIDCDiscoveryDocument) { d.TokenEndpoint = "" }, "missing token_endpoint"},
		{"missing jwks_uri", func(d *oauthproto.OIDCDiscoveryDocument) { d.JWKSURI = "" }, "missing jwks_uri"},
		{"missing response_types_supported", func(d *oauthproto.OIDCDiscoveryDocument) { d.ResponseTypesSupported = nil }, "missing response_types_supported"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			doc := &oauthproto.OIDCDiscoveryDocument{}
			doc.Issuer = testIssuer
			doc.AuthorizationEndpoint = testAuthEndpoint
			doc.TokenEndpoint = testTokenEndpoint
			doc.UserinfoEndpoint = testUserinfoURL
			doc.JWKSURI = testJWKSURI
			doc.ResponseTypesSupported = []string{"code"}

			if tt.modify != nil {
				tt.modify(doc)
			}
			err := validateDiscoveryDocument(doc, testIssuer)
			if tt.wantErr == "" {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
			}
		})
	}
}

func TestValidateEndpointOrigin(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		endpoint string
		issuer   string
		wantErr  string
	}{
		{"HTTPS endpoint with same host", "https://example.com/token", "https://example.com", ""},
		{"HTTPS endpoint with different host", "https://oauth.example.com/token", "https://example.com", ""}, // allowed per OIDC spec
		{"HTTP endpoint for non-localhost issuer", "http://example.com/token", "https://example.com", "scheme mismatch"},
		{"localhost allows HTTP", "http://localhost:8080/token", "http://localhost:8080", ""},
		{"localhost issuer requires localhost endpoint", "http://example.com/token", "http://localhost:8080", "host mismatch"},
		{"127.0.0.1 treated as localhost", "http://127.0.0.1:8080/token", "http://127.0.0.1:8080", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := validateEndpointOrigin(tt.endpoint, tt.issuer)
			if tt.wantErr == "" {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
			}
		})
	}
}

func TestOIDCProviderImpl_ResolveIdentity(t *testing.T) {
	t.Parallel()

	mock := newMockOIDCServer(t)
	t.Cleanup(mock.Close)

	config := &OIDCConfig{
		CommonOAuthConfig: CommonOAuthConfig{
			ClientID:     testClientID,
			ClientSecret: testClientSecret,
			RedirectURI:  testRedirectURI,
		},
		Issuer: mock.issuer,
	}

	ctx := context.Background()
	provider, err := NewOIDCProvider(ctx, config)
	require.NoError(t, err)

	t.Run("valid ID token returns subject", func(t *testing.T) {
		t.Parallel()
		idToken := mock.signIDToken(testClientID, "user-123", "", time.Now().Add(time.Hour))
		tokens := &Tokens{
			AccessToken: "test-access-token",
			IDToken:     idToken,
		}
		subject, err := provider.ResolveIdentity(ctx, tokens, "")
		require.NoError(t, err)
		assert.Equal(t, "user-123", subject)
	})

	t.Run("valid ID token with nonce returns subject", func(t *testing.T) {
		t.Parallel()
		idToken := mock.signIDToken(testClientID, "user-456", "test-nonce", time.Now().Add(time.Hour))
		tokens := &Tokens{
			AccessToken: "test-access-token",
			IDToken:     idToken,
		}
		subject, err := provider.ResolveIdentity(ctx, tokens, "test-nonce")
		require.NoError(t, err)
		assert.Equal(t, "user-456", subject)
	})

	t.Run("nonce mismatch returns error", func(t *testing.T) {
		t.Parallel()
		idToken := mock.signIDToken(testClientID, "user-123", "token-nonce", time.Now().Add(time.Hour))
		tokens := &Tokens{
			AccessToken: "test-access-token",
			IDToken:     idToken,
		}
		_, err := provider.ResolveIdentity(ctx, tokens, "different-nonce")
		require.Error(t, err)
		require.ErrorIs(t, err, ErrIdentityResolutionFailed)
	})

	t.Run("missing nonce in token when expected returns error", func(t *testing.T) {
		t.Parallel()
		// Sign ID token without nonce
		idToken := mock.signIDToken(testClientID, "user-123", "", time.Now().Add(time.Hour))
		tokens := &Tokens{
			AccessToken: "test-access-token",
			IDToken:     idToken,
		}
		// But caller expects a nonce - this should fail
		// (detailed error logged at DEBUG, generic error returned for security)
		_, err := provider.ResolveIdentity(ctx, tokens, "expected-nonce")
		require.Error(t, err)
		require.ErrorIs(t, err, ErrIdentityResolutionFailed)
	})

	// Error cases table
	tests := []struct {
		name        string
		tokens      *Tokens
		wantContain string // empty means just check ErrorIs
	}{
		{"invalid ID token returns validation error", &Tokens{AccessToken: "test-access-token", IDToken: "dummy-id-token"}, "ID token validation failed"},
		{"without ID token returns error", &Tokens{AccessToken: "test-access-token"}, "ID token required"},
		{"nil tokens returns error", nil, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := provider.ResolveIdentity(ctx, tt.tokens, "")
			require.ErrorIs(t, err, ErrIdentityResolutionFailed)
			if tt.wantContain != "" {
				assert.Contains(t, err.Error(), tt.wantContain)
			}
		})
	}
}

func TestOIDCProvider_AuthorizationURL(t *testing.T) {
	t.Parallel()

	mock := newMockOIDCServer(t)
	t.Cleanup(mock.Close)

	config := &OIDCConfig{
		CommonOAuthConfig: CommonOAuthConfig{
			ClientID:     testClientID,
			ClientSecret: testClientSecret,
			RedirectURI:  testRedirectURI,
			Scopes:       []string{"openid", "profile"},
		},
		Issuer: mock.issuer,
	}

	ctx := context.Background()
	provider, err := NewOIDCProvider(ctx, config)
	require.NoError(t, err)

	tests := []struct {
		name          string
		state         string
		codeChallenge string
		opts          []AuthorizationOption
		wantParams    map[string]string // exact match
		wantContains  map[string]string // substring match
		wantErr       string
	}{
		{
			name:  "builds correct URL with all parameters",
			state: "test-state",
			wantParams: map[string]string{
				"response_type": "code",
				"client_id":     testClientID,
				"redirect_uri":  testRedirectURI,
				"state":         "test-state",
			},
			wantContains: map[string]string{"scope": "openid"},
		},
		{
			name:          "includes PKCE code_challenge when provided",
			state:         "test-state",
			codeChallenge: "test-challenge-abc123",
			wantParams: map[string]string{
				"code_challenge":        "test-challenge-abc123",
				"code_challenge_method": "S256",
			},
		},
		{
			name:  "includes nonce with WithNonce option",
			state: "test-state",
			opts:  []AuthorizationOption{WithNonce("test-nonce-123")},
			wantParams: map[string]string{
				"nonce": "test-nonce-123",
			},
		},
		{
			name:  "includes additional params",
			state: "test-state",
			opts: []AuthorizationOption{WithAdditionalParams(map[string]string{
				"login_hint": "user@example.com",
				"acr_values": "urn:mace:incommon:iap:silver",
			})},
			wantParams: map[string]string{
				"login_hint": "user@example.com",
				"acr_values": "urn:mace:incommon:iap:silver",
			},
		},
		{
			name:    "returns error for empty state",
			state:   "",
			wantErr: "state parameter is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			authURL, err := provider.AuthorizationURL(tt.state, tt.codeChallenge, tt.opts...)

			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}

			require.NoError(t, err)
			parsed, err := url.Parse(authURL)
			require.NoError(t, err)

			query := parsed.Query()
			for key, want := range tt.wantParams {
				assert.Equal(t, want, query.Get(key), "param %s", key)
			}
			for key, want := range tt.wantContains {
				assert.Contains(t, query.Get(key), want, "param %s", key)
			}
		})
	}
}

func TestOIDCProvider_ExchangeCode(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	t.Run("successful token exchange with ID token", func(t *testing.T) {
		t.Parallel()

		mock := newMockOIDCServer(t)
		t.Cleanup(mock.Close)

		var receivedParams url.Values
		mock.tokenHandler = func(w http.ResponseWriter, r *http.Request) {
			if err := r.ParseForm(); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			receivedParams = r.PostForm

			idToken := mock.signIDToken(testClientID, "user-123", "", time.Now().Add(time.Hour))

			resp := testTokenResponse{
				AccessToken:  "exchanged-access-token",
				TokenType:    "Bearer",
				RefreshToken: "exchanged-refresh-token",
				IDToken:      idToken,
				ExpiresIn:    7200,
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(resp)
		}

		config := &OIDCConfig{
			CommonOAuthConfig: CommonOAuthConfig{
				ClientID:     testClientID,
				ClientSecret: testClientSecret,
				RedirectURI:  testRedirectURI,
			},
			Issuer: mock.issuer,
		}

		provider, err := NewOIDCProvider(ctx, config)
		require.NoError(t, err)

		tokens, err := provider.ExchangeCode(ctx, "test-auth-code", "test-verifier")
		require.NoError(t, err)

		// Verify request parameters
		assert.Equal(t, "authorization_code", receivedParams.Get("grant_type"))
		assert.Equal(t, "test-auth-code", receivedParams.Get("code"))
		assert.Equal(t, "test-verifier", receivedParams.Get("code_verifier"))

		// Verify response
		assert.Equal(t, "exchanged-access-token", tokens.AccessToken)
		assert.Equal(t, "exchanged-refresh-token", tokens.RefreshToken)
		assert.NotEmpty(t, tokens.IDToken)
	})

	t.Run("empty code returns error", func(t *testing.T) {
		t.Parallel()

		mock := newMockOIDCServer(t)
		t.Cleanup(mock.Close)

		config := &OIDCConfig{
			CommonOAuthConfig: CommonOAuthConfig{
				ClientID:     testClientID,
				ClientSecret: testClientSecret,
				RedirectURI:  testRedirectURI,
			},
			Issuer: mock.issuer,
		}

		provider, err := NewOIDCProvider(ctx, config)
		require.NoError(t, err)

		_, err = provider.ExchangeCode(ctx, "", "")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "authorization code is required")
	})

	t.Run("invalid ID token fails validation", func(t *testing.T) {
		t.Parallel()

		mock := newMockOIDCServer(t)
		t.Cleanup(mock.Close)

		mock.tokenHandler = func(w http.ResponseWriter, _ *http.Request) {
			resp := testTokenResponse{
				AccessToken: "access-token",
				TokenType:   "Bearer",
				IDToken:     "invalid.token.here",
				ExpiresIn:   3600,
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(resp)
		}

		config := &OIDCConfig{
			CommonOAuthConfig: CommonOAuthConfig{
				ClientID:     testClientID,
				ClientSecret: testClientSecret,
				RedirectURI:  testRedirectURI,
			},
			Issuer: mock.issuer,
		}

		provider, err := NewOIDCProvider(ctx, config)
		require.NoError(t, err)

		_, err = provider.ExchangeCode(ctx, "test-code", "")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "ID token validation failed")
	})

	t.Run("token endpoint error", func(t *testing.T) {
		t.Parallel()

		mock := newMockOIDCServer(t)
		t.Cleanup(mock.Close)

		mock.tokenHandler = func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			resp := testTokenErrorResponse{
				Error:            "invalid_grant",
				ErrorDescription: "The authorization code has expired",
			}
			_ = json.NewEncoder(w).Encode(resp)
		}

		config := &OIDCConfig{
			CommonOAuthConfig: CommonOAuthConfig{
				ClientID:     testClientID,
				ClientSecret: testClientSecret,
				RedirectURI:  testRedirectURI,
			},
			Issuer: mock.issuer,
		}

		provider, err := NewOIDCProvider(ctx, config)
		require.NoError(t, err)

		_, err = provider.ExchangeCode(ctx, "expired-code", "")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "invalid_grant")
	})
}

func TestOIDCProvider_RefreshTokens(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	t.Run("successful token refresh", func(t *testing.T) {
		t.Parallel()

		mock := newMockOIDCServer(t)
		t.Cleanup(mock.Close)

		var receivedParams url.Values
		mock.tokenHandler = func(w http.ResponseWriter, r *http.Request) {
			if err := r.ParseForm(); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			receivedParams = r.PostForm

			resp := testTokenResponse{
				AccessToken:  "refreshed-access-token",
				TokenType:    "Bearer",
				RefreshToken: "new-refresh-token",
				ExpiresIn:    3600,
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(resp)
		}

		config := &OIDCConfig{
			CommonOAuthConfig: CommonOAuthConfig{
				ClientID:     testClientID,
				ClientSecret: testClientSecret,
				RedirectURI:  testRedirectURI,
			},
			Issuer: mock.issuer,
		}

		provider, err := NewOIDCProvider(ctx, config)
		require.NoError(t, err)

		tokens, err := provider.RefreshTokens(ctx, "old-refresh-token", "")
		require.NoError(t, err)

		// Verify request parameters
		assert.Equal(t, "refresh_token", receivedParams.Get("grant_type"))
		assert.Equal(t, "old-refresh-token", receivedParams.Get("refresh_token"))

		// Verify response
		assert.Equal(t, "refreshed-access-token", tokens.AccessToken)
		assert.Equal(t, "new-refresh-token", tokens.RefreshToken)
	})

	t.Run("empty refresh token returns error", func(t *testing.T) {
		t.Parallel()

		mock := newMockOIDCServer(t)
		t.Cleanup(mock.Close)

		config := &OIDCConfig{
			CommonOAuthConfig: CommonOAuthConfig{
				ClientID:     testClientID,
				ClientSecret: testClientSecret,
				RedirectURI:  testRedirectURI,
			},
			Issuer: mock.issuer,
		}

		provider, err := NewOIDCProvider(ctx, config)
		require.NoError(t, err)

		_, err = provider.RefreshTokens(ctx, "", "")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "refresh token is required")
	})

	t.Run("refresh with matching subject succeeds", func(t *testing.T) {
		t.Parallel()

		mock := newMockOIDCServer(t)
		t.Cleanup(mock.Close)

		mock.tokenHandler = func(w http.ResponseWriter, _ *http.Request) {
			idToken := mock.signIDToken(testClientID, "user-123", "", time.Now().Add(time.Hour))
			resp := testTokenResponse{
				AccessToken:  "refreshed-access-token",
				TokenType:    "Bearer",
				RefreshToken: "new-refresh-token",
				ExpiresIn:    3600,
				IDToken:      idToken,
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(resp)
		}

		config := &OIDCConfig{
			CommonOAuthConfig: CommonOAuthConfig{
				ClientID:     testClientID,
				ClientSecret: testClientSecret,
				RedirectURI:  testRedirectURI,
			},
			Issuer: mock.issuer,
		}

		provider, err := NewOIDCProvider(ctx, config)
		require.NoError(t, err)

		tokens, err := provider.RefreshTokens(ctx, "old-refresh-token", "user-123")
		require.NoError(t, err)
		assert.Equal(t, "refreshed-access-token", tokens.AccessToken)
	})

	t.Run("refresh with mismatched subject returns ErrSubjectMismatch", func(t *testing.T) {
		t.Parallel()

		mock := newMockOIDCServer(t)
		t.Cleanup(mock.Close)

		mock.tokenHandler = func(w http.ResponseWriter, _ *http.Request) {
			idToken := mock.signIDToken(testClientID, "user-123", "", time.Now().Add(time.Hour))
			resp := testTokenResponse{
				AccessToken:  "refreshed-access-token",
				TokenType:    "Bearer",
				RefreshToken: "new-refresh-token",
				ExpiresIn:    3600,
				IDToken:      idToken,
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(resp)
		}

		config := &OIDCConfig{
			CommonOAuthConfig: CommonOAuthConfig{
				ClientID:     testClientID,
				ClientSecret: testClientSecret,
				RedirectURI:  testRedirectURI,
			},
			Issuer: mock.issuer,
		}

		provider, err := NewOIDCProvider(ctx, config)
		require.NoError(t, err)

		_, err = provider.RefreshTokens(ctx, "old-refresh-token", "different-user")
		require.ErrorIs(t, err, ErrSubjectMismatch)
	})

	t.Run("refresh without ID token skips subject validation", func(t *testing.T) {
		t.Parallel()

		mock := newMockOIDCServer(t)
		t.Cleanup(mock.Close)

		mock.tokenHandler = func(w http.ResponseWriter, _ *http.Request) {
			resp := testTokenResponse{
				AccessToken:  "refreshed-access-token",
				TokenType:    "Bearer",
				RefreshToken: "new-refresh-token",
				ExpiresIn:    3600,
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(resp)
		}

		config := &OIDCConfig{
			CommonOAuthConfig: CommonOAuthConfig{
				ClientID:     testClientID,
				ClientSecret: testClientSecret,
				RedirectURI:  testRedirectURI,
			},
			Issuer: mock.issuer,
		}

		provider, err := NewOIDCProvider(ctx, config)
		require.NoError(t, err)

		tokens, err := provider.RefreshTokens(ctx, "old-refresh-token", "user-123")
		require.NoError(t, err)
		assert.Equal(t, "refreshed-access-token", tokens.AccessToken)
	})

	t.Run("refresh with empty expectedSubject skips subject validation", func(t *testing.T) {
		t.Parallel()

		mock := newMockOIDCServer(t)
		t.Cleanup(mock.Close)

		mock.tokenHandler = func(w http.ResponseWriter, _ *http.Request) {
			idToken := mock.signIDToken(testClientID, "user-123", "", time.Now().Add(time.Hour))
			resp := testTokenResponse{
				AccessToken:  "refreshed-access-token",
				TokenType:    "Bearer",
				RefreshToken: "new-refresh-token",
				ExpiresIn:    3600,
				IDToken:      idToken,
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(resp)
		}

		config := &OIDCConfig{
			CommonOAuthConfig: CommonOAuthConfig{
				ClientID:     testClientID,
				ClientSecret: testClientSecret,
				RedirectURI:  testRedirectURI,
			},
			Issuer: mock.issuer,
		}

		provider, err := NewOIDCProvider(ctx, config)
		require.NoError(t, err)

		tokens, err := provider.RefreshTokens(ctx, "old-refresh-token", "")
		require.NoError(t, err)
		assert.Equal(t, "refreshed-access-token", tokens.AccessToken)
	})
}

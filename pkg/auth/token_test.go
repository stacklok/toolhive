package auth

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/lestrrat-go/jwx/v3/jwk"
)

const (
	testKeyID = "test-key-1"
	expClaim  = "exp"
	issuer    = "https://issuer.example.com"
)

//nolint:gocyclo // This test function is complex but manageable
func TestTokenValidator(t *testing.T) {
	t.Parallel()
	// Generate a new RSA key pair for signing tokens
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("Failed to generate RSA key pair: %v", err)
	}
	publicKey := &privateKey.PublicKey

	// Create a key set with the public key
	key, err := jwk.Import(publicKey)
	if err != nil {
		t.Fatalf("Failed to create JWK from public key: %v", err)
	}

	// Set key ID and other properties
	if err := key.Set(jwk.KeyIDKey, testKeyID); err != nil {
		t.Fatalf("Failed to set key ID: %v", err)
	}
	if err := key.Set(jwk.AlgorithmKey, "RS256"); err != nil {
		t.Fatalf("Failed to set algorithm: %v", err)
	}
	if err := key.Set(jwk.KeyUsageKey, "sig"); err != nil {
		t.Fatalf("Failed to set key usage: %v", err)
	}

	// Create a key set
	keySet := jwk.NewSet()
	if err := keySet.AddKey(key); err != nil {
		t.Fatalf("Failed to add key to set: %v", err)
	}

	// Create a test JWKS server with TLS
	jwksServer, caCertPath := createTestJWKSServer(t, keySet)
	t.Cleanup(func() {
		jwksServer.Close()
	})

	// Create a context for the test
	ctx := context.Background()

	// Create a JWT validator
	validator, err := NewTokenValidator(ctx, TokenValidatorConfig{
		Issuer:         "test-issuer",
		Audience:       "test-audience",
		JWKSURL:        jwksServer.URL,
		ClientID:       "test-client",
		CACertPath:     caCertPath,
		AllowPrivateIP: true,
	})
	if err != nil {
		t.Fatalf("Failed to create token validator: %v", err)
	}

	// Ensure JWKS is registered before lookup
	err = validator.ensureJWKSRegistered(ctx)
	if err != nil {
		t.Fatalf("Failed to register JWKS: %v", err)
	}

	// Force a refresh of the JWKS cache
	_, err = validator.jwksClient.Lookup(ctx, jwksServer.URL)
	if err != nil {
		t.Fatalf("Failed to refresh JWKS cache: %v", err)
	}

	// Test cases
	testCases := []struct {
		name      string
		claims    jwt.MapClaims
		expectErr bool
		errType   error
	}{
		{
			name: "Valid token",
			claims: jwt.MapClaims{
				"iss": "test-issuer",
				"aud": "test-audience",
				"exp": time.Now().Add(time.Hour).Unix(),
			},
			expectErr: false,
		},
		{
			name: "Invalid issuer",
			claims: jwt.MapClaims{
				"iss": "wrong-issuer",
				"aud": "test-audience",
				"exp": time.Now().Add(time.Hour).Unix(),
			},
			expectErr: true,
			errType:   ErrInvalidIssuer,
		},
		{
			name: "Invalid audience",
			claims: jwt.MapClaims{
				"iss": "test-issuer",
				"aud": "wrong-audience",
				"exp": time.Now().Add(time.Hour).Unix(),
			},
			expectErr: true,
			errType:   ErrInvalidAudience,
		},
		{
			name: "Expired token",
			claims: jwt.MapClaims{
				"iss": "test-issuer",
				"aud": "test-audience",
				"exp": time.Now().Add(-time.Hour).Unix(),
			},
			expectErr: true,
			// The JWT library returns its own error for expired tokens
			errType: nil,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			// Create a token with the test claims
			token := jwt.NewWithClaims(jwt.SigningMethodRS256, tc.claims)
			token.Header["kid"] = testKeyID

			// Sign the token
			tokenString, err := token.SignedString(privateKey)
			if err != nil {
				t.Fatalf("Failed to sign token: %v", err)
			}

			// Validate the token
			_, err = validator.ValidateToken(context.Background(), tokenString)

			// Check the result
			if tc.expectErr {
				if err == nil {
					t.Errorf("Expected error but got nil")
				} else if tc.errType != nil && err != tc.errType {
					t.Errorf("Expected error %v but got %v", tc.errType, err)
				}
			} else {
				if err != nil {
					t.Errorf("Expected no error but got %v", err)
				}
			}
		})
	}
}

//nolint:gocyclo // This test function is complex but manageable
func TestTokenValidatorMiddleware(t *testing.T) {
	t.Parallel()
	// Generate a new RSA key pair for signing tokens
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("Failed to generate RSA key pair: %v", err)
	}
	publicKey := &privateKey.PublicKey

	// Create a key set with the public key
	key, err := jwk.Import(publicKey)
	if err != nil {
		t.Fatalf("Failed to create JWK from public key: %v", err)
	}

	// Set key ID and other properties
	if err := key.Set(jwk.KeyIDKey, testKeyID); err != nil {
		t.Fatalf("Failed to set key ID: %v", err)
	}
	if err := key.Set(jwk.AlgorithmKey, "RS256"); err != nil {
		t.Fatalf("Failed to set algorithm: %v", err)
	}
	if err := key.Set(jwk.KeyUsageKey, "sig"); err != nil {
		t.Fatalf("Failed to set key usage: %v", err)
	}

	// Create a key set
	keySet := jwk.NewSet()
	if err := keySet.AddKey(key); err != nil {
		t.Fatalf("Failed to add key to set: %v", err)
	}

	// Create a test JWKS server with TLS
	jwksServer, caCertPath := createTestJWKSServer(t, keySet)
	t.Cleanup(func() {
		jwksServer.Close()
	})

	// Create a context for the test
	ctx := context.Background()

	// Create a JWT validator
	validator, err := NewTokenValidator(ctx, TokenValidatorConfig{
		Issuer:         "test-issuer",
		Audience:       "test-audience",
		JWKSURL:        jwksServer.URL,
		ClientID:       "test-client",
		CACertPath:     caCertPath,
		AllowPrivateIP: true,
	})
	if err != nil {
		t.Fatalf("Failed to create token validator: %v", err)
	}

	// Ensure JWKS is registered before lookup
	err = validator.ensureJWKSRegistered(ctx)
	if err != nil {
		t.Fatalf("Failed to register JWKS: %v", err)
	}

	// Force a refresh of the JWKS cache
	_, err = validator.jwksClient.Lookup(ctx, jwksServer.URL)
	if err != nil {
		t.Fatalf("Failed to refresh JWKS cache: %v", err)
	}

	// Create a test handler
	testHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Get the claims from the context using the proper key type
		claims, ok := r.Context().Value(ClaimsContextKey{}).(jwt.MapClaims)
		if !ok {
			t.Errorf("Failed to get claims from context")
			http.Error(w, "Failed to get claims from context", http.StatusInternalServerError)
			return
		}

		// Write the claims as the response
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(claims); err != nil {
			t.Errorf("Failed to encode claims: %v", err)
			http.Error(w, fmt.Sprintf("Failed to encode claims: %v", err), http.StatusInternalServerError)
			return
		}
	})

	// Create a middleware handler
	handler := validator.Middleware(testHandler)

	// Test cases
	testCases := []struct {
		name           string
		claims         jwt.MapClaims
		expectStatus   int
		expectResponse bool
	}{
		{
			name: "Valid token",
			claims: jwt.MapClaims{
				"iss": "test-issuer",
				"aud": "test-audience",
				"exp": time.Now().Add(time.Hour).Unix(),
				"sub": "test-user",
			},
			expectStatus:   http.StatusOK,
			expectResponse: true,
		},
		{
			name: "Invalid issuer",
			claims: jwt.MapClaims{
				"iss": "wrong-issuer",
				"aud": "test-audience",
				"exp": time.Now().Add(time.Hour).Unix(),
			},
			expectStatus:   http.StatusUnauthorized,
			expectResponse: false,
		},
		{
			name: "Invalid audience",
			claims: jwt.MapClaims{
				"iss": "test-issuer",
				"aud": "wrong-audience",
				"exp": time.Now().Add(time.Hour).Unix(),
			},
			expectStatus:   http.StatusUnauthorized,
			expectResponse: false,
		},
		{
			name: "Expired token",
			claims: jwt.MapClaims{
				"iss": "test-issuer",
				"aud": "test-audience",
				"exp": time.Now().Add(-time.Hour).Unix(),
			},
			expectStatus:   http.StatusUnauthorized,
			expectResponse: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			// Create a token with the test claims
			token := jwt.NewWithClaims(jwt.SigningMethodRS256, tc.claims)
			token.Header["kid"] = testKeyID

			// Sign the token
			tokenString, err := token.SignedString(privateKey)
			if err != nil {
				t.Fatalf("Failed to sign token: %v", err)
			}

			// Create a test request
			req := httptest.NewRequest("GET", "/", nil)
			req.Header.Set("Authorization", "Bearer "+tokenString)

			// Create a test response recorder
			rec := httptest.NewRecorder()

			// Serve the request
			handler.ServeHTTP(rec, req)

			// Check the response
			if rec.Code != tc.expectStatus {
				t.Errorf("Expected status %d but got %d", tc.expectStatus, rec.Code)
			}

			if tc.expectResponse {
				// Parse the response
				var respClaims jwt.MapClaims
				if err := json.NewDecoder(rec.Body).Decode(&respClaims); err != nil {
					t.Errorf("Failed to decode response: %v", err)
				}

				// Check the claims (except exp which might be formatted differently)
				for k, v := range tc.claims {
					if k == expClaim {
						// Skip exact comparison for exp claim
						continue
					}
					if respClaims[k] != v {
						t.Errorf("Expected claim %s to be %v but got %v", k, v, respClaims[k])
					}
				}
			}
		})
	}
}

// createTestOIDCServer creates a test OIDC discovery server that returns the given JWKS URL
func createTestOIDCServer(_ *testing.T, jwksURL string) *httptest.Server {
	return httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/.well-known/openid-configuration" {
			http.NotFound(w, r)
			return
		}

		// Use the request's host to construct the issuer URL
		scheme := "http"
		if r.TLS != nil {
			scheme = "https"
		}
		issuerURL := fmt.Sprintf("%s://%s", scheme, r.Host)

		doc := OIDCDiscoveryDocument{
			Issuer:                issuerURL,
			AuthorizationEndpoint: issuerURL + "/auth",
			TokenEndpoint:         issuerURL + "/token",
			UserinfoEndpoint:      issuerURL + "/userinfo",
			JWKSURI:               jwksURL,
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(doc)
	}))
}

// writeTestServerCert extracts the TLS certificate from a test server and writes it to a temp file
func writeTestServerCert(t *testing.T, server *httptest.Server) string {
	t.Helper()

	cert := server.Certificate()
	if cert == nil {
		t.Fatal("Test server has no certificate")
		return ""
	}

	// Create temp file
	tmpFile, err := os.CreateTemp("", "test-ca-*.crt")
	if err != nil {
		t.Fatalf("Failed to create temp file: %v", err)
	}
	t.Cleanup(func() {
		os.Remove(tmpFile.Name())
	})

	// Write PEM encoded certificate
	if err := pem.Encode(tmpFile, &pem.Block{
		Type:  "CERTIFICATE",
		Bytes: cert.Raw,
	}); err != nil {
		t.Fatalf("Failed to write certificate: %v", err)
	}

	if err := tmpFile.Close(); err != nil {
		t.Fatalf("Failed to close temp file: %v", err)
	}

	return tmpFile.Name()
}

// createTestJWKSServer creates a test JWKS server with TLS and returns the server and CA cert path
func createTestJWKSServer(t *testing.T, keySet jwk.Set) (*httptest.Server, string) {
	t.Helper()

	// Create a test JWKS server
	jwksServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// Marshal the key set to JSON
		buf, err := json.Marshal(keySet)
		if err != nil {
			t.Fatalf("Failed to marshal key set: %v", err)
		}

		// Set the content type
		w.Header().Set("Content-Type", "application/json")

		// Write the response
		if _, err := w.Write(buf); err != nil {
			t.Fatalf("Failed to write response: %v", err)
		}
	}))

	// Extract the test server's certificate
	caCertPath := writeTestServerCert(t, jwksServer)

	return jwksServer, caCertPath
}

func TestDiscoverOIDCConfiguration(t *testing.T) {
	t.Parallel()

	// Create a test OIDC discovery server
	oidcServer := createTestOIDCServer(t, "https://example.com/jwks")
	t.Cleanup(func() {
		oidcServer.Close()
	})

	// Extract the test server's certificate to a temp CA bundle file
	caCertPath := writeTestServerCert(t, oidcServer)

	ctx := context.Background()

	t.Run("successful discovery", func(t *testing.T) {
		t.Parallel()
		doc, err := discoverOIDCConfiguration(ctx, oidcServer.URL, caCertPath, "", true)
		if err != nil {
			t.Fatalf("Expected no error but got %v", err)
		}

		if doc.Issuer != oidcServer.URL {
			t.Errorf("Expected issuer %s but got %s", oidcServer.URL, doc.Issuer)
		}

		expectedJWKSURI := "https://example.com/jwks"
		if doc.JWKSURI != expectedJWKSURI {
			t.Errorf("Expected JWKS URI %s but got %s", expectedJWKSURI, doc.JWKSURI)
		}
	})

	t.Run("issuer with trailing slash", func(t *testing.T) {
		t.Parallel()
		doc, err := discoverOIDCConfiguration(ctx, oidcServer.URL+"/", caCertPath, "", true)
		if err != nil {
			t.Fatalf("Expected no error but got %v", err)
		}

		if doc.Issuer != oidcServer.URL {
			t.Errorf("Expected issuer %s but got %s", oidcServer.URL, doc.Issuer)
		}
	})

	t.Run("invalid issuer URL", func(t *testing.T) {
		t.Parallel()
		_, err := discoverOIDCConfiguration(ctx, "invalid-url", "", "", false)
		if err == nil {
			t.Error("Expected error but got nil")
		}
	})

	t.Run("non-existent endpoint", func(t *testing.T) {
		t.Parallel()
		_, err := discoverOIDCConfiguration(ctx, "https://non-existent-domain.example", "", "", false)
		if err == nil {
			t.Error("Expected error but got nil")
		}
	})
}

func TestNewTokenValidatorWithOIDCDiscovery(t *testing.T) {
	t.Parallel()

	// Generate a new RSA key pair for signing tokens
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("Failed to generate RSA key pair: %v", err)
	}
	publicKey := &privateKey.PublicKey

	// Create a key set with the public key
	key, err := jwk.Import(publicKey)
	if err != nil {
		t.Fatalf("Failed to create JWK from public key: %v", err)
	}

	// Set key ID and other properties
	if err := key.Set(jwk.KeyIDKey, testKeyID); err != nil {
		t.Fatalf("Failed to set key ID: %v", err)
	}
	if err := key.Set(jwk.AlgorithmKey, "RS256"); err != nil {
		t.Fatalf("Failed to set algorithm: %v", err)
	}
	if err := key.Set(jwk.KeyUsageKey, "sig"); err != nil {
		t.Fatalf("Failed to set key usage: %v", err)
	}

	// Create a key set
	keySet := jwk.NewSet()
	if err := keySet.AddKey(key); err != nil {
		t.Fatalf("Failed to add key to set: %v", err)
	}

	// Create a test JWKS server
	jwksServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/jwks" {
			http.NotFound(w, r)
			return
		}

		// Marshal the key set to JSON
		buf, err := json.Marshal(keySet)
		if err != nil {
			t.Fatalf("Failed to marshal key set: %v", err)
		}

		// Set the content type
		w.Header().Set("Content-Type", "application/json")

		// Write the response
		if _, err := w.Write(buf); err != nil {
			t.Fatalf("Failed to write response: %v", err)
		}
	}))
	t.Cleanup(func() {
		jwksServer.Close()
	})

	// Extract certificates from both servers
	jwksCertPath := writeTestServerCert(t, jwksServer)

	// Create a test OIDC discovery server
	oidcServer := createTestOIDCServer(t, jwksServer.URL+"/jwks")
	t.Cleanup(func() {
		oidcServer.Close()
	})

	// Extract OIDC server certificate
	oidcCertPath := writeTestServerCert(t, oidcServer)

	ctx := context.Background()

	t.Run("successful OIDC discovery", func(t *testing.T) {
		t.Parallel()
		config := TokenValidatorConfig{
			Issuer:   oidcServer.URL,
			Audience: "test-audience",
			// JWKSURL is intentionally omitted to test discovery
			ClientID:       "test-client",
			CACertPath:     oidcCertPath,
			AllowPrivateIP: true,
		}

		validator, err := NewTokenValidator(ctx, config)
		if err != nil {
			t.Fatalf("Failed to create token validator: %v", err)
		}

		if validator.issuer != oidcServer.URL {
			t.Errorf("Expected issuer %s but got %s", oidcServer.URL, validator.issuer)
		}

		expectedJWKSURL := jwksServer.URL + "/jwks"
		if validator.jwksURL != expectedJWKSURL {
			t.Errorf("Expected JWKS URL %s but got %s", expectedJWKSURL, validator.jwksURL)
		}

		// Test that the validator can actually validate tokens
		claims := jwt.MapClaims{
			"iss": oidcServer.URL,
			"aud": "test-audience",
			"exp": time.Now().Add(time.Hour).Unix(),
			"sub": "test-user",
		}

		token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
		token.Header["kid"] = testKeyID

		tokenString, err := token.SignedString(privateKey)
		if err != nil {
			t.Fatalf("Failed to sign token: %v", err)
		}

		// Ensure JWKS is registered before lookup
		err = validator.ensureJWKSRegistered(ctx)
		if err != nil {
			t.Fatalf("Failed to register JWKS: %v", err)
		}

		// Force a refresh of the JWKS cache
		_, err = validator.jwksClient.Lookup(ctx, validator.jwksURL)
		if err != nil {
			t.Fatalf("Failed to refresh JWKS cache: %v", err)
		}

		validatedClaims, err := validator.ValidateToken(ctx, tokenString)
		if err != nil {
			t.Fatalf("Failed to validate token: %v", err)
		}

		if validatedClaims["sub"] != "test-user" {
			t.Errorf("Expected sub claim to be 'test-user' but got %v", validatedClaims["sub"])
		}
	})

	t.Run("explicit JWKS URL takes precedence", func(t *testing.T) {
		t.Parallel()
		explicitJWKSURL := jwksServer.URL + "/jwks"
		config := TokenValidatorConfig{
			Issuer:         oidcServer.URL,
			Audience:       "test-audience",
			JWKSURL:        explicitJWKSURL, // Explicitly provided
			ClientID:       "test-client",
			CACertPath:     jwksCertPath,
			AllowPrivateIP: true,
		}

		validator, err := NewTokenValidator(ctx, config)
		if err != nil {
			t.Fatalf("Failed to create token validator: %v", err)
		}

		// Should use the explicit JWKS URL, not discover it
		if validator.jwksURL != explicitJWKSURL {
			t.Errorf("Expected JWKS URL %s but got %s", explicitJWKSURL, validator.jwksURL)
		}
	})

	t.Run("missing issuer and JWKS URL", func(t *testing.T) {
		t.Parallel()
		config := TokenValidatorConfig{
			Audience: "test-audience",
			// Both Issuer and JWKSURL are missing
			ClientID:       "test-client",
			CACertPath:     oidcCertPath,
			AllowPrivateIP: true,
		}

		validator, err := NewTokenValidator(ctx, config)
		if err != ErrMissingIssuerAndJWKSURL {
			t.Errorf("Expected error %v but got %v", ErrMissingIssuerAndJWKSURL, err)
		}
		if validator != nil {
			t.Error("Expected validator to be nil")
		}
	})

	t.Run("failed OIDC discovery", func(t *testing.T) {
		t.Parallel()
		config := TokenValidatorConfig{
			Issuer:   "https://non-existent-domain.example",
			Audience: "test-audience",
			ClientID: "test-client",
			// No CA cert or AllowPrivateIP for this test - it should fail
		}

		validator, err := NewTokenValidator(ctx, config)
		if err == nil {
			t.Error("Expected error but got nil")
		}
		if validator != nil {
			t.Error("Expected validator to be nil")
		}

		// Check that the error is related to OIDC discovery
		if !errors.Is(err, ErrFailedToDiscoverOIDC) {
			t.Errorf("Expected error to wrap %v but got %v", ErrFailedToDiscoverOIDC, err)
		}
	})
}

func TestTokenValidator_OpaqueToken(t *testing.T) {
	t.Parallel()

	// Create a fake introspection server
	introspectionServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Simulate introspection response for opaque tokens
		if err := r.ParseForm(); err != nil {
			t.Fatalf("Failed to parse form: %v", err)
		}
		token := r.FormValue("token")
		if token == "valid-opaque-token" {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]interface{}{
				"active": true,
				"sub":    "opaque-user",
				"iss":    "opaque-issuer",
				"aud":    "opaque-audience",
				"scope":  "read:stuff",
				"exp":    time.Now().Add(1 * time.Hour).Unix(),
			})
		} else {
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"active": false,
			})
		}
	}))
	t.Cleanup(func() {
		introspectionServer.Close()
	})

	ctx := context.Background()
	// Create a token validator that only uses introspection (no JWKS URL)
	validator := &TokenValidator{
		introspectURL: introspectionServer.URL,
		clientID:      "test-client-id",
		clientSecret:  "test-client-secret",
		client:        http.DefaultClient,
		issuer:        "opaque-issuer",
		audience:      "opaque-audience",
	}

	t.Run("valid opaque token", func(t *testing.T) {
		t.Parallel()
		claims, err := validator.ValidateToken(ctx, "valid-opaque-token")
		if err != nil {
			t.Fatalf("Expected no error, got %v", err)
		}

		if claims["sub"] != "opaque-user" {
			t.Errorf("Expected sub=opaque-user, got %v", claims["sub"])
		}
		if claims["iss"] != "opaque-issuer" {
			t.Errorf("Expected iss=opaque-issuer, got %v", claims["iss"])
		}
		if claims["aud"] != "opaque-audience" {
			t.Errorf("Expected aud=opaque-audience, got %v", claims["aud"])
		}
	})

	t.Run("inactive opaque token", func(t *testing.T) {
		t.Parallel()
		_, err := validator.ValidateToken(ctx, "invalid-opaque-token")
		if err == nil {
			t.Fatal("Expected error for inactive token, got nil")
		}
		if !errors.Is(err, ErrInvalidToken) {
			t.Errorf("Expected ErrInvalidToken, got %v", err)
		}
	})
}

func TestNewAuthInfoHandler(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name         string
		issuer       string
		jwksURL      string
		resourceURL  string
		scopes       []string
		method       string
		origin       string
		expectStatus int
		expectBody   bool
		expectCORS   bool
	}{
		{
			name:         "successful GET request with all parameters",
			issuer:       "https://auth.example.com",
			jwksURL:      "https://auth.example.com/.well-known/jwks.json",
			resourceURL:  "https://api.example.com",
			scopes:       []string{"read", "write"},
			method:       "GET",
			origin:       "https://client.example.com",
			expectStatus: http.StatusOK,
			expectBody:   true,
			expectCORS:   true,
		},
		{
			name:         "successful GET request without origin",
			issuer:       "https://auth.example.com",
			jwksURL:      "https://auth.example.com/.well-known/jwks.json",
			resourceURL:  "https://api.example.com",
			scopes:       nil, // Test default scopes
			method:       "GET",
			origin:       "",
			expectStatus: http.StatusOK,
			expectBody:   true,
			expectCORS:   true,
		},
		{
			name:         "OPTIONS preflight request",
			issuer:       "https://auth.example.com",
			jwksURL:      "https://auth.example.com/.well-known/jwks.json",
			resourceURL:  "https://api.example.com",
			scopes:       []string{"openid", "profile"},
			method:       "OPTIONS",
			origin:       "https://client.example.com",
			expectStatus: http.StatusNoContent,
			expectBody:   false,
			expectCORS:   true,
		},
		{
			name:         "missing resource URL returns 404",
			issuer:       "https://auth.example.com",
			jwksURL:      "https://auth.example.com/.well-known/jwks.json",
			resourceURL:  "",
			scopes:       []string{"openid"},
			method:       "GET",
			origin:       "https://client.example.com",
			expectStatus: http.StatusNotFound,
			expectBody:   false,
			expectCORS:   true,
		},
		{
			name:         "empty issuer and jwksURL with resource URL",
			issuer:       "",
			jwksURL:      "",
			resourceURL:  "https://api.example.com",
			scopes:       []string{}, // Test empty scopes (should default to openid)
			method:       "GET",
			origin:       "https://client.example.com",
			expectStatus: http.StatusOK,
			expectBody:   true,
			expectCORS:   true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			// Create the handler
			handler := NewAuthInfoHandler(tc.issuer, tc.jwksURL, tc.resourceURL, tc.scopes)

			// Create test request
			req := httptest.NewRequest(tc.method, "/", nil)
			if tc.origin != "" {
				req.Header.Set("Origin", tc.origin)
			}

			// Create response recorder
			rec := httptest.NewRecorder()

			// Serve the request
			handler.ServeHTTP(rec, req)

			// Check status code
			if rec.Code != tc.expectStatus {
				t.Errorf("Expected status %d but got %d", tc.expectStatus, rec.Code)
			}

			// Check CORS headers if expected
			if tc.expectCORS {
				expectedOrigin := tc.origin
				if expectedOrigin == "" {
					expectedOrigin = "*"
				}
				if actualOrigin := rec.Header().Get("Access-Control-Allow-Origin"); actualOrigin != expectedOrigin {
					t.Errorf("Expected Access-Control-Allow-Origin %s but got %s", expectedOrigin, actualOrigin)
				}

				if allowMethods := rec.Header().Get("Access-Control-Allow-Methods"); allowMethods != "GET, OPTIONS" {
					t.Errorf("Expected Access-Control-Allow-Methods 'GET, OPTIONS' but got %s", allowMethods)
				}

				expectedHeaders := "mcp-protocol-version, Content-Type, Authorization"
				if allowHeaders := rec.Header().Get("Access-Control-Allow-Headers"); allowHeaders != expectedHeaders {
					t.Errorf("Expected Access-Control-Allow-Headers '%s' but got %s", expectedHeaders, allowHeaders)
				}

				if maxAge := rec.Header().Get("Access-Control-Max-Age"); maxAge != "86400" {
					t.Errorf("Expected Access-Control-Max-Age '86400' but got %s", maxAge)
				}
			}

			// Check response body if expected
			if tc.expectBody {
				var authInfo RFC9728AuthInfo
				if err := json.NewDecoder(rec.Body).Decode(&authInfo); err != nil {
					t.Fatalf("Failed to decode response body: %v", err)
				}

				// Verify the response content
				if authInfo.Resource != tc.resourceURL {
					t.Errorf("Expected resource %s but got %s", tc.resourceURL, authInfo.Resource)
				}

				if tc.issuer != "" {
					if len(authInfo.AuthorizationServers) != 1 || authInfo.AuthorizationServers[0] != tc.issuer {
						t.Errorf("Expected authorization servers [%s] but got %v", tc.issuer, authInfo.AuthorizationServers)
					}
				} else {
					if len(authInfo.AuthorizationServers) != 1 || authInfo.AuthorizationServers[0] != "" {
						t.Errorf("Expected authorization servers [''] but got %v", authInfo.AuthorizationServers)
					}
				}

				if authInfo.JWKSURI != tc.jwksURL {
					t.Errorf("Expected JWKS URI %s but got %s", tc.jwksURL, authInfo.JWKSURI)
				}

				expectedMethods := []string{"header"}
				if len(authInfo.BearerMethodsSupported) != len(expectedMethods) {
					t.Errorf("Expected bearer methods %v but got %v", expectedMethods, authInfo.BearerMethodsSupported)
				} else {
					for i, method := range expectedMethods {
						if authInfo.BearerMethodsSupported[i] != method {
							t.Errorf("Expected bearer method %s but got %s", method, authInfo.BearerMethodsSupported[i])
						}
					}
				}

				// Determine expected scopes
				expectedScopes := tc.scopes
				if len(expectedScopes) == 0 {
					expectedScopes = []string{"openid"}
				}
				if len(authInfo.ScopesSupported) != len(expectedScopes) {
					t.Errorf("Expected scopes %v but got %v", expectedScopes, authInfo.ScopesSupported)
				} else {
					for i, scope := range expectedScopes {
						if authInfo.ScopesSupported[i] != scope {
							t.Errorf("Expected scope %s but got %s", scope, authInfo.ScopesSupported[i])
						}
					}
				}

				// Check content type
				if contentType := rec.Header().Get("Content-Type"); contentType != "application/json" {
					t.Errorf("Expected Content-Type 'application/json' but got %s", contentType)
				}
			}
		})
	}
}

func parseAuthParams(ch string) map[string]string {
	out := map[string]string{}
	ch = strings.TrimSpace(ch)
	if i := strings.IndexByte(ch, ' '); i >= 0 {
		ch = strings.TrimSpace(ch[i+1:])
	}
	var parts []string
	var b strings.Builder
	inQ := false
	for i := 0; i < len(ch); i++ {
		c := ch[i]
		switch c {
		case '"':
			inQ = !inQ
			b.WriteByte(c)
		case ',':
			if inQ {
				b.WriteByte(c)
			} else {
				parts = append(parts, strings.TrimSpace(b.String()))
				b.Reset()
			}
		default:
			b.WriteByte(c)
		}
	}
	if b.Len() > 0 {
		parts = append(parts, strings.TrimSpace(b.String()))
	}
	for _, p := range parts {
		if p == "" {
			continue
		}
		kv := strings.SplitN(p, "=", 2)
		if len(kv) != 2 {
			continue
		}
		k := strings.ToLower(strings.TrimSpace(kv[0]))
		v := strings.TrimSpace(kv[1])
		if len(v) >= 2 && v[0] == '"' && v[len(v)-1] == '"' {
			v = strings.ReplaceAll(v[1:len(v)-1], `\"`, `"`)
			v = strings.ReplaceAll(v, `\\`, `\`)
		}
		out[k] = v
	}
	return out
}
func TestMiddleware_WWWAuthenticate_NoHeader_And_WrongScheme(t *testing.T) {
	t.Parallel()

	resourceMeta := "https://resource.example.com/.well-known/oauth-protected-resource"

	tests := []struct {
		name      string
		setHeader func(req *http.Request)
	}{
		{
			name:      "missing Authorization",
			setHeader: func(_ *http.Request) {},
		},
		{
			name: "wrong scheme Basic",
			setHeader: func(r *http.Request) {
				r.Header.Set("Authorization", "Basic Zm9vOmJhcg==")
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			tv := &TokenValidator{
				issuer:      issuer,
				resourceURL: resourceMeta,
			}

			hitDownstream := false
			next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				hitDownstream = true
				w.WriteHeader(http.StatusOK)
			})

			// Create a NEW server per subtest (so no cross-parallel sharing)
			srv := httptest.NewServer(tv.Middleware(next))
			t.Cleanup(srv.Close)

			req, _ := http.NewRequest("GET", srv.URL+"/", nil)
			tt.setHeader(req)

			res, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("request failed: %v", err)
			}
			defer res.Body.Close()

			if res.StatusCode != http.StatusUnauthorized {
				t.Fatalf("expected 401, got %d", res.StatusCode)
			}
			if hitDownstream {
				t.Fatalf("downstream should not have been reached on 401")
			}

			h := res.Header.Get("WWW-Authenticate")
			if h == "" {
				t.Fatalf("WWW-Authenticate header missing")
			}

			params := parseAuthParams(h)
			if got := params["realm"]; got != issuer {
				t.Fatalf("realm mismatch: want %q, got %q", issuer, got)
			}
			if v, ok := params["resource_metadata"]; ok && v == "" {
				t.Fatalf("resource_metadata present but empty")
			}
			if _, ok := params["error"]; ok {
				t.Fatalf("unexpected error param for %s", tt.name)
			}
			if _, ok := params["error_description"]; ok {
				t.Fatalf("unexpected error_description for %s", tt.name)
			}
		})
	}
}

func TestParseGoogleTokeninfoClaims(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name           string
		responseBody   string
		expectError    bool
		expectActive   bool
		expectedClaims map[string]interface{}
	}{
		{
			name: "valid Google tokeninfo response",
			responseBody: `{
				"azp": "32553540559.apps.googleusercontent.com",
				"aud": "32553540559.apps.googleusercontent.com",
				"sub": "111260650121245072906",
				"scope": "openid https://www.googleapis.com/auth/userinfo.email",
				"exp": "` + fmt.Sprintf("%d", time.Now().Add(time.Hour).Unix()) + `",
				"expires_in": "3488",
				"email": "user@example.com",
				"email_verified": "true"
			}`,
			expectError:  false,
			expectActive: true,
			expectedClaims: map[string]interface{}{
				"sub":            "111260650121245072906",
				"aud":            "32553540559.apps.googleusercontent.com",
				"scope":          "openid https://www.googleapis.com/auth/userinfo.email",
				"iss":            "https://accounts.google.com",
				"email":          "user@example.com",
				"email_verified": "true",
				"azp":            "32553540559.apps.googleusercontent.com",
				"expires_in":     "3488",
				"active":         true,
			},
		},
		{
			name: "expired Google token",
			responseBody: `{
				"azp": "32553540559.apps.googleusercontent.com",
				"aud": "32553540559.apps.googleusercontent.com",
				"sub": "111260650121245072906",
				"scope": "openid",
				"exp": "` + fmt.Sprintf("%d", time.Now().Add(-time.Hour).Unix()) + `",
				"email": "user@example.com"
			}`,
			expectError:  true,
			expectActive: false,
		},
		{
			name: "missing exp field",
			responseBody: `{
				"azp": "32553540559.apps.googleusercontent.com",
				"aud": "32553540559.apps.googleusercontent.com",
				"sub": "111260650121245072906"
			}`,
			expectError:  true,
			expectActive: false,
		},
		{
			name: "invalid exp format",
			responseBody: `{
				"azp": "32553540559.apps.googleusercontent.com",
				"aud": "32553540559.apps.googleusercontent.com",
				"sub": "111260650121245072906",
				"exp": "invalid-timestamp"
			}`,
			expectError:  true,
			expectActive: false,
		},
		{
			name:         "invalid JSON",
			responseBody: `{invalid json`,
			expectError:  true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			reader := strings.NewReader(tc.responseBody)
			claims, err := parseGoogleTokeninfoClaims(reader)

			if tc.expectError {
				if err == nil {
					t.Error("Expected error but got nil")
				}
				return
			}

			if err != nil {
				t.Errorf("Expected no error but got: %v", err)
				return
			}

			// Verify expected claims
			for key, expectedValue := range tc.expectedClaims {
				if key == expClaim {
					// Check that exp is set as float64
					if _, ok := claims["exp"].(float64); !ok {
						t.Errorf("Expected exp to be float64, got %T", claims["exp"])
					}
					continue
				}

				if claims[key] != expectedValue {
					t.Errorf("Expected claim %s to be %v, got %v", key, expectedValue, claims[key])
				}
			}
		})
	}
}

func TestMiddleware_WWWAuthenticate_InvalidOpaqueToken_NoIntrospectionConfigured(t *testing.T) {
	t.Parallel()

	tv := &TokenValidator{
		issuer: issuer,
		// introspectURL intentionally empty to force the error path
	}

	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	srv := httptest.NewServer(tv.Middleware(next))
	t.Cleanup(srv.Close)

	req, _ := http.NewRequest("GET", srv.URL+"/", nil)
	req.Header.Set("Authorization", "Bearer not-a-jwt") // triggers opaque → introspection path

	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", res.StatusCode)
	}
	h := res.Header.Get("WWW-Authenticate")
	if h == "" {
		t.Fatalf("WWW-Authenticate header missing")
	}
	p := parseAuthParams(h)
	if p["realm"] != issuer {
		t.Fatalf("realm mismatch: want %q got %q", issuer, p["realm"])
	}
	if p["error"] != "invalid_token" {
		t.Fatalf("expected error=invalid_token, got %q", p["error"])
	}
	if p["error_description"] == "" {
		t.Fatalf("expected non-empty error_description")
	}
}

func TestMiddleware_WWWAuthenticate_WithMockIntrospection(t *testing.T) {
	t.Parallel()

	// Introspection mock that varies by token value
	mux := http.NewServeMux()
	mux.HandleFunc("/introspect", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		switch r.Form.Get("token") {
		case "good":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"active": true,
				"exp":    float64(time.Now().Add(60 * time.Second).Unix()),
				"iss":    issuer,
			})
		case "inactive":
			_ = json.NewEncoder(w).Encode(map[string]any{"active": false})
		case "unauth":
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"nope"}`))
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{"active": false})
		}
	})
	introspectTS := httptest.NewServer(mux)
	t.Cleanup(introspectTS.Close)

	type tc struct {
		name       string
		auth       string
		wantStatus int
		wantError  bool
		errSubstr  string
		hitNext    bool
	}
	cases := []tc{
		{
			name:       "inactive => 401",
			auth:       "Bearer inactive",
			wantStatus: http.StatusUnauthorized,
			wantError:  true,
			hitNext:    false,
		},
		{
			name:       "unauth introspection => 401",
			auth:       "Bearer unauth",
			wantStatus: http.StatusUnauthorized,
			wantError:  true,
			errSubstr:  "introspection unauthorized",
			hitNext:    false,
		},
		{
			name:       "good => passes",
			auth:       "Bearer good",
			wantStatus: http.StatusOK,
			wantError:  false,
			hitNext:    true,
		},
	}

	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			tv := &TokenValidator{
				issuer:        issuer,
				introspectURL: introspectTS.URL + "/introspect",
				clientID:      "cid",
				clientSecret:  "csecret",
				client:        http.DefaultClient,
			}

			hit := false
			next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				hit = true
				w.WriteHeader(http.StatusOK)
			})

			// NEW: server per subtest
			srv := httptest.NewServer(tv.Middleware(next))
			t.Cleanup(srv.Close)

			req, _ := http.NewRequest("GET", srv.URL+"/", nil)
			req.Header.Set("Authorization", c.auth)
			res, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("request failed: %v", err)
			}
			defer res.Body.Close()

			if res.StatusCode != c.wantStatus {
				t.Fatalf("status mismatch: want %d got %d", c.wantStatus, res.StatusCode)
			}
			if hit != c.hitNext {
				t.Fatalf("downstream hit mismatch: want %v got %v", c.hitNext, hit)
			}

			h := res.Header.Get("WWW-Authenticate")
			if c.wantStatus == http.StatusUnauthorized {
				if h == "" {
					t.Fatalf("missing WWW-Authenticate header")
				}
				p := parseAuthParams(h)
				if p["realm"] != issuer {
					t.Fatalf("realm mismatch: %q", p["realm"])
				}
				if c.wantError && p["error"] != "invalid_token" {
					t.Fatalf("expected error=invalid_token, got %q", p["error"])
				}
				if c.errSubstr != "" && !strings.Contains(p["error_description"], c.errSubstr) {
					t.Fatalf("error_description %q missing %q", p["error_description"], c.errSubstr)
				}
			} else if h != "" {
				t.Fatalf("did not expect WWW-Authenticate header on success")
			}
		})
	}
}

func TestBuildWWWAuthenticate_Format(t *testing.T) {
	t.Parallel()
	tv := &TokenValidator{
		issuer:      "https://issuer.example.com",
		resourceURL: "https://resource.example.com/.well-known/oauth-protected-resource",
	}
	got := tv.buildWWWAuthenticate(true, `failed to parse "token", reason`)
	want := `Bearer realm="https://issuer.example.com", resource_metadata="https://resource.example.com/.well-known/oauth-protected-resource", error="invalid_token", error_description="failed to parse \"token\", reason"`
	if got != want {
		t.Fatalf("format mismatch:\nwant: %s\n got: %s", want, got)
	}
}

func TestIntrospectGoogleToken(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name           string
		token          string
		serverResponse func(w http.ResponseWriter, r *http.Request)
		expectError    bool
		expectedClaims map[string]interface{}
	}{
		{
			name:  "valid Google token",
			token: "valid-google-token",
			serverResponse: func(w http.ResponseWriter, r *http.Request) {
				// Verify it's a GET request with correct query parameter
				if r.Method != "GET" {
					t.Errorf("Expected GET request, got %s", r.Method)
				}
				if token := r.URL.Query().Get("access_token"); token != "valid-google-token" {
					t.Errorf("Expected access_token=valid-google-token, got %s", token)
				}

				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(map[string]interface{}{
					"azp":            "test-client.apps.googleusercontent.com",
					"aud":            "test-client.apps.googleusercontent.com",
					"sub":            "123456789",
					"scope":          "openid email",
					"exp":            fmt.Sprintf("%d", time.Now().Add(time.Hour).Unix()),
					"email":          "test@example.com",
					"email_verified": "true",
				})
			},
			expectError: false,
			expectedClaims: map[string]interface{}{
				"sub":            "123456789",
				"aud":            "test-client.apps.googleusercontent.com",
				"scope":          "openid email",
				"iss":            "https://accounts.google.com",
				"email":          "test@example.com",
				"email_verified": "true",
				"azp":            "test-client.apps.googleusercontent.com",
				"active":         true,
			},
		},
		{
			name:  "Google returns 400 for invalid token",
			token: "invalid-token",
			serverResponse: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusBadRequest)
				json.NewEncoder(w).Encode(map[string]interface{}{
					"error":             "invalid_token",
					"error_description": "Invalid token",
				})
			},
			expectError: true,
		},
		{
			name:  "Google returns expired token",
			token: "expired-token",
			serverResponse: func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(map[string]interface{}{
					"azp":   "test-client.apps.googleusercontent.com",
					"aud":   "test-client.apps.googleusercontent.com",
					"sub":   "123456789",
					"scope": "openid email",
					"exp":   fmt.Sprintf("%d", time.Now().Add(-time.Hour).Unix()), // Expired
					"email": "test@example.com",
				})
			},
			expectError: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			// Create a test server that mimics Google's tokeninfo endpoint
			server := httptest.NewServer(http.HandlerFunc(tc.serverResponse))
			defer server.Close()

			// Create a validator with our test server URL
			// Note: We're testing the introspectGoogleToken method directly,
			// so the URL doesn't need to be the exact Google URL for this test
			validator := &TokenValidator{
				client: http.DefaultClient,
			}

			ctx := context.Background()
			claims, err := validator.introspectGoogleToken(ctx, tc.token, server.URL)

			if tc.expectError {
				if err == nil {
					t.Error("Expected error but got nil")
				}
				return
			}

			if err != nil {
				t.Errorf("Expected no error but got: %v", err)
				return
			}

			// Verify expected claims
			for key, expectedValue := range tc.expectedClaims {
				if key == expClaim {
					// Check that exp is set as float64
					if _, ok := claims["exp"].(float64); !ok {
						t.Errorf("Expected exp to be float64, got %T", claims["exp"])
					}
					continue
				}

				if claims[key] != expectedValue {
					t.Errorf("Expected claim %s to be %v, got %v", key, expectedValue, claims[key])
				}
			}
		})
	}
}

func TestTokenValidator_GoogleTokeninfoIntegration(t *testing.T) {
	t.Parallel()

	// Create a mock Google tokeninfo server
	googleServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := r.URL.Query().Get("access_token")

		if token == "valid-google-token" {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]interface{}{
				"azp":            "test-client.apps.googleusercontent.com",
				"aud":            "test-client.apps.googleusercontent.com",
				"sub":            "google-user-123",
				"scope":          "openid https://www.googleapis.com/auth/userinfo.email",
				"exp":            fmt.Sprintf("%d", time.Now().Add(time.Hour).Unix()),
				"expires_in":     "3600",
				"email":          "user@example.com",
				"email_verified": "true",
			})
		} else {
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"error":             "invalid_token",
				"error_description": "Invalid token",
			})
		}
	}))
	t.Cleanup(func() {
		googleServer.Close()
	})

	t.Run("Google tokeninfo direct call", func(t *testing.T) { //nolint:paralleltest // Server lifecycle requires sequential execution
		// Note: Not using t.Parallel() here because we need the googleServer to stay alive

		// Test the introspectGoogleToken method directly with our test server
		testValidator := &TokenValidator{
			client:   http.DefaultClient,
			issuer:   "https://accounts.google.com",
			audience: "test-client.apps.googleusercontent.com",
		}

		// Directly call introspectGoogleToken to test Google-specific functionality
		ctx := context.Background()
		claims, err := testValidator.introspectGoogleToken(ctx, "valid-google-token", googleServer.URL)
		if err != nil {
			t.Fatalf("Expected no error but got: %v", err)
		}

		// Verify Google-specific claims are properly handled
		if claims["sub"] != "google-user-123" {
			t.Errorf("Expected sub=google-user-123, got %v", claims["sub"])
		}
		if claims["iss"] != "https://accounts.google.com" {
			t.Errorf("Expected iss=https://accounts.google.com, got %v", claims["iss"])
		}
		if claims["email"] != "user@example.com" {
			t.Errorf("Expected email=user@example.com, got %v", claims["email"])
		}
		if claims["active"] != true {
			t.Errorf("Expected active=true, got %v", claims["active"])
		}
	})

	t.Run("routing logic test", func(t *testing.T) {
		t.Parallel()

		// Test that the routing logic correctly detects Google's endpoint
		// and routes to the Google-specific handler vs standard RFC 7662

		ctx := context.Background()

		// Test 1: Google URL should route to Google handler (we can't easily test the full flow
		// without mocking, but we can test that it attempts to use the Google method)
		googleValidator := &TokenValidator{
			introspectURL: googleTokeninfoURL,
			client:        http.DefaultClient,
			issuer:        "https://accounts.google.com",
			audience:      "test-client.apps.googleusercontent.com",
		}

		// This will fail because we can't reach the real Google endpoint,
		// but it should fail in the HTTP request, not in the routing logic
		_, err := googleValidator.introspectOpaqueToken(ctx, "test-token")
		if err == nil {
			t.Error("Expected error trying to reach real Google endpoint")
		}
		// The error should be about HTTP connection, not about routing
		if !strings.Contains(err.Error(), "google tokeninfo") {
			t.Logf("Got expected error attempting to use Google tokeninfo: %v", err)
		}

		// Test 2: Non-Google URL should use standard RFC 7662 flow
		standardValidator := &TokenValidator{
			introspectURL: googleServer.URL, // Our test server
			client:        http.DefaultClient,
			issuer:        "https://accounts.google.com",
			audience:      "test-client.apps.googleusercontent.com",
		}

		// This should use the standard RFC 7662 POST method, which our test server doesn't handle
		// So it should fail, but in a different way than the Google method
		_, err = standardValidator.introspectOpaqueToken(ctx, "valid-google-token")
		if err == nil {
			t.Error("Expected error with non-Google introspection endpoint")
		}
		// Should fail because our test server expects GET but standard introspection uses POST
		if strings.Contains(err.Error(), "google tokeninfo") {
			t.Errorf("Should not use Google tokeninfo method for non-Google URL, got error: %v", err)
		}
	})
}

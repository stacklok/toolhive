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
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/lestrrat-go/jwx/v2/jwk"
)

const testKeyID = "test-key-1"

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
	key, err := jwk.FromRaw(publicKey)
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

	// Force a refresh of the JWKS cache
	_, err = validator.jwksClient.Get(ctx, jwksServer.URL)
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
	key, err := jwk.FromRaw(publicKey)
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

	// Force a refresh of the JWKS cache
	_, err = validator.jwksClient.Get(ctx, jwksServer.URL)
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
					if k == "exp" {
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
	key, err := jwk.FromRaw(publicKey)
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

		// Force a refresh of the JWKS cache
		_, err = validator.jwksClient.Get(ctx, validator.jwksURL)
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

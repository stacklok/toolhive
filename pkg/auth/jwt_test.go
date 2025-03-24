package auth

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/lestrrat-go/jwx/v2/jwk"
)

//nolint:gocyclo // This test function is complex but manageable
func TestJWTValidator(t *testing.T) {
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
	if err := key.Set(jwk.KeyIDKey, "test-key-1"); err != nil {
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
	jwksServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
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

	// Create a context for the test
	ctx := context.Background()

	// Create a JWT validator
	validator, err := NewJWTValidator(ctx, JWTValidatorConfig{
		Issuer:   "test-issuer",
		Audience: "test-audience",
		JWKSURL:  jwksServer.URL,
		ClientID: "test-client",
	})
	if err != nil {
		t.Fatalf("Failed to create JWT validator: %v", err)
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
			token.Header["kid"] = "test-key-1"

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
func TestJWTValidatorMiddleware(t *testing.T) {
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
	if err := key.Set(jwk.KeyIDKey, "test-key-1"); err != nil {
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
	jwksServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
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

	// Create a context for the test
	ctx := context.Background()

	// Create a JWT validator
	validator, err := NewJWTValidator(ctx, JWTValidatorConfig{
		Issuer:   "test-issuer",
		Audience: "test-audience",
		JWKSURL:  jwksServer.URL,
		ClientID: "test-client",
	})
	if err != nil {
		t.Fatalf("Failed to create JWT validator: %v", err)
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
			token.Header["kid"] = "test-key-1"

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

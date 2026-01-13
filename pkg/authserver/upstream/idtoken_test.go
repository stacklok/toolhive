// Copyright 2025 Stacklok, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package upstream

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v3"
	"github.com/go-jose/go-jose/v3/jwt"
)

// Test constants
const (
	testSubject = "user-123"
	testKID     = "test-key-1"
)

// createTestIDToken creates an unsigned JWT ID token for testing.
// Uses skipSignatureVerification mode for claim validation tests.
func createTestIDToken(claims map[string]any) string {
	// Create an unsigned token in JWS format (header.payload.signature with empty signature)
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.HS256, Key: []byte("test-secret-key-for-test-only!")}, nil)
	if err != nil {
		panic(err)
	}
	builder := jwt.Signed(signer).Claims(claims)
	token, err := builder.CompactSerialize()
	if err != nil {
		panic(err)
	}
	return token
}

// createSignedIDToken creates an RSA-signed JWT ID token with the given key.
func createSignedIDToken(claims map[string]any, privateKey *rsa.PrivateKey, kid string) string {
	signerOpts := &jose.SignerOptions{}
	signerOpts.WithHeader("kid", kid)
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: privateKey}, signerOpts)
	if err != nil {
		panic(err)
	}
	builder := jwt.Signed(signer).Claims(claims)
	token, err := builder.CompactSerialize()
	if err != nil {
		panic(err)
	}
	return token
}

// setupJWKSServer creates a test server that serves a JWKS with the given public key.
func setupJWKSServer(publicKey *rsa.PublicKey, kid string) *httptest.Server {
	jwk := jose.JSONWebKey{
		Key:       publicKey,
		KeyID:     kid,
		Algorithm: string(jose.RS256),
		Use:       "sig",
	}
	jwks := jose.JSONWebKeySet{Keys: []jose.JSONWebKey{jwk}}

	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(jwks); err != nil {
			http.Error(w, "Failed to encode JWKS", http.StatusInternalServerError)
		}
	}))
}

func TestNewIDTokenValidator(t *testing.T) {
	t.Parallel()

	_, err := newIDTokenValidator(idTokenValidatorConfig{
		expectedIssuer:   "https://example.com",
		expectedAudience: "client-id",
	})
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}

	_, err = newIDTokenValidator(idTokenValidatorConfig{expectedAudience: "client-id"})
	if err == nil {
		t.Error("expected error for missing issuer")
	}

	_, err = newIDTokenValidator(idTokenValidatorConfig{expectedIssuer: "https://example.com"})
	if err == nil {
		t.Error("expected error for missing audience")
	}
}

func TestIDTokenValidator_ValidToken(t *testing.T) {
	t.Parallel()

	validator, _ := newIDTokenValidator(idTokenValidatorConfig{
		expectedIssuer:            "https://example.com",
		expectedAudience:          "client-id",
		skipSignatureVerification: true,
	})

	token := createTestIDToken(map[string]any{
		"iss":   "https://example.com",
		"sub":   testSubject,
		"aud":   "client-id",
		"exp":   time.Now().Add(time.Hour).Unix(),
		"email": "user@example.com",
	})

	claims, err := validator.validateIDToken(token)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if claims.Subject != testSubject {
		t.Errorf("expected subject testSubject, got %s", claims.Subject)
	}
	if claims.Email != "user@example.com" {
		t.Errorf("expected email user@example.com, got %s", claims.Email)
	}
}

func TestIDTokenValidator_ValidationErrors(t *testing.T) {
	t.Parallel()

	validator, _ := newIDTokenValidator(idTokenValidatorConfig{
		expectedIssuer:            "https://example.com",
		expectedAudience:          "client-id",
		skipSignatureVerification: true,
	})

	tests := []struct {
		name  string
		token string
		errIs error
	}{
		{"empty token", "", ErrIDTokenRequired},
		{"invalid format", "not-a-jwt", nil}, // generic parse error
		{"missing issuer", createTestIDToken(map[string]any{
			"sub": testSubject, "aud": "client-id", "exp": time.Now().Add(time.Hour).Unix(),
		}), ErrIDTokenMissingIssuer},
		{"wrong issuer", createTestIDToken(map[string]any{
			"iss": "https://wrong.com", "sub": testSubject, "aud": "client-id", "exp": time.Now().Add(time.Hour).Unix(),
		}), ErrIDTokenIssuerMismatch},
		{"missing audience", createTestIDToken(map[string]any{
			"iss": "https://example.com", "sub": testSubject, "exp": time.Now().Add(time.Hour).Unix(),
		}), ErrIDTokenMissingAud},
		{"wrong audience", createTestIDToken(map[string]any{
			"iss": "https://example.com", "sub": testSubject, "aud": "wrong-client", "exp": time.Now().Add(time.Hour).Unix(),
		}), ErrIDTokenAudMismatch},
		{"missing exp", createTestIDToken(map[string]any{
			"iss": "https://example.com", "sub": testSubject, "aud": "client-id",
		}), ErrIDTokenMissingExp},
		{"expired", createTestIDToken(map[string]any{
			"iss": "https://example.com", "sub": testSubject, "aud": "client-id", "exp": time.Now().Add(-time.Hour).Unix(),
		}), ErrIDTokenExpired},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := validator.validateIDToken(tt.token)
			if err == nil {
				t.Error("expected error")
				return
			}
			if tt.errIs != nil && !errors.Is(err, tt.errIs) {
				t.Errorf("expected %v, got %v", tt.errIs, err)
			}
		})
	}
}

func TestIDTokenValidator_ClockSkew(t *testing.T) {
	t.Parallel()

	validator, _ := newIDTokenValidator(idTokenValidatorConfig{
		expectedIssuer:            "https://example.com",
		expectedAudience:          "client-id",
		clockSkew:                 5 * time.Minute,
		skipSignatureVerification: true,
	})

	// Token expired 2 minutes ago, but we allow 5 minute skew
	token := createTestIDToken(map[string]any{
		"iss": "https://example.com",
		"sub": testSubject,
		"aud": "client-id",
		"exp": time.Now().Add(-2 * time.Minute).Unix(),
	})

	_, err := validator.validateIDToken(token)
	if err != nil {
		t.Errorf("expected token to be valid within clock skew: %v", err)
	}
}

func TestIDTokenValidator_ValidateWithNonce(t *testing.T) {
	t.Parallel()

	validator, _ := newIDTokenValidator(idTokenValidatorConfig{
		expectedIssuer:            "https://example.com",
		expectedAudience:          "client-id",
		skipSignatureVerification: true,
	})

	t.Run("valid token with matching nonce", func(t *testing.T) {
		t.Parallel()
		token := createTestIDToken(map[string]any{
			"iss":   "https://example.com",
			"sub":   testSubject,
			"aud":   "client-id",
			"exp":   time.Now().Add(time.Hour).Unix(),
			"nonce": "test-nonce-12345",
		})

		claims, err := validator.validateIDTokenWithNonce(token, "test-nonce-12345")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if claims.Nonce != "test-nonce-12345" {
			t.Errorf("expected nonce test-nonce-12345, got %s", claims.Nonce)
		}
	})

	t.Run("missing nonce in token", func(t *testing.T) {
		t.Parallel()
		token := createTestIDToken(map[string]any{
			"iss": "https://example.com",
			"sub": testSubject,
			"aud": "client-id",
			"exp": time.Now().Add(time.Hour).Unix(),
			// no nonce claim
		})

		_, err := validator.validateIDTokenWithNonce(token, "expected-nonce")
		if err == nil {
			t.Error("expected error for missing nonce")
		}
		if !errors.Is(err, ErrIDTokenMissingNonce) {
			t.Errorf("expected ErrIDTokenMissingNonce, got %v", err)
		}
	})

	t.Run("nonce mismatch", func(t *testing.T) {
		t.Parallel()
		token := createTestIDToken(map[string]any{
			"iss":   "https://example.com",
			"sub":   testSubject,
			"aud":   "client-id",
			"exp":   time.Now().Add(time.Hour).Unix(),
			"nonce": "wrong-nonce",
		})

		_, err := validator.validateIDTokenWithNonce(token, "expected-nonce")
		if err == nil {
			t.Error("expected error for nonce mismatch")
		}
		if !errors.Is(err, ErrIDTokenNonceMismatch) {
			t.Errorf("expected ErrIDTokenNonceMismatch, got %v", err)
		}
	})

	t.Run("empty expected nonce skips validation", func(t *testing.T) {
		t.Parallel()
		token := createTestIDToken(map[string]any{
			"iss": "https://example.com",
			"sub": testSubject,
			"aud": "client-id",
			"exp": time.Now().Add(time.Hour).Unix(),
			// no nonce claim
		})

		// Empty expected nonce should skip nonce validation
		_, err := validator.validateIDTokenWithNonce(token, "")
		if err != nil {
			t.Errorf("expected no error when expected nonce is empty: %v", err)
		}
	})

	t.Run("standard validation still applies", func(t *testing.T) {
		t.Parallel()
		// Token with wrong issuer but valid nonce
		token := createTestIDToken(map[string]any{
			"iss":   "https://wrong.com",
			"sub":   testSubject,
			"aud":   "client-id",
			"exp":   time.Now().Add(time.Hour).Unix(),
			"nonce": "test-nonce",
		})

		_, err := validator.validateIDTokenWithNonce(token, "test-nonce")
		if err == nil {
			t.Error("expected error for issuer mismatch")
		}
		if !errors.Is(err, ErrIDTokenIssuerMismatch) {
			t.Errorf("expected ErrIDTokenIssuerMismatch, got %v", err)
		}
	})
}

func TestIDTokenValidator_SignatureVerification_ValidSignature(t *testing.T) {
	t.Parallel()

	// Generate RSA key pair for signing
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("failed to generate key: %v", err)
	}

	kid := testKID

	// Start JWKS server
	jwksServer := setupJWKSServer(&privateKey.PublicKey, kid)
	defer jwksServer.Close()

	validator, err := newIDTokenValidator(idTokenValidatorConfig{
		expectedIssuer:   "https://example.com",
		expectedAudience: "client-id",
		jwksURI:          jwksServer.URL,
	})
	if err != nil {
		t.Fatalf("failed to create validator: %v", err)
	}

	token := createSignedIDToken(map[string]any{
		"iss": "https://example.com",
		"sub": testSubject,
		"aud": "client-id",
		"exp": time.Now().Add(time.Hour).Unix(),
	}, privateKey, kid)

	claims, err := validator.validateIDTokenWithContext(context.Background(), token)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if claims.Subject != testSubject {
		t.Errorf("expected subject testSubject, got %s", claims.Subject)
	}
}

func TestIDTokenValidator_SignatureVerification_InvalidSignature(t *testing.T) {
	t.Parallel()

	// Generate RSA key pair for JWKS
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("failed to generate key: %v", err)
	}

	// Create a different key for signing
	wrongKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("failed to generate wrong key: %v", err)
	}

	kid := testKID

	// Start JWKS server with the correct key
	jwksServer := setupJWKSServer(&privateKey.PublicKey, kid)
	defer jwksServer.Close()

	validator, err := newIDTokenValidator(idTokenValidatorConfig{
		expectedIssuer:   "https://example.com",
		expectedAudience: "client-id",
		jwksURI:          jwksServer.URL,
	})
	if err != nil {
		t.Fatalf("failed to create validator: %v", err)
	}

	// Sign with wrong key but use the correct kid
	token := createSignedIDToken(map[string]any{
		"iss": "https://example.com",
		"sub": testSubject,
		"aud": "client-id",
		"exp": time.Now().Add(time.Hour).Unix(),
	}, wrongKey, kid)

	_, err = validator.validateIDTokenWithContext(context.Background(), token)
	if err == nil {
		t.Error("expected error for invalid signature")
	}
	if !errors.Is(err, ErrIDTokenSignatureInvalid) {
		t.Errorf("expected ErrIDTokenSignatureInvalid, got %v", err)
	}
}

func TestIDTokenValidator_SignatureVerification_MissingKid(t *testing.T) {
	t.Parallel()

	// Generate RSA key pair for signing
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("failed to generate key: %v", err)
	}

	kid := testKID

	// Start JWKS server
	jwksServer := setupJWKSServer(&privateKey.PublicKey, kid)
	defer jwksServer.Close()

	validator, err := newIDTokenValidator(idTokenValidatorConfig{
		expectedIssuer:   "https://example.com",
		expectedAudience: "client-id",
		jwksURI:          jwksServer.URL,
	})
	if err != nil {
		t.Fatalf("failed to create validator: %v", err)
	}

	// Sign with unknown kid
	token := createSignedIDToken(map[string]any{
		"iss": "https://example.com",
		"sub": testSubject,
		"aud": "client-id",
		"exp": time.Now().Add(time.Hour).Unix(),
	}, privateKey, "unknown-kid")

	_, err = validator.validateIDTokenWithContext(context.Background(), token)
	if err == nil {
		t.Error("expected error for unknown kid")
	}
	if !errors.Is(err, ErrIDTokenKeyNotFound) {
		t.Errorf("expected ErrIDTokenKeyNotFound, got %v", err)
	}
}

func TestIDTokenValidator_SignatureVerification_JWKSFetchFailure(t *testing.T) {
	t.Parallel()

	// Generate RSA key pair for signing
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("failed to generate key: %v", err)
	}

	kid := testKID

	// Use a server that returns an error
	errorServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
	}))
	defer errorServer.Close()

	validator, err := newIDTokenValidator(idTokenValidatorConfig{
		expectedIssuer:   "https://example.com",
		expectedAudience: "client-id",
		jwksURI:          errorServer.URL,
	})
	if err != nil {
		t.Fatalf("failed to create validator: %v", err)
	}

	token := createSignedIDToken(map[string]any{
		"iss": "https://example.com",
		"sub": testSubject,
		"aud": "client-id",
		"exp": time.Now().Add(time.Hour).Unix(),
	}, privateKey, kid)

	_, err = validator.validateIDTokenWithContext(context.Background(), token)
	if err == nil {
		t.Error("expected error for JWKS fetch failure")
	}
	if !errors.Is(err, ErrIDTokenJWKSFetchFailed) {
		t.Errorf("expected ErrIDTokenJWKSFetchFailed, got %v", err)
	}
}

func TestIDTokenValidator_SignatureVerification_MissingJWKSURI(t *testing.T) {
	t.Parallel()

	// Generate RSA key pair for signing
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("failed to generate key: %v", err)
	}

	kid := testKID

	validator, err := newIDTokenValidator(idTokenValidatorConfig{
		expectedIssuer:   "https://example.com",
		expectedAudience: "client-id",
		// No jwksURI and skipSignatureVerification is false
	})
	if err != nil {
		t.Fatalf("failed to create validator: %v", err)
	}

	token := createSignedIDToken(map[string]any{
		"iss": "https://example.com",
		"sub": testSubject,
		"aud": "client-id",
		"exp": time.Now().Add(time.Hour).Unix(),
	}, privateKey, kid)

	_, err = validator.validateIDTokenWithContext(context.Background(), token)
	if err == nil {
		t.Error("expected error when jwksURI is not configured")
	}
	if !errors.Is(err, ErrIDTokenMissingSigningKey) {
		t.Errorf("expected ErrIDTokenMissingSigningKey, got %v", err)
	}
}

func TestIDTokenValidator_UnsupportedAlgorithm(t *testing.T) {
	t.Parallel()

	// Create a token signed with HS256 (symmetric algorithm - not in our supported list)
	// Use a JWKS server that returns an oct (symmetric) key
	symmetricKey := []byte("test-symmetric-key-32-bytes-long")
	jwks := jose.JSONWebKeySet{
		Keys: []jose.JSONWebKey{
			{
				Key:       symmetricKey,
				KeyID:     "sym-key-1",
				Algorithm: string(jose.HS256),
				Use:       "sig",
			},
		},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(jwks); err != nil {
			http.Error(w, "Failed to encode JWKS", http.StatusInternalServerError)
		}
	}))
	defer server.Close()

	validator, err := newIDTokenValidator(idTokenValidatorConfig{
		expectedIssuer:   "https://example.com",
		expectedAudience: "client-id",
		jwksURI:          server.URL,
	})
	if err != nil {
		t.Fatalf("failed to create validator: %v", err)
	}

	// Create token signed with HS256
	token := createTestIDToken(map[string]any{
		"iss": "https://example.com",
		"sub": testSubject,
		"aud": "client-id",
		"exp": time.Now().Add(time.Hour).Unix(),
	})

	_, err = validator.validateIDTokenWithContext(context.Background(), token)
	if err == nil {
		t.Error("expected error for unsupported algorithm")
	}
	if !errors.Is(err, ErrIDTokenUnsupportedAlg) {
		t.Errorf("expected ErrIDTokenUnsupportedAlg, got %v", err)
	}
}

func TestExtractAudience(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		claims   map[string]any
		expected []string
	}{
		{
			name:     "single string audience",
			claims:   map[string]any{"aud": "client-id"},
			expected: []string{"client-id"},
		},
		{
			name:     "array audience",
			claims:   map[string]any{"aud": []any{"client-1", "client-2"}},
			expected: []string{"client-1", "client-2"},
		},
		{
			name:     "missing audience",
			claims:   map[string]any{},
			expected: nil,
		},
		{
			name:     "empty array audience",
			claims:   map[string]any{"aud": []any{}},
			expected: []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := extractAudience(tt.claims)
			if len(result) != len(tt.expected) {
				t.Errorf("expected %v, got %v", tt.expected, result)
				return
			}
			for i, v := range result {
				if v != tt.expected[i] {
					t.Errorf("expected %v at index %d, got %v", tt.expected[i], i, v)
				}
			}
		})
	}
}

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

package idp

import (
	"errors"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// createTestIDToken creates a JWT ID token for testing with the "none" algorithm.
func createTestIDToken(claims jwt.MapClaims) string {
	token := jwt.NewWithClaims(jwt.SigningMethodNone, claims)
	tokenString, _ := token.SignedString(jwt.UnsafeAllowNoneSignatureType)
	return tokenString
}

func TestNewIDTokenValidator(t *testing.T) {
	t.Parallel()

	_, err := NewIDTokenValidator(IDTokenValidatorConfig{
		ExpectedIssuer:   "https://example.com",
		ExpectedAudience: "client-id",
	})
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}

	_, err = NewIDTokenValidator(IDTokenValidatorConfig{ExpectedAudience: "client-id"})
	if err == nil {
		t.Error("expected error for missing issuer")
	}

	_, err = NewIDTokenValidator(IDTokenValidatorConfig{ExpectedIssuer: "https://example.com"})
	if err == nil {
		t.Error("expected error for missing audience")
	}
}

func TestIDTokenValidator_ValidToken(t *testing.T) {
	t.Parallel()

	validator, _ := NewIDTokenValidator(IDTokenValidatorConfig{
		ExpectedIssuer:   "https://example.com",
		ExpectedAudience: "client-id",
	})

	token := createTestIDToken(jwt.MapClaims{
		"iss":   "https://example.com",
		"sub":   "user-123",
		"aud":   "client-id",
		"exp":   time.Now().Add(time.Hour).Unix(),
		"email": "user@example.com",
	})

	claims, err := validator.ValidateIDToken(token)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if claims.Subject != "user-123" {
		t.Errorf("expected subject user-123, got %s", claims.Subject)
	}
	if claims.Email != "user@example.com" {
		t.Errorf("expected email user@example.com, got %s", claims.Email)
	}
}

func TestIDTokenValidator_ValidationErrors(t *testing.T) {
	t.Parallel()

	validator, _ := NewIDTokenValidator(IDTokenValidatorConfig{
		ExpectedIssuer:   "https://example.com",
		ExpectedAudience: "client-id",
	})

	tests := []struct {
		name  string
		token string
		errIs error
	}{
		{"empty token", "", ErrIDTokenRequired},
		{"invalid format", "not-a-jwt", nil}, // generic parse error
		{"missing issuer", createTestIDToken(jwt.MapClaims{
			"sub": "user-123", "aud": "client-id", "exp": time.Now().Add(time.Hour).Unix(),
		}), ErrIDTokenMissingIssuer},
		{"wrong issuer", createTestIDToken(jwt.MapClaims{
			"iss": "https://wrong.com", "sub": "user-123", "aud": "client-id", "exp": time.Now().Add(time.Hour).Unix(),
		}), ErrIDTokenIssuerMismatch},
		{"missing audience", createTestIDToken(jwt.MapClaims{
			"iss": "https://example.com", "sub": "user-123", "exp": time.Now().Add(time.Hour).Unix(),
		}), ErrIDTokenMissingAud},
		{"wrong audience", createTestIDToken(jwt.MapClaims{
			"iss": "https://example.com", "sub": "user-123", "aud": "wrong-client", "exp": time.Now().Add(time.Hour).Unix(),
		}), ErrIDTokenAudMismatch},
		{"missing exp", createTestIDToken(jwt.MapClaims{
			"iss": "https://example.com", "sub": "user-123", "aud": "client-id",
		}), ErrIDTokenMissingExp},
		{"expired", createTestIDToken(jwt.MapClaims{
			"iss": "https://example.com", "sub": "user-123", "aud": "client-id", "exp": time.Now().Add(-time.Hour).Unix(),
		}), ErrIDTokenExpired},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := validator.ValidateIDToken(tt.token)
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

	validator, _ := NewIDTokenValidator(IDTokenValidatorConfig{
		ExpectedIssuer:   "https://example.com",
		ExpectedAudience: "client-id",
		ClockSkew:        5 * time.Minute,
	})

	// Token expired 2 minutes ago, but we allow 5 minute skew
	token := createTestIDToken(jwt.MapClaims{
		"iss": "https://example.com",
		"sub": "user-123",
		"aud": "client-id",
		"exp": time.Now().Add(-2 * time.Minute).Unix(),
	})

	_, err := validator.ValidateIDToken(token)
	if err != nil {
		t.Errorf("expected token to be valid within clock skew: %v", err)
	}
}

func TestIDTokenValidator_ValidateWithNonce(t *testing.T) {
	t.Parallel()

	validator, _ := NewIDTokenValidator(IDTokenValidatorConfig{
		ExpectedIssuer:   "https://example.com",
		ExpectedAudience: "client-id",
	})

	t.Run("valid token with matching nonce", func(t *testing.T) {
		t.Parallel()
		token := createTestIDToken(jwt.MapClaims{
			"iss":   "https://example.com",
			"sub":   "user-123",
			"aud":   "client-id",
			"exp":   time.Now().Add(time.Hour).Unix(),
			"nonce": "test-nonce-12345",
		})

		claims, err := validator.ValidateIDTokenWithNonce(token, "test-nonce-12345")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if claims.Nonce != "test-nonce-12345" {
			t.Errorf("expected nonce test-nonce-12345, got %s", claims.Nonce)
		}
	})

	t.Run("missing nonce in token", func(t *testing.T) {
		t.Parallel()
		token := createTestIDToken(jwt.MapClaims{
			"iss": "https://example.com",
			"sub": "user-123",
			"aud": "client-id",
			"exp": time.Now().Add(time.Hour).Unix(),
			// no nonce claim
		})

		_, err := validator.ValidateIDTokenWithNonce(token, "expected-nonce")
		if err == nil {
			t.Error("expected error for missing nonce")
		}
		if !errors.Is(err, ErrIDTokenMissingNonce) {
			t.Errorf("expected ErrIDTokenMissingNonce, got %v", err)
		}
	})

	t.Run("nonce mismatch", func(t *testing.T) {
		t.Parallel()
		token := createTestIDToken(jwt.MapClaims{
			"iss":   "https://example.com",
			"sub":   "user-123",
			"aud":   "client-id",
			"exp":   time.Now().Add(time.Hour).Unix(),
			"nonce": "wrong-nonce",
		})

		_, err := validator.ValidateIDTokenWithNonce(token, "expected-nonce")
		if err == nil {
			t.Error("expected error for nonce mismatch")
		}
		if !errors.Is(err, ErrIDTokenNonceMismatch) {
			t.Errorf("expected ErrIDTokenNonceMismatch, got %v", err)
		}
	})

	t.Run("empty expected nonce skips validation", func(t *testing.T) {
		t.Parallel()
		token := createTestIDToken(jwt.MapClaims{
			"iss": "https://example.com",
			"sub": "user-123",
			"aud": "client-id",
			"exp": time.Now().Add(time.Hour).Unix(),
			// no nonce claim
		})

		// Empty expected nonce should skip nonce validation
		_, err := validator.ValidateIDTokenWithNonce(token, "")
		if err != nil {
			t.Errorf("expected no error when expected nonce is empty: %v", err)
		}
	})

	t.Run("standard validation still applies", func(t *testing.T) {
		t.Parallel()
		// Token with wrong issuer but valid nonce
		token := createTestIDToken(jwt.MapClaims{
			"iss":   "https://wrong.com",
			"sub":   "user-123",
			"aud":   "client-id",
			"exp":   time.Now().Add(time.Hour).Unix(),
			"nonce": "test-nonce",
		})

		_, err := validator.ValidateIDTokenWithNonce(token, "test-nonce")
		if err == nil {
			t.Error("expected error for issuer mismatch")
		}
		if !errors.Is(err, ErrIDTokenIssuerMismatch) {
			t.Errorf("expected ErrIDTokenIssuerMismatch, got %v", err)
		}
	})
}

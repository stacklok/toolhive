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

package oauth

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"os"
	"strings"

	"github.com/go-jose/go-jose/v4"
)

// LoadSigningKey loads a private key from a PEM file.
// Supports both RSA (PKCS1 and PKCS8) and ECDSA (PKCS8) formats.
// Returns a crypto.Signer that can be used for JWT signing.
func LoadSigningKey(keyPath string) (crypto.Signer, error) {
	keyPEM, err := os.ReadFile(keyPath) // #nosec G304 - keyPath is provided by user via CLI flag or config
	if err != nil {
		return nil, fmt.Errorf("failed to read signing key: %w", err)
	}

	block, _ := pem.Decode(keyPEM)
	if block == nil {
		return nil, fmt.Errorf("failed to decode PEM block from signing key")
	}

	// Try PKCS1 first (RSA only)
	if rsaKey, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return rsaKey, nil
	}

	// Try EC private key (SEC 1, ASN.1 DER form)
	if ecKey, err := x509.ParseECPrivateKey(block.Bytes); err == nil {
		return ecKey, nil
	}

	// Try PKCS8 (supports both RSA and EC)
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("failed to parse signing key: %w", err)
	}

	signer, ok := key.(crypto.Signer)
	if !ok {
		return nil, fmt.Errorf("signing key does not implement crypto.Signer")
	}

	return signer, nil
}

// DeriveKeyID computes a key ID from the public key using RFC 7638 JWK Thumbprint.
// The thumbprint is computed as base64url(SHA-256(JWK canonical form)).
func DeriveKeyID(key crypto.Signer) (string, error) {
	// Create a JWK from the public key
	jwk := jose.JSONWebKey{
		Key: key.Public(),
	}

	// Compute the thumbprint using go-jose's built-in RFC 7638 implementation
	thumbprint, err := jwk.Thumbprint(crypto.SHA256)
	if err != nil {
		return "", fmt.Errorf("failed to compute key thumbprint: %w", err)
	}

	// Base64url encode without padding (RFC 7638 standard)
	return base64.RawURLEncoding.EncodeToString(thumbprint), nil
}

// DeriveAlgorithm determines the appropriate JWT signing algorithm for the given key.
// Returns the algorithm string (e.g., "RS256", "ES256") based on key type and parameters.
func DeriveAlgorithm(key crypto.Signer) (string, error) {
	switch k := key.(type) {
	case *rsa.PrivateKey:
		return "RS256", nil
	case *ecdsa.PrivateKey:
		return deriveECAlgorithm(k.Curve)
	default:
		return "", fmt.Errorf("unsupported key type: %T", key)
	}
}

// deriveECAlgorithm determines the ECDSA algorithm based on the curve.
func deriveECAlgorithm(curve elliptic.Curve) (string, error) {
	switch curve {
	case elliptic.P256():
		return "ES256", nil
	case elliptic.P384():
		return "ES384", nil
	case elliptic.P521():
		return "ES512", nil
	default:
		return "", fmt.Errorf("unsupported EC curve: %s", curve.Params().Name)
	}
}

// ValidateAlgorithmForKey checks if the provided algorithm is compatible with the key type.
// Returns an error if the algorithm doesn't match the key type.
func ValidateAlgorithmForKey(alg string, key crypto.Signer) error {
	switch k := key.(type) {
	case *rsa.PrivateKey:
		switch alg {
		case "RS256", "RS384", "RS512":
			return nil
		default:
			return fmt.Errorf("algorithm %s is not compatible with RSA key", alg)
		}
	case *ecdsa.PrivateKey:
		expectedAlg, err := deriveECAlgorithm(k.Curve)
		if err != nil {
			return err
		}
		if alg != expectedAlg {
			return fmt.Errorf("algorithm %s is not compatible with EC key using curve %s (expected %s)",
				alg, k.Curve.Params().Name, expectedAlg)
		}
		return nil
	default:
		return fmt.Errorf("unsupported key type: %T", key)
	}
}

// SigningKeyParams contains the derived or configured parameters for a signing key.
type SigningKeyParams struct {
	// Key is the private key used for signing.
	Key crypto.Signer
	// KeyID is the key identifier (either derived from thumbprint or configured).
	KeyID string
	// Algorithm is the signing algorithm (either derived from key type or configured).
	Algorithm string
}

// DeriveSigningKeyParams derives or validates signing key parameters.
// If keyID or algorithm are empty, they are derived from the key.
// If they are provided, they are validated against the key type.
func DeriveSigningKeyParams(key crypto.Signer, keyID, algorithm string) (*SigningKeyParams, error) {
	params := &SigningKeyParams{Key: key}

	// Derive or use provided KeyID
	if keyID == "" {
		derivedID, err := DeriveKeyID(key)
		if err != nil {
			return nil, fmt.Errorf("failed to derive key ID: %w", err)
		}
		params.KeyID = derivedID
	} else {
		params.KeyID = keyID
	}

	// Derive or validate Algorithm
	if algorithm == "" {
		derivedAlg, err := DeriveAlgorithm(key)
		if err != nil {
			return nil, fmt.Errorf("failed to derive algorithm: %w", err)
		}
		params.Algorithm = derivedAlg
	} else {
		// Validate that provided algorithm matches key type
		if err := ValidateAlgorithmForKey(algorithm, key); err != nil {
			return nil, err
		}
		params.Algorithm = algorithm
	}

	return params, nil
}

// LoadHMACSecret loads an HMAC secret from a file.
// Returns nil if path is empty (triggers random generation in toInternalConfig).
// The secret must be at least 32 bytes after trimming whitespace.
func LoadHMACSecret(secretPath string) ([]byte, error) {
	if secretPath == "" {
		return nil, nil
	}

	data, err := os.ReadFile(secretPath) // #nosec G304 - secretPath is provided by user via CLI flag or config
	if err != nil {
		return nil, fmt.Errorf("failed to read HMAC secret file: %w", err)
	}

	// Trim whitespace (common in Kubernetes Secret mounts which often add trailing newlines)
	secret := []byte(strings.TrimSpace(string(data)))

	if len(secret) < MinSecretLength {
		return nil, fmt.Errorf("HMAC secret must be at least %d bytes, got %d bytes", MinSecretLength, len(secret))
	}

	return secret, nil
}

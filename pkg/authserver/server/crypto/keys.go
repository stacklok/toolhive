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

// Package crypto provides cryptographic utilities for the OAuth authorization server.
package crypto

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"os"
	"slices"

	"github.com/go-jose/go-jose/v4"
)

// MinSecretLength is the minimum required length for the HMAC secret in bytes.
// Using 256 bits (32 bytes) provides adequate security for HMAC-SHA256.
const MinSecretLength = 32

// MinRSAKeyBits is the minimum required RSA key size in bits.
// NIST recommends at least 2048 bits for RSA keys.
const MinRSAKeyBits = 2048

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
		if keyBits := rsaKey.N.BitLen(); keyBits < MinRSAKeyBits {
			return nil, fmt.Errorf("RSA key size %d bits is below minimum required %d bits", keyBits, MinRSAKeyBits)
		}
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

	// Validate RSA key size if parsed key is RSA
	if rsaKey, ok := key.(*rsa.PrivateKey); ok {
		if keyBits := rsaKey.N.BitLen(); keyBits < MinRSAKeyBits {
			return nil, fmt.Errorf("RSA key size %d bits is below minimum required %d bits", keyBits, MinRSAKeyBits)
		}
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
// Returns the algorithm string (e.g., "RS256", "ES256", "EdDSA") based on key type and parameters.
func DeriveAlgorithm(key crypto.Signer) (string, error) {
	switch k := key.(type) {
	case *rsa.PrivateKey:
		return "RS256", nil
	case *ecdsa.PrivateKey:
		return deriveECAlgorithm(k.Curve)
	case ed25519.PrivateKey:
		return "EdDSA", nil
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

// rsaAlgorithmsForClientKeys are the RSA JWS algorithms accepted for a client's public key.
var rsaAlgorithmsForClientKeys = []string{"RS256", "RS384", "RS512", "PS256", "PS384", "PS512"}

// rsaAlgorithmsForSigningKeys are the RSA JWS algorithms accepted for the server's own
// signing key. Deliberately narrower than rsaAlgorithmsForClientKeys: PS* is excluded.
var rsaAlgorithmsForSigningKeys = []string{"RS256", "RS384", "RS512"}

// SupportedClientKeyAlgorithms returns the full allowlist of JWS algorithms
// accepted for a private_key_jwt client's registered key: rsaAlgorithmsForClientKeys,
// the three EC algorithms deriveECAlgorithm maps curves to, and EdDSA for
// Ed25519. This is the single source of truth for both key-compatibility
// validation here and DCR's signing-algorithm allowlist, so the two lists
// cannot silently drift apart (as happened when EdDSA was previously missing
// from a hand-maintained copy of this list).
func SupportedClientKeyAlgorithms() []string {
	algs := make([]string, 0, len(rsaAlgorithmsForClientKeys)+4)
	algs = append(algs, rsaAlgorithmsForClientKeys...)
	return append(algs, "ES256", "ES384", "ES512", "EdDSA")
}

// validateAlgorithmForPublicKeyValue checks whether alg is compatible with a public key,
// accepting exactly the RSA algorithms in allowedRSAAlgorithms.
func validateAlgorithmForPublicKeyValue(alg string, key crypto.PublicKey, allowedRSAAlgorithms []string) error {
	switch k := key.(type) {
	case *rsa.PublicKey:
		if slices.Contains(allowedRSAAlgorithms, alg) {
			return nil
		}
		return fmt.Errorf("algorithm %s is not compatible with RSA key", alg)
	case *ecdsa.PublicKey:
		expectedAlg, err := deriveECAlgorithm(k.Curve)
		if err != nil {
			return err
		}
		if alg != expectedAlg {
			return fmt.Errorf("algorithm %s is not compatible with EC key using curve %s (expected %s)",
				alg, k.Curve.Params().Name, expectedAlg)
		}
		return nil
	case ed25519.PublicKey:
		if alg != "EdDSA" {
			return fmt.Errorf("algorithm %s is not compatible with Ed25519 key (expected EdDSA)", alg)
		}
		return nil
	default:
		return fmt.Errorf("unsupported key type: %T", key)
	}
}

// ValidateAlgorithmForPublicKey checks whether alg is compatible with a public key.
// It accepts the public-key values stored in jose.JSONWebKey.Key.
func ValidateAlgorithmForPublicKey(alg string, key crypto.PublicKey) error {
	return validateAlgorithmForPublicKeyValue(alg, key, rsaAlgorithmsForClientKeys)
}

// ValidateAlgorithmForKey checks if the provided algorithm is compatible with the key type.
// Returns an error if the algorithm doesn't match the key type.
func ValidateAlgorithmForKey(alg string, key crypto.Signer) error {
	switch k := key.(type) {
	case *rsa.PrivateKey:
		if k == nil {
			return fmt.Errorf("signing key is nil")
		}
		return validateAlgorithmForPublicKeyValue(alg, &k.PublicKey, rsaAlgorithmsForSigningKeys)
	case *ecdsa.PrivateKey:
		if k == nil {
			return fmt.Errorf("signing key is nil")
		}
		return validateAlgorithmForPublicKeyValue(alg, &k.PublicKey, rsaAlgorithmsForSigningKeys)
	case ed25519.PrivateKey:
		if k == nil {
			return fmt.Errorf("signing key is nil")
		}
		return validateAlgorithmForPublicKeyValue(alg, k.Public(), rsaAlgorithmsForSigningKeys)
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

// HMACSecrets holds the current secret and any rotated (previous) secrets.
// This supports zero-downtime secret rotation for OAuth token signing.
type HMACSecrets struct {
	// Current is the active secret used for signing new tokens.
	Current []byte
	// Rotated contains previously-used secrets for verifying existing tokens.
	Rotated [][]byte
}

// NewHMACSecrets creates an HMACSecrets with just a current secret (no rotation).
// This is a convenience function for cases where secret rotation is not needed.
func NewHMACSecrets(current []byte) *HMACSecrets {
	return &HMACSecrets{
		Current: current,
		Rotated: nil,
	}
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

// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package keys provides signing key management for the OAuth authorization server.
// It handles key lifecycle including loading from files, generation, and retrieval.
package keys

import (
	"crypto"
	"time"
)

// DefaultAlgorithm is the default signing algorithm for auto-generated keys.
// ES256 (ECDSA with P-256) is recommended by NIST and OWASP for JWT signing.
// It provides equivalent security to RSA-3072 with smaller keys and faster operations.
const DefaultAlgorithm = "ES256"

// SigningKeyData represents a signing key with its metadata.
// This contains private key material and should not be exposed externally.
type SigningKeyData struct {
	// KeyID is the unique identifier for this key (RFC 7638 thumbprint).
	KeyID string

	// Algorithm is the signing algorithm (e.g., "ES256", "RS256").
	Algorithm string

	// Key is the private key used for signing.
	Key crypto.Signer

	// CreatedAt is when this key was generated or loaded.
	CreatedAt time.Time
}

// PublicKeyData represents the public portion of a signing key.
// This is safe to expose via the JWKS endpoint.
type PublicKeyData struct {
	// KeyID is the unique identifier for this key (RFC 7638 thumbprint).
	KeyID string

	// Algorithm is the signing algorithm (e.g., "ES256", "RS256").
	Algorithm string

	// PublicKey is the public key for verification.
	PublicKey crypto.PublicKey

	// CreatedAt is when this key was generated or loaded.
	CreatedAt time.Time
}

// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package registration

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"errors"

	"github.com/ory/fosite"
)

// SHA256Hasher is a fosite.Hasher for client secrets based on plain SHA-256.
//
// Client secrets issued by this server are 32 bytes of crypto/rand output,
// base64url-encoded, and are never client-chosen (the DCR request struct has
// no client_secret field). At 256 bits of CSPRNG entropy there is nothing for
// a password-stretching KDF to protect: brute force is infeasible regardless
// of hash cost. What a slow KDF *would* do is give an unauthenticated caller
// a CPU-amplification lever — one bcrypt verify per wrong-secret attempt
// against /oauth/token, in the same process as the proxy data path — so the
// work factor protects the attacker, not the secret.
var SHA256Hasher fosite.Hasher = sha256Hasher{}

type sha256Hasher struct{}

// Compare compares a SHA-256 hash with the plaintext data in constant time.
func (sha256Hasher) Compare(_ context.Context, hash, data []byte) error {
	dataHash := sha256.Sum256(data)
	if len(hash) != len(dataHash) || subtle.ConstantTimeCompare(hash, dataHash[:]) != 1 {
		return errors.New("client secret mismatch")
	}
	return nil
}

// Hash returns the raw SHA-256 digest of data.
func (sha256Hasher) Hash(_ context.Context, data []byte) ([]byte, error) {
	sum := sha256.Sum256(data)
	return sum[:], nil
}

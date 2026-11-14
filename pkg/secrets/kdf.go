// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package secrets

import (
	"bytes"
	"crypto/rand"
	"crypto/subtle"
	"fmt"
	"sync"

	"golang.org/x/crypto/argon2"
)

// Argon2id cost parameters for secrets file format version 1.
//
// These live in code rather than in the file. A secrets file records only its
// format version, and the version determines the cost, which keeps attacker
// controllable values out of Argon2id's memory allocation and makes raising the
// cost an explicit new format version rather than a silent per-file property.
const (
	argon2Time        uint32 = 2
	argon2MemoryKiB   uint32 = 19456 // 19 MiB, the OWASP minimum for Argon2id
	argon2Parallelism uint8  = 1

	// derivedKeyLength is the key size required by AES-256-GCM.
	derivedKeyLength = 32
	// saltLength is the size of the per-file salt.
	saltLength = 16
)

// generateSalt returns a fresh cryptographically random salt for a secrets file.
func generateSalt() ([]byte, error) {
	salt := make([]byte, saltLength)
	if _, err := rand.Read(salt); err != nil {
		return nil, fmt.Errorf("failed to generate salt: %w", err)
	}
	return salt, nil
}

// deriveKey derives the AES-256-GCM key for a secrets file from the user's
// password and that file's salt.
func deriveKey(password, salt []byte) []byte {
	return argon2.IDKey(password, salt, argon2Time, argon2MemoryKiB, argon2Parallelism, derivedKeyLength)
}

// lastDerived memoises the most recently derived key for the lifetime of the
// process.
//
// Deriving a key costs roughly 16ms and 19MiB by design, and a new
// EncryptedManager is built for nearly every operation that touches a secret,
// including once per request in the API server. Without this memo those costs
// are paid per request, which would turn the memory hardness that protects the
// file at rest into a denial of service vector against the server.
//
// A single slot is enough: a process works with one secrets file under one
// password, so a larger cache would only add an eviction policy to maintain.
// Holding the lock across the derivation is deliberate — concurrent callers
// then wait for the first derivation and reuse its result, rather than each
// allocating 19MiB at once.
var (
	lastDerivedMu sync.Mutex
	lastDerived   *derivedKey
)

type derivedKey struct {
	path     string
	salt     []byte
	password []byte
	key      []byte
}

// deriveKeyCached returns the key for the given file and salt, deriving it only
// if it is not the one already memoised.
//
// The memo is matched on the password as well as the file and salt, so a second
// password for the same file derives its own key rather than being handed the
// first one. The password is compared, never hashed: a fast hash of a password
// is the very thing this file exists to avoid.
func deriveKeyCached(filePath string, password, salt []byte) []byte {
	lastDerivedMu.Lock()
	defer lastDerivedMu.Unlock()

	if d := lastDerived; d != nil && d.path == filePath && bytes.Equal(d.salt, salt) &&
		subtle.ConstantTimeCompare(d.password, password) == 1 {
		return d.key
	}

	key := deriveKey(password, salt)
	lastDerived = &derivedKey{
		path:     filePath,
		salt:     bytes.Clone(salt),
		password: bytes.Clone(password),
		key:      key,
	}
	return key
}

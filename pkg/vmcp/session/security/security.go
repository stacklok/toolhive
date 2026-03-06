// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package security provides cryptographic utilities for session token binding
// and hijacking prevention. It handles HMAC-SHA256 token hashing, salt generation,
// and constant-time comparison to prevent timing attacks.
package security

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
)

const (
	// SHA256HexLen is the length of a hex-encoded SHA256 hash (32 bytes = 64 hex characters)
	SHA256HexLen = 64
)

// GenerateSalt generates a cryptographically secure random salt for token hashing.
// Returns 16 bytes of random data from crypto/rand.
//
// Each session should have a unique salt to provide additional entropy and prevent
// attacks that work across multiple sessions.
func GenerateSalt() ([]byte, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return nil, fmt.Errorf("failed to generate salt: %w", err)
	}
	return salt, nil
}

// HashToken returns the hex-encoded HMAC-SHA256 hash of a raw bearer token string.
// Uses HMAC with a server-managed secret and per-session salt to prevent offline
// attacks if session storage is compromised.
//
// For empty tokens (anonymous sessions) it returns the empty string, which is
// the sentinel value used to identify sessions created without credentials.
// The raw token is never stored — only the hash.
//
// Parameters:
//   - token: The bearer token to hash
//   - secret: Server-managed HMAC secret (should be 32+ bytes)
//   - salt: Per-session random salt (typically 16 bytes)
//
// Security: Uses HMAC-SHA256 instead of plain SHA256 to prevent rainbow table
// attacks and offline brute force if session state leaks from Redis/Valkey.
func HashToken(token string, secret, salt []byte) string {
	if token == "" {
		return ""
	}
	h := hmac.New(sha256.New, secret)
	h.Write(salt)
	h.Write([]byte(token))
	return hex.EncodeToString(h.Sum(nil))
}

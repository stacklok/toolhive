// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package binding is the single owner of the identity-binding format used by
// vMCP session storage. An identity binding encodes the OIDC principal that
// created a session as a single opaque string suitable for storage in a
// key/value store such as Redis or Valkey.
//
// # Format
//
// A bound identity is encoded as iss + "\x00" + sub. NUL is rejected from
// either half by Format and Parse: OIDC Core does not formally forbid NUL in
// sub, but no real-world issuer emits one, and accepting it would let a
// corrupted or adversarial value re-split during Parse.
//
// Sessions created before any auth middleware ran use the literal
// "unauthenticated" sentinel. The sentinel must not contain '\x00' so it
// cannot collide with any bound form; any future format change must keep
// the sentinel and the bound-form value sets non-overlapping.
//
// # Trust boundary
//
// Bindings are stored plaintext at rest. They are PII but not credentials —
// they identify but do not authenticate. They carry no freshness signal (no
// exp, no nonce) and are NOT a substitute for token validation: callers must
// only compare a stored binding against a freshly-validated (iss, sub) pair
// from the current request's token.
package binding

import (
	"errors"
	"strings"
)

// UnauthenticatedSentinel is the binding value stored for sessions that were
// created without an authenticated identity (auth middleware not present or
// identity nil).
const UnauthenticatedSentinel = "unauthenticated"

// ErrInvalidBinding is returned by Format when either input is empty or
// contains a NUL byte.
var ErrInvalidBinding = errors.New("invalid identity binding")

// Format returns the canonical on-the-wire form of an identity binding:
// iss + "\x00" + sub. Returns ErrInvalidBinding when either input is empty
// or contains a NUL byte.
func Format(iss, sub string) (string, error) {
	if iss == "" || sub == "" {
		return "", ErrInvalidBinding
	}
	if strings.ContainsRune(iss, '\x00') || strings.ContainsRune(sub, '\x00') {
		return "", ErrInvalidBinding
	}
	return iss + "\x00" + sub, nil
}

// Parse splits an on-the-wire binding into its (iss, sub) components.
// Returns ok=true only when s contains exactly one NUL and both halves are
// non-empty. Returns ok=false for the unauthenticated sentinel, for malformed
// input, and for empty strings. Callers must check ok; the empty-string
// return values are not meaningful when ok=false.
func Parse(s string) (iss, sub string, ok bool) {
	iss, sub, found := strings.Cut(s, "\x00")
	if !found || iss == "" || sub == "" {
		return "", "", false
	}
	// strings.Cut splits on the first NUL, so iss cannot contain one. Sub may
	// still carry trailing NULs from a malformed input; reject those.
	if strings.ContainsRune(sub, '\x00') {
		return "", "", false
	}
	return iss, sub, true
}

// IsUnauthenticated reports whether s is the literal unauthenticated sentinel.
func IsUnauthenticated(s string) bool {
	return s == UnauthenticatedSentinel
}

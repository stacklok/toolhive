// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package crypto

import (
	"golang.org/x/oauth2"
)

// PKCEChallengeMethodS256 is the PKCE challenge method using SHA-256 (RFC 7636).
const PKCEChallengeMethodS256 = "S256"

// GeneratePKCEVerifier generates a cryptographically random code_verifier
// per RFC 7636 Section 4.1.
// The verifier is 43 characters (32 bytes base64url encoded without padding),
// using characters from the base64url alphabet: [A-Z], [a-z], [0-9], "-", "_".
//
// This function delegates to oauth2.GenerateVerifier() from golang.org/x/oauth2.
// It will panic on crypto/rand read failure (which is appropriate for this case).
func GeneratePKCEVerifier() string {
	return oauth2.GenerateVerifier()
}

// ComputePKCEChallenge computes the code_challenge from a code_verifier
// using the S256 method per RFC 7636 Section 4.2.
// code_challenge = BASE64URL(SHA256(code_verifier))
//
// This function delegates to oauth2.S256ChallengeFromVerifier() from golang.org/x/oauth2.
func ComputePKCEChallenge(verifier string) string {
	return oauth2.S256ChallengeFromVerifier(verifier)
}

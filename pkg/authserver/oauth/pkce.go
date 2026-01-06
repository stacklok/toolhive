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
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
)

// PKCEChallengeMethodS256 is the PKCE challenge method using SHA-256 (RFC 7636).
const PKCEChallengeMethodS256 = "S256"

// pkceVerifierLength is the length of the code verifier in bytes before encoding.
// RFC 7636 requires the verifier to be 43-128 characters after base64url encoding.
// 32 bytes = 43 characters after base64url encoding (without padding).
const pkceVerifierLength = 32

// GeneratePKCEVerifier generates a cryptographically random code_verifier
// per RFC 7636 Section 4.1.
// The verifier is 43 characters (32 bytes base64url encoded without padding),
// using characters from the unreserved set: [A-Z], [a-z], [0-9], "-", ".", "_", "~".
func GeneratePKCEVerifier() (string, error) {
	b := make([]byte, pkceVerifierLength)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	// base64url encoding without padding produces URL-safe characters
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// ComputePKCEChallenge computes the code_challenge from a code_verifier
// using the S256 method per RFC 7636 Section 4.2.
// code_challenge = BASE64URL(SHA256(code_verifier))
func ComputePKCEChallenge(verifier string) string {
	hash := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(hash[:])
}

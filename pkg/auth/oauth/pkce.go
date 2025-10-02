// Package oauth provides OAuth 2.0 and OIDC authentication functionality.
package oauth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
)

// PKCEParams holds PKCE code verifier and challenge
type PKCEParams struct {
	CodeVerifier  string
	CodeChallenge string
}

// GeneratePKCEParams generates PKCE code verifier and challenge using S256 method
// Implements RFC 7636 (Proof Key for Code Exchange)
func GeneratePKCEParams() (*PKCEParams, error) {
	// Generate code verifier (43-128 characters, RFC 7636)
	verifierBytes := make([]byte, 32)
	if _, err := rand.Read(verifierBytes); err != nil {
		return nil, fmt.Errorf("failed to generate code verifier: %w", err)
	}
	codeVerifier := base64.RawURLEncoding.EncodeToString(verifierBytes)

	// Use S256 method for enhanced security (RFC 7636 recommendation)
	hash := sha256.Sum256([]byte(codeVerifier))
	codeChallenge := base64.RawURLEncoding.EncodeToString(hash[:])

	return &PKCEParams{
		CodeVerifier:  codeVerifier,
		CodeChallenge: codeChallenge,
	}, nil
}

// GenerateState generates a random state parameter for CSRF protection
func GenerateState() (string, error) {
	stateBytes := make([]byte, 16)
	if _, err := rand.Read(stateBytes); err != nil {
		return "", fmt.Errorf("failed to generate state: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(stateBytes), nil
}

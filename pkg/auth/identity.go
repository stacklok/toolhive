// Package auth provides authentication and authorization utilities.
package auth

import (
	"encoding/json"
	"fmt"
)

// Identity represents an authenticated user or service account.
// This is the primary type for representing authenticated principals throughout ToolHive.
type Identity struct {
	// Subject is the unique identifier for the principal (from 'sub' claim).
	// This is always required per OIDC Core 1.0 spec § 5.1.
	Subject string

	// Name is the human-readable name (from 'name' claim).
	Name string

	// Email is the email address (from 'email' claim, if available).
	Email string

	// Groups are the groups this identity belongs to.
	//
	// NOTE: This field is intentionally NOT populated by authentication middleware.
	// Authorization logic MUST extract groups from the Claims map, as group claim
	// names vary by provider (e.g., "groups", "roles", "cognito:groups").
	Groups []string

	// Claims contains additional claims from the auth token.
	// This preserves all JWT claims for authorization policies.
	Claims map[string]any

	// Token is the original authentication token (for pass-through scenarios).
	// This is redacted in String() and MarshalJSON() to prevent leakage.
	Token string

	// TokenType is the type of token (e.g., "Bearer", "JWT").
	TokenType string

	// Metadata stores additional identity information.
	Metadata map[string]string
}

// String returns a string representation of the Identity with sensitive fields redacted.
// This prevents accidental token leakage when the Identity is logged or printed.
func (i *Identity) String() string {
	if i == nil {
		return "<nil>"
	}

	return fmt.Sprintf("Identity{Subject:%q}", i.Subject)
}

// MarshalJSON implements json.Marshaler to redact sensitive fields during JSON serialization.
// This prevents accidental token leakage in structured logs, API responses, or audit logs.
func (i *Identity) MarshalJSON() ([]byte, error) {
	if i == nil {
		return []byte("null"), nil
	}

	// Create a safe representation with lowercase field names and redacted token
	type SafeIdentity struct {
		Subject   string            `json:"subject"`
		Name      string            `json:"name"`
		Email     string            `json:"email"`
		Groups    []string          `json:"groups"`
		Claims    map[string]any    `json:"claims"`
		Token     string            `json:"token"`
		TokenType string            `json:"tokenType"`
		Metadata  map[string]string `json:"metadata"`
	}

	token := i.Token
	if token != "" {
		token = "REDACTED"
	}

	return json.Marshal(&SafeIdentity{
		Subject:   i.Subject,
		Name:      i.Name,
		Email:     i.Email,
		Groups:    i.Groups,
		Claims:    i.Claims,
		Token:     token,
		TokenType: i.TokenType,
		Metadata:  i.Metadata,
	})
}

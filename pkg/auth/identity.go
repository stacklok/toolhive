// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package auth provides authentication and authorization utilities.
package auth

import (
	"encoding/json"
	"fmt"

	"github.com/golang-jwt/jwt/v5"
)

// PrincipalInfo contains the non-sensitive identity fields safe for external consumption.
// This is the canonical projection of Identity for webhook payloads, audit logs, and
// any context where credentials must not appear — not even in redacted form.
//
// Identity embeds this type, so fields are accessible directly on Identity
// (e.g., identity.Subject, identity.Email) while keeping the credential-free
// subset available as a first-class type for external APIs.
type PrincipalInfo struct {
	// Subject is the unique identifier for the principal (from 'sub' claim).
	// This is always required per OIDC Core 1.0 spec § 5.1.
	Subject string `json:"sub,omitempty"`

	// Name is the human-readable name (from 'name' claim).
	Name string `json:"name,omitempty"`

	// Email is the email address (from 'email' claim, if available).
	Email string `json:"email,omitempty"`

	// Groups are the groups this identity belongs to.
	//
	// NOTE: This field is intentionally NOT populated by authentication middleware.
	// Authorization logic MUST extract groups from the Claims map, as group claim
	// names vary by provider (e.g., "groups", "roles", "cognito:groups").
	Groups []string `json:"groups,omitempty"`

	// Claims contains additional claims from the auth token.
	// This preserves all JWT claims for authorization policies.
	Claims map[string]any `json:"claims,omitempty"`
}

// Identity represents an authenticated user or service account.
// This is the primary type for representing authenticated principals throughout ToolHive.
//
// It embeds PrincipalInfo (the credential-free subset) and adds sensitive fields
// (Token, TokenType) and internal metadata that must never be externalized.
type Identity struct {
	PrincipalInfo

	// Token is the original authentication token (for pass-through scenarios).
	// This is redacted in String() and MarshalJSON() to prevent leakage.
	Token string

	// TokenType is the type of token (e.g., "Bearer", "JWT").
	TokenType string

	// Metadata stores additional identity information.
	Metadata map[string]string

	// UpstreamTokens maps upstream provider names to their access tokens.
	// This is populated by the auth middleware when an embedded auth server
	// is active and the JWT contains a token session ID (tsid claim).
	// Redacted in MarshalJSON() to prevent token leakage.
	// MUST NOT be mutated after the Identity is placed in the request context.
	UpstreamTokens map[string]string
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
		Subject        string            `json:"subject"`
		Name           string            `json:"name"`
		Email          string            `json:"email"`
		Groups         []string          `json:"groups"`
		Claims         map[string]any    `json:"claims"`
		Token          string            `json:"token"`
		TokenType      string            `json:"tokenType"`
		Metadata       map[string]string `json:"metadata"`
		UpstreamTokens map[string]string `json:"upstreamTokens,omitempty"`
	}

	token := i.Token
	if token != "" {
		token = "REDACTED"
	}

	// Redact upstream tokens: preserve keys, replace non-empty values
	var redactedUpstreamTokens map[string]string
	// Guard with len() > 0 (not != nil) so that both nil and empty maps
	// produce a nil redactedUpstreamTokens, which omitempty then omits.
	if len(i.UpstreamTokens) > 0 {
		redactedUpstreamTokens = make(map[string]string, len(i.UpstreamTokens))
		for k, v := range i.UpstreamTokens {
			if v != "" {
				redactedUpstreamTokens[k] = "REDACTED"
			} else {
				redactedUpstreamTokens[k] = ""
			}
		}
	}

	return json.Marshal(&SafeIdentity{
		Subject:        i.Subject,
		Name:           i.Name,
		Email:          i.Email,
		Groups:         i.Groups,
		Claims:         i.Claims,
		Token:          token,
		TokenType:      i.TokenType,
		Metadata:       i.Metadata,
		UpstreamTokens: redactedUpstreamTokens,
	})
}

// GetPrincipalInfo returns a copy of the credential-free PrincipalInfo suitable
// for external consumption (webhook payloads, audit logs, etc.).
// Token, TokenType, and Metadata are structurally excluded.
func (i *Identity) GetPrincipalInfo() *PrincipalInfo {
	if i == nil {
		return nil
	}

	pi := i.PrincipalInfo
	return &pi
}

// defaultGroupClaimNames lists common group claim names across popular identity
// providers. They are checked in order; the first non-empty match is returned.
var defaultGroupClaimNames = []string{"groups", "roles", "cognito:groups"}

// ExtractGroupsFromClaims looks for group membership claims in the provided JWT
// claims map. It checks customClaimName first (if non-empty), then falls back to
// the well-known names "groups", "roles", and "cognito:groups". Returns the first
// non-empty string-slice match, or nil when no group claim is found.
//
// Passing a non-empty customClaimName allows callers to support IDPs that use
// URI-style claim names (e.g. "https://example.com/groups" used by Auth0/Okta).
func ExtractGroupsFromClaims(claims jwt.MapClaims, customClaimName string) []string {
	names := defaultGroupClaimNames
	if customClaimName != "" {
		// Prepend the custom name so it takes priority over well-known names.
		names = append([]string{customClaimName}, defaultGroupClaimNames...)
	}

	for _, name := range names {
		val, ok := claims[name]
		if !ok {
			continue
		}
		switch v := val.(type) {
		case []interface{}:
			groups := make([]string, 0, len(v))
			for _, g := range v {
				if s, ok := g.(string); ok {
					groups = append(groups, s)
				}
			}
			if len(groups) > 0 {
				return groups
			}
		case []string:
			if len(v) > 0 {
				return v
			}
		}
	}
	return nil
}

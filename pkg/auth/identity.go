// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package auth provides authentication and authorization utilities.
package auth

import (
	"encoding/json"
	"fmt"

	upstreamtoken "github.com/stacklok/toolhive/pkg/auth/upstreamtoken"
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

	// PlatformUserID is the platform's canonical user identifier.
	//
	// It defaults to the sub claim (see claimsToIdentity). That default is
	// correct for standalone OSS use (where sub IS the canonical local User.ID)
	// and for AS-bearer paths under an enterprise directory binding (where the
	// AS-minted sub equals the directory user_id). It is INCORRECT for any
	// middleware that validates a JWT issued by a different IdP whose sub is not
	// the platform-canonical user identifier (e.g. a corporate IdP whose sub
	// rotates per-application). Such middleware MUST override PlatformUserID
	// (typically via a resolution call into a directory service) before calling
	// WithIdentity. On request-serving paths storage reads this value via
	// CanonicalUserFromContext (which falls back to this field when no dedicated
	// platform-user key is set); an unresolved PlatformUserID from a corporate-IdP
	// bearer will silently mis-key writes.
	//
	// Only claimsToIdentity populates this field today. Other Identity
	// constructors in this repo (local.go, anonymous.go) intentionally leave it
	// unset for now: standalone OSS keys upstream-token storage on the session,
	// not PlatformUserID, so those paths have no canonical-user reader to
	// satisfy. Populating them is deferred until a storage layer that reads
	// PlatformUserID on those paths exists.
	PlatformUserID string `json:"platform_user_id,omitempty"`

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
//
// # Field-completeness contract
//
// A non-nil *Identity is always COMPLETE: all credential fields that are relevant
// for its authentication path are populated by the constructor that created it.
// Anonymous and "no principal known" states are represented by nil, not by a
// struct with empty credential fields. Code that receives a non-nil *Identity is
// entitled to assume it is fully initialized for its session-binding semantics.
//
// In particular, do NOT construct &Identity{Subject: …} with Token and
// UpstreamTokens unset as a substitute for nil when no live bearer token is
// available (e.g. during session restore). Pass nil instead and let downstream
// consumers read the identity from the per-request context, where
// TokenValidator.Middleware places a fully-populated identity on every incoming
// request.
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
	//
	// State semantics:
	//   - nil            — no tsid claim was present on the incoming JWT
	//                      (middleware did not attempt to load credentials).
	//   - empty map      — tsid claim was valid but no providers had a stored
	//                      access token for the session.
	//   - populated map  — keys are upstream provider names; values are the
	//                      stored access token strings.
	//
	// MUST NOT be mutated after the Identity is placed in the publicly-reachable
	// request context. It MAY be mutated while the Identity is reachable only via
	// a load-scoped ctx, provided the loader does not share that ctx with
	// concurrent code. See TokenValidator.Middleware for the canonical pattern.
	UpstreamTokens map[string]string

	// UpstreamIDTokens maps upstream provider names to their ID tokens.
	// This is populated by the auth middleware when an embedded auth server
	// is active and the JWT contains a token session ID (tsid claim).
	// Each value is the rotated ID token when a refresh produced one
	// (OIDC Core 1.0 §12.2), otherwise the original JWT captured at the initial
	// OIDC login; it is not independently validated for freshness.
	//
	// State semantics:
	//   - nil            — no tsid claim was present on the incoming JWT
	//                      (middleware did not attempt to load credentials).
	//   - empty map      — tsid claim was valid but no providers had an
	//                      ID token stored for the session.
	//   - populated map  — keys are upstream provider names; values are the
	//                      stored ID token JWTs (may be expired; callers MUST
	//                      validate the `exp` claim before use).
	//
	// Redacted in MarshalJSON() to prevent token leakage.
	// MUST NOT be mutated after the Identity is placed in the request context.
	UpstreamIDTokens map[string]string
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
		Subject          string            `json:"subject"`
		PlatformUserID   string            `json:"platformUserId,omitempty"`
		Name             string            `json:"name"`
		Email            string            `json:"email"`
		Groups           []string          `json:"groups"`
		Claims           map[string]any    `json:"claims"`
		Token            string            `json:"token"`
		TokenType        string            `json:"tokenType"`
		Metadata         map[string]string `json:"metadata"`
		UpstreamTokens   map[string]string `json:"upstreamTokens,omitempty"`
		UpstreamIDTokens map[string]string `json:"upstreamIDTokens,omitempty"`
	}

	const redacted = "REDACTED"

	token := i.Token
	if token != "" {
		token = redacted
	}

	claims := i.Claims
	if _, hasTsid := claims[upstreamtoken.TokenSessionIDClaimKey]; hasTsid {
		claims = make(map[string]any, len(i.Claims))
		for k, v := range i.Claims {
			if k != upstreamtoken.TokenSessionIDClaimKey {
				claims[k] = v
			}
		}
	}

	// Redact upstream tokens: preserve keys, replace non-empty values
	var redactedUpstreamTokens map[string]string
	// Guard with len() > 0 (not != nil) so that both nil and empty maps
	// produce a nil redactedUpstreamTokens, which omitempty then omits.
	if len(i.UpstreamTokens) > 0 {
		redactedUpstreamTokens = make(map[string]string, len(i.UpstreamTokens))
		for k, v := range i.UpstreamTokens {
			if v != "" {
				redactedUpstreamTokens[k] = redacted
			} else {
				redactedUpstreamTokens[k] = ""
			}
		}
	}

	// Redact upstream ID tokens with the same pattern as access tokens.
	var redactedUpstreamIDTokens map[string]string
	if len(i.UpstreamIDTokens) > 0 {
		redactedUpstreamIDTokens = make(map[string]string, len(i.UpstreamIDTokens))
		for k, v := range i.UpstreamIDTokens {
			if v != "" {
				redactedUpstreamIDTokens[k] = redacted
			} else {
				redactedUpstreamIDTokens[k] = ""
			}
		}
	}

	return json.Marshal(&SafeIdentity{
		Subject:          i.Subject,
		PlatformUserID:   i.PlatformUserID,
		Name:             i.Name,
		Email:            i.Email,
		Groups:           i.Groups,
		Claims:           claims,
		Token:            token,
		TokenType:        i.TokenType,
		Metadata:         i.Metadata,
		UpstreamTokens:   redactedUpstreamTokens,
		UpstreamIDTokens: redactedUpstreamIDTokens,
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

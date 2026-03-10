// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package security provides cryptographic utilities for session token binding
// and hijacking prevention. It handles HMAC-SHA256 token hashing, salt generation,
// and constant-time comparison to prevent timing attacks.
package security

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"

	"github.com/stacklok/toolhive/pkg/auth"
	pkgsecurity "github.com/stacklok/toolhive/pkg/security"
	"github.com/stacklok/toolhive/pkg/vmcp"
	sessiontypes "github.com/stacklok/toolhive/pkg/vmcp/session/types"
)

const (
	// SHA256HexLen is the length of a hex-encoded SHA256 hash (32 bytes = 64 hex characters)
	SHA256HexLen = 64

	// metadataKeyTokenHash is the session metadata key for the token hash.
	// Imported from types package to ensure consistency across all packages.
	metadataKeyTokenHash = sessiontypes.MetadataKeyTokenHash

	// metadataKeyTokenSalt is the session metadata key for the token salt.
	// Imported from types package to ensure consistency across all packages.
	metadataKeyTokenSalt = sessiontypes.MetadataKeyTokenSalt
)

// generateSalt generates a cryptographically secure random salt for token hashing.
// Returns 16 bytes of random data from crypto/rand.
//
// Each session should have a unique salt to provide additional entropy and prevent
// attacks that work across multiple sessions.
func generateSalt() ([]byte, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return nil, fmt.Errorf("failed to generate salt: %w", err)
	}
	return salt, nil
}

// hashToken returns the hex-encoded HMAC-SHA256 hash of a raw bearer token string.
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
func hashToken(token string, secret, salt []byte) string {
	if token == "" {
		return ""
	}
	h := hmac.New(sha256.New, secret)
	h.Write(salt)
	h.Write([]byte(token))
	return hex.EncodeToString(h.Sum(nil))
}

// hijackPreventionDecorator wraps a session and adds token binding validation
// to prevent session hijacking attacks. It validates that all requests come from
// the same identity that created the session.
//
// The decorator is applied by PreventSessionHijacking to ALL sessions (both authenticated
// and anonymous). For authenticated sessions, it validates the caller's token matches
// the creator's token. For anonymous sessions (allowAnonymous=true), it allows nil
// callers and prevents session upgrade attacks by rejecting any token presentation.
//
// The decorator embeds MultiSession and only overrides the methods that require
// validation (CallTool, ReadResource, GetPrompt). All other methods are automatically
// delegated to the embedded session.
type hijackPreventionDecorator struct {
	sessiontypes.MultiSession // Embedded interface - provides automatic delegation for most methods

	// Token binding fields: enforce that subsequent requests come from the same
	// identity that created the session.
	// These fields are immutable after decorator creation (no mutex needed).
	boundTokenHash string // HMAC-SHA256 hash of creator's token (empty for anonymous)
	tokenSalt      []byte // Random salt used for HMAC (empty for anonymous)
	hmacSecret     []byte // Server-managed secret for HMAC-SHA256
	allowAnonymous bool   // Whether to allow nil caller
}

// validateCaller checks if the provided caller identity matches the session owner.
// Returns nil if validation succeeds, or an error if:
//   - The session requires a bound identity but caller is nil (ErrNilCaller)
//   - The caller's token hash doesn't match the session owner (ErrUnauthorizedCaller)
//   - An anonymous session receives a caller with a non-empty token (ErrUnauthorizedCaller)
//
// For anonymous sessions (allowAnonymous=true, boundTokenHash=""), validation succeeds
// only when the caller is nil or has an empty token (prevents session upgrade attacks).
func (d hijackPreventionDecorator) validateCaller(caller *auth.Identity) error {
	// No lock needed - token binding fields are immutable after decorator creation

	// Anonymous sessions: reject callers that present tokens
	if d.allowAnonymous && d.boundTokenHash == "" {
		// Prevent session upgrade attack: anonymous sessions cannot accept tokens
		if caller != nil && caller.Token != "" {
			slog.Warn("token validation failed: session upgrade attack prevented",
				"reason", "token_presented_to_anonymous_session",
			)
			return sessiontypes.ErrUnauthorizedCaller
		}
		return nil
	}

	// Bound sessions require a caller
	if caller == nil {
		slog.Warn("token validation failed: nil caller for bound session",
			"reason", "nil_caller",
		)
		return sessiontypes.ErrNilCaller
	}

	// Defensive check: bound sessions must have a non-empty token hash.
	// This prevents misconfigured sessions from accepting empty tokens.
	// Scenario: if boundTokenHash="" and caller.Token="", both would hash to "",
	// and ConstantTimeHashCompare would return true (both empty case).
	if d.boundTokenHash == "" {
		slog.Error("token validation failed: bound session has empty token hash",
			"reason", "misconfigured_session",
		)
		return sessiontypes.ErrSessionOwnerUnknown
	}

	// Compute caller's token hash using the same HMAC secret and salt
	callerHash := hashToken(caller.Token, d.hmacSecret, d.tokenSalt)

	// Constant-time comparison to prevent timing attacks
	if !pkgsecurity.ConstantTimeHashCompare(d.boundTokenHash, callerHash, SHA256HexLen) {
		slog.Warn("token validation failed: token hash mismatch",
			"reason", "token_hash_mismatch",
		)
		return sessiontypes.ErrUnauthorizedCaller
	}

	return nil
}

// CallTool validates the caller identity before delegating to the embedded session.
func (d hijackPreventionDecorator) CallTool(
	ctx context.Context,
	caller *auth.Identity,
	toolName string,
	arguments map[string]any,
	meta map[string]any,
) (*vmcp.ToolCallResult, error) {
	// Validate caller identity
	if err := d.validateCaller(caller); err != nil {
		return nil, err
	}

	return d.MultiSession.CallTool(ctx, caller, toolName, arguments, meta)
}

// ReadResource validates the caller identity before delegating to the embedded session.
func (d hijackPreventionDecorator) ReadResource(
	ctx context.Context,
	caller *auth.Identity,
	uri string,
) (*vmcp.ResourceReadResult, error) {
	// Validate caller identity
	if err := d.validateCaller(caller); err != nil {
		return nil, err
	}

	return d.MultiSession.ReadResource(ctx, caller, uri)
}

// GetPrompt validates the caller identity before delegating to the embedded session.
func (d hijackPreventionDecorator) GetPrompt(
	ctx context.Context,
	caller *auth.Identity,
	name string,
	arguments map[string]any,
) (*vmcp.PromptGetResult, error) {
	// Validate caller identity
	if err := d.validateCaller(caller); err != nil {
		return nil, err
	}

	return d.MultiSession.GetPrompt(ctx, caller, name, arguments)
}

// PreventSessionHijacking wraps a session with hijack prevention security measures.
// It computes token binding hashes, stores them in session metadata, and returns
// a decorated session that validates caller identity on every operation.
//
// Whether the session is anonymous is derived from the identity: nil identity or
// empty token means anonymous, a non-empty token means bound/authenticated.
//
// For authenticated sessions (identity.Token != ""):
//   - Generates a unique random salt
//   - Computes HMAC-SHA256 hash of the bearer token
//   - Stores hash and salt in session metadata
//   - Returns decorator that validates every request against the creator's token
//
// For anonymous sessions (identity == nil or identity.Token == ""):
//   - Stores an empty string sentinel for the token hash metadata key
//   - Omits the salt metadata key entirely (no salt is generated for anonymous sessions)
//   - Returns decorator that allows nil callers and rejects token presentation
//
// Security:
//   - Makes defensive copies of secret and salt to prevent external mutation
//   - Uses constant-time comparison to prevent timing attacks
//   - Prevents session upgrade attacks (anonymous → authenticated)
//   - Raw tokens are never stored, only HMAC-SHA256 hashes
//
// Returns an error if:
//   - session is nil
//   - salt generation fails
func PreventSessionHijacking(
	session sessiontypes.MultiSession,
	hmacSecret []byte,
	identity *auth.Identity,
) (sessiontypes.MultiSession, error) {
	if session == nil {
		return nil, fmt.Errorf("session must not be nil")
	}
	allowAnonymous := sessiontypes.ShouldAllowAnonymous(identity)

	// Note: Pass-through methods (ID, Type, CreatedAt, etc.) are validated by the
	// type system when the decorator is used. We don't validate them here to keep
	// the constructor simple and allow minimal mocks for testing.

	var boundTokenHash string
	var tokenSalt []byte
	var err error

	// Compute token binding for authenticated sessions
	if !allowAnonymous && identity != nil && identity.Token != "" {
		// Generate unique salt for this session
		tokenSalt, err = generateSalt()
		if err != nil {
			return nil, fmt.Errorf("failed to generate token salt: %w", err)
		}
		// Compute HMAC-SHA256 hash with server secret and per-session salt
		boundTokenHash = hashToken(identity.Token, hmacSecret, tokenSalt)
	}

	// Store hash and salt in session metadata for persistence, auditing,
	// and backward compatibility
	session.SetMetadata(metadataKeyTokenHash, boundTokenHash)
	if len(tokenSalt) > 0 {
		session.SetMetadata(metadataKeyTokenSalt, hex.EncodeToString(tokenSalt))
	}

	// Make defensive copies of slices to prevent external mutation
	var hmacSecretCopy, tokenSaltCopy []byte
	if len(hmacSecret) > 0 {
		hmacSecretCopy = append([]byte(nil), hmacSecret...)
	}
	if len(tokenSalt) > 0 {
		tokenSaltCopy = append([]byte(nil), tokenSalt...)
	}

	// Wrap with hijackPreventionDecorator for runtime validation.
	// The decorator embeds the MultiSession interface, so all methods are automatically
	// delegated except for the three we override (CallTool, ReadResource, GetPrompt).
	return &hijackPreventionDecorator{
		MultiSession:   session,
		allowAnonymous: allowAnonymous,
		hmacSecret:     hmacSecretCopy,
		boundTokenHash: boundTokenHash,
		tokenSalt:      tokenSaltCopy,
	}, nil
}

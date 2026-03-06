// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package session

import (
	"context"
	"log/slog"

	"github.com/stacklok/toolhive/pkg/auth"
	pkgsecurity "github.com/stacklok/toolhive/pkg/security"
	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/session/security"
	sessiontypes "github.com/stacklok/toolhive/pkg/vmcp/session/types"
)

// HijackPreventionDecorator wraps a MultiSession and adds token binding validation
// to prevent session hijacking attacks. It validates that all requests come from
// the same identity that created the session.
//
// The decorator is applied by the session factory to ALL sessions (both authenticated
// and anonymous). For authenticated sessions, it validates the caller's token matches
// the creator's token. For anonymous sessions (allowAnonymous=true), it allows nil
// callers and prevents session upgrade attacks by rejecting any token presentation.
type HijackPreventionDecorator struct {
	MultiSession // embedded to delegate non-overridden methods

	// Token binding fields: enforce that subsequent requests come from the same
	// identity that created the session.
	// These fields are immutable after decorator creation (no mutex needed).
	boundTokenHash string // HMAC-SHA256 hash of creator's token (empty for anonymous)
	tokenSalt      []byte // Random salt used for HMAC (empty for anonymous)
	hmacSecret     []byte // Server-managed secret for HMAC-SHA256
	allowAnonymous bool   // Whether to allow nil caller
}

// NewHijackPreventionDecorator creates a decorator that validates caller identity
// on every method call to prevent session hijacking.
//
// Parameters:
//   - session: The underlying MultiSession to wrap
//   - allowAnonymous: Whether to allow nil caller on subsequent requests
//   - hmacSecret: Server-managed secret for HMAC-SHA256 hashing
//   - boundTokenHash: Pre-computed HMAC-SHA256 hash of the creator's token (empty for anonymous)
//   - tokenSalt: The salt used to compute boundTokenHash (empty for anonymous)
//
// For anonymous sessions (allowAnonymous=true), the decorator allows nil callers
// and rejects any caller that presents a token (prevents session upgrade attacks).
//
// For bound sessions (allowAnonymous=false), the decorator uses the provided
// boundTokenHash and tokenSalt to validate every subsequent request against the
// creator's token using constant-time comparison.
//
// The hash and salt should be computed once by the factory using computeTokenBinding()
// to ensure consistency between metadata storage and runtime validation.
//
// Security: The constructor makes defensive copies of hmacSecret and tokenSalt
// to prevent external mutation after decorator creation.
func NewHijackPreventionDecorator(
	session MultiSession,
	allowAnonymous bool,
	hmacSecret []byte,
	boundTokenHash string,
	tokenSalt []byte,
) *HijackPreventionDecorator {
	// Make defensive copies of slices to prevent external mutation
	var hmacSecretCopy, tokenSaltCopy []byte
	if len(hmacSecret) > 0 {
		hmacSecretCopy = append([]byte(nil), hmacSecret...)
	}
	if len(tokenSalt) > 0 {
		tokenSaltCopy = append([]byte(nil), tokenSalt...)
	}

	return &HijackPreventionDecorator{
		MultiSession:   session,
		allowAnonymous: allowAnonymous,
		hmacSecret:     hmacSecretCopy,
		boundTokenHash: boundTokenHash,
		tokenSalt:      tokenSaltCopy,
	}
}

// validateCaller checks if the provided caller identity matches the session owner.
// Returns nil if validation succeeds, or an error if:
//   - The session requires a bound identity but caller is nil (ErrNilCaller)
//   - The caller's token hash doesn't match the session owner (ErrUnauthorizedCaller)
//   - An anonymous session receives a caller with a non-empty token (ErrUnauthorizedCaller)
//
// For anonymous sessions (allowAnonymous=true, boundTokenHash=""), validation succeeds
// only when the caller is nil or has an empty token (prevents session upgrade attacks).
func (d *HijackPreventionDecorator) validateCaller(caller *auth.Identity) error {
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
	callerHash := security.HashToken(caller.Token, d.hmacSecret, d.tokenSalt)

	// Constant-time comparison to prevent timing attacks
	if !pkgsecurity.ConstantTimeHashCompare(d.boundTokenHash, callerHash, security.SHA256HexLen) {
		slog.Warn("token validation failed: token hash mismatch",
			"reason", "token_hash_mismatch",
		)
		return sessiontypes.ErrUnauthorizedCaller
	}

	return nil
}

// CallTool validates the caller identity before delegating to the underlying session.
func (d *HijackPreventionDecorator) CallTool(
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

// ReadResource validates the caller identity before delegating to the underlying session.
func (d *HijackPreventionDecorator) ReadResource(
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

// GetPrompt validates the caller identity before delegating to the underlying session.
func (d *HijackPreventionDecorator) GetPrompt(
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

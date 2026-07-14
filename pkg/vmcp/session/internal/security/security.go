// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package security provides the session-hijack-prevention decorator for
// vMCP sessions. It binds a session to a stable identity tuple (iss, sub)
// extracted from the OIDC identity that created the session, and validates
// that every subsequent request comes from the same identity.
//
// Session bindings are stored as plaintext at rest in the session metadata
// (see pkg/vmcp/session/binding for the format and trust-boundary statement).
// The binding is NOT a credential; it identifies but does not authenticate.
// Callers must validate the request's token independently before passing the
// resulting *auth.Identity to validateCaller.
package security

import (
	"context"
	"crypto/subtle"
	"fmt"
	"log/slog"

	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/session/binding"
	sessiontypes "github.com/stacklok/toolhive/pkg/vmcp/session/types"
)

// sessionBindingDecorator wraps a session and adds identity-binding validation
// to prevent session hijacking attacks. It validates that all requests come from
// the same identity that created the session.
//
// The decorator is applied by BindSession to ALL sessions (both authenticated
// and anonymous). For authenticated sessions, it validates the caller's (iss, sub)
// identity binding matches the creator's binding. For anonymous sessions
// (allowAnonymous=true), it allows nil callers and prevents session upgrade
// attacks by rejecting any token presentation.
//
// The decorator embeds MultiSession and only overrides the methods that require
// validation (CallTool, ReadResource, GetPrompt). All other methods are
// automatically delegated to the embedded session.
type sessionBindingDecorator struct {
	sessiontypes.MultiSession // embedded — automatic delegation for unwrapped methods

	// boundIdentity is the canonical identity binding written at session
	// creation. Immutable after construction.
	//
	// For sessions allowed to be anonymous, boundIdentity is
	// binding.UnauthenticatedSentinel.
	// For sessions bound to an authenticated identity, boundIdentity is the
	// output of binding.Format(iss, sub).
	boundIdentity string

	// allowAnonymous tracks whether the session was created without a bound
	// identity. Used to reject session-upgrade attacks (caller presents a
	// token on an anonymous session).
	allowAnonymous bool
}

// extractBindingID derives the canonical identity-binding string from the
// given auth identity's OIDC claims. It reads "iss" and "sub" from
// identity.Claims (not identity.Subject) so that JWT-validation and
// introspection paths canonicalize against the same source.
//
// Returns ("", error) when:
//   - identity is nil.
//   - identity.Claims is missing "iss" or "sub".
//   - either claim is present but not a string.
//   - binding.Format rejects the (iss, sub) pair (empty halves or stray NULs).
//
// Callers MUST treat a non-nil error as "no identifying claims available"
// and fail closed (do not silently fall through to anonymous).
//
// TODO(#5306-followup): if/when RFC 7662 introspection becomes a top-level
// incoming-auth type, add a startup probe that verifies the IdP emits iss
// and sub in introspection responses (extractBindingID requires both).
func extractBindingID(identity *auth.Identity) (string, error) {
	if identity == nil {
		return "", fmt.Errorf("auth identity is nil")
	}

	issRaw, issPresent := identity.Claims["iss"]
	if !issPresent {
		return "", fmt.Errorf("auth identity is missing iss claim")
	}
	iss, issIsString := issRaw.(string)
	if !issIsString {
		return "", fmt.Errorf("auth identity has non-string iss claim")
	}

	subRaw, subPresent := identity.Claims["sub"]
	if !subPresent {
		return "", fmt.Errorf("auth identity is missing sub claim")
	}
	sub, subIsString := subRaw.(string)
	if !subIsString {
		return "", fmt.Errorf("auth identity has non-string sub claim")
	}

	b, err := binding.Format(iss, sub)
	if err != nil {
		return "", fmt.Errorf("auth identity (iss, sub) pair is invalid: %w", err)
	}
	return b, nil
}

// validateCaller checks the caller against the session's bound identity.
// It delegates to the free-function validateCallerBinding so the same audited
// logic backs both the decorator path and the exported [ValidateCaller] seam.
func (d sessionBindingDecorator) validateCaller(caller *auth.Identity) error {
	return validateCallerBinding(d.boundIdentity, d.allowAnonymous, caller)
}

// ValidateCaller checks caller against a stored identity-binding string (the
// value persisted under MetadataKeyIdentityBinding). It reproduces the
// decorator's per-request check for call paths that do not go through the
// [sessionBindingDecorator] — notably the Serve transport path, where the
// advertised set and call routing are owned by the core but identity binding
// must still be enforced by the session layer.
//
// allowAnonymous is derived from storedBinding: an anonymous session stores the
// UnauthenticatedSentinel, so IsUnauthenticated(storedBinding) is exactly the
// allowAnonymous flag BindSession recorded (BindSession writes the sentinel iff
// ShouldAllowAnonymous(identity)).
func ValidateCaller(storedBinding string, caller *auth.Identity) error {
	return validateCallerBinding(storedBinding, binding.IsUnauthenticated(storedBinding), caller)
}

// validateCallerBinding checks caller against a bound identity.
//
// Returns:
//   - ErrSessionOwnerUnknown when neither an anonymous marker nor a real binding
//     is present (programming error / corrupted metadata).
//   - ErrNilCaller when a bound session receives nil.
//   - ErrUnauthorizedCaller when:
//   - an anonymous session receives a caller presenting a token (upgrade attack),
//   - the caller's identity binding does not match the session's bound identity.
func validateCallerBinding(boundIdentity string, allowAnonymous bool, caller *auth.Identity) error {
	// Anonymous path: sessions that were created without a bound identity.
	if allowAnonymous && binding.IsUnauthenticated(boundIdentity) {
		// Prevent session upgrade attack: anonymous sessions cannot accept tokens.
		if caller != nil && caller.Token != "" {
			slog.Warn("identity binding validation failed: session upgrade attack prevented",
				"reason", "token_presented_to_anonymous_session",
			)
			return sessiontypes.ErrUnauthorizedCaller
		}
		return nil
	}

	// Bound sessions require a non-nil caller.
	if caller == nil {
		slog.Warn("identity binding validation failed: nil caller for bound session",
			"reason", "nil_caller",
		)
		return sessiontypes.ErrNilCaller
	}

	// Defensive check: the stored binding must be parsable. An unparsable value
	// means the session was misconfigured at construction time — fail closed
	// rather than accepting or rejecting based on garbage state.
	if !binding.IsUnauthenticated(boundIdentity) {
		if _, _, ok := binding.Parse(boundIdentity); !ok {
			slog.Error("identity binding validation failed: stored binding is not parsable",
				"reason", "misconfigured_session",
			)
			return sessiontypes.ErrSessionOwnerUnknown
		}
	}

	// Compute the caller's binding from their identity claims.
	callerBinding, err := extractBindingID(caller)
	if err != nil {
		slog.Warn("identity binding validation failed: could not extract caller binding",
			"reason", "caller_binding_extraction_failed",
			"error", err,
		)
		return sessiontypes.ErrUnauthorizedCaller
	}

	// ConstantTimeCompare is constant-time over content but short-circuits on
	// length mismatch. Leaking binding length is acceptable: iss is the OIDC
	// issuer (public, in the discovery document) and sub is an opaque
	// identifier whose length is typically per-issuer. Neither is secret.
	if subtle.ConstantTimeCompare([]byte(boundIdentity), []byte(callerBinding)) != 1 {
		slog.Warn("identity binding validation failed: identity binding mismatch",
			"reason", "identity_binding_mismatch",
		)
		return sessiontypes.ErrUnauthorizedCaller
	}

	return nil
}

// CallTool validates the caller identity before delegating to the embedded session.
func (d sessionBindingDecorator) CallTool(
	ctx context.Context,
	caller *auth.Identity,
	toolName string,
	arguments map[string]any,
	meta map[string]any,
) (*vmcp.ToolCallResult, error) {
	if err := d.validateCaller(caller); err != nil {
		return nil, err
	}

	return d.MultiSession.CallTool(ctx, caller, toolName, arguments, meta)
}

// ReadResource validates the caller identity before delegating to the embedded session.
func (d sessionBindingDecorator) ReadResource(
	ctx context.Context,
	caller *auth.Identity,
	uri string,
) (*vmcp.ResourceReadResult, error) {
	if err := d.validateCaller(caller); err != nil {
		return nil, err
	}

	return d.MultiSession.ReadResource(ctx, caller, uri)
}

// GetPrompt validates the caller identity before delegating to the embedded session.
func (d sessionBindingDecorator) GetPrompt(
	ctx context.Context,
	caller *auth.Identity,
	name string,
	arguments map[string]any,
) (*vmcp.PromptGetResult, error) {
	if err := d.validateCaller(caller); err != nil {
		return nil, err
	}

	return d.MultiSession.GetPrompt(ctx, caller, name, arguments)
}

// BindSession wraps a session with identity-binding validation. It writes
// the canonical binding into session metadata under MetadataKeyIdentityBinding
// and returns a decorator that validates the caller on every operation.
//
// Whether the session is anonymous is derived from the identity via
// types.ShouldAllowAnonymous.
//
// For bound sessions, the (iss, sub) tuple is extracted from identity.Claims.
// If the tuple cannot be extracted (missing claims, non-string claims, or
// invalid format), BindSession returns an error BEFORE writing anything to
// the session metadata — preserving the invariant that a session always has
// a valid MetadataKeyIdentityBinding value once written.
//
// Returns an error if session is nil, or if a bound identity is required
// but no valid (iss, sub) binding can be produced.
func BindSession(
	session sessiontypes.MultiSession,
	identity *auth.Identity,
) (sessiontypes.MultiSession, error) {
	if session == nil {
		return nil, fmt.Errorf("session must not be nil")
	}

	allowAnonymous := sessiontypes.ShouldAllowAnonymous(identity)

	// Determine the binding value to write. For bound sessions, extract the
	// (iss, sub) binding before touching session metadata — if extraction fails,
	// we must not leave a partial or sentinel value in the session.
	bid, err := func() (string, error) {
		if allowAnonymous {
			return binding.UnauthenticatedSentinel, nil
		}
		b, err := extractBindingID(identity)
		if err != nil {
			return "", fmt.Errorf("BindSession: cannot derive identity binding: %w", err)
		}
		return b, nil
	}()
	if err != nil {
		return nil, err
	}

	// Write the resolved binding. This is the only metadata key written here;
	// backend IDs and per-backend session keys are written by makeBaseSession.
	session.SetMetadata(sessiontypes.MetadataKeyIdentityBinding, bid)

	return &sessionBindingDecorator{
		MultiSession:   session,
		boundIdentity:  bid,
		allowAnonymous: allowAnonymous,
	}, nil
}

// RestoreSessionBinding recreates the session-binding decorator from a
// persisted binding string read out of session metadata. Use this when
// reconstructing a MultiSession after a pod restart or cross-pod failover.
//
// This function is the symmetric counterpart of BindSession for the restore
// path. It is invoked by the session factory after RestoreSession deserializes
// the binding from session metadata. Unlike BindSession, it does NOT write
// metadata — the factory has already restored the metadata layer separately.
//
// storedBinding must be either:
//   - binding.UnauthenticatedSentinel (the session was anonymous), or
//   - a valid bound binding (binding.Parse returns ok).
//
// Anything else (empty string, malformed value) is rejected as corrupted
// metadata.
func RestoreSessionBinding(
	session sessiontypes.MultiSession,
	storedBinding string,
) (sessiontypes.MultiSession, error) {
	if session == nil {
		return nil, fmt.Errorf("session must not be nil")
	}

	if binding.IsUnauthenticated(storedBinding) {
		return &sessionBindingDecorator{
			MultiSession:   session,
			boundIdentity:  storedBinding,
			allowAnonymous: true,
		}, nil
	}

	// Validate the stored binding is parsable. We do not use iss/sub here —
	// the factory calls binding.Parse separately when it needs them to
	// reconstruct identity. This call is purely a validation gate.
	if _, _, ok := binding.Parse(storedBinding); !ok {
		return nil, fmt.Errorf("RestoreSessionBinding: stored binding is neither the unauthenticated sentinel " +
			"nor a valid bound binding (corrupted metadata)")
	}

	return &sessionBindingDecorator{
		MultiSession:   session,
		boundIdentity:  storedBinding,
		allowAnonymous: false,
	}, nil
}

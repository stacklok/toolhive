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
	"time"

	"github.com/stacklok/toolhive/pkg/auth"
	pkgsecurity "github.com/stacklok/toolhive/pkg/security"
	transportsession "github.com/stacklok/toolhive/pkg/transport/session"
	"github.com/stacklok/toolhive/pkg/vmcp"
	sessiontypes "github.com/stacklok/toolhive/pkg/vmcp/session/types"
)

const (
	// SHA256HexLen is the length of a hex-encoded SHA256 hash (32 bytes = 64 hex characters)
	SHA256HexLen = 64

	// MetadataKeyTokenHash is the session metadata key for the token hash.
	// Imported from types package to ensure consistency across all packages.
	MetadataKeyTokenHash = sessiontypes.MetadataKeyTokenHash

	// MetadataKeyTokenSalt is the session metadata key for the token salt.
	// Imported from types package to ensure consistency across all packages.
	MetadataKeyTokenSalt = sessiontypes.MetadataKeyTokenSalt
)

// GenerateSalt generates a cryptographically secure random salt for token hashing.
// Returns 16 bytes of random data from crypto/rand.
//
// Each session should have a unique salt to provide additional entropy and prevent
// attacks that work across multiple sessions.
func GenerateSalt() ([]byte, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return nil, fmt.Errorf("failed to generate salt: %w", err)
	}
	return salt, nil
}

// HashToken returns the hex-encoded HMAC-SHA256 hash of a raw bearer token string.
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
func HashToken(token string, secret, salt []byte) string {
	if token == "" {
		return ""
	}
	h := hmac.New(sha256.New, secret)
	h.Write(salt)
	h.Write([]byte(token))
	return hex.EncodeToString(h.Sum(nil))
}

// SessionMetadataWriter is an interface for writing session metadata.
// It abstracts the underlying session storage mechanism, allowing the security
// package to persist token binding metadata without depending on the concrete
// session implementation.
type SessionMetadataWriter interface {
	// SetMetadata sets a metadata key-value pair on the session.
	SetMetadata(key, value string)

	// GetMetadata returns all session metadata as a map.
	GetMetadata() map[string]string
}

// HijackableSession represents the minimal interface needed for a session
// to support hijack prevention. This is intentionally minimal to avoid
// circular dependencies between the security and session packages.
//
// The HijackPreventionDecorator will hold a reference to the concrete session
// type as interface{} and type-assert to call other methods as needed.
type HijackableSession interface {
	SessionMetadataWriter

	// CallTool invokes the named tool with the given arguments.
	CallTool(
		ctx context.Context,
		caller *auth.Identity,
		toolName string,
		arguments map[string]any,
		meta map[string]any,
	) (*vmcp.ToolCallResult, error)

	// ReadResource reads the resource at the given URI.
	ReadResource(ctx context.Context, caller *auth.Identity, uri string) (*vmcp.ResourceReadResult, error)

	// GetPrompt retrieves the named prompt with the given arguments.
	GetPrompt(ctx context.Context, caller *auth.Identity, name string, arguments map[string]any) (*vmcp.PromptGetResult, error)
}

// HijackPreventionDecorator wraps a session and adds token binding validation
// to prevent session hijacking attacks. It validates that all requests come from
// the same identity that created the session.
//
// The decorator is applied by PreventSessionHijacking to ALL sessions (both authenticated
// and anonymous). For authenticated sessions, it validates the caller's token matches
// the creator's token. For anonymous sessions (allowAnonymous=true), it allows nil
// callers and prevents session upgrade attacks by rejecting any token presentation.
//
// The decorator holds the underlying session as an interface{} to avoid circular
// dependencies, and implements pass-through methods by type-asserting to the
// necessary interfaces.
type HijackPreventionDecorator struct {
	session interface{} // The underlying session (stored as interface{} to avoid circular deps)

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
	callerHash := HashToken(caller.Token, d.hmacSecret, d.tokenSalt)

	// Constant-time comparison to prevent timing attacks
	if !pkgsecurity.ConstantTimeHashCompare(d.boundTokenHash, callerHash, SHA256HexLen) {
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

	return d.session.(HijackableSession).CallTool(ctx, caller, toolName, arguments, meta)
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

	return d.session.(HijackableSession).ReadResource(ctx, caller, uri)
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

	return d.session.(HijackableSession).GetPrompt(ctx, caller, name, arguments)
}

// PreventSessionHijacking wraps a session with hijack prevention security measures.
// It computes token binding hashes, stores them in session metadata, and returns
// a decorated session that validates caller identity on every operation.
//
// This function encapsulates all initialization, persistence, and decoration logic
// for session hijacking prevention in a single place.
//
// Parameters:
//   - session: The underlying session to protect (must implement SessionMetadataWriter)
//   - hmacSecret: Server-managed secret for HMAC-SHA256 hashing (should be 32+ bytes)
//   - identity: The identity of the session creator (nil for anonymous)
//   - allowAnonymous: Whether to allow nil caller on subsequent requests
//
// For authenticated sessions (allowAnonymous=false, identity.Token != ""):
//   - Generates a unique random salt
//   - Computes HMAC-SHA256 hash of the bearer token
//   - Stores hash and salt in session metadata
//   - Returns decorator that validates every request against the creator's token
//
// For anonymous sessions (allowAnonymous=true, identity == nil or identity.Token == ""):
//   - Stores empty sentinel values in metadata
//   - Returns decorator that allows nil callers and rejects token presentation
//
// Security:
//   - Makes defensive copies of secret and salt to prevent external mutation
//   - Uses constant-time comparison to prevent timing attacks
//   - Prevents session upgrade attacks (anonymous → authenticated)
//   - Raw tokens are never stored, only HMAC-SHA256 hashes
//
// The returned value is *HijackPreventionDecorator which wraps the input session
// in an interface{} field and delegates method calls using type assertions.
// The decorator manually implements pass-through methods for the wrapped session,
// so it satisfies the same interfaces as the input through explicit delegation.
//
// Note: The input is interface{} to avoid circular dependencies (security is internal
// to session package). The session must implement SessionMetadataWriter and all methods
// that HijackPreventionDecorator delegates to (see struct definition for full list).
// The return type is concrete to eliminate the need for runtime casts at call sites.
//
// Returns an error if:
//   - session doesn't implement SessionMetadataWriter interface
//   - salt generation fails
func PreventSessionHijacking(
	session interface{},
	hmacSecret []byte,
	identity *auth.Identity,
	allowAnonymous bool,
) (*HijackPreventionDecorator, error) {
	// Validate upfront that session implements critical interfaces.
	// This provides fail-fast behavior for security-critical operations
	// instead of panics at runtime.

	// Required for metadata persistence
	metadataWriter, ok := session.(SessionMetadataWriter)
	if !ok {
		return nil, fmt.Errorf("session must implement SessionMetadataWriter interface, got %T", session)
	}

	// Required for security-critical operations (CallTool/ReadResource/GetPrompt)
	if _, ok := session.(HijackableSession); !ok {
		return nil, fmt.Errorf("session must implement HijackableSession interface (CallTool/ReadResource/GetPrompt), got %T", session)
	}

	// Note: Pass-through methods (ID, Type, CreatedAt, etc.) are validated by the
	// type system when the decorator is used. We don't validate them here to keep
	// the constructor simple and allow minimal mocks for testing.

	var boundTokenHash string
	var tokenSalt []byte
	var err error

	// Compute token binding for authenticated sessions
	if !allowAnonymous && identity != nil && identity.Token != "" {
		// Generate unique salt for this session
		tokenSalt, err = GenerateSalt()
		if err != nil {
			return nil, fmt.Errorf("failed to generate token salt: %w", err)
		}
		// Compute HMAC-SHA256 hash with server secret and per-session salt
		boundTokenHash = HashToken(identity.Token, hmacSecret, tokenSalt)
	}

	// Store hash and salt in session metadata for persistence, auditing,
	// and backward compatibility
	metadataWriter.SetMetadata(MetadataKeyTokenHash, boundTokenHash)
	if len(tokenSalt) > 0 {
		metadataWriter.SetMetadata(MetadataKeyTokenSalt, hex.EncodeToString(tokenSalt))
	}

	// Make defensive copies of slices to prevent external mutation
	var hmacSecretCopy, tokenSaltCopy []byte
	if len(hmacSecret) > 0 {
		hmacSecretCopy = append([]byte(nil), hmacSecret...)
	}
	if len(tokenSalt) > 0 {
		tokenSaltCopy = append([]byte(nil), tokenSalt...)
	}

	// Wrap with HijackPreventionDecorator for runtime validation.
	// Store the session as interface{} to avoid circular dependencies.
	// The decorator will type-assert to the necessary interfaces when calling methods.
	return &HijackPreventionDecorator{
		session:        session,
		allowAnonymous: allowAnonymous,
		hmacSecret:     hmacSecretCopy,
		boundTokenHash: boundTokenHash,
		tokenSalt:      tokenSaltCopy,
	}, nil
}

// Pass-through methods to satisfy the MultiSession interface.
// These methods delegate to the underlying session without adding validation.

// ID returns the session ID.
func (d *HijackPreventionDecorator) ID() string {
	type hasID interface{ ID() string }
	return d.session.(hasID).ID()
}

// Type returns the session type (SessionType is a string alias).
func (d *HijackPreventionDecorator) Type() transportsession.SessionType {
	type hasType interface {
		Type() transportsession.SessionType
	}
	return d.session.(hasType).Type()
}

// CreatedAt returns when the session was created.
func (d *HijackPreventionDecorator) CreatedAt() time.Time {
	type hasCreatedAt interface{ CreatedAt() time.Time }
	return d.session.(hasCreatedAt).CreatedAt()
}

// UpdatedAt returns when the session was last updated.
func (d *HijackPreventionDecorator) UpdatedAt() time.Time {
	type hasUpdatedAt interface{ UpdatedAt() time.Time }
	return d.session.(hasUpdatedAt).UpdatedAt()
}

// Touch updates the session's last access time.
func (d *HijackPreventionDecorator) Touch() {
	type hasTouch interface{ Touch() }
	d.session.(hasTouch).Touch()
}

// GetData returns the session data.
func (d *HijackPreventionDecorator) GetData() interface{} {
	type hasGetData interface{ GetData() interface{} }
	return d.session.(hasGetData).GetData()
}

// SetData sets the session data.
func (d *HijackPreventionDecorator) SetData(data interface{}) {
	type hasSetData interface{ SetData(interface{}) }
	d.session.(hasSetData).SetData(data)
}

// GetMetadata returns all session metadata.
func (d *HijackPreventionDecorator) GetMetadata() map[string]string {
	return d.session.(SessionMetadataWriter).GetMetadata()
}

// SetMetadata sets a metadata key-value pair.
func (d *HijackPreventionDecorator) SetMetadata(key, value string) {
	d.session.(SessionMetadataWriter).SetMetadata(key, value)
}

// Tools returns the tools available in this session.
func (d *HijackPreventionDecorator) Tools() []vmcp.Tool {
	type hasTools interface{ Tools() []vmcp.Tool }
	return d.session.(hasTools).Tools()
}

// Resources returns the resources available in this session.
func (d *HijackPreventionDecorator) Resources() []vmcp.Resource {
	type hasResources interface{ Resources() []vmcp.Resource }
	return d.session.(hasResources).Resources()
}

// Prompts returns the prompts available in this session.
func (d *HijackPreventionDecorator) Prompts() []vmcp.Prompt {
	type hasPrompts interface{ Prompts() []vmcp.Prompt }
	return d.session.(hasPrompts).Prompts()
}

// BackendSessions returns the backend session IDs.
func (d *HijackPreventionDecorator) BackendSessions() map[string]string {
	type hasBackendSessions interface{ BackendSessions() map[string]string }
	return d.session.(hasBackendSessions).BackendSessions()
}

// Close closes the session and all backend connections.
func (d *HijackPreventionDecorator) Close() error {
	type hasClose interface{ Close() error }
	return d.session.(hasClose).Close()
}

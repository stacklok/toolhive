// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"crypto/subtle"
	"log/slog"
	"net/http"

	"github.com/stacklok/toolhive/pkg/auth"
	transportsession "github.com/stacklok/toolhive/pkg/transport/session"
	vmcpsession "github.com/stacklok/toolhive/pkg/vmcp/session"
)

const (
	// mcpSessionIDHeader is the HTTP header that carries the MCP session ID on
	// every request after the initial initialize handshake.
	mcpSessionIDHeader = "Mcp-Session-Id"

	// errMsgSessionAuthMismatch is the error message returned to the client
	// when the bearer token presented on a request does not match the token
	// that was used to create the session.
	errMsgSessionAuthMismatch = "session authentication mismatch"
)

// sessionTerminator can terminate a session identified by its ID.
// Both vmcpSessionManager and sessionIDAdapter implement this interface.
type sessionTerminator interface {
	Terminate(sessionID string) (isNotAllowed bool, err error)
}

// terminateAndDelete terminates a session and deletes it from storage, logging any errors.
// This helper reduces cyclomatic complexity in tokenBindingMiddleware.
func terminateAndDelete(
	sessionID string,
	terminator sessionTerminator,
	storage *transportsession.Manager,
	logContext string,
) {
	if _, termErr := terminator.Terminate(sessionID); termErr != nil {
		slog.Warn("failed to terminate session",
			"context", logContext,
			"error", termErr)
	}
	if deleteErr := storage.Delete(sessionID); deleteErr != nil {
		slog.Warn("failed to delete session from storage",
			"context", logContext,
			"error", deleteErr)
	}
}

// tokenBindingMiddleware returns a middleware that enforces bearer-token binding
// for established MCP sessions.
//
// Security model:
//
//   - Authenticated sessions: at creation time, SHA256(bearerToken) is stored in
//     session metadata under MetadataKeyTokenHash. Each subsequent request must
//     present a token whose hash matches the stored value. On mismatch the session
//     is immediately terminated and HTTP 401 is returned.
//
//   - Anonymous sessions (no token at creation): the empty-string sentinel is
//     stored. Subsequent requests with no Authorization header are allowed.
//     Requests that present any Authorization header (valid bearer token,
//     malformed bearer token, or non-bearer auth) are rejected with HTTP 401
//     and the session is terminated, because the session is bound to "no
//     credentials" not "any credentials".
//
//   - Requests without an Mcp-Session-Id header (session-creating requests,
//     e.g. initialize) pass through — token binding only applies to established
//     sessions.
//
//   - Sessions whose metadata contains no MetadataKeyTokenHash entry pass
//     through unchanged; this preserves backward compatibility with sessions
//     created before token binding was introduced.
func tokenBindingMiddleware(
	sessionStorage *transportsession.Manager,
	terminator sessionTerminator,
) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			sessionID := r.Header.Get(mcpSessionIDHeader)
			if sessionID == "" {
				// No session yet (e.g., initial initialize request).
				// Token binding does not apply.
				next.ServeHTTP(w, r)
				return
			}

			sess, exists := sessionStorage.Get(sessionID)
			if !exists {
				// Unknown session — the SDK's Validate() will reject this with
				// 404. Pass through so the SDK can produce the correct response.
				next.ServeHTTP(w, r)
				return
			}

			storedHash, hashPresent := sess.GetMetadata()[vmcpsession.MetadataKeyTokenHash]
			if !hashPresent {
				// No hash stored. Distinguish between legacy sessions and broken MultiSessions:
				//   - Legacy sessions (predating token binding): pass through for backward compatibility
				//   - Placeholder sessions (Phase 1): should have hash set immediately by CreateSession,
				//     but if caught in the brief window before CreateSession runs, allow through
				//   - MultiSession (Phase 2): MUST have a hash; fail closed if missing
				if _, isMultiSession := sess.(vmcpsession.MultiSession); isMultiSession {
					// Fully-formed MultiSession without token hash is a security violation.
					// This should never happen with SessionManagementV2 — CreateSession sets
					// the hash before upgrading the placeholder to MultiSession.
					// Note: sessionID is not logged here to avoid gosec G706 taint analysis warnings,
					// but the session will be terminated and the termination logs will include the ID.
					slog.Error("token binding: MultiSession missing token hash; terminating session")
					terminateAndDelete(sessionID, terminator, sessionStorage, "missing token hash")
					http.Error(w, errMsgSessionAuthMismatch, http.StatusUnauthorized)
					return
				}
				// Placeholder or legacy session — pass through.
				next.ServeHTTP(w, r)
				return
			}

			// Compute hash of the current request's bearer token.
			currentToken, tokenErr := auth.ExtractBearerToken(r)
			currentHash := ""

			// Map token extraction error to a safe, non-sensitive error code for logging.
			// This prevents gosec G706 warnings about potentially logging sensitive data.
			var tokenErrorCode string
			switch tokenErr {
			case nil:
				currentHash = vmcpsession.HashToken(currentToken)
				tokenErrorCode = ""
			case auth.ErrAuthHeaderMissing:
				tokenErrorCode = "missing"
			case auth.ErrInvalidAuthHeaderFormat:
				tokenErrorCode = "invalid_format"
			case auth.ErrEmptyBearerToken:
				tokenErrorCode = "empty_token"
			default:
				tokenErrorCode = "unknown"
			}

			if tokenErr != nil && tokenErr != auth.ErrAuthHeaderMissing {
				// Authorization header present but malformed (wrong format, empty token, etc.).
				// For anonymous sessions (storedHash == ""), this is a mismatch: the session
				// is bound to "no credentials", not "malformed credentials".
				// For authenticated sessions, this is also a mismatch.
				// Treat as authentication failure and terminate the session.
				// Note: sessionID is not logged here to avoid gosec G706 taint analysis warnings,
				// but the session will be terminated and the termination logs will include the ID.
				slog.Warn("session authentication mismatch: malformed authorization header; terminating session",
					"error_code", tokenErrorCode)
				// Immediately delete from storage to prevent any further use.
				terminateAndDelete(sessionID, terminator, sessionStorage, "malformed header")
				http.Error(w, errMsgSessionAuthMismatch, http.StatusUnauthorized)
				return
			}
			// If tokenErr == ErrAuthHeaderMissing, currentHash remains "" (no header).
			// This is legitimate for anonymous sessions.

			// Use constant-time comparison to prevent timing side-channel attacks.
			// subtle.ConstantTimeCompare is only constant-time for equal-length inputs,
			// so normalize both hashes to a fixed-length representation before
			// comparing them. We also fold in a constant-time check of the original
			// string lengths so the semantics remain "string equality".
			// Use stack-allocated arrays to avoid per-request heap allocations.
			const normalizedLen = 64 // hex-encoded SHA256 length
			var normalizedStored, normalizedCurrent [normalizedLen]byte
			copy(normalizedStored[:], storedHash)
			copy(normalizedCurrent[:], currentHash)
			cmp := subtle.ConstantTimeCompare(normalizedStored[:], normalizedCurrent[:])
			// G115: Safe conversion - storedHash/currentHash are either "" (0 bytes)
			// or hex-encoded SHA256 (64 bytes), both well within int32 range.
			lengthEq := subtle.ConstantTimeEq(int32(len(storedHash)), int32(len(currentHash))) //nolint:gosec
			hashesMatch := (cmp & lengthEq) == 1

			if !hashesMatch {
				// Note: sessionID is not logged here to avoid gosec G706 taint analysis warnings,
				// but the session will be terminated and the termination logs will include the ID.
				slog.Warn("session authentication mismatch; terminating session",
					"has_token", tokenErrorCode == "")
				// Immediately delete from storage to prevent any further use.
				terminateAndDelete(sessionID, terminator, sessionStorage, "authentication mismatch")
				http.Error(w, errMsgSessionAuthMismatch, http.StatusUnauthorized)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

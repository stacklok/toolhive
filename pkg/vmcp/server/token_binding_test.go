// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	transportsession "github.com/stacklok/toolhive/pkg/transport/session"
	vmcpsession "github.com/stacklok/toolhive/pkg/vmcp/session"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// newTestStorage creates a transport session manager suitable for middleware tests.
func newTestStorage(t *testing.T) *transportsession.Manager {
	t.Helper()
	mgr := transportsession.NewTypedManager(30*time.Minute, transportsession.SessionTypeStreamable)
	t.Cleanup(func() { _ = mgr.Stop() })
	return mgr
}

// addSessionWithHash creates a session in storage with the given token hash.
// Pass an empty hash for anonymous sessions (sentinel).
func addSessionWithHash(t *testing.T, storage *transportsession.Manager, sessionID, tokenHash string) {
	t.Helper()
	require.NoError(t, storage.AddWithID(sessionID))
	sess, exists := storage.Get(sessionID)
	require.True(t, exists)
	sess.SetMetadata(vmcpsession.MetadataKeyTokenHash, tokenHash)
}

// stubTerminator records Terminate calls so tests can assert on them.
type stubTerminator struct {
	terminated []string
}

func (s *stubTerminator) Terminate(sessionID string) (bool, error) {
	s.terminated = append(s.terminated, sessionID)
	return false, nil
}

// okHandler is a minimal next-handler that records it was called.
type okHandler struct{ called bool }

func (h *okHandler) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	h.called = true
	w.WriteHeader(http.StatusOK)
}

// buildRequest builds an HTTP request, optionally with an Mcp-Session-Id header
// and/or an Authorization: Bearer <token> header.
func buildRequest(sessionID, bearerToken string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/mcp", http.NoBody)
	if sessionID != "" {
		r.Header.Set(mcpSessionIDHeader, sessionID)
	}
	if bearerToken != "" {
		r.Header.Set("Authorization", "Bearer "+bearerToken)
	}
	return r
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestTokenBindingMiddleware_NoSessionHeader(t *testing.T) {
	t.Parallel()

	// A request without Mcp-Session-Id (e.g., initialize) must pass through
	// unchanged — token binding does not apply to session-creating requests.
	storage := newTestStorage(t)
	terminator := &stubTerminator{}
	next := &okHandler{}
	handler := tokenBindingMiddleware(storage, terminator)(next)

	r := buildRequest("", "some-token")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	assert.Equal(t, http.StatusOK, w.Code, "request without session ID must pass through")
	assert.True(t, next.called, "next handler must be called")
	assert.Empty(t, terminator.terminated, "no session should be terminated")
}

func TestTokenBindingMiddleware_UnknownSession(t *testing.T) {
	t.Parallel()

	// A request with an Mcp-Session-Id that is not in storage must pass through
	// so the SDK can produce the proper 404 response.
	storage := newTestStorage(t)
	terminator := &stubTerminator{}
	next := &okHandler{}
	handler := tokenBindingMiddleware(storage, terminator)(next)

	r := buildRequest("ghost-session-id", "some-token")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	assert.Equal(t, http.StatusOK, w.Code, "unknown session must pass through for SDK to handle")
	assert.True(t, next.called)
	assert.Empty(t, terminator.terminated)
}

func TestTokenBindingMiddleware_NoHashInMetadata(t *testing.T) {
	t.Parallel()

	// Sessions without MetadataKeyTokenHash (created before token binding was
	// introduced) must pass through for backward compatibility.
	storage := newTestStorage(t)
	require.NoError(t, storage.AddWithID("legacy-session"))
	// Deliberately do NOT set MetadataKeyTokenHash.

	terminator := &stubTerminator{}
	next := &okHandler{}
	handler := tokenBindingMiddleware(storage, terminator)(next)

	r := buildRequest("legacy-session", "some-token")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	assert.Equal(t, http.StatusOK, w.Code, "legacy session without hash must pass through")
	assert.True(t, next.called)
	assert.Empty(t, terminator.terminated)
}

func TestTokenBindingMiddleware_AuthenticatedSession_ValidToken(t *testing.T) {
	t.Parallel()

	// A request with the same bearer token that was used to create the session
	// must be allowed through.
	const sessionID = "auth-session-1"
	const token = "my-valid-bearer-token"

	storage := newTestStorage(t)
	addSessionWithHash(t, storage, sessionID, vmcpsession.HashToken(token))

	terminator := &stubTerminator{}
	next := &okHandler{}
	handler := tokenBindingMiddleware(storage, terminator)(next)

	r := buildRequest(sessionID, token)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	assert.Equal(t, http.StatusOK, w.Code, "matching token must be allowed")
	assert.True(t, next.called)
	assert.Empty(t, terminator.terminated, "session must not be terminated on match")
}

func TestTokenBindingMiddleware_AuthenticatedSession_MismatchedToken(t *testing.T) {
	t.Parallel()

	// A request with a different token than the one used at session creation
	// must receive HTTP 401 and the session must be terminated.
	const sessionID = "auth-session-2"
	const originalToken = "original-token"
	const stolenToken = "attacker-different-token"

	storage := newTestStorage(t)
	addSessionWithHash(t, storage, sessionID, vmcpsession.HashToken(originalToken))

	terminator := &stubTerminator{}
	next := &okHandler{}
	handler := tokenBindingMiddleware(storage, terminator)(next)

	r := buildRequest(sessionID, stolenToken)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	assert.Equal(t, http.StatusUnauthorized, w.Code, "mismatched token must receive 401")
	assert.Contains(t, w.Body.String(), errMsgSessionAuthMismatch)
	assert.False(t, next.called, "next handler must NOT be called on mismatch")
	require.Len(t, terminator.terminated, 1, "session must be terminated on mismatch")
	assert.Equal(t, sessionID, terminator.terminated[0])
}

func TestTokenBindingMiddleware_AuthenticatedSession_NoTokenOnSubsequentRequest(t *testing.T) {
	t.Parallel()

	// An authenticated session must reject a follow-up request that presents
	// no bearer token at all (hash mismatch: stored=sha256, current="").
	const sessionID = "auth-session-3"
	const originalToken = "token-used-at-creation"

	storage := newTestStorage(t)
	addSessionWithHash(t, storage, sessionID, vmcpsession.HashToken(originalToken))

	terminator := &stubTerminator{}
	next := &okHandler{}
	handler := tokenBindingMiddleware(storage, terminator)(next)

	// No bearer token on this request.
	r := buildRequest(sessionID, "")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	assert.Equal(t, http.StatusUnauthorized, w.Code, "missing token on authenticated session must receive 401")
	assert.False(t, next.called)
	require.Len(t, terminator.terminated, 1)
	assert.Equal(t, sessionID, terminator.terminated[0])
}

func TestTokenBindingMiddleware_AnonymousSession_NoToken(t *testing.T) {
	t.Parallel()

	// An anonymous session (created with no token) must allow follow-up
	// requests that also carry no token.
	const sessionID = "anon-session-1"

	storage := newTestStorage(t)
	addSessionWithHash(t, storage, sessionID, "") // empty sentinel = anonymous

	terminator := &stubTerminator{}
	next := &okHandler{}
	handler := tokenBindingMiddleware(storage, terminator)(next)

	// No bearer token on this request — matches anonymous session.
	r := buildRequest(sessionID, "")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	assert.Equal(t, http.StatusOK, w.Code, "anonymous session with no token must be allowed")
	assert.True(t, next.called)
	assert.Empty(t, terminator.terminated)
}

func TestTokenBindingMiddleware_AnonymousSession_SuddenToken(t *testing.T) {
	t.Parallel()

	// An anonymous session must reject any follow-up request that suddenly
	// presents a bearer token — the session is bound to "no credentials".
	const sessionID = "anon-session-2"

	storage := newTestStorage(t)
	addSessionWithHash(t, storage, sessionID, "") // empty sentinel = anonymous

	terminator := &stubTerminator{}
	next := &okHandler{}
	handler := tokenBindingMiddleware(storage, terminator)(next)

	// Request presents a token but the session was created anonymously.
	r := buildRequest(sessionID, "unexpected-token")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	assert.Equal(t, http.StatusUnauthorized, w.Code, "anonymous session must reject a request with a token")
	assert.Contains(t, w.Body.String(), errMsgSessionAuthMismatch)
	assert.False(t, next.called)
	require.Len(t, terminator.terminated, 1)
	assert.Equal(t, sessionID, terminator.terminated[0])
}

func TestTokenBindingMiddleware_TerminatorCalledExactlyOnce(t *testing.T) {
	t.Parallel()

	// On a token mismatch the terminator must be called exactly once with the
	// session ID. A stub is used here because the full termination lifecycle
	// (Close + storage delete) is verified in the integration tests.
	const sessionID = "terminated-session"
	const originalToken = "real-token"

	storage := newTestStorage(t)
	addSessionWithHash(t, storage, sessionID, vmcpsession.HashToken(originalToken))

	terminator := &stubTerminator{}
	next := &okHandler{}
	handler := tokenBindingMiddleware(storage, terminator)(next)

	r := buildRequest(sessionID, "wrong-token")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	assert.Equal(t, http.StatusUnauthorized, w.Code)
	assert.False(t, next.called)
	require.Len(t, terminator.terminated, 1, "terminator must be called exactly once")
	assert.Equal(t, sessionID, terminator.terminated[0])
}

func TestTokenBindingMiddleware_AnonymousSession_MalformedHeader_EmptyToken(t *testing.T) {
	t.Parallel()

	// An anonymous session must reject requests with a malformed Authorization
	// header (e.g., "Bearer " with no token). This is a security fix: malformed
	// headers should not be silently treated as "no token".
	const sessionID = "anon-session-malformed"

	storage := newTestStorage(t)
	addSessionWithHash(t, storage, sessionID, "") // empty sentinel = anonymous

	terminator := &stubTerminator{}
	next := &okHandler{}
	handler := tokenBindingMiddleware(storage, terminator)(next)

	// Manually construct a request with malformed Authorization header.
	r := httptest.NewRequest(http.MethodPost, "/mcp", http.NoBody)
	r.Header.Set(mcpSessionIDHeader, sessionID)
	r.Header.Set("Authorization", "Bearer ") // Empty token after "Bearer "

	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	assert.Equal(t, http.StatusUnauthorized, w.Code, "malformed header must be rejected")
	assert.Contains(t, w.Body.String(), errMsgSessionAuthMismatch)
	assert.False(t, next.called)
	require.Len(t, terminator.terminated, 1, "session must be terminated on malformed header")
	assert.Equal(t, sessionID, terminator.terminated[0])
}

func TestTokenBindingMiddleware_AnonymousSession_MalformedHeader_WrongFormat(t *testing.T) {
	t.Parallel()

	// An anonymous session must reject requests with wrong Authorization format
	// (e.g., "Basic xyz" instead of "Bearer <token>").
	const sessionID = "anon-session-wrong-format"

	storage := newTestStorage(t)
	addSessionWithHash(t, storage, sessionID, "") // empty sentinel = anonymous

	terminator := &stubTerminator{}
	next := &okHandler{}
	handler := tokenBindingMiddleware(storage, terminator)(next)

	// Manually construct a request with non-Bearer auth.
	r := httptest.NewRequest(http.MethodPost, "/mcp", http.NoBody)
	r.Header.Set(mcpSessionIDHeader, sessionID)
	r.Header.Set("Authorization", "Basic dXNlcjpwYXNz") // Basic auth, not Bearer

	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	assert.Equal(t, http.StatusUnauthorized, w.Code, "non-Bearer auth must be rejected")
	assert.Contains(t, w.Body.String(), errMsgSessionAuthMismatch)
	assert.False(t, next.called)
	require.Len(t, terminator.terminated, 1)
	assert.Equal(t, sessionID, terminator.terminated[0])
}

func TestTokenBindingMiddleware_AuthenticatedSession_MalformedHeader(t *testing.T) {
	t.Parallel()

	// An authenticated session must also reject malformed Authorization headers.
	const sessionID = "auth-session-malformed"
	const originalToken = "valid-token"

	storage := newTestStorage(t)
	addSessionWithHash(t, storage, sessionID, vmcpsession.HashToken(originalToken))

	terminator := &stubTerminator{}
	next := &okHandler{}
	handler := tokenBindingMiddleware(storage, terminator)(next)

	// Manually construct a request with malformed Authorization header.
	r := httptest.NewRequest(http.MethodPost, "/mcp", http.NoBody)
	r.Header.Set(mcpSessionIDHeader, sessionID)
	r.Header.Set("Authorization", "Bearer ") // Empty token

	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	assert.Equal(t, http.StatusUnauthorized, w.Code, "malformed header must be rejected")
	assert.Contains(t, w.Body.String(), errMsgSessionAuthMismatch)
	assert.False(t, next.called)
	require.Len(t, terminator.terminated, 1)
	assert.Equal(t, sessionID, terminator.terminated[0])
}

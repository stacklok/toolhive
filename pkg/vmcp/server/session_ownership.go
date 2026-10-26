// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/auth/sessionbinding"
)

// sessionOwnershipMiddleware enforces that every request carrying an
// Mcp-Session-Id comes from the principal that created the session. It must run
// after authentication and before the Streamable HTTP transport, which would
// otherwise route the request on the session ID alone: open or replace the
// session's standalone stream, accept client responses and notifications,
// answer list methods from the session's capability view, terminate it on
// DELETE, or rehydrate it on a replica that did not create it.
//
// The owner is read from the session store without restoring the session or
// refreshing its TTL, so a rejected request has no side effect on the owner's
// session. Missing, unowned, foreign and ambiguous-carrier requests all get the
// same plain-text 404 "Session terminated", byte for byte, so the response
// discloses nothing; a storage failure gets 503. The 404 is deliberately not the
// JSON-RPC body the direct proxies write: go-sdk 1.7+ clients treat a JSON-RPC
// error body on a 404 as a per-call rejection and never re-initialize, whereas
// a plain-text 404 is what the transport itself returns for an unknown session
// and makes them start a new one. Sessionless requests (initialize) pass
// through unchanged; Modern (2026-07-28) requests are dispatched by
// classifyingHandler before this middleware and never reach it.
//
// The caller is compared with sessionbinding.Validate, the check shared with the
// other proxy families: the same issuer and subject (a refreshed token
// included), or, for a session created with no identity at all, no identity.
// A non-nil identity without valid issuer and subject claims never matches,
// including on a session bound to the unauthenticated sentinel.
//
// Invariant: the session's owner is persisted before the client can learn its
// ID. The SDK shim fires OnRegisterSession, and so Manager.CreateSession,
// synchronously inside the initialize dispatch, before the initialize response
// carrying Mcp-Session-Id is written. A record without an owner is therefore a
// session that is still being created or whose creation failed, and is
// rejected like a missing one.
func (s *Server) sessionOwnershipMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := s.checkSessionOwnership(r); err != nil {
			if errors.Is(err, sessionbinding.ErrNotFound) {
				http.Error(w, "Session terminated", http.StatusNotFound)
				return
			}
			http.Error(w, "Session store unavailable", http.StatusServiceUnavailable)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// checkSessionOwnership returns nil for a sessionless request or one from the
// session's owner, sessionbinding.ErrNotFound for anything that must look like
// a missing session, and the storage error otherwise.
func (s *Server) checkSessionOwnership(r *http.Request) error {
	sessionID, err := sessionbinding.RequestID(r, "")
	if err != nil {
		slog.Warn("rejected MCP request with an ambiguous session ID",
			"method", r.Method, "path", r.URL.Path)
		return sessionbinding.ErrNotFound
	}
	if sessionID == "" {
		return nil
	}

	owner, err := s.vmcpSessionMgr.LookupOwner(r.Context(), sessionID)
	switch {
	case errors.Is(err, sessionbinding.ErrNotFound):
		return sessionbinding.ErrNotFound
	case err != nil && r.Context().Err() != nil:
		// The client went away; nothing to warn an operator about.
		slog.Debug("session ownership lookup abandoned by the client",
			"method", r.Method, "path", r.URL.Path, "error", err)
		return err
	case err != nil:
		slog.Warn("session ownership lookup failed",
			"method", r.Method, "path", r.URL.Path, "error", err)
		return err
	case owner == "":
		// No owner yet: see the invariant on sessionOwnershipMiddleware.
		// sessionbinding.Validate would reject an empty owner too; this branch
		// exists so placeholders and unowned records are rejected without the
		// WARN below, which is for callers that fail the ownership check.
		return sessionbinding.ErrNotFound
	}

	identity, _ := auth.IdentityFromContext(r.Context())
	if err := sessionbinding.Validate(owner, identity); err != nil {
		slog.Warn("rejected MCP request for a session the caller does not own",
			rejectionAttrs(r, owner, identity)...)
		return sessionbinding.ErrNotFound
	}
	return nil
}

// rejectionAttrs describes why sessionbinding.Validate rejected the caller. It
// carries the caller's public issuer but never the session ID or the subject.
func rejectionAttrs(r *http.Request, owner string, identity *auth.Identity) []any {
	reason := "principal_mismatch"
	_, fromIdentityErr := sessionbinding.FromIdentity(identity)
	switch {
	case owner != sessionbinding.UnauthenticatedSentinel && !isParseableBinding(owner):
		reason = "stored_binding_invalid"
	case identity == nil:
		reason = "nil_caller"
	case fromIdentityErr != nil:
		reason = "caller_identity_malformed"
	}
	attrs := []any{"method", r.Method, "path", r.URL.Path, "reason", reason}
	if identity != nil {
		if iss, ok := identity.Claims["iss"].(string); ok && iss != "" {
			attrs = append(attrs, "iss", iss)
		}
		attrs = append(attrs, "token_present", identity.Token != "")
	}
	return attrs
}

func isParseableBinding(owner string) bool {
	_, _, ok := sessionbinding.Parse(owner)
	return ok
}

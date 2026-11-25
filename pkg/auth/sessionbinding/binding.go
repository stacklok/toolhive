// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package sessionbinding binds sessions to the validated issuer and subject, never
// to a bearer token. Authentication must run before ownership enforcement.
package sessionbinding

import (
	"context"
	"crypto/subtle"
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/transport/session"
)

// MetadataKey is shared with persisted vMCP sessions for compatibility.
const MetadataKey = session.MetadataKeyIdentityBinding

// UnauthenticatedSentinel is reserved for an actually absent identity.
const UnauthenticatedSentinel = "unauthenticated"

var (
	// ErrInvalidBinding denotes malformed issuer/subject claims.
	ErrInvalidBinding = errors.New("invalid identity binding")
	// ErrNotFound deliberately conceals missing, unowned and foreign sessions.
	ErrNotFound = session.ErrSessionNotFound
)

// Format encodes a nonempty issuer and subject separated by exactly one NUL.
func Format(iss, sub string) (string, error) {
	if iss == "" || sub == "" || strings.ContainsRune(iss, '\x00') || strings.ContainsRune(sub, '\x00') {
		return "", ErrInvalidBinding
	}
	return iss + "\x00" + sub, nil
}

// Parse rejects malformed bindings and the unauthenticated sentinel.
func Parse(value string) (iss, sub string, ok bool) {
	iss, sub, ok = strings.Cut(value, "\x00")
	if !ok || iss == "" || sub == "" || strings.ContainsRune(sub, '\x00') {
		return "", "", false
	}
	return iss, sub, true
}

// FromIdentity accepts nil only for auth-disabled deployments. A synthetic
// anonymous/local identity with valid claims binds normally; malformed non-nil
// identities must never downgrade to the unauthenticated sentinel.
func FromIdentity(identity *auth.Identity) (string, error) {
	if identity == nil {
		return UnauthenticatedSentinel, nil
	}
	iss, _ := identity.Claims["iss"].(string)
	sub, _ := identity.Claims["sub"].(string)
	return Format(iss, sub)
}

// Validate fails closed for legacy records without an owner. Refreshed tokens
// for the same issuer and subject are accepted. It never mutates session state.
func Validate(stored string, identity *auth.Identity) error {
	if stored != UnauthenticatedSentinel {
		if _, _, ok := Parse(stored); !ok {
			return ErrNotFound
		}
	}
	current, err := FromIdentity(identity)
	if err != nil || subtle.ConstantTimeCompare([]byte(stored), []byte(current)) != 1 {
		return ErrNotFound
	}
	return nil
}

// RequestID reads protocol session carriers, rejecting duplicate or conflicting
// values before a backend can interpret them differently. queryKey may be empty.
func RequestID(r *http.Request, queryKey string) (string, error) {
	headers := r.Header.Values("Mcp-Session-Id")
	if len(headers) > 1 {
		return "", ErrNotFound
	}
	id := r.Header.Get("Mcp-Session-Id")
	if queryKey == "" {
		return id, nil
	}
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return "", ErrNotFound
	}
	values := query[queryKey]
	if len(values) > 1 {
		return "", ErrNotFound
	}
	if len(values) == 1 {
		if values[0] == "" || (id != "" && id != values[0]) {
			return "", ErrNotFound
		}
		id = values[0]
	}
	return id, nil
}

// Lookup reads authoritative owner metadata only, without restoring SDK sessions
// or connecting to backends. Return ErrNotFound for absent records, and propagate
// storage errors. Shared storage does not provide live-stream affinity.
type Lookup func(context.Context, string) (string, error)

// Check validates a session-bearing request using its authenticated context.
// Empty IDs are sessionless; callers enforce any protocol-required ID separately.
func Check(r *http.Request, id string, lookup Lookup) error {
	if id == "" {
		return nil
	}
	owner, err := lookup(r.Context(), id)
	if err != nil {
		return err
	}
	identity, _ := auth.IdentityFromContext(r.Context())
	return Validate(owner, identity)
}

// NewMiddleware constructs ownership enforcement. extract must classify requests
// without consuming their bodies and return an empty ID only for truly sessionless
// requests. reject must conceal ErrNotFound (404) and map storage failures to 503.
// Install inside authentication and outside stateful handlers/restoration.
func NewMiddleware(extract func(*http.Request) (string, error), lookup Lookup,
	reject func(http.ResponseWriter, *http.Request, error)) (func(http.Handler) http.Handler, error) {
	if extract == nil || lookup == nil || reject == nil {
		return nil, errors.New("session ownership middleware requires extractor, lookup and rejection handler")
	}
	return func(next http.Handler) http.Handler {
		if next == nil {
			panic("session ownership middleware requires a handler")
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id, err := extract(r)
			if err == nil {
				err = Check(r, id, lookup)
			}
			if err != nil {
				reject(w, r, err)
				return
			}
			next.ServeHTTP(w, r)
		})
	}, nil
}

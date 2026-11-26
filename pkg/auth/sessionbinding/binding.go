// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package sessionbinding binds sessions to the validated issuer and subject, plus
// the client and optional outermost actor for delegated identities. It never binds
// to a bearer token. Authentication must run before ownership enforcement.
package sessionbinding

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/stacklok/toolhive-core/audit"
	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/transport/session"
)

const clientIDClaimKey = "client_id"

// MetadataKey is shared with persisted vMCP sessions for compatibility.
const MetadataKey = session.MetadataKeyIdentityBinding

// UnauthenticatedSentinel is reserved for an actually absent identity.
const UnauthenticatedSentinel = "unauthenticated"

var (
	// ErrInvalidBinding denotes an identity that cannot be bound to a session.
	ErrInvalidBinding = errors.New("invalid identity binding")
	// ErrNotFound deliberately conceals missing, unowned and foreign sessions.
	ErrNotFound = session.ErrSessionNotFound
)

// Format encodes a nonempty issuer and subject separated by exactly one NUL.
func Format(iss, sub string) (string, error) {
	return formatBinding(parsedBinding{iss: iss, sub: sub})
}

// Parse rejects malformed bindings and the unauthenticated sentinel. For a
// delegated binding it returns only the issuer and subject prefix. Use Validate
// to compare full bindings; passing the parsed fields to Format loses ownership data.
func Parse(value string) (iss, sub string, ok bool) {
	b, ok := parseBinding(value)
	return b.iss, b.sub, ok
}

// FromIdentity accepts nil only for auth-disabled deployments. A synthetic
// anonymous/local identity with valid claims binds normally; malformed non-nil
// identities must never downgrade to the unauthenticated sentinel.
//
// The binding depends on the admission route. Interactive and unspecified
// identities, including sessionless grants without the delegated-access marker,
// bind two fields: issuer and subject. A delegated identity binds four fields
// (issuer, subject, route, client_id), or six when it carries an actor (plus
// the outermost act issuer and subject). A delegated identity with a missing or
// invalid client_id, a malformed, incomplete or explicitly null act, or an
// unknown route returns ErrInvalidBinding. An absent act is not an error.
func FromIdentity(identity *auth.Identity) (string, error) {
	if identity == nil {
		return UnauthenticatedSentinel, nil
	}
	iss, _ := identity.Claims["iss"].(string)
	sub, _ := identity.Claims["sub"].(string)
	b := parsedBinding{iss: iss, sub: sub}
	switch identity.AdmissionRoute {
	case auth.AdmissionRouteUnspecified, auth.AdmissionRouteInteractive:
		return formatBinding(b)
	case auth.AdmissionRouteDelegated:
		b.route = string(auth.AdmissionRouteDelegated)
		b.clientID, _ = identity.Claims[clientIDClaimKey].(string)
		if rawAct, present := identity.Claims["act"]; present && rawAct == nil {
			return "", fmt.Errorf("%w: null act claim", ErrInvalidBinding)
		}
		chain := func() *audit.DelegationChain {
			if rawAct := identity.Claims["act"]; identity.DelegationChain.IsZero() && rawAct != nil {
				return audit.ParseDelegationChain(rawAct, auth.DefaultMaxDelegationDepth)
			}
			return identity.DelegationChain
		}()
		if !chain.IsZero() {
			// An empty actor issuer is invalid here because issuedAct fills an
			// omitted act.iss and actor.Effective refuses a present-but-empty
			// act.iss. Revisit this if issuance drops the fill or adds a SourceClient route.
			if chain.Malformed || len(chain.Chain) == 0 || chain.Chain[0].Issuer == "" || chain.Chain[0].Subject == "" {
				return "", fmt.Errorf("%w: malformed delegation chain", ErrInvalidBinding)
			}
			b.actIss, b.actSub = chain.Chain[0].Issuer, chain.Chain[0].Subject
		}
		return formatBinding(b)
	default:
		return "", fmt.Errorf("%w: unsupported admission route", ErrInvalidBinding)
	}
}

// Validate fails closed for legacy records without an owner. Refreshed tokens
// are accepted when the full binding matches. It never mutates session state.
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

type parsedBinding struct {
	iss, sub, route, clientID, actIss, actSub string
}

func parseBinding(value string) (parsedBinding, bool) {
	iss, rest, ok := strings.Cut(value, "\x00")
	if !ok || iss == "" {
		return parsedBinding{}, false
	}
	sub, rest, extended := strings.Cut(rest, "\x00")
	if sub == "" {
		return parsedBinding{}, false
	}
	b := parsedBinding{iss: iss, sub: sub}
	if !extended {
		return b, true
	}

	route, rest, ok := strings.Cut(rest, "\x00")
	if !ok {
		return parsedBinding{}, false
	}
	b.route = route
	switch route {
	case string(auth.AdmissionRouteDelegated):
		clientID, rest, hasActor := strings.Cut(rest, "\x00")
		if clientID == "" {
			return parsedBinding{}, false
		}
		b.clientID = clientID
		if hasActor {
			actIss, actSub, ok := strings.Cut(rest, "\x00")
			if !ok || actIss == "" || actSub == "" || strings.ContainsRune(actSub, '\x00') {
				return parsedBinding{}, false
			}
			b.actIss, b.actSub = actIss, actSub
		}
		return b, true
	default:
		return parsedBinding{}, false
	}
}

func formatBinding(b parsedBinding) (string, error) {
	if b.iss == "" || b.sub == "" {
		return "", fmt.Errorf("%w: missing issuer or subject", ErrInvalidBinding)
	}
	for _, field := range [...]string{b.iss, b.sub, b.route, b.clientID, b.actIss, b.actSub} {
		if strings.ContainsRune(field, '\x00') {
			return "", fmt.Errorf("%w: NUL in binding field", ErrInvalidBinding)
		}
	}
	switch b.route {
	case "":
		if b.clientID != "" || b.actIss != "" || b.actSub != "" {
			return "", fmt.Errorf("%w: unexpected delegated fields", ErrInvalidBinding)
		}
		return b.iss + "\x00" + b.sub, nil
	case string(auth.AdmissionRouteDelegated):
		if b.clientID == "" {
			return "", fmt.Errorf("%w: missing client ID", ErrInvalidBinding)
		}
		if (b.actIss == "") != (b.actSub == "") {
			return "", fmt.Errorf("%w: incomplete actor", ErrInvalidBinding)
		}
		if b.actIss == "" {
			return b.iss + "\x00" + b.sub + "\x00" + b.route + "\x00" + b.clientID, nil
		}
		return b.iss + "\x00" + b.sub + "\x00" + b.route + "\x00" + b.clientID + "\x00" + b.actIss + "\x00" + b.actSub, nil
	default:
		return "", fmt.Errorf("%w: unsupported binding route", ErrInvalidBinding)
	}
}

// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package authorizationdetails validates the RFC 9396 authorization_details
// parameter of a token request and asks per-type hooks what to grant.
//
// The package only parses and delegates. It does not persist the granted
// details, does not put them in tokens, and does not convey them to resource
// servers. Register only types whose enforcement the deployment performs
// itself.
//
// Usage has two phases. Registry.Parse runs early and cheaply, before any
// expensive checks, and rejects malformed input. Pending.Grant runs late, after
// the request is otherwise accepted, and calls the hooks.
package authorizationdetails

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	maxSize  = 64 * 1024
	maxDepth = 64
)

// Type registers one authorization-details type.
type Type struct {
	// GrantTypes lists the grant types this type is accepted on. It is required
	// and must be non-empty. An entry of this type on any other grant type is
	// rejected as invalid.
	GrantTypes []string

	// Grant decides what to grant for all of this type's entries in a request.
	// It is called at most once per request, after the other checks passed,
	// with every entry of the type in request order.
	//
	// Grant is decide-only: it may read state but must not commit it, because
	// token issuance can still fail after it returns. Returning Error rejects
	// the request with that code and reason; any other error is an internal
	// failure.
	Grant func(context.Context, Request) (Result, error)
}

// Request is the context a Grant hook receives. The caller supplies every
// field except Entries and GrantType, which the package fills.
//
// All hooks of one request share the same Request values: the slices, Subject
// and Subject.Claims are not copied per hook, so a hook must not modify them.
// Only Entries is specific to the hook's type.
type Request struct {
	// Entries holds this type's entries in request order.
	Entries []json.RawMessage
	// GrantType is the grant type of the token request.
	GrantType string
	// ClientID is the authenticated client.
	ClientID string
	// Actor is the resolved actor; it equals ClientID without an actor_token.
	Actor string
	// ActorTokenPresent reports whether the request carried an actor_token.
	ActorTokenPresent bool
	// RequestedTokenType is the requested_token_type of the request.
	RequestedTokenType string
	// RequestedScopes are the scopes the client asked for.
	RequestedScopes []string
	// GrantedAudience is the audience the server has already decided to grant.
	GrantedAudience []string
	// Subject describes the validated subject token.
	Subject Subject
}

// Subject describes a validated subject token.
type Subject struct {
	Issuer   string
	Subject  string
	ClientID string
	JWTID    string
	Audience []string
	IssuedAt time.Time
	Expiry   time.Time
	External bool // validated through a trusted issuer
	// Claims holds all verified claims, passed to hooks as given.
	Claims map[string]any
}

// Result is what a Grant hook decided.
type Result struct {
	// Entries are the granted entries. The list must be non-empty and every
	// entry must be a JSON object whose "type" is the hook's own type.
	Entries []json.RawMessage
}

// Registry holds the registered types. It is immutable and safe for concurrent
// use.
type Registry struct {
	types map[string]Type
}

// New copies and validates the registered types, keyed by exact,
// case-sensitive type name. It rejects empty or whitespace names, a nil Grant,
// and an empty or blank GrantTypes list. A registry with no types is valid; it
// accepts only the empty array.
func New(types map[string]Type) (*Registry, error) {
	copied := make(map[string]Type, len(types))
	for name, t := range types {
		if strings.TrimSpace(name) == "" {
			return nil, fmt.Errorf("authorization details type name must not be empty or whitespace")
		}
		if t.Grant == nil {
			return nil, fmt.Errorf("authorization details type %q must have a Grant function", name)
		}
		if len(t.GrantTypes) == 0 {
			return nil, fmt.Errorf("authorization details type %q must list at least one grant type", name)
		}
		for _, grantType := range t.GrantTypes {
			if strings.TrimSpace(grantType) == "" {
				return nil, fmt.Errorf("authorization details type %q lists an empty grant type", name)
			}
		}
		t.GrantTypes = slices.Clone(t.GrantTypes)
		copied[name] = t
	}
	return &Registry{types: copied}, nil
}

// Parse checks the raw authorization_details value for the given grant type.
// The value must be at most 64 KiB of valid UTF-8 holding one JSON array of
// objects, nested at most 64 containers deep, in which no object repeats a
// member name (also not differing only in letter case; that rule is ToolHive
// policy, not RFC 9396). Every entry needs a non-empty string "type" that is
// registered for grantType. The empty array "[]" is valid.
//
// Any failure returns ErrInvalid.
func (r *Registry) Parse(raw, grantType string) (*Pending, error) {
	data := []byte(raw)
	if len(data) > maxSize || !utf8.Valid(data) || !validArray(data) {
		return nil, ErrInvalid
	}
	// Unmarshalling copies the bytes, so each entry owns its backing array.
	var entries []json.RawMessage
	if err := json.Unmarshal(data, &entries); err != nil {
		return nil, ErrInvalid
	}

	pending := &Pending{
		grantType: grantType,
		types:     r.types,
		entries:   make(map[string][]json.RawMessage),
	}
	for _, entry := range entries {
		name, ok := entryType(entry)
		if !ok {
			return nil, ErrInvalid
		}
		t, registered := r.types[name]
		if !registered || !slices.Contains(t.GrantTypes, grantType) {
			return nil, ErrInvalid
		}
		if _, seen := pending.entries[name]; !seen {
			pending.order = append(pending.order, name)
		}
		pending.entries[name] = append(pending.entries[name], entry)
	}
	return pending, nil
}

// Pending is a parsed, not yet granted authorization_details value. It is
// single-use: call Grant at most once.
type Pending struct {
	grantType string
	types     map[string]Type
	order     []string
	entries   map[string][]json.RawMessage
}

// Grant calls each type's hook once, in the order the types first appear in
// the request, and returns the granted entries as a JSON array: types in that
// order, each type's entries in the order its hook returned them ("[]" when
// the request held no entries). The package fills req.Entries and
// req.GrantType.
//
// A hook must return at least one entry, and every entry is checked as
// strictly as the request: valid UTF-8, within the size limit, no duplicate
// member names, and an object of the hook's own type. The combined array must
// also fit the size limit. The output is built with encoding/json, so entries
// are compacted and HTML-escaped (<, > and & become \u003c, \u003e and
// \u0026): equal to the hook's output as JSON, not byte-identical.
//
// Errors: Error is a hook rejection to pass to the client. A plain error means
// the package rejected hook output, and any other error is an untyped hook
// failure; both are internal failures whose text must not reach the client.
func (p *Pending) Grant(ctx context.Context, req Request) (json.RawMessage, error) {
	req.GrantType = p.grantType

	granted := []json.RawMessage{}
	for _, name := range p.order {
		typeReq := req
		typeReq.Entries = p.entries[name]
		result, err := p.types[name].Grant(ctx, typeReq)
		if err != nil {
			return nil, hookError(name, err)
		}
		if len(result.Entries) == 0 {
			return nil, fmt.Errorf("type %q returned no entries: %w", name, errInvalidHookResult)
		}
		for _, entry := range result.Entries {
			if err := checkGranted(name, entry); err != nil {
				return nil, err
			}
			granted = append(granted, entry)
		}
	}

	out, err := json.Marshal(granted)
	if err != nil || len(out) > maxSize {
		return nil, fmt.Errorf("combined hook output is not valid JSON or exceeds %d bytes: %w", maxSize, errInvalidHookResult)
	}
	return out, nil
}

func hookError(typeName string, err error) error {
	var typed Error
	if asError(err, &typed) {
		if !typed.valid() {
			return fmt.Errorf("type %q returned an invalid error: %w", typeName, errInvalidHookResult)
		}
		return typed
	}
	return fmt.Errorf("type %q: %w", typeName, err)
}

// checkGranted applies the request-side checks to one entry a hook returned.
func checkGranted(typeName string, entry json.RawMessage) error {
	if len(entry) > maxSize || !utf8.Valid(entry) {
		return fmt.Errorf("type %q returned an entry that is oversized or not valid UTF-8: %w", typeName, errInvalidHookResult)
	}
	if got, ok := entryType(entry); !ok || got != typeName {
		return fmt.Errorf("type %q returned an entry that is not an object of that type: %w", typeName, errInvalidHookResult)
	}
	wrapped := make([]byte, 0, len(entry)+2)
	wrapped = append(append(append(wrapped, '['), entry...), ']')
	if !validArray(wrapped) {
		return fmt.Errorf("type %q returned an entry with duplicate member names or too deep nesting: %w",
			typeName, errInvalidHookResult)
	}
	return nil
}

// asError extracts an Error returned by value or by pointer, possibly wrapped.
func asError(err error, out *Error) bool {
	var value Error
	if errors.As(err, &value) {
		*out = value
		return true
	}
	var pointer *Error
	if errors.As(err, &pointer) && pointer != nil {
		*out = *pointer
		return true
	}
	return false
}

// entryType returns the string "type" member of a JSON object.
func entryType(entry json.RawMessage) (string, bool) {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(entry, &object); err != nil || object == nil {
		return "", false
	}
	var name string
	if err := json.Unmarshal(object["type"], &name); err != nil || name == "" {
		return "", false
	}
	return name, true
}

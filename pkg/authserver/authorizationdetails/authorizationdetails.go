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
// expensive checks, and rejects malformed input. Pending.Grant or
// Pending.GrantSubset runs late, after the request is otherwise accepted, and
// calls the hooks: Grant when the hooks decide the granted entries, and
// GrantSubset when they may only keep or drop entries someone else signed.
package authorizationdetails

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
	//
	// On ToolHive's authorization server, listing the JWT-bearer grant type
	// (urn:ietf:params:oauth:grant-type:jwt-bearer) opts the type into ID-JAG
	// redemption only: the server processes the authorization_details claim
	// of an ID-JAG and refuses a plain RFC 7523 assertion that carries one.
	GrantTypes []string

	// Grant decides what to grant for all of this type's entries in a request.
	// It is called at most once per request, after the other checks passed,
	// with every entry of the type in request order. What it may return
	// depends on how the server asks: see Pending.Grant (token exchange) and
	// Pending.GrantSubset (ID-JAG redemption).
	//
	// Grant is decide-only: it may read state but must not commit it, because
	// token issuance can still fail after it returns. On ID-JAG redemption the
	// ID-JAG is consumed only after the hooks ran, so a hook can run again for
	// the same ID-JAG when a redemption of it is later refused as a replay.
	// Returning Error rejects the request with that code and reason; any other
	// error is an internal failure.
	Grant func(context.Context, Request) (Result, error)
}

// Request is the context a Grant hook receives. The caller supplies every
// field except Entries and GrantType, which the package fills.
//
// The fields every grant fills are at the top level. What only one grant has
// is in its own part: Exchange on token exchange and Redemption on ID-JAG
// redemption, each nil on the other grant. A hook reads a grant's fields only
// after checking that its part is not nil, so an empty field is never mistaken
// for one that does not apply.
//
// All hooks of one request share the same Request values: the slices, the
// parts, Subject and Subject.Claims are not copied per hook, so a hook must not
// modify them. Only Entries is specific to the hook's type.
type Request struct {
	// Entries holds this type's entries in request order.
	Entries []json.RawMessage
	// GrantType is the grant type of the token request.
	GrantType string
	// ClientID is the authenticated client.
	ClientID string
	// GrantedAudience is the audience the server has already decided to grant:
	// on token exchange the audience of the token being issued, and on ID-JAG
	// redemption the one resource the issued token is for.
	GrantedAudience []string
	// Subject describes the validated token the request is about: the
	// subject_token on token exchange, and the ID-JAG on ID-JAG redemption.
	Subject Subject
	// Exchange holds what only token exchange has. It is set on token exchange
	// and nil otherwise.
	Exchange *ExchangeContext
	// Redemption holds what only ID-JAG redemption has. It is set on ID-JAG
	// redemption, which rides the JWT-bearer grant, and nil otherwise.
	Redemption *RedemptionContext
}

// ExchangeContext is the part of a Request only RFC 8693 token exchange has.
type ExchangeContext struct {
	// Actor is the resolved actor. Without an actor_token it equals the
	// client ID, or the verified SPIFFE ID for a SPIFFE client.
	Actor string
	// ActorTokenPresent reports whether the request carried an actor_token.
	ActorTokenPresent bool
	// RequestedTokenType is the requested_token_type of the request.
	RequestedTokenType string
	// RequestedScopes are the scopes the client asked for.
	RequestedScopes []string
}

// RedemptionContext is the part of a Request only ID-JAG redemption has.
type RedemptionContext struct {
	// ResolvedUser is the local user the redemption resolved the ID-JAG's
	// subject to. It is set for an issuer with subject_mode resolve and empty
	// for a namespaced one, whose token subject is <issuer>#<sub>.
	ResolvedUser string
}

// Subject describes a validated token: the subject_token on token exchange,
// the ID-JAG on ID-JAG redemption.
type Subject struct {
	Issuer string
	// Subject is the token's sub claim, on every grant. It is never replaced by
	// a resolved local user; see RedemptionContext.ResolvedUser.
	Subject  string
	ClientID string
	JWTID    string
	Audience []string
	IssuedAt time.Time
	Expiry   time.Time
	External bool // validated through a trusted issuer
	// Claims holds the verified claims, passed to hooks as given, except
	// authorization_details: a hook reads the entries from Request.Entries,
	// which keep every digit of a number.
	Claims map[string]any
}

// Result is what a Grant hook decided.
type Result struct {
	// Entries are the granted entries. For Pending.Grant the list must be
	// non-empty and every entry must be a JSON object whose "type" is the
	// hook's own type. For Pending.GrantSubset every entry must be one of
	// Request.Entries returned exactly as received, and an empty list drops
	// all of the type's entries.
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
// Any failure returns ErrInvalid. When every entry is a well-formed object with
// a non-empty string "type" but some type is not registered for grantType, the
// error is ErrUnknownType, which wraps ErrInvalid.
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

	names := make([]string, len(entries))
	for i, entry := range entries {
		name, ok := entryType(entry)
		if !ok {
			return nil, ErrInvalid
		}
		names[i] = name
	}

	pending := &Pending{
		grantType: grantType,
		types:     r.types,
		entries:   make(map[string][]json.RawMessage),
	}
	for i, name := range names {
		t, registered := r.types[name]
		if !registered || !slices.Contains(t.GrantTypes, grantType) {
			return nil, ErrUnknownType
		}
		if _, seen := pending.entries[name]; !seen {
			pending.order = append(pending.order, name)
		}
		pending.entries[name] = append(pending.entries[name], entries[i])
	}
	return pending, nil
}

// Pending is a parsed, not yet granted authorization_details value. It is
// single-use: call Grant or GrantSubset at most once.
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
// A hook must return at least one entry: a type whose hook returns none fails
// the whole call as a hook defect, and no later hook runs. Every entry is
// checked as strictly as the request: valid UTF-8, within the size limit, no
// duplicate member names, and an object of the hook's own type. The combined
// array must also fit the size limit. The output is built with encoding/json, so entries
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

// Subset is what GrantSubset kept of the parsed entries.
type Subset struct {
	// Entries is a JSON array of the kept entries, each with the exact bytes
	// it had in the parsed value, nil when no hook kept any entry. Types are
	// in the order they first appear in the parsed value, and each type's
	// entries in their parsed order.
	Entries json.RawMessage
	// Dropped lists, in the same type order, the types whose hook kept none
	// of their entries.
	Dropped []string
}

// GrantSubset calls each type's hook once, in the order the types first
// appear in the parsed value, and keeps the entries the hooks returned. It is
// for a value whose entries a third party signed: a hook may only keep or
// drop whole entries, and the result carries the parsed bytes of each kept
// entry, never the hook's. The package fills req.Entries and req.GrantType;
// each hook gets its own copy of its entries.
//
// Every entry a hook returns must equal a distinct one of its type's entries
// as a JSON value, with object members in any order and numbers compared as
// written, so 1 and 1.0 differ. A hook that keeps an entry returns it exactly
// as received in Request.Entries; re-encoding it can change a number's
// spelling. A hook that returns no entry drops its type, which is reported in
// Subset.Dropped and is not an error. Whether a result with no entry left is
// acceptable is the caller's decision.
//
// Errors are as for Grant: Error is a hook rejection to pass to the client,
// and any other error, including an entry that matches none of the type's
// entries, is an internal failure.
func (p *Pending) GrantSubset(ctx context.Context, req Request) (Subset, error) {
	req.GrantType = p.grantType

	var kept []json.RawMessage
	var dropped []string
	for _, name := range p.order {
		parsed := p.entries[name]
		typeReq := req
		typeReq.Entries = cloneEntries(parsed)
		result, err := p.types[name].Grant(ctx, typeReq)
		if err != nil {
			return Subset{}, hookError(name, err)
		}
		entries, err := keptEntries(name, parsed, result.Entries)
		if err != nil {
			return Subset{}, err
		}
		if len(entries) == 0 {
			dropped = append(dropped, name)
		}
		kept = append(kept, entries...)
	}
	return Subset{Entries: joinEntries(kept), Dropped: dropped}, nil
}

// keptEntries returns the parsed entries a hook kept, in parsed order. Each
// returned entry must equal a parsed entry not already matched, compared by
// their canonical forms.
func keptEntries(typeName string, parsed, returned []json.RawMessage) ([]json.RawMessage, error) {
	if len(returned) == 0 {
		return nil, nil
	}
	unmatched := make(map[string][]int, len(parsed))
	for i, entry := range parsed {
		key, err := canonicalJSON(entry)
		if err != nil {
			return nil, fmt.Errorf("type %q has an entry that cannot be compared: %w", typeName, errInvalidHookResult)
		}
		unmatched[key] = append(unmatched[key], i)
	}
	keep := make([]bool, len(parsed))
	for _, entry := range returned {
		var key string
		var err error
		if len(entry) <= maxSize {
			key, err = canonicalJSON(entry)
		}
		indexes := unmatched[key]
		if len(entry) > maxSize || err != nil || len(indexes) == 0 {
			return nil, fmt.Errorf("type %q returned an entry that is not one of its entries as received: %w",
				typeName, errInvalidHookResult)
		}
		keep[indexes[0]] = true
		unmatched[key] = indexes[1:]
	}
	var kept []json.RawMessage
	for i, entry := range parsed {
		if keep[i] {
			kept = append(kept, entry)
		}
	}
	return kept, nil
}

// canonicalJSON re-encodes one JSON value with object members sorted and
// numbers kept as written. Two values with the same canonical form are equal
// as JSON values, numbers compared as written. Trailing data is refused.
func canonicalJSON(raw json.RawMessage) (string, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return "", err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return "", errors.New("trailing data after JSON value")
	}
	canonical, err := json.Marshal(value)
	return string(canonical), err
}

// cloneEntries copies entries and their bytes, so a hook that modifies what
// it received cannot change the parsed value.
func cloneEntries(entries []json.RawMessage) []json.RawMessage {
	cloned := make([]json.RawMessage, len(entries))
	for i, entry := range entries {
		cloned[i] = slices.Clone(entry)
	}
	return cloned
}

// joinEntries writes entries as a JSON array with their bytes unchanged, nil
// for no entry.
func joinEntries(entries []json.RawMessage) json.RawMessage {
	if len(entries) == 0 {
		return nil
	}
	out := []byte{'['}
	for i, entry := range entries {
		if i > 0 {
			out = append(out, ',')
		}
		out = append(out, entry...)
	}
	return append(out, ']')
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

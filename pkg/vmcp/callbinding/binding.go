// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package callbinding

import (
	"bytes"
	"encoding/json"
	"encoding/json/jsontext"
	"errors"
	"io"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/stacklok/toolhive/pkg/mcp/toolcalldetails"
)

// Config is how an embedder sets up call binding on the vMCP. The zero value
// is valid: no client must send an entry, calls last at most
// DefaultMaxCallDuration, and the record is in memory.
//
// The vMCP enforces an mcp_tool_call entry whenever a token carries one,
// whatever Config says. Config only adds the clients whose tokens must carry
// one.
type Config struct {
	// Clients lists the OAuth client IDs (the access token's client_id claim)
	// whose tokens must carry an mcp_tool_call entry. A listed client's token
	// without one is refused with ReasonEntryRequired. Empty by default.
	//
	// List a client only once the authorization servers in front of the vMCP
	// put the entry into its access tokens; otherwise every call from that
	// client is refused.
	Clients []string

	// MaxCallDuration bounds how long one call keeps its record open: the
	// record of a token lasts until its exp, or until the call has run for
	// MaxCallDuration, whichever is later. It does not stop a call that runs
	// longer. Zero means DefaultMaxCallDuration.
	MaxCallDuration time.Duration

	// Record is the single-use record. Nil means a MemoryRecord, which
	// covers one replica only: a replay to another replica is not seen.
	Record Record
}

// DefaultMaxCallDuration is the MaxCallDuration used when Config leaves it
// zero.
const DefaultMaxCallDuration = 30 * time.Minute

// Reasons for refusing a request that carries a call-bound token, or a token
// that must be call-bound. They are fixed strings and never carry request
// data.
const (
	// ReasonEntryRequired: the token's client is listed in Config.Clients and
	// the token carries no mcp_tool_call entry.
	ReasonEntryRequired = "entry_required"
	// ReasonSingleEntry: the token carries more than one mcp_tool_call entry.
	ReasonSingleEntry = toolcalldetails.ReasonSingleEntry
	// ReasonMalformedEntry: the token's authorization_details claim is not an
	// array of objects with a string type, or its mcp_tool_call entry does
	// not have the entry shape.
	ReasonMalformedEntry = toolcalldetails.ReasonMalformedEntry
	// ReasonClaimsRequired: a call-bound token has no jti or no exp.
	ReasonClaimsRequired = "claims_required"
	// ReasonListCallUnsupported: the entry names a list call, which the vMCP
	// does not accept yet.
	ReasonListCallUnsupported = "list_call_unsupported"
	// ReasonMethodNotAllowed: the request is not one a call-bound token may
	// send.
	ReasonMethodNotAllowed = "method_not_allowed"
	// ReasonToolMismatch: params.name is not the entry's tool.
	ReasonToolMismatch = "tool_mismatch"
	// ReasonToolNotFound: the vMCP does not route params.name for this caller.
	ReasonToolNotFound = "tool_not_found"
	// ReasonToolNotBindable: params.name resolves to a tool that has no single
	// backend, such as a composite tool, the optimizer's call_tool or code
	// mode's execute_tool_script.
	ReasonToolNotBindable = "tool_not_bindable"
	// ReasonServerMismatch: the vMCP routes the call to a backend other than
	// the entry's server.
	ReasonServerMismatch = "server_mismatch"
	// ReasonInvalidArguments: params.arguments is not one I-JSON object, so
	// it has no argument hash.
	ReasonInvalidArguments = "invalid_arguments"
	// ReasonArgumentsMismatch: the argument hash is not the entry's
	// arguments_sha256.
	ReasonArgumentsMismatch = "arguments_mismatch"
	// ReasonAmbiguousParams: params has a member that some decoders would read
	// as name, arguments, _meta or requestId but is not spelled exactly so.
	ReasonAmbiguousParams = "ambiguous_params"
	// ReasonTokenReused: the token has already started a call.
	ReasonTokenReused = "token_reused"
	// ReasonNoOpenCall: a cancellation or response does not belong to the
	// call the token started, or that call is no longer open.
	ReasonNoOpenCall = "no_open_call"
	// ReasonTokenExpired: the token's exp has passed.
	ReasonTokenExpired = "token_expired"
)

// Token is the binding a call-bound access token carries.
type Token struct {
	// Entry is the token's one mcp_tool_call entry.
	Entry toolcalldetails.Entry
	// Key names the token in the Record: its issuer and jti.
	Key string
	// JTI is the token's jti claim. It identifies the token in logs.
	JTI string
	// ClientID is the token's client_id claim, empty when absent.
	ClientID string
	// ExpiresAt is the token's exp claim.
	ExpiresAt time.Time
}

// FromClaims reads the call binding from an access token's claims. It returns
// a nil Token and an empty reason when the token carries no mcp_tool_call
// entry and its client is not in required. It returns a non-empty reason when
// the token must be refused, and otherwise the Token.
//
// Entries of other authorization_details types are ignored. A claim that is
// not an array of objects, each with a string type, is refused, because an
// element without a readable type might be an mcp_tool_call entry.
func FromClaims(claims map[string]any, required map[string]struct{}) (*Token, string) {
	clientID, _ := claims["client_id"].(string)
	raw, present := claims["authorization_details"]
	var entries []json.RawMessage
	if present {
		var ok bool
		if entries, ok = callEntries(raw); !ok {
			return nil, ReasonMalformedEntry
		}
	}
	switch len(entries) {
	case 0:
		if _, listed := required[clientID]; listed && clientID != "" {
			return nil, ReasonEntryRequired
		}
		return nil, ""
	case 1:
	default:
		return nil, ReasonSingleEntry
	}
	entry, err := toolcalldetails.ParseEntry(entries[0])
	if err != nil {
		return nil, ReasonMalformedEntry
	}

	jti, _ := claims["jti"].(string)
	exp, ok := numericDate(claims["exp"])
	if jti == "" || !ok {
		return nil, ReasonClaimsRequired
	}
	iss, _ := claims["iss"].(string)
	return &Token{
		Entry:     entry,
		Key:       iss + "\x00" + jti,
		JTI:       jti,
		ClientID:  clientID,
		ExpiresAt: exp,
	}, ""
}

// callEntries returns the mcp_tool_call entries of an authorization_details
// claim value, re-encoded as JSON. It reports false when the value is not an
// array of objects each with a string type.
func callEntries(raw any) ([]json.RawMessage, bool) {
	items, ok := raw.([]any)
	if !ok {
		return nil, false
	}
	var entries []json.RawMessage
	for _, item := range items {
		obj, isObject := item.(map[string]any)
		if !isObject {
			return nil, false
		}
		typ, isString := obj["type"].(string)
		if !isString {
			return nil, false
		}
		if typ != toolcalldetails.Type {
			continue
		}
		encoded, err := json.Marshal(obj)
		if err != nil {
			return nil, false
		}
		entries = append(entries, encoded)
	}
	return entries, true
}

// numericDate reads a JWT NumericDate claim as decoded into a claims map.
func numericDate(v any) (time.Time, bool) {
	var seconds float64
	switch n := v.(type) {
	case float64:
		seconds = n
	case json.Number:
		f, err := n.Float64()
		if err != nil {
			return time.Time{}, false
		}
		seconds = f
	case int64:
		seconds = float64(n)
	case int:
		seconds = float64(n)
	default:
		return time.Time{}, false
	}
	if math.IsNaN(seconds) || math.IsInf(seconds, 0) || seconds <= 0 {
		return time.Time{}, false
	}
	whole, frac := math.Modf(seconds)
	return time.Unix(int64(whole), int64(frac*float64(time.Second))), true
}

// ToolCallParams returns params.name and the raw params.arguments of a
// tools/call. arguments is nil when absent. It returns an error for params
// that is not one JSON object, has a non-string name, or has a member that a
// case-insensitive decoder would read as name, arguments or _meta without
// being spelled exactly so: the vMCP's dispatch decodes params that way, so
// hashing the exact member alone would let such a member carry the real
// arguments past the check.
func ToolCallParams(params json.RawMessage) (name string, arguments json.RawMessage, err error) {
	members, err := paramMembers(params, "name", "arguments", "_meta")
	if err != nil {
		return "", nil, err
	}
	if raw, ok := members["name"]; ok {
		if err := json.Unmarshal(raw, &name); err != nil {
			return "", nil, errAmbiguousParams
		}
	}
	return name, members["arguments"], nil
}

// CancelledRequestID returns the requestId of a notifications/cancelled as its
// JSON encoding, in the same form as Call.RequestID. It reports false when
// params has no usable requestId.
func CancelledRequestID(params json.RawMessage) (string, bool) {
	members, err := paramMembers(params, "requestId", "_meta")
	if err != nil {
		return "", false
	}
	raw, ok := members["requestId"]
	if !ok {
		return "", false
	}
	return RequestID(raw)
}

// RequestID returns a JSON-RPC ID, given as its JSON encoding or as the value
// a JSON-RPC decoder produced (int64, float64 or string), in the form of
// Call.RequestID. It reports false for anything that is not a string or an
// integer.
func RequestID(id any) (string, bool) {
	switch v := id.(type) {
	case json.RawMessage:
		var decoded any
		dec := json.NewDecoder(bytes.NewReader(v))
		dec.UseNumber()
		if dec.Decode(&decoded) != nil {
			return "", false
		}
		return RequestID(decoded)
	case json.Number:
		if _, err := v.Int64(); err != nil {
			return "", false
		}
		return v.String(), true
	case string:
		encoded, err := json.Marshal(v)
		if err != nil {
			return "", false
		}
		return string(encoded), true
	case int64:
		return strconv.FormatInt(v, 10), true
	case float64:
		if v != math.Trunc(v) || math.Abs(v) > 1<<53 {
			return "", false
		}
		return strconv.FormatInt(int64(v), 10), true
	default:
		return "", false
	}
}

var errAmbiguousParams = errors.New("callbinding: ambiguous params")

// IsAmbiguousParams reports whether err came from ToolCallParams refusing
// params.
func IsAmbiguousParams(err error) bool {
	return errors.Is(err, errAmbiguousParams)
}

// paramMembers decodes params as one JSON object and returns its members
// whose names are exactly one of names. It refuses a member whose name differs
// from one of names but matches it under Unicode case folding, with or
// without '-' and '_': encoding/json matches struct fields that way.
func paramMembers(params json.RawMessage, names ...string) (map[string]json.RawMessage, error) {
	members := make(map[string]json.RawMessage, len(names))
	if len(bytes.Trim(params, jsonWhitespace)) == 0 {
		return members, nil
	}
	dec := jsontext.NewDecoder(bytes.NewReader(params))
	if tok, err := dec.ReadToken(); err != nil || tok.Kind() != '{' {
		return nil, errAmbiguousParams
	}
	for dec.PeekKind() != '}' {
		tok, err := dec.ReadToken()
		if err != nil {
			return nil, errAmbiguousParams
		}
		member := tok.String()
		value, err := dec.ReadValue()
		if err != nil {
			return nil, errAmbiguousParams
		}
		for _, name := range names {
			switch {
			case member == name:
				members[name] = json.RawMessage(bytes.Clone(value))
			case foldsTo(member, name):
				return nil, errAmbiguousParams
			}
		}
	}
	if _, err := dec.ReadToken(); err != nil {
		return nil, errAmbiguousParams
	}
	if _, err := dec.ReadToken(); !errors.Is(err, io.EOF) {
		return nil, errAmbiguousParams
	}
	return members, nil
}

// foldsTo reports whether member could be decoded into a struct field named
// name by a case-insensitive decoder.
func foldsTo(member, name string) bool {
	if strings.EqualFold(member, name) {
		return true
	}
	strip := strings.NewReplacer("_", "", "-", "")
	return strings.EqualFold(strip.Replace(member), strip.Replace(name))
}

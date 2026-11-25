// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package callbinding

import (
	"encoding/json"
	"time"

	"github.com/golang-jwt/jwt/v5"

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
	// ReasonClaimsRequired: a call-bound token has no jti or no usable exp.
	ReasonClaimsRequired = "claims_required"
	// ReasonListCallUnsupported: the entry names a list call, which the vMCP
	// does not accept yet.
	ReasonListCallUnsupported = "list_call_unsupported"
	// ReasonMethodNotAllowed: the request is not one a call-bound token may
	// send, or it is not sent to the MCP endpoint.
	ReasonMethodNotAllowed = "method_not_allowed"
	// ReasonToolMismatch: params.name is not the entry's tool.
	ReasonToolMismatch = "tool_mismatch"
	// ReasonToolNotFound: the vMCP does not route params.name for this
	// caller: the tool is unknown, unadvertised, or hidden by authorization.
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
	// ReasonInvalidParams: the tools/call params cannot be read the one way
	// dispatch reads them: params is not a single JSON object, name is not a
	// string, or a member's name would match name, arguments or _meta in a
	// case-insensitive decoder without being spelled exactly so.
	ReasonInvalidParams = "invalid_params"
	// ReasonTokenReused: the token has already started a call.
	ReasonTokenReused = "token_reused"
	// ReasonNoOpenCall: a cancellation or response does not belong to the
	// call the token started, on that call's session, or (for a response)
	// that call's request is no longer open.
	ReasonNoOpenCall = "no_open_call"
	// ReasonTokenExpired: the token's exp has passed.
	ReasonTokenExpired = "token_expired"
)

// Token is the binding a call-bound access token carries.
type Token struct {
	// Entry is the token's one mcp_tool_call entry.
	Entry toolcalldetails.Entry
	// Key names the token in the Record: its issuer and jti, joined by a NUL.
	Key string
	// JTI is the token's jti claim. It identifies the token in logs.
	JTI string
	// ClientID is the token's client_id claim, empty when absent.
	ClientID string
	// ExpiresAt is the token's exp claim.
	ExpiresAt time.Time
}

// FromClaims reads the call binding from an access token's validated claims.
// required is the set of client IDs listed in Config.Clients. It returns a nil
// Token and an empty reason when the token carries no mcp_tool_call entry and
// its client is not required to. It returns a non-empty reason when the token
// must be refused, and otherwise the Token.
//
// Entries of other authorization_details types are ignored. A claim that is
// not an array of objects, each with a string type, is refused whatever the
// client: RFC 9396 requires that shape, and an element without a readable
// type might be an mcp_tool_call entry.
func FromClaims(claims map[string]any, required map[string]struct{}) (*Token, string) {
	clientID, _ := claims["client_id"].(string)
	entries, ok := callEntries(claims)
	if !ok {
		return nil, ReasonMalformedEntry
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

	// The token validator reads exp the same way.
	exp, err := jwt.MapClaims(claims).GetExpirationTime()
	jti, _ := claims["jti"].(string)
	if err != nil || exp == nil || exp.Unix() <= 0 || jti == "" {
		return nil, ReasonClaimsRequired
	}
	iss, _ := claims["iss"].(string)
	return &Token{
		Entry:     entry,
		Key:       iss + "\x00" + jti,
		JTI:       jti,
		ClientID:  clientID,
		ExpiresAt: exp.Time,
	}, ""
}

// callEntries returns the mcp_tool_call entries of the claims'
// authorization_details, re-encoded as JSON. It reports false when the claim
// is present but is not an array of objects each with a string type.
func callEntries(claims map[string]any) ([]json.RawMessage, bool) {
	raw, present := claims["authorization_details"]
	if !present {
		return nil, true
	}
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

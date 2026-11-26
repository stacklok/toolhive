// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package callbinding

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/stacklok/toolhive/pkg/auth"
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
	// Clients lists the OAuth client IDs whose tokens must carry an
	// mcp_tool_call entry. A listed client's token without one is refused with
	// ReasonEntryRequired. Empty by default.
	//
	// A token's client is its client_id claim (RFC 9068), or its azp claim
	// when client_id is absent or empty, as in tokens from authorization
	// servers that name the client only in azp. Other claims, such as cid or
	// appid, are not read: a client those tokens name is never matched. The
	// vMCP logs one warning when Clients is set and a token names its client
	// in neither claim.
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

	// Challenge is the realm, resource metadata and scope of the token
	// validator in front of the vMCP. A refusal that describes the token,
	// such as ReasonTokenReused, is answered with HTTP 401 and an
	// invalid_token challenge built from it, so the challenge matches the
	// validator's own. The zero value sends a challenge without them.
	Challenge auth.BearerChallenge
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
	// array of objects with a non-empty string type, or its mcp_tool_call
	// entry does not have the entry shape.
	ReasonMalformedEntry = toolcalldetails.ReasonMalformedEntry
	// ReasonClaimsRequired: a call-bound token has no usable jti, iss or exp:
	// jti or iss is missing, empty or holds a NUL, or exp is missing or not a
	// positive time.
	ReasonClaimsRequired = "claims_required"
	// ReasonMethodNotAllowed: the request is not one a call-bound token may
	// send, or it is not sent to the MCP endpoint.
	ReasonMethodNotAllowed = "method_not_allowed"
	// ReasonMethodMismatch: the entry names a list call, and the request is a
	// tools/call or a list call with another method.
	ReasonMethodMismatch = "method_mismatch"
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
	// ReasonTokenReused: the token has already started a call, and the
	// request would start another one or a new session.
	ReasonTokenReused = "token_reused"
	// ReasonSessionMismatch: the token has started a call, and a GET or
	// DELETE names a session other than that call's.
	ReasonSessionMismatch = "session_mismatch"
	// ReasonNoOpenCall: a cancellation or response does not belong to the
	// call the token started, on that call's session, or that call's request
	// is no longer open.
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
	// ClientID is the token's client, as ClientID reads it.
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
// not an array of objects, each with a non-empty string type, is refused
// whatever the client: RFC 9396 requires that shape, and an element without a
// readable type might be an mcp_tool_call entry.
func FromClaims(claims map[string]any, required map[string]struct{}) (*Token, string) {
	clientID := ClientID(claims)
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
	iss, _ := claims["iss"].(string)
	// The key joins iss and jti with a NUL, as sessionbinding.Format joins
	// iss and sub, and refuses the same values: an empty part or one holding
	// a NUL would let two tokens share a key. A validator that checks no
	// issuer accepts a token without iss.
	if err != nil || exp == nil || exp.Unix() <= 0 || !keyPart(jti) || !keyPart(iss) {
		return nil, ReasonClaimsRequired
	}
	return &Token{
		Entry:     entry,
		Key:       iss + "\x00" + jti,
		JTI:       jti,
		ClientID:  clientID,
		ExpiresAt: exp.Time,
	}, ""
}

// ClientID returns the OAuth client a token names: its client_id claim, or its
// azp claim when client_id is absent or empty. It returns "" when neither
// names one.
func ClientID(claims map[string]any) string {
	if clientID, _ := claims["client_id"].(string); clientID != "" {
		return clientID
	}
	azp, _ := claims["azp"].(string)
	return azp
}

// keyPart reports whether s may be one part of a Record key.
func keyPart(s string) bool {
	return s != "" && !strings.ContainsRune(s, '\x00')
}

// callEntries returns the mcp_tool_call entries of the claims'
// authorization_details, re-encoded as JSON. It reports false when the claim
// is present but is not an array of objects each with a non-empty string
// type, as the authorization server's authorizationdetails requires.
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
		if !isString || typ == "" {
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

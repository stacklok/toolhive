// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package toolcalldetails defines the RFC 9396 authorization_details type that
// narrows an access token to one MCP call. An entry is either a tool call
//
//	{"type": "https://toolhive.dev/mcp_tool_call", "server": "github",
//	 "tool": "pull_request_read", "arguments_sha256": "<64 lowercase hex>",
//	 "operation_id": "op-1"}
//
// or a list call
//
//	{"type": "https://toolhive.dev/mcp_tool_call", "server": "github",
//	 "method": "tools/list"}
//
// tool is the name the vMCP routes by, as in the request's params.name.
// arguments_sha256 is the lowercase hex SHA-256 of the RFC 8785 (JSON
// Canonicalization Scheme) serialization of params.arguments, or of {} when
// params.arguments is absent. A request carries exactly one entry of this
// type, with no members other than those shown above. The hook sees only its
// own type's entries, so it cannot refuse entries of other registered types
// sent alongside.
//
// The intended use is the ID-JAG flow. The authorization server that issues
// an ID-JAG (AS-1) is meant to accept the entry on the RFC 8693
// token-exchange request for an ID-JAG and sign it into the ID-JAG's
// authorization_details claim. The authorization server that redeems the
// ID-JAG with the RFC 7523 JWT-bearer grant (AS-2) is meant to take the entry
// only from that signed claim, never from a redemption parameter or from a
// JWT-bearer assertion that is not an ID-JAG, and copy it unchanged into the
// access token.
//
// Registering the type only makes it known to a server's registry; each
// handler decides whether and where it reads the entry. The handlers in
// pkg/authserver do not read it yet: the token-exchange request for an ID-JAG
// refuses the authorization_details parameter, and JWT-bearer redemption does
// not consult the registry. A registered type therefore narrows no token. A
// token exchange that reaches the hook without requesting an ID-JAG is
// refused with ReasonIDJAGRequired.
//
// No authorization server computes the arguments hash or compares it with
// real arguments: the receiver of the access token enforces the call.
// ParseEntry gives the receiver the same shape check the hook applies.
//
// Nothing registers the type by default. An embedder adds it with Register to
// the types it passes to authorizationdetails.New.
package toolcalldetails

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/stacklok/toolhive/pkg/authserver/authorizationdetails"
	"github.com/stacklok/toolhive/pkg/oauthproto"
)

// Type is the authorization_details type name of an MCP call entry.
const Type = "https://toolhive.dev/mcp_tool_call"

// Reasons returned to the client. ReasonIDJAGRequired comes with
// invalid_request, every other reason with invalid_authorization_details. They
// are fixed strings and never carry request data.
const (
	// ReasonSingleEntry: the request held more than one entry of this type.
	ReasonSingleEntry = "single_entry_required"
	// ReasonMalformedEntry: an unknown, missing, repeated, empty or non-string
	// member, a value holding U+FFFD, or a mix of tool-call and list-call
	// members.
	ReasonMalformedEntry = "malformed_entry"
	// ReasonInvalidArgs: arguments_sha256 is not 64 lowercase hex characters.
	ReasonInvalidArgs = "invalid_arguments_hash"
	// ReasonInvalidMethod: method is not one of the list methods.
	ReasonInvalidMethod = "invalid_method"
	// ReasonIDJAGRequired: the entry was sent on a token exchange whose
	// requested_token_type is not the ID-JAG type.
	ReasonIDJAGRequired = "id_jag_required"
)

// Entry is one validated MCP call entry. Exactly one of the two shapes is set:
// a tool call has Tool, ArgumentsSHA256 and OperationID; a list call has
// Method. Server is always set. Build an Entry with ParseEntry, which enforces
// these rules.
type Entry struct {
	Server          string
	Tool            string
	ArgumentsSHA256 string
	OperationID     string
	Method          string
}

// IsList reports whether the entry is a list call.
func (e Entry) IsList() bool {
	return e.Method != ""
}

// Register adds the MCP call type to types. It is accepted on RFC 8693 token
// exchange, only when requesting an ID-JAG, and on the RFC 7523 JWT-bearer
// grant. It overwrites an existing entry of the same name.
func Register(types map[string]authorizationdetails.Type) {
	types[Type] = authorizationdetails.Type{
		GrantTypes: []string{oauthproto.GrantTypeTokenExchange, oauthproto.GrantTypeJWTBearer},
		Grant:      grant,
	}
}

// ParseEntry checks one JSON object against the MCP call shape and returns it
// as an Entry. The object must be valid UTF-8, have "type" equal to Type, hold
// only string members with no member repeated and no U+FFFD in any value, and
// match exactly one shape.
//
// A rejection is an authorizationdetails.Error with code
// invalid_authorization_details and one of ReasonMalformedEntry,
// ReasonInvalidArgs or ReasonInvalidMethod.
func ParseEntry(raw json.RawMessage) (Entry, error) {
	members, ok := stringMembers(raw)
	if !ok || members[memberType] != Type {
		return Entry{}, invalid(ReasonMalformedEntry)
	}

	_, isList := members[memberMethod]
	isCall := slices.ContainsFunc(callMembers, func(name string) bool {
		_, present := members[name]
		return present
	})
	if isList == isCall {
		// Mixed shapes, or neither shape.
		return Entry{}, invalid(ReasonMalformedEntry)
	}
	required := listMembers
	if isCall {
		required = toolCallMembers
	}
	if len(members) != len(required) {
		return Entry{}, invalid(ReasonMalformedEntry)
	}
	for _, name := range required {
		if members[name] == "" {
			return Entry{}, invalid(ReasonMalformedEntry)
		}
	}

	entry := Entry{
		Server:          members[memberServer],
		Tool:            members[memberTool],
		ArgumentsSHA256: members[memberArgumentsSHA256],
		OperationID:     members[memberOperationID],
		Method:          members[memberMethod],
	}
	if isList && !slices.Contains(listMethods, entry.Method) {
		return Entry{}, invalid(ReasonInvalidMethod)
	}
	if isCall && !argumentsSHA256Format.MatchString(entry.ArgumentsSHA256) {
		return Entry{}, invalid(ReasonInvalidArgs)
	}
	return entry, nil
}

const (
	memberType            = "type"
	memberServer          = "server"
	memberTool            = "tool"
	memberArgumentsSHA256 = "arguments_sha256"
	memberOperationID     = "operation_id"
	memberMethod          = "method"
)

// listMethods is the exact set of methods a list call may name.
var listMethods = []string{"tools/list", "prompts/list", "resources/list", "resources/templates/list"}

var (
	callMembers           = []string{memberTool, memberArgumentsSHA256, memberOperationID}
	toolCallMembers       = []string{memberType, memberServer, memberTool, memberArgumentsSHA256, memberOperationID}
	listMembers           = []string{memberType, memberServer, memberMethod}
	knownMembers          = []string{memberType, memberServer, memberTool, memberArgumentsSHA256, memberOperationID, memberMethod}
	argumentsSHA256Format = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// grant checks that exactly one well-formed entry was sent and returns it
// unchanged. The result is never used to pick a resource, scope or audience.
func grant(_ context.Context, req authorizationdetails.Request) (authorizationdetails.Result, error) {
	// Token exchange only echoes the granted entries and never puts them in
	// the token it issues, except when it mints an ID-JAG. Accepting the entry
	// on any other exchange would return a token the entry does not restrict.
	if req.GrantType == oauthproto.GrantTypeTokenExchange && req.RequestedTokenType != oauthproto.TokenTypeIDJAG {
		return authorizationdetails.Result{}, authorizationdetails.Error{
			Code:   authorizationdetails.CodeInvalidRequest,
			Reason: ReasonIDJAGRequired,
		}
	}
	if len(req.Entries) != 1 {
		return authorizationdetails.Result{}, invalid(ReasonSingleEntry)
	}
	if _, err := ParseEntry(req.Entries[0]); err != nil {
		return authorizationdetails.Result{}, err
	}
	return authorizationdetails.Result{Entries: req.Entries}, nil
}

// stringMembers decodes a JSON object whose members are all known names with
// string values, each appearing once. It reports false for anything else,
// including trailing data after the object. encoding/json silently replaces
// invalid UTF-8 and unpaired surrogate escapes such as \ud800 with U+FFFD, so
// two different signed values could decode to the same string. To keep the
// decoded value one-to-one with the bytes, it refuses invalid UTF-8 and any
// value that contains U+FFFD, whether written literally or produced by the
// decoder.
func stringMembers(raw json.RawMessage) (map[string]string, bool) {
	if !utf8.Valid(raw) {
		return nil, false
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return nil, false
	}
	members := make(map[string]string, len(knownMembers))
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return nil, false
		}
		name, _ := tok.(string)
		if _, seen := members[name]; seen || !slices.Contains(knownMembers, name) {
			return nil, false
		}
		tok, err = dec.Token()
		if err != nil {
			return nil, false
		}
		value, isString := tok.(string)
		if !isString || strings.ContainsRune(value, utf8.RuneError) {
			return nil, false
		}
		members[name] = value
	}
	if tok, err := dec.Token(); err != nil || tok != json.Delim('}') {
		return nil, false
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, false
	}
	return members, true
}

func invalid(reason string) authorizationdetails.Error {
	return authorizationdetails.Error{Code: authorizationdetails.CodeInvalidAuthorizationDetails, Reason: reason}
}

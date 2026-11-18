// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package toolcalldetails defines the RFC 9396 authorization_details type that
// carries the details of one MCP call in a token obtained by JWT-bearer or
// ID-JAG redemption. An entry is either a tool call
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
// The authorization server only checks the shape of the entry and signs it
// into the access token. It never computes the arguments hash or compares it
// with real arguments: the receiver of the token enforces the call.
//
// Nothing registers the type by default. An embedder adds it with Register to
// the types it passes to authorizationdetails.New.
package toolcalldetails

import (
	"context"
	"encoding/json"
	"regexp"
	"slices"

	"github.com/stacklok/toolhive/pkg/authserver/authorizationdetails"
	"github.com/stacklok/toolhive/pkg/oauthproto"
)

// Type is the authorization_details type name of an MCP call entry.
const Type = "https://toolhive.dev/mcp_tool_call"

// Reasons returned to the client as invalid_authorization_details. They are
// fixed strings and never carry request data.
const (
	ReasonSingleEntry    = "single_entry_required"
	ReasonMalformedEntry = "malformed_entry"
	ReasonInvalidArgs    = "invalid_arguments_hash"
	ReasonInvalidMethod  = "invalid_method"
)

const (
	memberServer          = "server"
	memberTool            = "tool"
	memberArgumentsSHA256 = "arguments_sha256"
	memberOperationID     = "operation_id"
	memberMethod          = "method"
)

// listMethods is the exact set of methods a list call may name.
var listMethods = []string{"tools/list", "prompts/list", "resources/list", "resources/templates/list"}

var (
	callFields            = []string{memberTool, memberArgumentsSHA256, memberOperationID}
	allFields             = []string{"type", memberServer, memberTool, memberArgumentsSHA256, memberOperationID, memberMethod}
	argumentsSHA256Format = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// Register adds the MCP call type to types, accepted on the JWT-bearer grant
// (plain and ID-JAG redemption) and on no other grant. It overwrites an
// existing entry of the same name.
func Register(types map[string]authorizationdetails.Type) {
	types[Type] = authorizationdetails.Type{
		GrantTypes: []string{oauthproto.GrantTypeJWTBearer},
		Grant:      grant,
	}
}

// grant checks that exactly one well-formed entry was sent and returns it
// unchanged. The result is never used to pick a resource, scope or audience.
func grant(_ context.Context, req authorizationdetails.Request) (authorizationdetails.Result, error) {
	if len(req.Entries) != 1 {
		return authorizationdetails.Result{}, invalid(ReasonSingleEntry)
	}
	if err := check(req.Entries[0]); err != nil {
		return authorizationdetails.Result{}, err
	}
	return authorizationdetails.Result{Entries: req.Entries}, nil
}

// check validates the members of one entry. The input must have passed
// Registry.Parse, which rejects duplicate member names.
func check(raw json.RawMessage) error {
	var members map[string]json.RawMessage
	if err := json.Unmarshal(raw, &members); err != nil || members == nil {
		return invalid(ReasonMalformedEntry)
	}
	for name := range members {
		if !slices.Contains(allFields, name) {
			return invalid(ReasonMalformedEntry)
		}
	}

	_, isList := members[memberMethod]
	isCall := slices.ContainsFunc(callFields, func(name string) bool { _, ok := members[name]; return ok })
	if isList && isCall {
		return invalid(ReasonMalformedEntry)
	}
	required := []string{memberServer, memberMethod}
	if !isList {
		required = append([]string{memberServer}, callFields...)
	}

	values := make(map[string]string, len(required))
	for _, name := range required {
		member, ok := members[name]
		if !ok {
			return invalid(ReasonMalformedEntry)
		}
		var value string
		if err := json.Unmarshal(member, &value); err != nil || value == "" {
			return invalid(ReasonMalformedEntry)
		}
		values[name] = value
	}
	if isList {
		if !slices.Contains(listMethods, values[memberMethod]) {
			return invalid(ReasonInvalidMethod)
		}
	} else if !argumentsSHA256Format.MatchString(values[memberArgumentsSHA256]) {
		return invalid(ReasonInvalidArgs)
	}
	return nil
}

func invalid(reason string) authorizationdetails.Error {
	return authorizationdetails.Error{Code: authorizationdetails.CodeInvalidAuthorizationDetails, Reason: reason}
}

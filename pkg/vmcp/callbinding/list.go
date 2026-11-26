// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package callbinding

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"

	mcpparser "github.com/stacklok/toolhive/pkg/mcp"
)

// ErrListResult reports a list response that ReadListResponse cannot read,
// or an answer that cannot be encoded. Errors wrapping it name what was wrong
// with the response, never its contents.
var ErrListResult = errors.New("callbinding: unreadable list result")

// listItemsMembers maps each list method a list-call entry may name (see
// toolcalldetails.IsListMethod) to the member of its result that holds the
// items.
var listItemsMembers = map[string]string{
	"tools/list":               "tools",
	"prompts/list":             "prompts",
	"resources/list":           "resources",
	"resources/templates/list": "resourceTemplates",
}

// ListResponse is the JSON-RPC response to a list call, read by
// ReadListResponse, whose items the vMCP replaces with the answer it gives a
// list-call token.
type ListResponse struct {
	envelope, result []mcpparser.JSONObjectMember
	itemsMember      string
}

// ReadListResponse reads the JSON-RPC response to a list call. It returns a
// nil response and isError true for a JSON-RPC error response. Anything else
// that is not a JSON-RPC response object whose result object holds the
// method's items member as an array (or null) is refused with an error
// wrapping ErrListResult, as is a member that occurs twice or is spelled in
// another letter case.
func ReadListResponse(method string, response []byte) (*ListResponse, bool, error) {
	itemsMember, ok := listItemsMembers[method]
	if !ok {
		return nil, false, fmt.Errorf("%w: not a list method", ErrListResult)
	}
	envelope, err := mcpparser.DecodeJSONObjectMembers(response)
	if err != nil {
		return nil, false, fmt.Errorf("%w: not a JSON object", ErrListResult)
	}
	rawResult, hasResult, err := mcpparser.UniqueCanonicalMember(envelope, "result")
	if err != nil {
		return nil, false, fmt.Errorf("%w: ambiguous result member", ErrListResult)
	}
	if !hasResult {
		if _, isError, err := mcpparser.UniqueCanonicalMember(envelope, "error"); err == nil && isError {
			return nil, true, nil
		}
		return nil, false, fmt.Errorf("%w: neither a result nor an error", ErrListResult)
	}
	result, err := mcpparser.DecodeJSONObjectMembers(rawResult)
	if err != nil {
		return nil, false, fmt.Errorf("%w: result is not an object", ErrListResult)
	}
	rawItems, hasItems, err := mcpparser.UniqueCanonicalMember(result, itemsMember)
	switch {
	case err != nil:
		return nil, false, fmt.Errorf("%w: ambiguous items member", ErrListResult)
	case !hasItems:
		return nil, false, fmt.Errorf("%w: no items member", ErrListResult)
	}
	if trimmed := bytes.TrimSpace(rawItems); !bytes.Equal(trimmed, []byte("null")) &&
		(len(trimmed) == 0 || trimmed[0] != '[') {
		return nil, false, fmt.Errorf("%w: items member is not an array", ErrListResult)
	}
	return &ListResponse{envelope: envelope, result: result, itemsMember: itemsMember}, false, nil
}

// Encode writes the response with items in place of its items and no
// nextCursor: the whole list in one response. Every other member is kept as
// written and in order, except that a cacheScope or ttlMs member is set to
// "private" or 0, since the answer depends on the token.
func (r *ListResponse) Encode(items []json.RawMessage) ([]byte, error) {
	result := make([]mcpparser.JSONObjectMember, 0, len(r.result))
	for _, member := range r.result {
		if member.Name != "nextCursor" {
			result = append(result, member)
		}
	}
	answer, err := mcpparser.EncodeJSONObjectMembers(nil, result, map[string]json.RawMessage{
		r.itemsMember: mcpparser.EncodeJSONArrayValues(nil, items),
		"cacheScope":  json.RawMessage(`"private"`),
		"ttlMs":       json.RawMessage(`0`),
	})
	if err != nil {
		return nil, fmt.Errorf("%w: cannot encode the result", ErrListResult)
	}
	out, err := mcpparser.EncodeJSONObjectMembers(nil, r.envelope, map[string]json.RawMessage{"result": answer})
	if err != nil {
		return nil, fmt.Errorf("%w: cannot encode the response", ErrListResult)
	}
	return out, nil
}

// ListParamsCarryCursor reports whether a list request's params carry a
// cursor (a non-null cursor member). It reports an error when params are
// neither absent, null nor one JSON object, or when a cursor member occurs
// twice or is spelled in another letter case, so params a decoder could read
// two ways never pass.
func ListParamsCarryCursor(params json.RawMessage) (bool, error) {
	trimmed := bytes.TrimSpace(params)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return false, nil
	}
	members, err := mcpparser.DecodeJSONObjectMembers(trimmed)
	if err != nil {
		return false, fmt.Errorf("%w: params is not an object", ErrListResult)
	}
	cursor, found, err := mcpparser.UniqueCanonicalMember(members, "cursor")
	if err != nil {
		return false, fmt.Errorf("%w: ambiguous cursor member", ErrListResult)
	}
	return found && !bytes.Equal(bytes.TrimSpace(cursor), []byte("null")), nil
}

// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package callbinding

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

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
// wrapping ErrListResult, as is an envelope with both a result and an error,
// and a result, error, items, nextCursor, cacheScope or ttlMs member that
// occurs twice or is spelled in another letter case: Encode replaces members
// by their exact name, so no other copy may stand beside them.
func ReadListResponse(method string, response []byte) (*ListResponse, bool, error) {
	itemsMember, ok := listItemsMembers[method]
	if !ok {
		return nil, false, fmt.Errorf("%w: not a list method", ErrListResult)
	}
	envelope, rawResult, isError, err := readEnvelope(response)
	if err != nil || isError {
		return nil, isError, err
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
	for _, name := range []string{"nextCursor", "cacheScope", "ttlMs"} {
		if _, _, err := mcpparser.UniqueCanonicalMember(result, name); err != nil {
			return nil, false, fmt.Errorf("%w: ambiguous %s member", ErrListResult, name)
		}
	}
	return &ListResponse{envelope: envelope, result: result, itemsMember: itemsMember}, false, nil
}

// readEnvelope decodes a JSON-RPC response object and returns its members and
// its result, or isError for an error response. It refuses anything else, an
// envelope with both a result and an error, and a result or error member that
// occurs twice or is spelled in another letter case.
func readEnvelope(response []byte) ([]mcpparser.JSONObjectMember, json.RawMessage, bool, error) {
	envelope, err := mcpparser.DecodeJSONObjectMembers(response)
	if err != nil {
		return nil, nil, false, fmt.Errorf("%w: not a JSON object", ErrListResult)
	}
	rawResult, hasResult, err := mcpparser.UniqueCanonicalMember(envelope, "result")
	if err != nil {
		return nil, nil, false, fmt.Errorf("%w: ambiguous result member", ErrListResult)
	}
	_, hasError, err := mcpparser.UniqueCanonicalMember(envelope, "error")
	switch {
	case err != nil:
		return nil, nil, false, fmt.Errorf("%w: ambiguous error member", ErrListResult)
	case hasResult && hasError:
		return nil, nil, false, fmt.Errorf("%w: both a result and an error", ErrListResult)
	case hasError:
		return nil, nil, true, nil
	case !hasResult:
		return nil, nil, false, fmt.Errorf("%w: neither a result nor an error", ErrListResult)
	}
	return envelope, rawResult, false, nil
}

// Encode writes the response with items in place of its items and no
// nextCursor: the whole list in one response. Every other member is kept as
// written and in order, except that a cacheScope or ttlMs member the response
// has is replaced with "private" or 0, since the answer depends on the token;
// one it does not have is not added.
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

// SSEResponse returns the JSON-RPC response that an event stream answering a
// request carries: a Legacy session the SDK rehydrated answers in
// text/event-stream rather than application/json. Events are read as the
// go-sdk client reads them: a leading BOM is dropped, lines end at LF with an
// optional CR before it, a blank line ends an event, and an event's data
// field values, each after "data:" and one optional space, are joined with
// LF. Events whose data is not a JSON-RPC response, such as a notification,
// are skipped. Exactly one event must carry a response, or an error wrapping
// ErrListResult is returned.
func SSEResponse(stream []byte) ([]byte, error) {
	stream = bytes.TrimPrefix(stream, mcpparser.UTF8BOM)
	var response []byte
	var data [][]byte
	inData := false
	flush := func() error {
		defer func() { data, inData = nil, false }()
		if !inData {
			return nil
		}
		payload := bytes.Join(data, []byte("\n"))
		if !isResponse(payload) {
			return nil
		}
		if response != nil {
			return fmt.Errorf("%w: more than one response in the event stream", ErrListResult)
		}
		response = payload
		return nil
	}
	for len(stream) > 0 {
		line := stream
		if i := bytes.IndexByte(stream, '\n'); i >= 0 {
			line, stream = stream[:i], stream[i+1:]
		} else {
			stream = nil
		}
		line = bytes.TrimSuffix(line, []byte("\r"))
		if len(line) == 0 {
			if err := flush(); err != nil {
				return nil, err
			}
			continue
		}
		if value, ok := bytes.CutPrefix(line, []byte("data:")); ok {
			data = append(data, bytes.TrimPrefix(value, []byte(" ")))
			inData = true
		}
	}
	if err := flush(); err != nil {
		return nil, err
	}
	if response == nil {
		return nil, fmt.Errorf("%w: no response in the event stream", ErrListResult)
	}
	return response, nil
}

// isResponse reports whether payload is a JSON object with a result or an
// error member, as a JSON-RPC response has and a request or notification
// does not.
func isResponse(payload []byte) bool {
	members, err := mcpparser.DecodeJSONObjectMembers(payload)
	if err != nil {
		return false
	}
	for _, member := range members {
		if strings.EqualFold(member.Name, "result") || strings.EqualFold(member.Name, "error") {
			return true
		}
	}
	return false
}

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

// ErrListResult reports a list response that ReadListPage cannot read, or a
// page that cannot be encoded. Errors wrapping it name what was wrong with the
// response, never its contents.
var ErrListResult = errors.New("callbinding: unreadable list result")

// listResultMembers maps each list method a list-call entry may name (see
// toolcalldetails.IsListMethod) to the member of its result that holds the
// items, and the member of an item that names it.
var listResultMembers = map[string]struct{ items, key string }{
	"tools/list":               {items: "tools", key: "name"},
	"prompts/list":             {items: "prompts", key: "name"},
	"resources/list":           {items: "resources", key: "uri"},
	"resources/templates/list": {items: "resourceTemplates", key: "uriTemplate"},
}

// ListPage is one page of a list result with its items filtered (see
// ReadListPage).
type ListPage struct {
	// Items are the page's items whose key is kept, as written.
	Items []json.RawMessage
	// NextCursor is the page's nextCursor, and HasNext reports whether it
	// has one. An empty string is a cursor.
	NextCursor string
	HasNext    bool

	envelope, result []mcpparser.JSONObjectMember
	itemsMember      string
}

// ReadListPage reads the JSON-RPC response to a list call and keeps the
// items whose name (for tools and prompts), URI (for resources) or URI
// template (for resource templates) is in keep. A list request carries no
// server, so the vMCP answers a list-call token with only its server's items
// this way: keep holds the keys of the items that server provides.
//
// It returns a nil page and isError true for a JSON-RPC error response. An
// item without a string key is dropped, and a null items member holds none.
// Anything else that is not a JSON-RPC response object whose result object
// holds the items member as an array of objects, or a nextCursor that is not
// a string, is refused with an error wrapping ErrListResult, as is a member
// that occurs twice or is spelled in another letter case.
func ReadListPage(method string, response []byte, keep map[string]struct{}) (*ListPage, bool, error) {
	members, ok := listResultMembers[method]
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
	page := &ListPage{envelope: envelope, result: result, itemsMember: members.items}

	rawItems, hasItems, err := mcpparser.UniqueCanonicalMember(result, members.items)
	switch {
	case err != nil:
		return nil, false, fmt.Errorf("%w: ambiguous items member", ErrListResult)
	case !hasItems:
		return nil, false, fmt.Errorf("%w: no items member", ErrListResult)
	case !bytes.Equal(bytes.TrimSpace(rawItems), []byte("null")):
		if page.Items, err = keptItems(rawItems, members.key, keep); err != nil {
			return nil, false, err
		}
	}

	if page.NextCursor, page.HasNext, err = nextCursor(result); err != nil {
		return nil, false, err
	}
	return page, false, nil
}

// nextCursor returns a list result's nextCursor and whether it has one: a
// null or absent member is none.
func nextCursor(result []mcpparser.JSONObjectMember) (string, bool, error) {
	rawCursor, hasCursor, err := mcpparser.UniqueCanonicalMember(result, "nextCursor")
	if err != nil {
		return "", false, fmt.Errorf("%w: ambiguous nextCursor member", ErrListResult)
	}
	if !hasCursor || bytes.Equal(bytes.TrimSpace(rawCursor), []byte("null")) {
		return "", false, nil
	}
	var cursor string
	if json.Unmarshal(rawCursor, &cursor) != nil {
		return "", false, fmt.Errorf("%w: nextCursor is not a string", ErrListResult)
	}
	return cursor, true, nil
}

// Encode writes the page's response with items in place of its items and no
// nextCursor: the whole list in one response. Every other member is kept as
// written and in order, except that a cacheScope or ttlMs member is set to
// "private" or 0, since the result depends on the token.
func (p *ListPage) Encode(items []json.RawMessage) ([]byte, error) {
	result := make([]mcpparser.JSONObjectMember, 0, len(p.result))
	for _, member := range p.result {
		if member.Name != "nextCursor" {
			result = append(result, member)
		}
	}
	filteredResult, err := mcpparser.EncodeJSONObjectMembers(nil, result, map[string]json.RawMessage{
		p.itemsMember: mcpparser.EncodeJSONArrayValues(nil, items),
		"cacheScope":  json.RawMessage(`"private"`),
		"ttlMs":       json.RawMessage(`0`),
	})
	if err != nil {
		return nil, fmt.Errorf("%w: cannot encode the result", ErrListResult)
	}
	out, err := mcpparser.EncodeJSONObjectMembers(nil, p.envelope, map[string]json.RawMessage{"result": filteredResult})
	if err != nil {
		return nil, fmt.Errorf("%w: cannot encode the response", ErrListResult)
	}
	return out, nil
}

// keptItems returns the items of the JSON array rawItems whose member key is
// a string in keep. An item without a string key is dropped.
func keptItems(rawItems json.RawMessage, key string, keep map[string]struct{}) ([]json.RawMessage, error) {
	var items []json.RawMessage
	if err := json.Unmarshal(rawItems, &items); err != nil {
		return nil, fmt.Errorf("%w: items member is not an array", ErrListResult)
	}
	kept := make([]json.RawMessage, 0, len(items))
	for _, item := range items {
		fields, err := mcpparser.DecodeJSONObjectMembers(item)
		if err != nil {
			return nil, fmt.Errorf("%w: an item is not an object", ErrListResult)
		}
		rawName, found, err := mcpparser.UniqueCanonicalMember(fields, key)
		if err != nil {
			return nil, fmt.Errorf("%w: an item's key is ambiguous", ErrListResult)
		}
		var name string
		if !found || json.Unmarshal(rawName, &name) != nil {
			continue
		}
		if _, ok := keep[name]; ok {
			kept = append(kept, item)
		}
	}
	return kept, nil
}

// WithCursor returns the JSON-RPC request for method with params, with id as
// its ID and params' cursor member set to cursor (added when absent): the
// request for the page after one whose nextCursor is cursor. params may be
// empty. Every other member of params is kept as written.
func WithCursor(id any, method string, params json.RawMessage, cursor string) ([]byte, error) {
	var members []mcpparser.JSONObjectMember
	if len(bytes.TrimSpace(params)) > 0 && !bytes.Equal(bytes.TrimSpace(params), []byte("null")) {
		var err error
		if members, err = mcpparser.DecodeJSONObjectMembers(params); err != nil {
			return nil, fmt.Errorf("%w: params is not an object", ErrListResult)
		}
	}
	rawCursor, err := json.Marshal(cursor)
	if err != nil {
		return nil, fmt.Errorf("%w: cannot encode the cursor", ErrListResult)
	}
	if _, found, err := mcpparser.UniqueCanonicalMember(members, "cursor"); err != nil {
		return nil, fmt.Errorf("%w: ambiguous cursor member", ErrListResult)
	} else if !found {
		members = append(members, mcpparser.JSONObjectMember{Name: "cursor", Value: rawCursor})
	}
	newParams, err := mcpparser.EncodeJSONObjectMembers(nil, members, map[string]json.RawMessage{"cursor": rawCursor})
	if err != nil {
		return nil, fmt.Errorf("%w: cannot encode the params", ErrListResult)
	}
	return json.Marshal(struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      any             `json:"id"`
		Method  string          `json:"method"`
		Params  json.RawMessage `json:"params"`
	}{JSONRPC: "2.0", ID: id, Method: method, Params: newParams})
}

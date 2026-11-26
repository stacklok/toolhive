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

// ErrListResult reports a list response that FilterListResult cannot read.
// Errors wrapping it name what was wrong with the response, never its
// contents.
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

// FilterListResult limits the JSON-RPC response to a list call to the items
// whose name (for tools and prompts), URI (for resources) or URI template
// (for resource templates) is in keep. A list request carries no server, so
// the vMCP answers a list-call token with only its server's items this way:
// keep holds the keys of the items that server provides.
//
// Every other member of the response and of its result, such as nextCursor
// or _meta, is kept as written and in order, except that a cacheScope or
// ttlMs member is set to "private" or 0: the result now depends on the token.
// An item without a string key is dropped. A JSON-RPC error response, and a
// result whose items member is null, are returned unchanged: they hold no
// items. Anything else that is not a JSON-RPC response object whose result
// object holds the items member as an array of objects is refused with an
// error wrapping ErrListResult, as is a member that occurs twice or is
// spelled in another letter case.
func FilterListResult(method string, response []byte, keep map[string]struct{}) ([]byte, error) {
	members, ok := listResultMembers[method]
	if !ok {
		return nil, fmt.Errorf("%w: not a list method", ErrListResult)
	}
	envelope, err := mcpparser.DecodeJSONObjectMembers(response)
	if err != nil {
		return nil, fmt.Errorf("%w: not a JSON object", ErrListResult)
	}
	rawResult, hasResult, err := mcpparser.UniqueCanonicalMember(envelope, "result")
	if err != nil {
		return nil, fmt.Errorf("%w: ambiguous result member", ErrListResult)
	}
	if !hasResult {
		if _, isError, err := mcpparser.UniqueCanonicalMember(envelope, "error"); err == nil && isError {
			return response, nil
		}
		return nil, fmt.Errorf("%w: neither a result nor an error", ErrListResult)
	}
	result, err := mcpparser.DecodeJSONObjectMembers(rawResult)
	if err != nil {
		return nil, fmt.Errorf("%w: result is not an object", ErrListResult)
	}
	rawItems, hasItems, err := mcpparser.UniqueCanonicalMember(result, members.items)
	switch {
	case err != nil:
		return nil, fmt.Errorf("%w: ambiguous items member", ErrListResult)
	case !hasItems:
		return nil, fmt.Errorf("%w: no items member", ErrListResult)
	case bytes.Equal(bytes.TrimSpace(rawItems), []byte("null")):
		return response, nil
	}
	kept, err := keptItems(rawItems, members.key, keep)
	if err != nil {
		return nil, err
	}

	filteredResult, err := mcpparser.EncodeJSONObjectMembers(nil, result, map[string]json.RawMessage{
		members.items: mcpparser.EncodeJSONArrayValues(nil, kept),
		"cacheScope":  json.RawMessage(`"private"`),
		"ttlMs":       json.RawMessage(`0`),
	})
	if err != nil {
		return nil, fmt.Errorf("%w: cannot encode the result", ErrListResult)
	}
	out, err := mcpparser.EncodeJSONObjectMembers(nil, envelope, map[string]json.RawMessage{"result": filteredResult})
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

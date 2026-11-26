// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package callbinding

import (
	"bytes"
	"encoding/json"
	"errors"
)

// ErrListResult reports a list response that FilterListResult cannot read. It
// never carries the response.
var ErrListResult = errors.New("callbinding: unreadable list result")

// listResultMembers maps each list method a list-call entry may name to the
// member of its result that holds the items, and the member of an item that
// names it.
var listResultMembers = map[string]struct{ items, key string }{
	"tools/list":               {items: "tools", key: "name"},
	"prompts/list":             {items: "prompts", key: "name"},
	"resources/list":           {items: "resources", key: "uri"},
	"resources/templates/list": {items: "resourceTemplates", key: "uriTemplate"},
}

// IsListMethod reports whether method is one of the list methods a list-call
// entry may name.
func IsListMethod(method string) bool {
	_, ok := listResultMembers[method]
	return ok
}

// FilterListResult limits the JSON-RPC response to a list call to the items
// whose name (for tools and prompts), URI (for resources) or URI template
// (for resource templates) is in keep. A list request carries no server, so
// the vMCP answers a list-call token with only its server's items this way:
// keep holds the keys of the items that server provides.
//
// Every other member of the response and of its result, such as nextCursor,
// is kept. An item without a string key is dropped. A JSON-RPC error
// response, and a result without the items member or with it null, are
// returned unchanged: they hold no items. Anything else that is not a
// JSON-RPC response object with an object result whose items member is an
// array of objects is refused with ErrListResult.
func FilterListResult(method string, response []byte, keep map[string]struct{}) ([]byte, error) {
	members, ok := listResultMembers[method]
	if !ok {
		return nil, ErrListResult
	}
	envelope, result, ok := decodeListResponse(response)
	if !ok {
		return nil, ErrListResult
	}
	if result == nil {
		// A JSON-RPC error.
		return response, nil
	}
	rawItems, hasItems := result[members.items]
	if !hasItems || bytes.Equal(bytes.TrimSpace(rawItems), []byte("null")) {
		return response, nil
	}
	kept, ok := keptItems(rawItems, members.key, keep)
	if !ok {
		return nil, ErrListResult
	}

	var err error
	if result[members.items], err = encode(kept); err != nil {
		return nil, ErrListResult
	}
	if envelope["result"], err = encode(result); err != nil {
		return nil, ErrListResult
	}
	out, err := encode(envelope)
	if err != nil {
		return nil, ErrListResult
	}
	return out, nil
}

// decodeListResponse decodes a JSON-RPC response object and its result
// object. For an error response it returns a nil result. It reports false for
// anything else.
func decodeListResponse(response []byte) (envelope, result map[string]json.RawMessage, ok bool) {
	if err := json.Unmarshal(response, &envelope); err != nil || envelope == nil {
		return nil, nil, false
	}
	rawResult, hasResult := envelope["result"]
	if !hasResult {
		_, isError := envelope["error"]
		return envelope, nil, isError
	}
	if err := json.Unmarshal(rawResult, &result); err != nil || result == nil {
		return nil, nil, false
	}
	return envelope, result, true
}

// keptItems returns the items of the JSON array rawItems whose member key is
// a string in keep. An item without a string key is dropped. It reports false
// when rawItems is not an array of objects.
func keptItems(rawItems json.RawMessage, key string, keep map[string]struct{}) ([]json.RawMessage, bool) {
	var items []json.RawMessage
	if err := json.Unmarshal(rawItems, &items); err != nil {
		return nil, false
	}
	kept := make([]json.RawMessage, 0, len(items))
	for _, item := range items {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(item, &fields); err != nil || fields == nil {
			return nil, false
		}
		// Read the key member by its exact name: encoding/json would match a
		// struct field case-insensitively.
		var name string
		if err := json.Unmarshal(fields[key], &name); err != nil {
			continue
		}
		if _, ok := keep[name]; ok {
			kept = append(kept, item)
		}
	}
	return kept, true
}

// encode marshals v without escaping <, > and &, so kept items keep the
// characters the handler wrote.
func encode(v any) (json.RawMessage, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}

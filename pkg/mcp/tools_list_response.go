// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"encoding/json"
)

// encodeToolsListResponse returns body, a tools/list response that decodes
// into response, with the tools of its result replaced by tools. Every other
// member of the message and of its result, such as id, nextCursor,
// resultType, cacheScope, ttlMs or _meta, keeps the bytes the backend sent,
// while tools is encoded the way encoding/json encodes it. When the result
// member or its tools member is not found exactly once under its own name,
// the response is encoded from response as a whole, with tools in place of
// the result's tools.
func encodeToolsListResponse(body []byte, response toolsListResponse, tools []map[string]any) ([]byte, error) {
	encodedTools, err := json.Marshal(tools)
	if err != nil {
		return nil, err
	}
	members, resultMembers, ok := toolsListMembers(body)
	if !ok {
		response.Result.Tools = &tools
		return json.Marshal(response)
	}
	result, err := EncodeJSONObjectMembers(nil, resultMembers, map[string]json.RawMessage{"tools": encodedTools})
	if err != nil {
		return nil, err
	}
	return EncodeJSONObjectMembers(nil, members, map[string]json.RawMessage{"result": result})
}

// toolsListMembers returns the members of body and of its result when body
// is an object whose result member occurs exactly once and is an object whose
// tools member occurs exactly once, and ok false otherwise.
func toolsListMembers(body []byte) (members, resultMembers []JSONObjectMember, ok bool) {
	members, err := DecodeJSONObjectMembers(body)
	if err != nil {
		return nil, nil, false
	}
	result, found, err := UniqueCanonicalMember(members, "result")
	if err != nil || !found {
		return nil, nil, false
	}
	resultMembers, err = DecodeJSONObjectMembers(result)
	if err != nil {
		return nil, nil, false
	}
	if _, found, err := UniqueCanonicalMember(resultMembers, "tools"); err != nil || !found {
		return nil, nil, false
	}
	return members, resultMembers, true
}

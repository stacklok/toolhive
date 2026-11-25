// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"bytes"
	"encoding/json"
	"encoding/json/jsontext"
	"errors"
	"io"
	"strconv"
	"strings"
)

var errInvalidCallParams = errors.New("call binding: invalid params")

// toolCallParams returns params.name and the raw params.arguments of a
// tools/call, as received. arguments is nil when absent. It refuses params
// that is not one JSON object, a non-string name, and a member that a
// case-insensitive decoder would read as name, arguments or _meta without
// being spelled exactly so: hashing the exact member alone would then let
// such a member carry other arguments past the check.
func toolCallParams(params json.RawMessage) (name string, arguments json.RawMessage, err error) {
	members, err := paramMembers(params, "name", "arguments", "_meta")
	if err != nil {
		return "", nil, err
	}
	if raw, ok := members["name"]; ok {
		if err := json.Unmarshal(raw, &name); err != nil {
			return "", nil, errInvalidCallParams
		}
	}
	return name, members["arguments"], nil
}

// cancelledRequestID returns the requestId of a notifications/cancelled in
// the form requestIDKey gives. It reports false when params has no usable
// requestId.
func cancelledRequestID(params json.RawMessage) (string, bool) {
	members, err := paramMembers(params, "requestId", "_meta")
	if err != nil {
		return "", false
	}
	raw, ok := members["requestId"]
	if !ok {
		return "", false
	}
	var id any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if dec.Decode(&id) != nil {
		return "", false
	}
	return requestIDKey(id)
}

// requestIDKey returns a JSON-RPC ID as its JSON encoding, so that 7 and "7"
// stay different and every spelling of one integer is the same key. id is a
// string, an int64 (as the parser hands over a request's ID) or a json.Number.
// It reports false for anything else, including non-integer numbers, which
// MCP does not allow as IDs.
func requestIDKey(id any) (string, bool) {
	switch v := id.(type) {
	case string:
		encoded, err := json.Marshal(v)
		if err != nil {
			return "", false
		}
		return string(encoded), true
	case int64:
		return strconv.FormatInt(v, 10), true
	case json.Number:
		n, err := v.Int64()
		if err != nil {
			return "", false
		}
		return strconv.FormatInt(n, 10), true
	default:
		return "", false
	}
}

// paramMembers decodes params as one JSON object and returns its members
// whose names are exactly one of names. It refuses invalid UTF-8, duplicate
// names, and a member whose name differs from one of names but matches it
// under Unicode case folding, with or without '-' and '_': encoding/json
// matches struct fields that way. Empty params has no members.
func paramMembers(params json.RawMessage, names ...string) (map[string]json.RawMessage, error) {
	members := make(map[string]json.RawMessage, len(names))
	if len(bytes.Trim(params, " \t\r\n")) == 0 {
		return members, nil
	}
	dec := jsontext.NewDecoder(bytes.NewReader(params))
	if tok, err := dec.ReadToken(); err != nil || tok.Kind() != '{' {
		return nil, errInvalidCallParams
	}
	for dec.PeekKind() != '}' {
		tok, err := dec.ReadToken()
		if err != nil {
			return nil, errInvalidCallParams
		}
		member := tok.String()
		value, err := dec.ReadValue()
		if err != nil {
			return nil, errInvalidCallParams
		}
		for _, name := range names {
			switch {
			case member == name:
				members[name] = json.RawMessage(bytes.Clone(value))
			case foldsTo(member, name):
				return nil, errInvalidCallParams
			}
		}
	}
	if _, err := dec.ReadToken(); err != nil {
		return nil, errInvalidCallParams
	}
	if _, err := dec.ReadToken(); !errors.Is(err, io.EOF) {
		return nil, errInvalidCallParams
	}
	return members, nil
}

// memberDelimiters are the characters a case-insensitive decoder may ignore
// when it matches a member name to a field.
var memberDelimiters = strings.NewReplacer("_", "", "-", "")

// foldsTo reports whether member could be decoded into a field named name by
// a case-insensitive decoder. '_' and '-' fold only to themselves, so
// comparing with them removed also covers plain case folding.
func foldsTo(member, name string) bool {
	return strings.EqualFold(memberDelimiters.Replace(member), memberDelimiters.Replace(name))
}

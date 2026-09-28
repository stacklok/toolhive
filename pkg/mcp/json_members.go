// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

// JSONObjectMember is one member of a JSON object, exactly as it appeared on
// the wire.
type JSONObjectMember struct {
	Name  string
	Value json.RawMessage
}

// DecodeJSONObjectMembers retains object member order and duplicates so the
// members a filter decides on can be validated before encoding/json's usual
// case-insensitive matching or last-value-wins behavior can hide them.
func DecodeJSONObjectMembers(data []byte) ([]JSONObjectMember, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	opening, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	if delimiter, ok := opening.(json.Delim); !ok || delimiter != '{' {
		return nil, errors.New("value must be an object")
	}

	members := make([]JSONObjectMember, 0)
	for decoder.More() {
		nameToken, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		name, ok := nameToken.(string)
		if !ok {
			return nil, errors.New("object member name must be a string")
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return nil, err
		}
		members = append(members, JSONObjectMember{Name: name, Value: value})
	}

	closing, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	if delimiter, ok := closing.(json.Delim); !ok || delimiter != '}' {
		return nil, errors.New("unterminated object")
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, errors.New("unexpected data after object")
		}
		return nil, err
	}
	return members, nil
}

// UniqueCanonicalMember returns the value of canonical when it occurs exactly
// once. Duplicates and case-folded aliases are rejected: encoding/json binds a
// member to a struct field whenever the two names are equal under
// strings.EqualFold, which applies Unicode simple case folding (so "TOOLS",
// and "tools" spelled with U+017F LATIN SMALL LETTER LONG S, both bind to
// "tools"), and it keeps the last of several matches, while other decoders
// match names exactly or keep the first. Any of them could otherwise read a
// different value than the one used for authorization or filtering.
func UniqueCanonicalMember(
	members []JSONObjectMember,
	canonical string,
) (json.RawMessage, bool, error) {
	var value json.RawMessage
	found := false
	for _, member := range members {
		if !strings.EqualFold(member.Name, canonical) {
			continue
		}
		if member.Name != canonical {
			return nil, false, fmt.Errorf("field %q has non-canonical alias %q", canonical, member.Name)
		}
		if found {
			return nil, false, fmt.Errorf("field %q occurs more than once", canonical)
		}
		value = member.Value
		found = true
	}
	return value, found, nil
}

// EncodeJSONObjectMembers appends to dst a JSON object holding members in the
// order given. A member whose name has an entry in replacements is written
// with that value instead of its own; only exactly equal names match, and an
// entry that names no member adds nothing. replacements may be nil. Values
// are written as they are, so each must be a single JSON value.
func EncodeJSONObjectMembers(
	dst []byte,
	members []JSONObjectMember,
	replacements map[string]json.RawMessage,
) ([]byte, error) {
	dst = append(dst, '{')
	for i, member := range members {
		if i > 0 {
			dst = append(dst, ',')
		}
		name, err := json.Marshal(member.Name)
		if err != nil {
			return nil, err
		}
		dst = append(dst, name...)
		dst = append(dst, ':')
		value := member.Value
		if replacement, ok := replacements[member.Name]; ok {
			value = replacement
		}
		dst = append(dst, value...)
	}
	return append(dst, '}'), nil
}

// EncodeJSONArrayValues appends to dst a JSON array holding values in the
// order given. Values are written as they are, so each must be a single JSON
// value.
func EncodeJSONArrayValues(dst []byte, values []json.RawMessage) []byte {
	dst = append(dst, '[')
	for i, value := range values {
		if i > 0 {
			dst = append(dst, ',')
		}
		dst = append(dst, value...)
	}
	return append(dst, ']')
}

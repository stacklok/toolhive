// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCallBindingToolCallParams(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		params    string
		wantName  string
		wantArgs  string
		ambiguous bool
	}{
		{name: "name and arguments", params: `{"name":"t","arguments":{"a":1}}`, wantName: "t", wantArgs: `{"a":1}`},
		{name: "absent arguments", params: `{"name":"t"}`, wantName: "t"},
		{name: "null arguments kept raw", params: `{"name":"t","arguments":null}`, wantName: "t", wantArgs: `null`},
		{name: "other members ignored", params: `{"task":{},"name":"t","_meta":{"k":"v"}}`, wantName: "t"},
		{name: "absent params", params: ``},
		{name: "escaped name", params: `{"\u006eame":"t"}`, wantName: "t"},
		{name: "escaped arguments", params: `{"name":"t","\u0061rguments":{"a":1}}`, wantName: "t", wantArgs: `{"a":1}`},
		{name: "escaped capitalized name", params: `{"\u004eame":"t"}`, ambiguous: true},
		{name: "capitalized arguments", params: `{"name":"t","Arguments":{"a":1}}`, ambiguous: true},
		{name: "upper-case arguments", params: `{"name":"t","ARGUMENTS":{"a":1}}`, ambiguous: true},
		{name: "long s folds to arguments", params: `{"name":"t","argumentſ":{"a":1}}`, ambiguous: true},
		{name: "capitalized name", params: `{"Name":"t"}`, ambiguous: true},
		{name: "meta without underscore", params: `{"name":"t","meta":{}}`, ambiguous: true},
		{name: "capitalized meta", params: `{"name":"t","_Meta":{}}`, ambiguous: true},
		{name: "arguments with a dash", params: `{"name":"t","argu-ments":{}}`, ambiguous: true},
		{name: "non-string name", params: `{"name":1}`, ambiguous: true},
		{name: "duplicate member", params: `{"name":"t","name":"u"}`, ambiguous: true},
		{name: "array params", params: `["t"]`, ambiguous: true},
		{name: "trailing data", params: `{"name":"t"} {}`, ambiguous: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			name, args, err := toolCallParams(json.RawMessage(tc.params))
			if tc.ambiguous {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.wantName, name)
			if tc.wantArgs == "" {
				assert.Nil(t, args)
			} else {
				assert.JSONEq(t, tc.wantArgs, string(args))
			}
		})
	}
}

func TestCallBindingRequestIDKey(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		id   any
		want string
		ok   bool
	}{
		{name: "int64", id: int64(7), want: "7", ok: true},
		{name: "string", id: "7", want: `"7"`, ok: true},
		{name: "number", id: json.Number("7"), want: "7", ok: true},
		{name: "negative zero is zero", id: json.Number("-0"), want: "0", ok: true},
		{name: "fraction", id: json.Number("7.0")},
		{name: "exponent", id: json.Number("7e0")},
		{name: "beyond int64", id: json.Number("9223372036854775808")},
		{name: "float64 is not an ID form the parser produces", id: float64(7)},
		{name: "nil", id: nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, ok := requestIDKey(tc.id)
			assert.Equal(t, tc.ok, ok)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestCallBindingCancelledRequestID(t *testing.T) {
	t.Parallel()

	id, ok := cancelledRequestID(json.RawMessage(`{"requestId":7,"reason":"user"}`))
	assert.True(t, ok)
	assert.Equal(t, "7", id)

	id, ok = cancelledRequestID(json.RawMessage(`{"requestId":"7"}`))
	assert.True(t, ok)
	assert.Equal(t, `"7"`, id)

	_, ok = cancelledRequestID(json.RawMessage(`{"reason":"user"}`))
	assert.False(t, ok, "no requestId")

	id, ok = cancelledRequestID(json.RawMessage(`{"requestId":-0}`))
	assert.True(t, ok)
	assert.Equal(t, "0", id, "-0 and 0 are one integer ID")

	_, ok = cancelledRequestID(json.RawMessage(`{"requestId":7.5}`))
	assert.False(t, ok, "a fractional requestId")

	_, ok = cancelledRequestID(json.RawMessage(`{"requestID":7}`))
	assert.False(t, ok, "a case variant of requestId is refused")
}

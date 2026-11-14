// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestHasAmbiguousJSONMembers(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		body      string
		ambiguous bool
	}{
		{name: "duplicate root member", body: `{"method":"tools/list","method":"tools/call"}`, ambiguous: true},
		{name: "case variant root member", body: `{"method":"tools/list","Method":"tools/call"}`, ambiguous: true},
		{name: "duplicate nested member", body: `{"params":{"name":"allowed","name":"denied"}}`, ambiguous: true},
		{name: "case variant argument", body: `{"params":{"arguments":{"mode":"safe","Mode":"unsafe"}}}`, ambiguous: true},
		{name: "duplicate in array object", body: `{"items":[{"uri":"a","uri":"b"}]}`, ambiguous: true},
		{name: "unicode simple fold collision", body: `{"K":1,"K":2}`, ambiguous: true},
		{name: "BOM-prefixed collision", body: "\xEF\xBB\xBF" + `{"name":"allowed","Name":"denied"}`, ambiguous: true},
		{name: "overflow before collision", body: `{"padding":1e1000,"name":"allowed","Name":"denied"}`, ambiguous: true},
		{name: "overflow after collision", body: `{"name":"allowed","Name":"denied","padding":1e1000}`, ambiguous: true},
		{name: "ordinary request", body: `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"weather","arguments":{"city":"Paris"}}}`},
		{name: "empty containers", body: `{"object":{},"array":[]}`},
		{name: "nested containers", body: `{"items":[{"first":1},{"second":{"third":3}}]}`},
		{name: "same key in sibling objects", body: `{"a":{"x":1},"b":{"x":1}}`},
		{name: "key matching string value", body: `{"a":"a"}`},
		{name: "large integer preserved", body: `{"value":9007199254740993}`},
		{name: "large exponent accepted", body: `{"padding":1e1000}`},
		{name: "unrelated names", body: `{"mode":1,"model":2}`},
		{name: "malformed JSON", body: `{"name":"a","name":"b"`, ambiguous: false},
		{name: "trailing JSON value", body: `{"name":"a","name":"b"} {}`, ambiguous: false},
		{name: "empty input", body: ``},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.ambiguous, hasAmbiguousJSONMembers([]byte(tt.body)))
		})
	}
}

func TestFoldJSONMemberNameMatchesEqualFold(t *testing.T) {
	t.Parallel()

	for _, pair := range [][2]string{
		{"name", "Name"},
		{"K", "K"},
		{"S", "ſ"},
		{"Σ", "ς"},
		{"Σ", "σ"},
	} {
		isEqualFold := strings.EqualFold(pair[0], pair[1])
		assert.True(t, isEqualFold, "test pair must be EqualFold-equivalent")
		assert.Equal(t, foldJSONMemberName(pair[0]), foldJSONMemberName(pair[1]))
	}
}

func BenchmarkHasAmbiguousJSONMembers(b *testing.B) {
	largeArguments := func() []byte {
		var body strings.Builder
		body.WriteString(`{"arguments":{`)
		for i := range 10_000 {
			if i > 0 {
				body.WriteByte(',')
			}
			body.WriteString(`"argument`)
			body.WriteString(strconv.Itoa(i))
			body.WriteString(`":true`)
		}
		body.WriteString(`}}`)
		return []byte(body.String())
	}()
	const wrapperBytes = len(`{"payload":""}`)
	nearLimit := []byte(`{"payload":"` + strings.Repeat("x", (8<<20)-wrapperBytes) + `"}`)

	for name, body := range map[string][]byte{
		"typical request": []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"weather","arguments":{"city":"Paris"}}}`),
		"large arguments": largeArguments,
		"near body limit": nearLimit,
	} {
		b.Run(name, func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(len(body)))
			for range b.N {
				hasAmbiguousJSONMembers(body)
			}
		})
	}
}

// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDecodeJSONObjectMembers(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		data      string
		expected  []JSONObjectMember
		expectErr bool
	}{
		{
			name: "keeps order, duplicates and case variants",
			data: `{"b":1,"a":{"x":[1]},"b":2,"B":3}`,
			expected: []JSONObjectMember{
				{Name: "b", Value: json.RawMessage(`1`)},
				{Name: "a", Value: json.RawMessage(`{"x":[1]}`)},
				{Name: "b", Value: json.RawMessage(`2`)},
				{Name: "B", Value: json.RawMessage(`3`)},
			},
		},
		{name: "empty object", data: ` {} `, expected: []JSONObjectMember{}},
		{name: "array", data: `[{"a":1}]`, expectErr: true},
		{name: "null", data: `null`, expectErr: true},
		{name: "trailing data", data: `{"a":1}{"b":2}`, expectErr: true},
		{name: "unterminated", data: `{"a":1`, expectErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			members, err := DecodeJSONObjectMembers([]byte(tt.data))
			if tt.expectErr {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.expected, members)
		})
	}
}

// TestUniqueCanonicalMemberMatchesEncodingJSON checks, against the
// encoding/json in use, that UniqueCanonicalMember treats a member spelling as
// the canonical member or as an alias of it exactly when encoding/json would
// bind that spelling to a struct field tagged with the canonical name.
func TestUniqueCanonicalMemberMatchesEncodingJSON(t *testing.T) {
	t.Parallel()

	tests := []struct {
		canonical string
		spelling  string
	}{
		{"tools", "tools"},
		{"tools", "TOOLS"},
		{"tools", "Tools"},
		{"tools", "tool\u017f"}, // LATIN SMALL LETTER LONG S
		{"tools", "TOOL\u017f"},
		{"tools", "tooIs"},
		{"tools", "to_ols"},
		{"tools", "to-ols"},
		{"tools", "tools "},
		{"tools", "\uff54ools"}, // FULLWIDTH LATIN SMALL LETTER T
		{"result", "re\u017fult"},
		{"result", "RESULT"},
		{"name", "NAME"},
		{"name", "nAmE"},
		{"description", "de\u017fcription"},
		{"kind", "\u212aind"}, // KELVIN SIGN
		{"kind", "k\u0131nd"}, // LATIN SMALL LETTER DOTLESS I
		{"kind", "k\u0130nd"}, // LATIN CAPITAL LETTER I WITH DOT ABOVE
	}

	for _, tc := range tests {
		t.Run(tc.canonical+"/"+tc.spelling, func(t *testing.T) {
			t.Parallel()

			body, err := json.Marshal(map[string]string{tc.spelling: "bound"})
			require.NoError(t, err)

			target := reflect.New(reflect.StructOf([]reflect.StructField{{
				Name: "Field",
				Type: reflect.TypeOf(""),
				Tag:  reflect.StructTag(`json:"` + tc.canonical + `"`),
			}}))
			require.NoError(t, json.Unmarshal(body, target.Interface()))
			bound := target.Elem().Field(0).String() == "bound"

			members, err := DecodeJSONObjectMembers(body)
			require.NoError(t, err)
			_, found, err := UniqueCanonicalMember(members, tc.canonical)

			switch {
			case tc.spelling == tc.canonical:
				require.NoError(t, err)
				assert.True(t, found)
				assert.True(t, bound)
			case bound:
				assert.Error(t, err, "encoding/json binds %q to %q, so it must be rejected", tc.spelling, tc.canonical)
			default:
				assert.NoError(t, err)
				assert.False(t, found)
			}
		})
	}
}

func TestUniqueCanonicalMemberRejectsDuplicates(t *testing.T) {
	t.Parallel()

	members, err := DecodeJSONObjectMembers([]byte(`{"tools":[],"tools":null}`))
	require.NoError(t, err)

	_, _, err = UniqueCanonicalMember(members, "tools")
	assert.ErrorContains(t, err, "occurs more than once")
}

func TestEncodeJSONObjectMembers(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		dst          string
		members      []JSONObjectMember
		replacements map[string]json.RawMessage
		expected     string
	}{
		{
			name: "escapes names",
			members: []JSONObjectMember{
				{Name: `a"b\c`, Value: json.RawMessage(`1`)},
				{Name: "<tag>&", Value: json.RawMessage(`2`)},
				{Name: "line\nbreak", Value: json.RawMessage(`3`)},
			},
			expected: `{"a\"b\\c":1,"\u003ctag\u003e\u0026":2,"line\nbreak":3}`,
		},
		{
			name: "replaces only members whose name matches exactly",
			members: []JSONObjectMember{
				{Name: "text", Value: json.RawMessage(`"a"`)},
				{Name: "TEXT", Value: json.RawMessage(`"b"`)},
				{Name: "Text", Value: json.RawMessage(`"c"`)},
			},
			replacements: map[string]json.RawMessage{"text": json.RawMessage(`"x"`)},
			expected:     `{"text":"x","TEXT":"b","Text":"c"}`,
		},
		{
			name:         "adds no member for a replacement without one",
			members:      []JSONObjectMember{{Name: "a", Value: json.RawMessage(`1`)}},
			replacements: map[string]json.RawMessage{"b": json.RawMessage(`2`)},
			expected:     `{"a":1}`,
		},
		{
			name: "nil replacements keep every value as it is",
			members: []JSONObjectMember{
				{Name: "b", Value: json.RawMessage(`{ "y" : 1.50 }`)},
				{Name: "a", Value: json.RawMessage(`[1, 2]`)},
			},
			expected: `{"b":{ "y" : 1.50 },"a":[1, 2]}`,
		},
		{name: "no members", expected: `{}`},
		{
			name:     "appends to dst",
			dst:      `data: `,
			members:  []JSONObjectMember{{Name: "a", Value: json.RawMessage(`1`)}},
			expected: `data: {"a":1}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			encoded, err := EncodeJSONObjectMembers([]byte(tt.dst), tt.members, tt.replacements)
			require.NoError(t, err)
			assert.Equal(t, tt.expected, string(encoded))

			decoded, err := DecodeJSONObjectMembers(encoded[len(tt.dst):])
			require.NoError(t, err)
			require.Len(t, decoded, len(tt.members))
			for i, member := range tt.members {
				assert.Equal(t, member.Name, decoded[i].Name)
			}
		})
	}
}

func TestEncodeJSONArrayValues(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		dst      string
		values   []json.RawMessage
		expected string
	}{
		{name: "empty array", expected: `[]`},
		{
			name:     "keeps values as they are, in order",
			values:   []json.RawMessage{json.RawMessage(`{"b":2, "a":1}`), json.RawMessage(`"x"`), json.RawMessage(`null`)},
			expected: `[{"b":2, "a":1},"x",null]`,
		},
		{
			name:     "appends to dst",
			dst:      `{"tools":`,
			values:   []json.RawMessage{json.RawMessage(`1`)},
			expected: `{"tools":[1]`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.expected, string(EncodeJSONArrayValues([]byte(tt.dst), tt.values)))
		})
	}
}

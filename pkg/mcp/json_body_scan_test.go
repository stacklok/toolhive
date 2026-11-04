// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestJSONBodyScan verifies that a scan fed a body in pieces gives, after
// each piece, the verdict json.Valid gives on the body so far, and that it
// reads each byte of an object or array once.
func TestJSONBodyScan(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		pieces []string
		// valid is the verdict after each piece.
		valid []bool
	}{
		{
			name:   "object in one piece",
			pieces: []string{`{"a":1}`},
			valid:  []bool{true},
		},
		{
			name:   "object split across writes",
			pieces: []string{`{"a":`, `1,"b":[1,`, `2]}`},
			valid:  []bool{false, false, true},
		},
		{
			name:   "nested arrays split across writes",
			pieces: []string{`[[1,[2]],`, `[3]]`},
			valid:  []bool{false, true},
		},
		{
			name:   "braces and brackets inside strings",
			pieces: []string{`{"a":"}{][`, `"}`},
			valid:  []bool{false, true},
		},
		{
			name:   "escaped quotes inside strings",
			pieces: []string{`{"a":"\"}"`, `,"b":"\\"}`},
			valid:  []bool{false, true},
		},
		{
			name:   "a string the body never ends",
			pieces: []string{`{"a":"`, `}`},
			valid:  []bool{false, false},
		},
		{
			name:   "white space before and after the value",
			pieces: []string{" \t", `{"a":1}`, "\r\n"},
			valid:  []bool{false, true, true},
		},
		{
			name:   "bytes after the value",
			pieces: []string{`{"a":1}`, `x`, ` `},
			valid:  []bool{true, false, false},
		},
		{
			name:   "a second value after the first",
			pieces: []string{`{}`, `{}`},
			valid:  []bool{true, false},
		},
		{
			name:   "a closed value encoding/json rejects stays invalid",
			pieces: []string{`{"a":NaN}`, ` `},
			valid:  []bool{false, false},
		},
		{
			name:   "a string body",
			pieces: []string{`"ab`, `c"`},
			valid:  []bool{false, true},
		},
		{
			name:   "a number body",
			pieces: []string{`12`, `3`},
			valid:  []bool{true, true},
		},
		{
			name:   "a literal body",
			pieces: []string{`tru`, `e`},
			valid:  []bool{false, true},
		},
		{
			name:   "an empty body",
			pieces: []string{``, ` `},
			valid:  []bool{false, false},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.Len(t, tt.valid, len(tt.pieces))
			var scan jsonBodyScan
			var body []byte
			for i, piece := range tt.pieces {
				body = append(body, piece...)
				valid := scan.isValid(body)
				assert.Equal(t, tt.valid[i], valid, "verdict after piece %d", i)
				assert.Equal(t, json.Valid(body), valid, "verdict after piece %d differs from json.Valid", i)
				assert.Equal(t, len(body), scan.read, "bytes read after piece %d", i)
			}
		})
	}
}

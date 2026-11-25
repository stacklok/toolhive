// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"golang.org/x/exp/jsonrpc2"
)

// TestTrimLeadingBOMs verifies that every leading byte order mark is dropped,
// in either form and in any order, and that nothing after the first body byte
// or an incomplete mark is touched.
func TestTrimLeadingBOMs(t *testing.T) {
	t.Parallel()

	const bom, latin1 = "\xEF\xBB\xBF", "\xC3\xAF\xC2\xBB\xC2\xBF"

	tests := []struct {
		name     string
		data     string
		expected string
	}{
		{name: "no mark", data: `{"a":1}`, expected: `{"a":1}`},
		{name: "one UTF-8 mark", data: bom + `{"a":1}`, expected: `{"a":1}`},
		{name: "three UTF-8 marks", data: bom + bom + bom + `{"a":1}`, expected: `{"a":1}`},
		{name: "one Latin-1 mark", data: latin1 + `{"a":1}`, expected: `{"a":1}`},
		{name: "two Latin-1 marks", data: latin1 + latin1 + `{"a":1}`, expected: `{"a":1}`},
		{name: "UTF-8 then Latin-1", data: bom + latin1 + `{"a":1}`, expected: `{"a":1}`},
		{name: "Latin-1 then UTF-8", data: latin1 + bom + `{"a":1}`, expected: `{"a":1}`},
		{name: "UTF-8, Latin-1, UTF-8", data: bom + latin1 + bom + `{"a":1}`, expected: `{"a":1}`},
		{name: "nothing but marks", data: bom + latin1, expected: ""},
		{name: "a mark after the first byte stays", data: "x" + bom + latin1, expected: "x" + bom + latin1},
		{name: "an incomplete Latin-1 mark stays", data: latin1[:4] + `{"a":1}`, expected: latin1[:4] + `{"a":1}`},
		{name: "an incomplete UTF-8 mark stays", data: bom[:2] + `{"a":1}`, expected: bom[:2] + `{"a":1}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, []byte(tt.expected), trimLeadingBOMs([]byte(tt.data)))
		})
	}
}

// TestConvertToJSONRPC2ID tests the ConvertToJSONRPC2ID function with various ID types
func TestConvertToJSONRPC2ID(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name        string
		input       interface{}
		expectError bool
	}{
		{
			name:        "nil ID",
			input:       nil,
			expectError: false,
		},
		{
			name:        "string ID",
			input:       "test-id",
			expectError: false,
		},
		{
			name:        "int ID",
			input:       42,
			expectError: false,
		},
		{
			name:        "int64 ID",
			input:       int64(123456789),
			expectError: false,
		},
		{
			name:        "float64 ID (JSON number)",
			input:       float64(99.0),
			expectError: false,
		},
		{
			name:        "unsupported type (slice)",
			input:       []string{"invalid"},
			expectError: true,
		},
		{
			name:        "unsupported type (map)",
			input:       map[string]string{"key": "value"},
			expectError: true,
		},
		{
			name:        "unsupported type (struct)",
			input:       struct{ Name string }{Name: "test"},
			expectError: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			result, err := ConvertToJSONRPC2ID(tc.input)

			if tc.expectError {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), "unsupported ID type")
			} else {
				assert.NoError(t, err)
				// For nil input, we expect an empty ID
				if tc.input == nil {
					assert.Equal(t, jsonrpc2.ID{}, result)
				} else {
					// For other valid inputs, we just verify no error
					assert.NotNil(t, result)
				}
			}
		})
	}
}

// TestWithoutAcceptEncoding covers the copy of a request the tools/list
// filter forwards: it carries no Accept-Encoding, keeps everything else, and
// leaves the request it was made from as it was.
func TestWithoutAcceptEncoding(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		values []string
	}{
		{name: "no header"},
		{name: "one value", values: []string{"gzip"}},
		{name: "two values", values: []string{"gzip, br", "zstd"}},
		{name: "identity", values: []string{"identity"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			type ctxKey struct{}
			ctx := context.WithValue(context.Background(), ctxKey{}, "marker")
			original := httptest.NewRequestWithContext(ctx, http.MethodPost, "/mcp", strings.NewReader(`{}`))
			original.Header.Set("Content-Type", "application/json")
			for _, value := range tt.values {
				original.Header.Add("Accept-Encoding", value)
			}
			before := original.Header.Clone()

			copied := WithoutAcceptEncoding(original)

			_, carries := copied.Header["Accept-Encoding"]
			assert.False(t, carries, "the copy must carry no Accept-Encoding")
			assert.Equal(t, "application/json", copied.Header.Get("Content-Type"))
			assert.Equal(t, original.Body, copied.Body)
			assert.Equal(t, ctx, copied.Context())
			assert.Equal(t, before, original.Header, "the request the copy was made from must keep its headers")
		})
	}
}

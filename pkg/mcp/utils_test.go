// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"golang.org/x/exp/jsonrpc2"
)

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

// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package app

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseHeaderString(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		input         string
		expectedName  string
		expectedValue string
		expectError   bool
	}{
		{
			name:          "simple header",
			input:         "X-Custom-Header=some-value",
			expectedName:  "X-Custom-Header",
			expectedValue: "some-value",
			expectError:   false,
		},
		{
			name:          "header with empty value",
			input:         "X-Empty=",
			expectedName:  "X-Empty",
			expectedValue: "",
			expectError:   false,
		},
		{
			name:          "header with equals in value",
			input:         "X-Complex=value=with=equals",
			expectedName:  "X-Complex",
			expectedValue: "value=with=equals",
			expectError:   false,
		},
		{
			name:          "header with spaces in value",
			input:         "X-Spaced=value with spaces",
			expectedName:  "X-Spaced",
			expectedValue: "value with spaces",
			expectError:   false,
		},
		{
			name:          "header name with whitespace trimmed",
			input:         "  X-Trimmed  =value",
			expectedName:  "X-Trimmed",
			expectedValue: "value",
			expectError:   false,
		},
		{
			name:        "missing equals sign",
			input:       "InvalidHeader",
			expectError: true,
		},
		{
			name:        "empty name",
			input:       "=value-only",
			expectError: true,
		},
		{
			name:        "whitespace only name",
			input:       "   =value-only",
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			name, value, err := parseHeaderString(tt.input)

			if tt.expectError {
				assert.Error(t, err)
				return
			}

			require.NoError(t, err)
			assert.Equal(t, tt.expectedName, name)
			assert.Equal(t, tt.expectedValue, value)
		})
	}
}

func TestParseHeaderForwardFlags(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		headers     []string
		expected    map[string]string
		expectError bool
	}{
		{
			name:        "multiple headers",
			headers:     []string{"X-Header1=value1", "X-Header2=value2"},
			expected:    map[string]string{"X-Header1": "value1", "X-Header2": "value2"},
			expectError: false,
		},
		{
			name:        "empty inputs",
			headers:     []string{},
			expected:    map[string]string{},
			expectError: false,
		},
		{
			name:        "invalid header",
			headers:     []string{"InvalidHeader"},
			expectError: true,
		},
		{
			name:        "restricted header Host rejected",
			headers:     []string{"Host=evil.example.com"},
			expectError: true,
		},
		{
			name:        "restricted header case insensitive",
			headers:     []string{"transfer-encoding=chunked"},
			expectError: true,
		},
		{
			name:        "restricted header among valid headers",
			headers:     []string{"X-Good=ok", "X-Forwarded-For=1.2.3.4"},
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result, err := parseHeaderForwardFlags(tt.headers)

			if tt.expectError {
				assert.Error(t, err)
				return
			}

			require.NoError(t, err)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestValidateHeaderNames(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		headers     map[string]string
		expectError bool
	}{
		{
			name:        "allowed headers pass",
			headers:     map[string]string{"X-Custom": "value", "Authorization": "Bearer tok"},
			expectError: false,
		},
		{
			name:        "empty map passes",
			headers:     map[string]string{},
			expectError: false,
		},
		{
			name:        "Host is blocked",
			headers:     map[string]string{"Host": "evil.example.com"},
			expectError: true,
		},
		{
			name:        "Connection is blocked",
			headers:     map[string]string{"connection": "keep-alive"},
			expectError: true,
		},
		{
			name:        "X-Forwarded-For is blocked",
			headers:     map[string]string{"x-forwarded-for": "1.2.3.4"},
			expectError: true,
		},
		{
			name:        "Content-Length is blocked",
			headers:     map[string]string{"content-length": "42"},
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := validateHeaderNames(tt.headers)
			if tt.expectError {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), "restricted")
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestParseHeaderSecretFlagsRestrictedHeaders(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		inputs      []string
		expectError bool
	}{
		{
			name:        "allowed secret header",
			inputs:      []string{"X-Api-Key=my-secret"},
			expectError: false,
		},
		{
			name:        "restricted secret header Host",
			inputs:      []string{"Host=some-secret"},
			expectError: true,
		},
		{
			name:        "restricted secret header Transfer-Encoding",
			inputs:      []string{"Transfer-Encoding=some-secret"},
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := parseHeaderSecretFlags(tt.inputs)
			if tt.expectError {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), "restricted")
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/stacklok/toolhive/pkg/transport/errors"
)

func TestTransportType_String(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		transport TransportType
		expected  string
	}{
		{
			name:      "stdio transport",
			transport: TransportTypeStdio,
			expected:  "stdio",
		},
		{
			name:      "sse transport",
			transport: TransportTypeSSE,
			expected:  "sse",
		},
		{
			name:      "streamable-http transport",
			transport: TransportTypeStreamableHTTP,
			expected:  "streamable-http",
		},
		{
			name:      "inspector transport",
			transport: TransportTypeInspector,
			expected:  "inspector",
		},
		{
			name:      "custom transport type",
			transport: TransportType("custom"),
			expected:  "custom",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := tt.transport.String()
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestParseTransportType(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		input       string
		expected    TransportType
		expectError bool
	}{
		{
			name:        "stdio lowercase",
			input:       "stdio",
			expected:    TransportTypeStdio,
			expectError: false,
		},
		{
			name:        "stdio uppercase",
			input:       "STDIO",
			expected:    TransportTypeStdio,
			expectError: false,
		},
		{
			name:        "sse lowercase",
			input:       "sse",
			expected:    TransportTypeSSE,
			expectError: false,
		},
		{
			name:        "sse uppercase",
			input:       "SSE",
			expected:    TransportTypeSSE,
			expectError: false,
		},
		{
			name:        "streamable-http lowercase",
			input:       "streamable-http",
			expected:    TransportTypeStreamableHTTP,
			expectError: false,
		},
		{
			name:        "streamable-http uppercase",
			input:       "STREAMABLE-HTTP",
			expected:    TransportTypeStreamableHTTP,
			expectError: false,
		},
		{
			name:        "inspector lowercase",
			input:       "inspector",
			expected:    TransportTypeInspector,
			expectError: false,
		},
		{
			name:        "inspector uppercase",
			input:       "INSPECTOR",
			expected:    TransportTypeInspector,
			expectError: false,
		},
		{
			name:        "unsupported transport",
			input:       "unsupported",
			expected:    "",
			expectError: true,
		},
		{
			name:        "empty string",
			input:       "",
			expected:    "",
			expectError: true,
		},
		{
			name:        "mixed case not supported",
			input:       "Stdio",
			expected:    "",
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result, err := ParseTransportType(tt.input)

			if tt.expectError {
				assert.Error(t, err)
				assert.Equal(t, errors.ErrUnsupportedTransport, err)
				assert.Equal(t, tt.expected, result)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.expected, result)
			}
		})
	}
}

func TestTransportTypeConstants(t *testing.T) {
	t.Parallel()

	// Test that constants have expected values
	assert.Equal(t, "stdio", string(TransportTypeStdio))
	assert.Equal(t, "sse", string(TransportTypeSSE))
	assert.Equal(t, "streamable-http", string(TransportTypeStreamableHTTP))
	assert.Equal(t, "inspector", string(TransportTypeInspector))
}

func TestEffectiveProxyMode(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		transport TransportType
		proxyMode ProxyMode
		expected  ProxyMode
	}{
		{
			name:      "stdio with sse proxy mode returns sse",
			transport: TransportTypeStdio,
			proxyMode: ProxyModeSSE,
			expected:  ProxyModeSSE,
		},
		{
			name:      "stdio with streamable-http proxy mode returns streamable-http",
			transport: TransportTypeStdio,
			proxyMode: ProxyModeStreamableHTTP,
			expected:  ProxyModeStreamableHTTP,
		},
		{
			name:      "stdio with empty proxy mode defaults to streamable-http",
			transport: TransportTypeStdio,
			proxyMode: "",
			expected:  ProxyModeStreamableHTTP,
		},
		{
			name:      "sse transport with empty proxy mode returns sse",
			transport: TransportTypeSSE,
			proxyMode: "",
			expected:  ProxyMode("sse"),
		},
		{
			name:      "sse transport with sse proxy mode returns sse",
			transport: TransportTypeSSE,
			proxyMode: ProxyModeSSE,
			expected:  ProxyMode("sse"),
		},
		{
			name:      "sse transport with streamable-http proxy mode returns sse",
			transport: TransportTypeSSE,
			proxyMode: ProxyModeStreamableHTTP,
			expected:  ProxyMode("sse"),
		},
		{
			name:      "streamable-http transport with empty proxy mode returns streamable-http",
			transport: TransportTypeStreamableHTTP,
			proxyMode: "",
			expected:  ProxyMode("streamable-http"),
		},
		{
			name:      "streamable-http transport with sse proxy mode returns streamable-http",
			transport: TransportTypeStreamableHTTP,
			proxyMode: ProxyModeSSE,
			expected:  ProxyMode("streamable-http"),
		},
		{
			name:      "streamable-http transport with streamable-http proxy mode returns streamable-http",
			transport: TransportTypeStreamableHTTP,
			proxyMode: ProxyModeStreamableHTTP,
			expected:  ProxyMode("streamable-http"),
		},
		{
			name:      "inspector transport with empty proxy mode returns inspector",
			transport: TransportTypeInspector,
			proxyMode: "",
			expected:  ProxyMode("inspector"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := EffectiveProxyMode(tt.transport, tt.proxyMode)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestTransportType_RoundTrip(t *testing.T) {
	t.Parallel()

	// Test that parsing and string conversion are consistent
	transports := []TransportType{
		TransportTypeStdio,
		TransportTypeSSE,
		TransportTypeStreamableHTTP,
		TransportTypeInspector,
	}

	for _, transport := range transports {
		t.Run(string(transport), func(t *testing.T) {
			t.Parallel()

			// Convert to string and parse back
			str := transport.String()
			parsed, err := ParseTransportType(str)

			assert.NoError(t, err)
			assert.Equal(t, transport, parsed)
		})
	}
}

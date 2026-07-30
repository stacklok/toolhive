// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package streamable

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestIsSupportedMCPVersion verifies membership in supportedMCPVersions: every
// known MCP revision date returns true, and unknown/garbage/empty strings
// return false. This function is only consulted by handlePost when strict
// protocol validation is enabled (see WithStrictProtocolValidation).
func TestIsSupportedMCPVersion(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		version string
		want    bool
	}{
		{name: "2024-11-05 is supported", version: "2024-11-05", want: true},
		{name: "2025-03-26 is supported", version: "2025-03-26", want: true},
		{name: "2025-06-18 is supported", version: "2025-06-18", want: true},
		{name: "2025-11-25 is supported", version: "2025-11-25", want: true},
		{name: "2026-07-28 is supported", version: "2026-07-28", want: true},
		{name: "unknown future date is not supported", version: "1999-01-01", want: false},
		{name: "garbage string is not supported", version: "not-a-version", want: false},
		{name: "empty string is not supported", version: "", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, isSupportedMCPVersion(tt.version))
		})
	}
}

// recordingFlusher is a minimal http.Flusher that records Flush calls.
type recordingFlusher struct {
	flushed int
}

func (f *recordingFlusher) Flush() { f.flushed++ }

// TestWriteSSEDataIncludesEventMessage verifies writeSSEData emits an explicit
// SSE event name before the data line. Spec-lenient MCP clients (for example
// @ai-sdk/mcp) only dispatch frames where event === "message" and drop
// data-only frames; ToolHive's own SSE serializer and the MCP SDK reference
// transports always include "event: message".
//
// Regression for #5655: every JSON-RPC SSE writer in this package goes through
// writeSSEData (POST success, POST error, GET standalone notifications, and
// progress), so this unit test covers all of those paths.
func TestWriteSSEDataIncludesEventMessage(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	flusher := &recordingFlusher{}
	payload := []byte(`{"jsonrpc":"2.0","id":1,"result":{"ok":true}}`)

	err := writeSSEData(&buf, flusher, payload)
	require.NoError(t, err)
	assert.Equal(t, 1, flusher.flushed)

	got := buf.String()
	assert.Equal(t, "event: message\ndata: "+string(payload)+"\n\n", got,
		"SSE frame must include event: message before data (raw bytes: %q)", got)
	assert.True(t, bytes.HasPrefix([]byte(got), []byte("event: message\n")),
		"frame must start with event: message, not a bare data: line")
}

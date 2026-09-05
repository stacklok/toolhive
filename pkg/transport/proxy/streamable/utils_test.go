// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package streamable

import (
	"bytes"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/exp/jsonrpc2"
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

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

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

// TestDrainPendingSSEProgressWritesQueuedMessagesBeforeFinal verifies that
// progress already buffered when a POST-SSE request receives its final
// response is emitted first. This makes the select race in
// handleSingleRequestSSE deterministic: the helper drains all currently ready
// request-scoped progress messages without waiting for future ones.
func TestDrainPendingSSEProgressWritesQueuedMessagesBeforeFinal(t *testing.T) {
	t.Parallel()

	progressCh := make(chan jsonrpc2.Message, 2)
	first, err := jsonrpc2.NewNotification("notifications/progress", map[string]any{"progress": 1})
	require.NoError(t, err)
	second, err := jsonrpc2.NewNotification("notifications/progress", map[string]any{"progress": 2})
	require.NoError(t, err)
	final, err := jsonrpc2.NewResponse(jsonrpc2.StringID("request-1"), map[string]any{"status": "done"}, nil)
	require.NoError(t, err)
	progressCh <- first
	progressCh <- second

	var buf bytes.Buffer
	flusher := &recordingFlusher{}
	require.True(t, drainPendingSSEProgress(&buf, flusher, progressCh))
	finalData, err := jsonrpc2.EncodeMessage(final)
	require.NoError(t, err)
	require.NoError(t, writeSSEData(&buf, flusher, finalData))

	got := buf.String()
	firstIndex := strings.Index(got, `"progress":1`)
	secondIndex := strings.Index(got, `"progress":2`)
	finalIndex := strings.Index(got, `"status":"done"`)
	require.GreaterOrEqual(t, firstIndex, 0, "first progress frame was not written")
	require.GreaterOrEqual(t, secondIndex, 0, "second progress frame was not written")
	require.GreaterOrEqual(t, finalIndex, 0, "final response frame was not written")
	assert.Less(t, firstIndex, secondIndex)
	assert.Less(t, secondIndex, finalIndex)
	assert.Equal(t, 3, flusher.flushed, "every progress and final frame must flush")
}

// TestDrainPendingSSEProgressReturnsImmediatelyWithoutQueuedProgress verifies
// that requests without progress routing, and requests with an empty route,
// retain their prior final-response behavior without blocking.
func TestDrainPendingSSEProgressReturnsImmediatelyWithoutQueuedProgress(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		deliverCh <-chan jsonrpc2.Message
	}{
		{name: "nil route", deliverCh: nil},
		{name: "empty route", deliverCh: make(chan jsonrpc2.Message, 1)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var buf bytes.Buffer
			flusher := &recordingFlusher{}
			assert.True(t, drainPendingSSEProgress(&buf, flusher, tt.deliverCh))
			assert.Empty(t, buf.String())
			assert.Zero(t, flusher.flushed)
		})
	}
}

// TestWriteSSEProgressMessageStopsOnWriteFailure verifies a disconnected SSE
// client stops further draining rather than silently discarding queued frames.
func TestWriteSSEProgressMessageStopsOnWriteFailure(t *testing.T) {
	t.Parallel()

	progress, err := jsonrpc2.NewNotification("notifications/progress", map[string]any{"progress": 1})
	require.NoError(t, err)
	flusher := &recordingFlusher{}

	assert.False(t, writeSSEProgressMessage(failingWriter{}, flusher, progress))
	assert.Zero(t, flusher.flushed)
}

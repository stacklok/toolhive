// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	mcpparser "github.com/stacklok/toolhive/pkg/mcp"
	"github.com/stacklok/toolhive/pkg/vmcp"
)

// bodyRecordingServer stands up a fake Modern backend that records the last
// request's headers and decoded params (so tests can assert the body identifier
// matches the Mcp-Name header), and replies with result. Callers pre-seed the
// revision cache Modern so the verb skips the discover probe and hits this server.
func bodyRecordingServer(t *testing.T, result map[string]any) (*httptest.Server, *http.Header, *map[string]any) {
	t.Helper()
	var gotHeader http.Header
	gotBody := map[string]any{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeader = r.Header.Clone()
		body, _ := readAll(t, r)
		var req struct {
			ID     any            `json:"id"`
			Params map[string]any `json:"params"`
		}
		require.NoError(t, json.Unmarshal(body, &req))
		for k, v := range req.Params {
			gotBody[k] = v
		}
		writeModernResult(t, w, req.ID, result)
	}))
	t.Cleanup(srv.Close)
	return srv, &gotHeader, &gotBody
}

func modernClient(t *testing.T, url string) (*httpBackendClient, *vmcp.BackendTarget) {
	t.Helper()
	h := newProbeClient(t)
	target := &vmcp.BackendTarget{WorkloadID: "b", BaseURL: url, TransportType: "streamable-http"}
	h.setRevision(target.WorkloadID, mcpparser.RevisionModern) // skip the probe
	return h, target
}

// TestModernCallTool verifies tools/call request shaping (Mcp-Method, translated
// Mcp-Name matching the body name, no session id) and result decode (isError,
// structuredContent, _meta forwarded).
func TestModernCallTool(t *testing.T) {
	t.Parallel()

	srv, hdr, body := bodyRecordingServer(t, map[string]any{
		"content":           []any{map[string]any{"type": "text", "text": "out"}},
		"structuredContent": map[string]any{"k": "v"},
		"isError":           true,
		"_meta":             map[string]any{"trace": "x"},
	})
	h, target := modernClient(t, srv.URL)
	target.OriginalCapabilityName = "backend_echo" // advertised "echo" -> backend "backend_echo"

	res, err := h.CallTool(context.Background(), target, "echo", map[string]any{"input": "hi"}, map[string]any{"caller": "meta"})
	require.NoError(t, err)

	assert.Equal(t, "tools/call", hdr.Get("Mcp-Method"))
	assert.Equal(t, "backend_echo", hdr.Get("Mcp-Name"), "Mcp-Name must be the translated name")
	assert.Equal(t, "backend_echo", (*body)["name"], "body name must match Mcp-Name")
	assert.Empty(t, hdr.Get("Mcp-Session-Id"))

	require.Len(t, res.Content, 1)
	assert.Equal(t, "out", res.Content[0].Text)
	assert.Equal(t, "v", res.StructuredContent["k"])
	assert.True(t, res.IsError)
	assert.Equal(t, "x", res.Meta["trace"], "result _meta must be forwarded to core")
}

// TestModernReadResource verifies resources/read shaping (Mcp-Name mirrors the
// translated uri) and text/blob content decode.
func TestModernReadResource(t *testing.T) {
	t.Parallel()

	srv, hdr, body := bodyRecordingServer(t, map[string]any{
		"contents": []any{
			map[string]any{"uri": "file:///x", "mimeType": "text/plain", "text": "hello"},
			map[string]any{"uri": "file:///y", "mimeType": "application/octet-stream", "blob": "AAAA"},
		},
		"_meta": map[string]any{"trace": "r"},
	})
	h, target := modernClient(t, srv.URL)
	target.OriginalCapabilityName = "file:///backend"

	res, err := h.ReadResource(context.Background(), target, "file:///advertised")
	require.NoError(t, err)

	assert.Equal(t, "resources/read", hdr.Get("Mcp-Method"))
	assert.Equal(t, "file:///backend", hdr.Get("Mcp-Name"))
	assert.Equal(t, "file:///backend", (*body)["uri"], "body uri must match Mcp-Name")
	require.Len(t, res.Contents, 2)
	assert.Equal(t, "hello", res.Contents[0].Text)
	assert.Equal(t, "AAAA", res.Contents[1].Blob)
	assert.Equal(t, "r", res.Meta["trace"])
}

// TestModernGetPrompt verifies prompts/get shaping (translated Mcp-Name matching
// the body name) and message content decode.
func TestModernGetPrompt(t *testing.T) {
	t.Parallel()

	srv, hdr, body := bodyRecordingServer(t, map[string]any{
		"description": "d",
		"messages": []any{
			map[string]any{"role": "user", "content": map[string]any{"type": "text", "text": "hi"}},
		},
		"_meta": map[string]any{"trace": "p"},
	})
	h, target := modernClient(t, srv.URL)
	target.OriginalCapabilityName = "backend_prompt"

	res, err := h.GetPrompt(context.Background(), target, "advertised_prompt", map[string]any{"a": "b"})
	require.NoError(t, err)

	assert.Equal(t, "prompts/get", hdr.Get("Mcp-Method"))
	assert.Equal(t, "backend_prompt", hdr.Get("Mcp-Name"))
	assert.Equal(t, "backend_prompt", (*body)["name"])
	assert.Equal(t, "d", res.Description)
	require.Len(t, res.Messages, 1)
	assert.Equal(t, "user", res.Messages[0].Role)
	assert.Equal(t, "hi", res.Messages[0].Content.Text)
	assert.Equal(t, "p", res.Meta["trace"])
}

// TestModernComplete verifies completion/complete shaping (NOT name-required, so
// no Mcp-Name) and value decode, plus the -32601 -> empty leniency.
func TestModernComplete(t *testing.T) {
	t.Parallel()

	t.Run("returns values, no Mcp-Name", func(t *testing.T) {
		t.Parallel()
		srv, hdr, _ := bodyRecordingServer(t, map[string]any{
			"completion": map[string]any{"values": []any{"a", "b"}, "total": 2, "hasMore": false},
		})
		h, target := modernClient(t, srv.URL)

		res, err := h.Complete(context.Background(), target,
			vmcp.CompletionRef{Type: vmcp.CompletionRefTypeResource, URI: "file:///x"}, "arg", "va", nil)
		require.NoError(t, err)
		assert.Equal(t, "completion/complete", hdr.Get("Mcp-Method"))
		assert.Empty(t, hdr.Get("Mcp-Name"), "completion/complete is not name-required")
		assert.Equal(t, []string{"a", "b"}, res.Values)
	})

	t.Run("method not found yields empty result", func(t *testing.T) {
		t.Parallel()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"error":{"code":-32601,"message":"method not found"}}`))
		}))
		t.Cleanup(srv.Close)
		h, target := modernClient(t, srv.URL)

		res, err := h.Complete(context.Background(), target,
			vmcp.CompletionRef{Type: vmcp.CompletionRefTypeResource, URI: "file:///x"}, "arg", "", nil)
		require.NoError(t, err)
		assert.Equal(t, []string{}, res.Values, "a backend without completions degrades to empty, not an error")
	})
}

// TestIntegration_ModernCallTool_NameRequired proves a name-required verb
// round-trips end-to-end against the real dispatchModern server: Mcp-Name is
// mirrored in header + body, no Mcp-Session-Id on the wire, and the echo tool
// result comes back.
func TestIntegration_ModernCallTool_NameRequired(t *testing.T) {
	t.Parallel()

	backendURL := startEchoBackend(t)
	vmcpSrv := newModernVMCPServer(t, backendURL)

	h := newProbeClient(t)
	target := &vmcp.BackendTarget{
		WorkloadID:    "vmcp-modern",
		WorkloadName:  "vMCP Modern",
		BaseURL:       vmcpSrv.URL + "/mcp",
		TransportType: "streamable-http",
	}

	res, err := h.CallTool(context.Background(), target, "echo", map[string]any{"input": "hello modern"}, nil)
	require.NoError(t, err)

	rev, ok := h.cachedRevision(target.WorkloadID)
	require.True(t, ok)
	assert.Equal(t, mcpparser.RevisionModern, rev)

	require.Len(t, res.Content, 1)
	assert.Equal(t, "hello modern", res.Content[0].Text)
	assert.False(t, res.IsError)
}

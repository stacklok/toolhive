// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package client

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	mcpserver "github.com/stacklok/toolhive-core/mcpcompat/server"
	"github.com/stacklok/toolhive/pkg/vmcp"
)

// TestListCapabilities_Instructions verifies that a backend's `instructions`
// text reaches CapabilityList.Instructions on both handshake paths (Legacy
// initialize, Modern server/discover) and stays empty when the backend sends
// none.
func TestListCapabilities_Instructions(t *testing.T) {
	t.Parallel()

	t.Run("legacy initialize", func(t *testing.T) {
		t.Parallel()
		backend := newRealEchoServer(t, false, nil,
			mcpserver.WithInstructions("Start with echo. Never call it twice."))
		h := newProbeClient(t)
		target := &vmcp.BackendTarget{
			WorkloadID: "echo", WorkloadName: "echo", BaseURL: backend.URL + "/mcp", TransportType: "streamable-http",
		}

		list, err := h.ListCapabilities(context.Background(), target)
		require.NoError(t, err)
		assert.Equal(t, "Start with echo. Never call it twice.", list.Instructions)
		require.Len(t, list.Tools, 1, "the tool list is unchanged")
	})

	t.Run("legacy initialize without instructions", func(t *testing.T) {
		t.Parallel()
		backend := newRealEchoServer(t, false, nil)
		h := newProbeClient(t)
		target := &vmcp.BackendTarget{
			WorkloadID: "echo", WorkloadName: "echo", BaseURL: backend.URL + "/mcp", TransportType: "streamable-http",
		}

		list, err := h.ListCapabilities(context.Background(), target)
		require.NoError(t, err)
		assert.Empty(t, list.Instructions)
	})

	// modernBackend is a fake Modern backend whose server/discover result
	// carries the given instructions text (none when empty).
	modernBackend := func(t *testing.T, instructions string) *httptest.Server {
		t.Helper()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			method := r.Header.Get("Mcp-Method")
			if method == "" {
				w.WriteHeader(http.StatusNotFound) // no Legacy path on this backend
				return
			}
			id, _ := modernReq(t, r)
			switch method {
			case "server/discover":
				discover := map[string]any{
					"capabilities":      map[string]any{"tools": map[string]any{}},
					"supportedVersions": []string{"2026-07-28"},
				}
				if instructions != "" {
					discover["instructions"] = instructions
				}
				writeModernResult(t, w, id, discover)
			case "tools/list":
				writeModernResult(t, w, id, map[string]any{
					"tools": []any{map[string]any{"name": "echo", "inputSchema": map[string]any{"type": "object"}}},
				})
			case "subscriptions/listen":
				writeModernResult(t, w, id, map[string]any{})
			default:
				w.WriteHeader(http.StatusNotFound)
			}
		}))
		t.Cleanup(srv.Close)
		return srv
	}

	t.Run("modern discover", func(t *testing.T) {
		t.Parallel()
		srv := modernBackend(t, "Modern: call echo once.")
		h := newProbeClient(t)
		target := &vmcp.BackendTarget{
			WorkloadID: "modern", WorkloadName: "modern", BaseURL: srv.URL, TransportType: "streamable-http",
		}

		list, err := h.ListCapabilities(context.Background(), target)
		require.NoError(t, err)
		assert.Equal(t, "Modern: call echo once.", list.Instructions)
		require.Len(t, list.Tools, 1)
	})

	t.Run("modern discover without instructions", func(t *testing.T) {
		t.Parallel()
		srv := modernBackend(t, "")
		h := newProbeClient(t)
		target := &vmcp.BackendTarget{
			WorkloadID: "modern", WorkloadName: "modern", BaseURL: srv.URL, TransportType: "streamable-http",
		}

		list, err := h.ListCapabilities(context.Background(), target)
		require.NoError(t, err)
		assert.Empty(t, list.Instructions)
		require.Len(t, list.Tools, 1)
	})
}

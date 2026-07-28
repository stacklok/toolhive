// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	mcpmcp "github.com/stacklok/toolhive-core/mcpcompat/mcp"
	"github.com/stacklok/toolhive/pkg/vmcp"
)

// modernReq decodes a Modern JSON-RPC request, reading the body exactly once
// (callers must not read r.Body separately) and returning the id and cursor.
func modernReq(t *testing.T, r *http.Request) (id any, cursor string) {
	t.Helper()
	body, err := readAll(t, r)
	require.NoError(t, err)
	var req struct {
		ID     any `json:"id"`
		Params struct {
			Cursor string `json:"cursor"`
		} `json:"params"`
	}
	require.NoError(t, json.Unmarshal(body, &req))
	return req.ID, req.Params.Cursor
}

// writeModernResult writes a Modern JSON-RPC success envelope for the given
// request id, wrapping result under a "complete" resultType.
func writeModernResult(t *testing.T, w http.ResponseWriter, id any, result map[string]any) {
	t.Helper()
	if result["resultType"] == nil {
		result["resultType"] = "complete"
	}
	out, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
	require.NoError(t, err)
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(out)
}

// TestModernEnumerate_Pagination verifies tools/list follows nextCursor across
// pages and aggregates every tool (no page-1 truncation, #5851).
func TestModernEnumerate_Pagination(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "tools/list", r.Header.Get("Mcp-Method"))
		id, cursor := modernReq(t, r)
		if cursor == "" {
			writeModernResult(t, w, id, map[string]any{
				"tools":      []any{map[string]any{"name": "t1", "inputSchema": map[string]any{"type": "object"}}},
				"nextCursor": "page2",
			})
			return
		}
		require.Equal(t, "page2", cursor)
		writeModernResult(t, w, id, map[string]any{
			"tools": []any{map[string]any{"name": "t2", "inputSchema": map[string]any{"type": "object"}}},
		})
	}))
	t.Cleanup(srv.Close)

	h := newProbeClient(t)
	target := &vmcp.BackendTarget{WorkloadID: "b", BaseURL: srv.URL, TransportType: "streamable-http"}
	caps := &mcpmcp.ServerCapabilities{Tools: &struct {
		ListChanged bool `json:"listChanged,omitempty"`
	}{}}

	list, err := h.modernEnumerate(context.Background(), target, caps)
	require.NoError(t, err)
	require.Len(t, list.Tools, 2, "both pages must aggregate")
	assert.Equal(t, "t1", list.Tools[0].Name)
	assert.Equal(t, "t2", list.Tools[1].Name)
	assert.Equal(t, "b", list.Tools[0].BackendID)
}

// TestModernEnumerate_GatesOnFlags verifies only advertised capabilities are
// enumerated: a backend advertising only Tools is never asked for resources or
// prompts.
func TestModernEnumerate_GatesOnFlags(t *testing.T) {
	t.Parallel()

	var toolsList, otherList atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Mcp-Method") == "tools/list" {
			toolsList.Add(1)
		} else {
			otherList.Add(1)
		}
		id, _ := modernReq(t, r)
		writeModernResult(t, w, id, map[string]any{"tools": []any{}})
	}))
	t.Cleanup(srv.Close)

	h := newProbeClient(t)
	target := &vmcp.BackendTarget{WorkloadID: "b", BaseURL: srv.URL, TransportType: "streamable-http"}
	caps := &mcpmcp.ServerCapabilities{Tools: &struct {
		ListChanged bool `json:"listChanged,omitempty"`
	}{}}

	_, err := h.modernEnumerate(context.Background(), target, caps)
	require.NoError(t, err)

	assert.Equal(t, int32(1), toolsList.Load(), "tools advertised: listed once")
	assert.Zero(t, otherList.Load(), "resources/prompts not advertised: must not be listed")
}

// TestModernEnumerate_NilCapsEmpty verifies nil caps (a -3202x backend) yields
// an empty list with no list calls at all.
func TestModernEnumerate_NilCapsEmpty(t *testing.T) {
	t.Parallel()

	var called atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called.Store(true)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)

	h := newProbeClient(t)
	target := &vmcp.BackendTarget{WorkloadID: "b", BaseURL: srv.URL, TransportType: "streamable-http"}

	list, err := h.modernEnumerate(context.Background(), target, nil)
	require.NoError(t, err)
	assert.Empty(t, list.Tools)
	assert.Empty(t, list.Resources)
	assert.Empty(t, list.Prompts)
	assert.False(t, called.Load(), "nil caps must not issue any list request")
}

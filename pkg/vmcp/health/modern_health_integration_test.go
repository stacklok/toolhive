// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package health

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/vmcp"
	vmcpauth "github.com/stacklok/toolhive/pkg/vmcp/auth"
	"github.com/stacklok/toolhive/pkg/vmcp/auth/strategies"
	authtypes "github.com/stacklok/toolhive/pkg/vmcp/auth/types"
	"github.com/stacklok/toolhive/pkg/vmcp/client"
)

// modernRequestID decodes the JSON-RPC request id from a Modern request body,
// reading the body exactly once.
func modernRequestID(t *testing.T, r *http.Request) any {
	t.Helper()
	body, err := io.ReadAll(r.Body)
	require.NoError(t, err)
	var req struct {
		ID any `json:"id"`
	}
	require.NoError(t, json.Unmarshal(body, &req))
	return req.ID
}

// writeModernEnvelope writes a Modern JSON-RPC success envelope echoing the
// request id and wrapping result under a "complete" resultType.
func writeModernEnvelope(t *testing.T, w http.ResponseWriter, id any, result map[string]any) {
	t.Helper()
	if result["resultType"] == nil {
		result["resultType"] = "complete"
	}
	out, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
	require.NoError(t, err)
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(out)
}

// writeModernDiscover writes a server/discover envelope advertising
// 2026-07-28 (MCPVersionModern) with tools, resources, and prompts
// capabilities so dispatch classifies the backend Modern and enumerates all
// three optional lists.
func writeModernDiscover(t *testing.T, w http.ResponseWriter, id any) {
	t.Helper()
	writeModernEnvelope(t, w, id, map[string]any{
		"capabilities": map[string]any{
			"tools":     map[string]any{},
			"resources": map[string]any{},
			"prompts":   map[string]any{},
		},
		"supportedVersions": []string{"2026-07-28", "2025-11-25"},
	})
}

// TestCheckHealth_ModernBackendTransientTemplatesStaysHealthy reproduces issue
// #6347 end to end: a real HTTP backend client probing a Modern backend whose
// resources/templates/list answers HTTP 429 must still report the backend
// healthy. Before the fix, modernEnumerate classified the transient 429 as
// ErrBackendUnavailable, failing ListCapabilities and flipping the aggregate
// Ready=false.
func TestCheckHealth_ModernBackendTransientTemplatesStaysHealthy(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := modernRequestID(t, r)
		switch r.Header.Get("Mcp-Method") {
		case "server/discover":
			writeModernDiscover(t, w, id)
		case "tools/list":
			writeModernEnvelope(t, w, id, map[string]any{
				"tools": []any{map[string]any{"name": "t1", "inputSchema": map[string]any{"type": "object"}}},
			})
		case "resources/list":
			writeModernEnvelope(t, w, id, map[string]any{
				"resources": []any{map[string]any{"name": "r1", "uri": "file:///r1"}},
			})
		case "resources/templates/list":
			w.WriteHeader(http.StatusTooManyRequests)
		case "prompts/list":
			writeModernEnvelope(t, w, id, map[string]any{
				"prompts": []any{map[string]any{"name": "p1"}},
			})
		default:
			t.Fatalf("unexpected method %q", r.Header.Get("Mcp-Method"))
		}
	}))
	t.Cleanup(srv.Close)

	reg := vmcpauth.NewDefaultOutgoingAuthRegistry()
	require.NoError(t, reg.RegisterStrategy(authtypes.StrategyTypeUnauthenticated, &strategies.UnauthenticatedStrategy{}))
	backendClient, err := client.NewHTTPBackendClient(reg)
	require.NoError(t, err)

	checker := NewHealthChecker(backendClient, 5*time.Second, 0)
	target := &vmcp.BackendTarget{
		WorkloadID:    "modern-backend",
		WorkloadName:  "Modern Backend",
		BaseURL:       srv.URL,
		TransportType: "streamable-http",
	}

	status, err := checker.CheckHealth(context.Background(), target)
	require.NoError(t, err, "a transient resources/templates/list must not fail the health check")
	assert.Equal(t, vmcp.BackendHealthy, status)
}

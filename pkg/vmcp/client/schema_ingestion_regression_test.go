// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package client

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

	"github.com/stacklok/toolhive-core/mcpcompat/client"
	mcptransport "github.com/stacklok/toolhive-core/mcpcompat/client/transport"
	"github.com/stacklok/toolhive/pkg/vmcp"
)

// TestRegression_ToolSchemaFidelity_PreservesCompositors pins that the
// ListCapabilities ingestion path preserves top-level JSON Schema compositor
// keywords (e.g. "oneOf") on a tool's input schema, and does not fabricate a
// spurious "type" field for a schema that has none at the top level (e.g. a
// oneOf-only schema).
//
// It is currently blocked: toolhive-core's mcp.ToolArgumentsSchema (backing
// mcp.Tool.InputSchema's UnmarshalJSON) only captures $defs/type/properties/
// required/additionalProperties. A raw "oneOf" in the wire JSON is silently
// dropped during the SDK's own JSON->mcp.Tool unmarshal — before
// conversion.ConvertToolInputSchema ever sees it — and MarshalJSON
// unconditionally re-emits "type": tas.Type, fabricating "type":"" for a tool
// whose schema legitimately omits a top-level type. See #5976 (toolhive-core
// Tool.UnmarshalJSON) for the upstream fix; unskip this test in the
// toolhive-core bump PR that lands it.
func TestRegression_ToolSchemaFidelity_PreservesCompositors(t *testing.T) {
	t.Parallel()
	t.Skip("blocked on #5976 (toolhive-core Tool.UnmarshalJSON); unskip in the toolhive-core bump PR")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		body, err := io.ReadAll(r.Body)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		var req jsonRPCRequest
		if err := json.Unmarshal(body, &req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		switch req.Method {
		case "initialize":
			resp := jsonRPCResponse{
				JSONRPC: "2.0",
				ID:      req.ID,
				Result: json.RawMessage(`{
					"protocolVersion": "2024-11-05",
					"capabilities": {"tools": {}},
					"serverInfo": {"name": "schema-fidelity-backend", "version": "1.0.0"}
				}`),
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(resp)

		case "tools/list":
			// tool-one has a top-level "oneOf" compositor and no top-level "type".
			// tool-two has only properties/required, also no top-level "type".
			resp := jsonRPCResponse{
				JSONRPC: "2.0",
				ID:      req.ID,
				Result: json.RawMessage(`{
					"tools": [
						{
							"name": "tool-one",
							"description": "a tool whose schema is a oneOf of two shapes",
							"inputSchema": {
								"oneOf": [
									{"type": "object", "properties": {"a": {"type": "string"}}, "required": ["a"]},
									{"type": "object", "properties": {"b": {"type": "string"}}, "required": ["b"]}
								]
							}
						},
						{
							"name": "tool-two",
							"description": "a tool whose schema has properties/required but no top-level type",
							"inputSchema": {
								"properties": {"c": {"type": "string"}},
								"required": ["c"]
							}
						}
					]
				}`),
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(resp)

		default:
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(jsonRPCResponse{
				JSONRPC: "2.0",
				ID:      req.ID,
				Result:  json.RawMessage(`{}`),
			})
		}
	}))
	t.Cleanup(srv.Close)

	h := &httpBackendClient{
		clientFactory: func(ctx context.Context, target *vmcp.BackendTarget, _ bool) (*client.Client, error) {
			c, err := client.NewStreamableHttpClient(
				target.BaseURL,
				mcptransport.WithHTTPTimeout(30*time.Second),
			)
			if err != nil {
				return nil, err
			}
			if err := c.Start(ctx); err != nil {
				return nil, err
			}
			return c, nil
		},
	}

	target := &vmcp.BackendTarget{
		WorkloadID:    "schema-fidelity-backend",
		WorkloadName:  "Schema Fidelity Backend",
		BaseURL:       srv.URL,
		TransportType: "streamable-http",
	}

	caps, err := h.ListCapabilities(context.Background(), target)
	require.NoError(t, err)
	require.Len(t, caps.Tools, 2)

	byName := make(map[string]vmcp.Tool, len(caps.Tools))
	for _, tool := range caps.Tools {
		byName[tool.Name] = tool
	}

	toolOne, ok := byName["tool-one"]
	require.True(t, ok, "tool-one must be present")
	assert.Contains(t, toolOne.InputSchema, "oneOf",
		"tool-one's projected InputSchema must still contain the oneOf compositor")
	assertNoFabricatedEmptyType(t, toolOne.InputSchema, "tool-one")

	toolTwo, ok := byName["tool-two"]
	require.True(t, ok, "tool-two must be present")
	assertNoFabricatedEmptyType(t, toolTwo.InputSchema, "tool-two")
}

// assertNoFabricatedEmptyType asserts that schema does not contain a "type"
// key with an empty string value — the fabrication produced by
// ToolArgumentsSchema.MarshalJSON unconditionally re-emitting "type": tas.Type
// for a schema that never had a top-level type in the original wire JSON.
func assertNoFabricatedEmptyType(t *testing.T, schema map[string]any, toolName string) {
	t.Helper()
	if typ, ok := schema["type"]; ok {
		assert.NotEqual(t, "", typ,
			"%s's projected InputSchema must not fabricate an empty top-level \"type\"", toolName)
	}
}

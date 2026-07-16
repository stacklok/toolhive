// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/vmcp"
)

// TestRegression_ToolSchemaWithTypeObject_ProjectedIntact guards schema
// fidelity for the SUPPORTED case: a tool whose InputSchema includes a
// top-level "type":"object" is projected to the client UNCHANGED in tools/list,
// with its properties and required array preserved. The Serve path
// (coreSessionTools in serve_handlers.go) marshals the core's InputSchema into
// RawInputSchema; the go-sdk bridge's normalizeObjectSchema passes object-typed
// schemas through verbatim, so properties/required survive.
//
// All assertions use encoding/json and generic maps — not SDK types — so the
// check is against the wire representation the client sees.
func TestRegression_ToolSchemaWithTypeObject_ProjectedIntact(t *testing.T) {
	t.Parallel()

	originalSchema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"x": map[string]any{"type": "string"},
		},
		"required": []any{"x"},
	}
	fc := &fakeCore{tools: []vmcp.Tool{{
		Name:        "schema-tool",
		Description: "a schema-fidelity test tool",
		InputSchema: originalSchema,
	}}}
	_, sessionID, baseURL := registerServeSession(t, fc)

	resp := postServeMCP(t, baseURL, map[string]any{
		"jsonrpc": "2.0",
		"id":      2,
		"method":  "tools/list",
		"params":  map[string]any{},
	}, sessionID)
	defer resp.Body.Close()
	require.Equal(t, 200, resp.StatusCode, "tools/list should succeed")

	env, body := readServeJSONRPC(t, resp)
	result, ok := env["result"].(map[string]any)
	require.True(t, ok, "tools/list must have a result; body: %s", string(body))

	tools, ok := result["tools"].([]any)
	require.True(t, ok, "result.tools must be an array; result: %v", result)

	// Find the projected tool by name.
	var found map[string]any
	for _, tRaw := range tools {
		tm, ok := tRaw.(map[string]any)
		if !ok {
			continue
		}
		if tm["name"] == "schema-tool" {
			found = tm
			break
		}
	}
	require.NotNil(t, found, "schema-tool must be present in tools/list; tools: %v", tools)

	inputSchema, ok := found["inputSchema"].(map[string]any)
	require.True(t, ok, "inputSchema must be an object; tool: %v", found)

	// The projected schema must equal the original map exactly: properties and
	// required preserved, type unchanged.
	assert.Equal(t, originalSchema, inputSchema,
		"an object-typed inputSchema must be projected intact; got %v", inputSchema)
	assert.Equal(t, "object", inputSchema["type"],
		"the top-level type must remain \"object\"; got %v", inputSchema)
}

// TestRegression_ToolSchemaWithoutTypeObject_ProjectedIntact guards the go-sdk
// bridge's handling of a schema that omits the top-level "type":"object".
// Earlier mcpcompat releases dropped properties/required for such schemas (a
// fidelity gap versus mcp-go); as of toolhive-core v0.0.28 normalizeObjectSchema
// preserves them and supplies the missing "type":"object". This test now pins
// that intact projection so any future regression is a deliberate, visible flip.
func TestRegression_ToolSchemaWithoutTypeObject_ProjectedIntact(t *testing.T) {
	t.Parallel()

	originalSchema := map[string]any{
		"properties": map[string]any{
			"x": map[string]any{"type": "string"},
		},
		"required": []any{"x"},
		// NOTE: deliberately NO top-level "type": "object".
	}
	fc := &fakeCore{tools: []vmcp.Tool{{
		Name:        "schema-tool",
		Description: "a schema-fidelity test tool",
		InputSchema: originalSchema,
	}}}
	_, sessionID, baseURL := registerServeSession(t, fc)

	resp := postServeMCP(t, baseURL, map[string]any{
		"jsonrpc": "2.0",
		"id":      2,
		"method":  "tools/list",
		"params":  map[string]any{},
	}, sessionID)
	defer resp.Body.Close()
	require.Equal(t, 200, resp.StatusCode, "tools/list should succeed")

	env, body := readServeJSONRPC(t, resp)
	result, ok := env["result"].(map[string]any)
	require.True(t, ok, "tools/list must have a result; body: %s", string(body))

	tools, ok := result["tools"].([]any)
	require.True(t, ok, "result.tools must be an array; result: %v", result)

	var found map[string]any
	for _, tRaw := range tools {
		tm, ok := tRaw.(map[string]any)
		if !ok {
			continue
		}
		if tm["name"] == "schema-tool" {
			found = tm
			break
		}
	}
	require.NotNil(t, found, "schema-tool must be present in tools/list; tools: %v", tools)

	inputSchema, ok := found["inputSchema"].(map[string]any)
	require.True(t, ok, "inputSchema must be an object; tool: %v", found)

	// The schema is projected intact: properties and required are preserved and
	// the missing top-level "type":"object" is supplied (toolhive-core v0.0.28).
	assert.Equal(t, "object", inputSchema["type"],
		"a missing top-level type must be supplied as \"object\"; got %v", inputSchema)
	assert.Equal(t, originalSchema["properties"], inputSchema["properties"],
		"properties must be preserved; got %v", inputSchema)
	assert.Equal(t, originalSchema["required"], inputSchema["required"],
		"required must be preserved; got %v", inputSchema)
}

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

// TestRegression_ToolSchemaWithoutTypeObject_NormalizedToEmptyObject pins the
// go-sdk bridge's normalization of a schema that omits the top-level
// "type":"object". normalizeObjectSchema (in the mcpcompat server bridge)
// REPLACES any non-object-typed schema with {"type":"object"}, dropping
// properties/required. This is a known schema-fidelity gap versus mcp-go (which
// projected such schemas verbatim).
//
// This test pins the CURRENT behavior so a future fix to normalizeObjectSchema
// (e.g. preserving properties when type is absent) is a deliberate, visible
// flip. When that fix lands, replace this test's assertions with an
// integrity check mirroring TestRegression_ToolSchemaWithTypeObject_ProjectedIntact.
func TestRegression_ToolSchemaWithoutTypeObject_NormalizedToEmptyObject(t *testing.T) {
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

	// Pin the current (lossy) normalization: a schema without a top-level
	// "type":"object" is replaced with the empty object schema, dropping
	// properties and required. This documents the fidelity gap.
	assert.Equal(t, map[string]any{"type": "object"}, inputSchema,
		"a non-object-typed schema is normalized to {\"type\":\"object\"} (fidelity gap); got %v", inputSchema)
	assert.NotContains(t, inputSchema, "properties",
		"properties are dropped by the normalization; got %v", inputSchema)
	assert.NotContains(t, inputSchema, "required",
		"required is dropped by the normalization; got %v", inputSchema)
}

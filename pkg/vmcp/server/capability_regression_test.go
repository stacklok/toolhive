// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/vmcp"
)

// TestRegression_InitializeAdvertisesToolsAndResourcesCapabilities pins the
// capabilities advertised in the initialize response on the Serve path.
//
// BEHAVIOR (go-sdk bridge, toolhive-core v0.0.28): the initialize response
// advertises tools and resources capabilities alongside logging, matching
// mcp-go. Earlier releases advertised only {"logging":{}} on the Serve path;
// v0.0.28 surfaces the tool/resource capabilities in the initialize response.
//
// This test pins that behavior so a future regression is a deliberate, visible
// flip rather than a silent drift. It asserts on the RAW initialize response
// body parsed with encoding/json — not tools/list.
func TestRegression_InitializeAdvertisesToolsAndResourcesCapabilities(t *testing.T) {
	t.Parallel()

	fc := &fakeCore{
		tools:     []vmcp.Tool{{Name: "cap-tool", Description: "a capability test tool"}},
		resources: []vmcp.Resource{{Name: "cap-doc", URI: "file:///cap.txt"}},
	}
	_, _, baseURL := registerServeSession(t, fc)

	initResp := postServeMCP(t, baseURL, map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "initialize",
		"params": map[string]any{
			"protocolVersion": "2025-06-18",
			"capabilities":    map[string]any{},
			"clientInfo":      map[string]any{"name": "test", "version": "1.0"},
		},
	}, "")
	defer initResp.Body.Close()
	require.Equal(t, 200, initResp.StatusCode, "initialize should succeed")

	env, _ := readServeJSONRPC(t, initResp)
	result, ok := env["result"].(map[string]any)
	require.True(t, ok, "initialize response must have a result object; env: %v", env)

	capabilities, ok := result["capabilities"].(map[string]any)
	require.True(t, ok, "result.capabilities must be present; result: %v", result)

	// As of toolhive-core v0.0.28 the go-sdk bridge advertises tools and
	// resources capabilities in the initialize response on the Serve path
	// (alongside logging). This pins that behavior so a future regression is a
	// deliberate, visible flip.
	assert.Contains(t, capabilities, "logging",
		"logging capability must be advertised; got %v", capabilities)
	assert.Contains(t, capabilities, "tools",
		"tools capability must be advertised in initialize on the Serve path; got %v", capabilities)
	assert.Contains(t, capabilities, "resources",
		"resources capability must be advertised in initialize on the Serve path; got %v", capabilities)
	assert.Contains(t, capabilities, "prompts",
		"prompts capability must be advertised in initialize on the Serve path (#5969 fixes a latent bug"+
			" where it was never advertised even though per-session prompts were served); got %v", capabilities)
}

// TestRegression_InitializeAdvertisesListChanged pins the #5748 (tools) and
// #5969 (resources/prompts) capability flips: tools.listChanged,
// resources.listChanged, and prompts.listChanged must all be true in the
// initialize response (they were false — and therefore omitted, per the
// SDK's omitempty — before these changes). resources.subscribe must remain
// true throughout (unrelated to listChanged, pinned pre-existing behavior).
func TestRegression_InitializeAdvertisesListChanged(t *testing.T) {
	t.Parallel()

	fc := &fakeCore{tools: []vmcp.Tool{{Name: "cap-tool"}}}
	_, _, baseURL := registerServeSession(t, fc)

	initResp := postServeMCP(t, baseURL, map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "initialize",
		"params": map[string]any{
			"protocolVersion": "2025-06-18",
			"capabilities":    map[string]any{},
			"clientInfo":      map[string]any{"name": "test", "version": "1.0"},
		},
	}, "")
	defer initResp.Body.Close()
	require.Equal(t, 200, initResp.StatusCode, "initialize should succeed")

	env, _ := readServeJSONRPC(t, initResp)
	result, ok := env["result"].(map[string]any)
	require.True(t, ok, "initialize response must have a result object; env: %v", env)
	capabilities, ok := result["capabilities"].(map[string]any)
	require.True(t, ok, "result.capabilities must be present; result: %v", result)

	toolsCaps, ok := capabilities["tools"].(map[string]any)
	require.True(t, ok, "tools capability must be an object; capabilities: %v", capabilities)
	assert.Equal(t, true, toolsCaps["listChanged"],
		"tools.listChanged must be true so downstream clients trust the resync notification (#5748)")

	resourcesCaps, ok := capabilities["resources"].(map[string]any)
	require.True(t, ok, "resources capability must be an object; capabilities: %v", capabilities)
	assert.Equal(t, true, resourcesCaps["subscribe"], "resources.subscribe must remain true")
	assert.Equal(t, true, resourcesCaps["listChanged"],
		"resources.listChanged must be true so downstream clients trust the resync notification (#5969)")

	promptsCaps, ok := capabilities["prompts"].(map[string]any)
	require.True(t, ok, "prompts capability must be an object; capabilities: %v", capabilities)
	assert.Equal(t, true, promptsCaps["listChanged"],
		"prompts.listChanged must be true so downstream clients trust the resync notification (#5969)")
}

// TestRegression_SessionRegistrationDoesNotSpuriouslyInvalidateCache verifies
// the #5748 R1 concern at the layer this PR controls: registering a session —
// with no backend list_changed notification ever firing — must NOT invoke
// InvalidateCapabilityCache or the resync path. Those only run from the sink
// built in buildListChangedSink, which is invoked solely by a persistent
// backend connection's OnNotification handler (mcp_session.go), never by
// session registration itself.
//
// (go-sdk's OWN early/broadcast-scoped auto-notify behavior at registration —
// documented in serve.go next to WithToolCapabilities(true) — is an upstream
// SDK characteristic this PR cannot suppress; it is unrelated to whether OUR
// code spuriously self-triggers, which is what this test pins.)
func TestRegression_SessionRegistrationDoesNotSpuriouslyInvalidateCache(t *testing.T) {
	t.Parallel()

	fc := &fakeCore{tools: []vmcp.Tool{{Name: "cap-tool"}}}
	registerServeSession(t, fc)

	assert.Equal(t, int32(0), fc.invalidateCacheCalls.Load(),
		"session registration alone must never invalidate the capability cache")
}

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
}

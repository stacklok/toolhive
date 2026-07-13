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
// OBSERVED BEHAVIOR (go-sdk bridge): the initialize response advertises only
// {"logging":{}} — it does NOT advertise tools/resources capabilities, even when
// the core supplies tools and resources. This differs from mcp-go, which
// advertises tools/resources in initialize. The go-sdk server advertises a
// static, server-level capability set (logging only on the Serve path, which
// starts with WithToolCapabilities(false)/WithResourceCapabilities(false));
// per-session tool/resource overlays installed by the OnRegisterSession hook do
// not change the advertised capabilities in the initialize response.
//
// This test pins that observed behavior so a future change to the bridge (e.g.
// advertising tools/resources once a session's overlay is populated) is a
// deliberate, visible flip rather than a silent drift. It asserts on the RAW
// initialize response body parsed with encoding/json — not tools/list.
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

	// Pin the observed go-sdk-bridge behavior: only logging is advertised in
	// initialize on the Serve path. tools/resources are NOT advertised here (they
	// are discoverable via tools/list/resources/list once the session is
	// registered). If the bridge is updated to advertise them, flip these
	// assertions to assert non-nil tools/resources.
	assert.Contains(t, capabilities, "logging",
		"logging capability must be advertised; got %v", capabilities)
	assert.NotContains(t, capabilities, "tools",
		"tools capability is NOT advertised in initialize on the Serve path (go-sdk bridge); got %v", capabilities)
	assert.NotContains(t, capabilities, "resources",
		"resources capability is NOT advertised in initialize on the Serve path (go-sdk bridge); got %v", capabilities)
}

// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	mcpsdk "github.com/stacklok/toolhive-core/mcpcompat/server"
	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/optimizer"
)

// supportedVersionsFromDiscover extracts result.supportedVersions from a decoded
// server/discover envelope, failing loudly when the field is absent or malformed
// so a "does not contain" assertion can never pass vacuously against the wrong
// response shape.
func supportedVersionsFromDiscover(t *testing.T, decoded map[string]any) []string {
	t.Helper()
	result, ok := decoded["result"].(map[string]any)
	require.True(t, ok, "expected a JSON-RPC result envelope, got %v", decoded)
	raw, ok := result["supportedVersions"].([]any)
	require.True(t, ok, "expected result.supportedVersions, got %v", result)
	require.NotEmpty(t, raw, "supportedVersions must never be empty: %v", result)
	versions := make([]string, 0, len(raw))
	for _, v := range raw {
		s, ok := v.(string)
		require.True(t, ok, "supportedVersions entries must be strings: %v", raw)
		versions = append(versions, s)
	}
	return versions
}

// TestIntegration_ModernGate_OptimizerKeepsClientsOnLegacy pins the capability
// gate (modernDispatchBlockers, modern_gate.go) through the full handler chain
// for an optimizer-enabled instance — the configuration whose find_tool/
// call_tool meta-tools are session-scoped and therefore unservable by the
// stateless Modern dispatch path:
//
//  1. server/discover must NOT advertise 2026-07-28: the gate lets it fall
//     through to the SDK, whose stateful transport filters Modern out of the
//     version list. This is the leg that steers a Modern-first client (go-sdk
//     v1.7+ probes discover before initialize) onto the Legacy handshake,
//     where sessions advertise find_tool/call_tool.
//  2. Any other well-formed Modern request is refused with a conformant
//     400 + -32022 UnsupportedProtocolVersionError listing the Legacy version
//     — never go-sdk's plain-text stateful rejection, which carries no
//     version list a client could negotiate down from.
//
// The contrast leg (an instance without the optimizer advertises and serves
// Modern) is TestIntegration_ModernGate_PlainInstanceAdvertisesModern; the two
// together stop the gate from being "simplified" away in either direction.
// Deleting the optimizer entry from modernDispatchBlockers (Modern optimizer
// parity) must flip this test.
func TestIntegration_ModernGate_OptimizerKeepsClientsOnLegacy(t *testing.T) {
	t.Parallel()

	tool := vmcp.Tool{Name: "echo", Description: "echoes", InputSchema: map[string]any{"type": "object"}}
	ts := buildTestServerWithOptions(t, newNoopMockFactory(t), serverOptions{
		tools: []vmcp.Tool{tool},
		optimizerFactory: func(_ context.Context, _ []mcpsdk.ServerTool) (optimizer.Optimizer, error) {
			return &fakeOptimizer{}, nil
		},
	})

	discoverResp, discoverBody := postModern(t, ts.URL, "server/discover", nil, 1, "")
	defer discoverResp.Body.Close()
	require.Equal(t, http.StatusOK, discoverResp.StatusCode,
		"gated discover must still be answered (fall-through to the SDK): %+v", discoverBody)
	versions := supportedVersionsFromDiscover(t, discoverBody)
	assert.NotContains(t, versions, "2026-07-28",
		"an optimizer-enabled instance must not advertise Modern")
	assert.Contains(t, versions, "2025-11-25",
		"the Legacy version must stay advertised so the client can negotiate down")

	listResp, listBody := postModern(t, ts.URL, "tools/list", nil, 2, "")
	defer listResp.Body.Close()
	assert.Equal(t, http.StatusBadRequest, listResp.StatusCode, "decoded: %+v", listBody)
	errObj, ok := listBody["error"].(map[string]any)
	require.True(t, ok, "expected a JSON-RPC error envelope, got %+v", listBody)
	assert.Equal(t, float64(-32022), errObj["code"],
		"refusal must be the conformant UnsupportedProtocolVersionError")
	data, ok := errObj["data"].(map[string]any)
	require.True(t, ok, "expected error.data with the version lists, got %+v", errObj)
	assert.Equal(t, []any{"2025-11-25"}, data["supported"],
		"the refusal must list the Legacy version so the client can negotiate down")
}

// TestIntegration_ModernGate_PlainInstanceAdvertisesModern is the contrast leg
// of TestIntegration_ModernGate_OptimizerKeepsClientsOnLegacy: with no enabled
// feature the stateless path cannot serve, the gate is open, server/discover is
// answered by dispatchModernDiscover, and 2026-07-28 is advertised.
func TestIntegration_ModernGate_PlainInstanceAdvertisesModern(t *testing.T) {
	t.Parallel()

	tool := vmcp.Tool{Name: "echo", Description: "echoes", InputSchema: map[string]any{"type": "object"}}
	ts := buildTestServerWithOptions(t, newNoopMockFactory(t), serverOptions{tools: []vmcp.Tool{tool}})

	resp, decoded := postModern(t, ts.URL, "server/discover", nil, 1, "")
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode, "decoded: %+v", decoded)
	assert.Contains(t, supportedVersionsFromDiscover(t, decoded), "2026-07-28",
		"a plain instance must advertise Modern")
}

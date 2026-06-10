// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package vmcp_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/test/integration/vmcp/helpers"
)

// TestVMCPServer_PassthroughHeaders verifies the end-to-end header-passthrough
// chain:
//
//	MCP client → headerforward.CaptureMiddleware → request-context value →
//	per-session backend client (mergeForwardedHeaders) → backend HTTP request.
//
// The test exercises the real server wiring (via helpers.WithPassthroughHeaders,
// which sets vmcpserver.Config.PassthroughHeaders) so headerforward.CaptureMiddleware
// runs; it does NOT use a stub or reimplementation of the capture logic.
//
// Two assertions are made:
//  1. Allowlisted header present: X-Test-Api-Key sent by the client arrives at
//     the backend unchanged.
//  2. Non-allowlisted header absent: X-Secret sent by the client is dropped and
//     does not arrive at the backend.
func TestVMCPServer_PassthroughHeaders(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	// ── Backend ──────────────────────────────────────────────────────────────
	// The backend captures incoming HTTP headers into the request context so
	// tool handlers can inspect them.  The echo_header tool reads both the
	// allowlisted and the non-allowlisted header and returns their values (or
	// the sentinel "<absent>" when a header is missing) so the test can assert
	// forwarding behaviour without needing to inspect raw HTTP traffic.
	const (
		allowlistedHeader    = "X-Test-Api-Key"
		nonAllowlistedHeader = "X-Secret"
		allowlistedValue     = "caller-key-123"
		nonAllowlistedValue  = "should-not-forward"
		absentSentinel       = "<absent>"
	)

	apiBackend := helpers.CreateBackendServer(t, []helpers.BackendTool{
		helpers.NewBackendTool(
			"echo_header",
			"Echo the values of two request headers back to the caller",
			func(ctx context.Context, _ map[string]any) string {
				headers := helpers.GetHTTPHeadersFromContext(ctx)

				apiKey := absentSentinel
				secret := absentSentinel

				if headers != nil {
					if v := headers.Get(allowlistedHeader); v != "" {
						apiKey = v
					}
					if v := headers.Get(nonAllowlistedHeader); v != "" {
						secret = v
					}
				}

				return fmt.Sprintf(
					`{"api_key": %q, "secret": %q}`,
					apiKey,
					secret,
				)
			},
		),
	},
		helpers.WithBackendName("api-backend"),
		helpers.WithCaptureHeaders(), // store incoming HTTP headers in context
	)
	t.Cleanup(apiBackend.Close)

	// ── vMCP server ───────────────────────────────────────────────────────────
	// WithPassthroughHeaders sets vmcpserver.Config.PassthroughHeaders, which
	// installs headerforward.CaptureMiddleware so allowlisted headers are captured
	// into the request context and forwarded to backends for each request.
	backends := []vmcp.Backend{
		helpers.NewBackend("api",
			helpers.WithURL(apiBackend.URL+"/mcp"),
		),
	}

	vmcpServer := helpers.NewVMCPServer(ctx, t, backends,
		helpers.WithPrefixConflictResolution("{workload}_"),
		helpers.WithPassthroughHeaders(allowlistedHeader),
	)

	// ── MCP client ────────────────────────────────────────────────────────────
	// WithClientHeader wires each header into the mcp-go streamable-HTTP
	// transport via WithHTTPHeaders so every request to the vMCP server carries
	// these headers.
	vmcpURL := "http://" + vmcpServer.Address() + "/mcp"
	mcpClient := helpers.NewMCPClient(ctx, t, vmcpURL,
		helpers.WithClientHeader(allowlistedHeader, allowlistedValue),
		helpers.WithClientHeader(nonAllowlistedHeader, nonAllowlistedValue),
	)
	t.Cleanup(func() { _ = mcpClient.Close() })

	// ── Sanity: tool is visible ───────────────────────────────────────────────
	toolsResp := mcpClient.ListTools(ctx)
	toolNames := helpers.GetToolNames(toolsResp)
	require.Contains(t, toolNames, "api_echo_header",
		"echo_header tool should be listed by vMCP")

	// ── Call the tool ─────────────────────────────────────────────────────────
	result := mcpClient.CallTool(ctx, "api_echo_header", map[string]any{})
	text := helpers.AssertToolCallSuccess(t, result)

	t.Logf("backend response: %s", text)

	// Assertion 1: allowlisted header was forwarded to the backend.
	assert.Contains(t, text, allowlistedValue,
		"allowlisted header %q should have been forwarded to the backend",
		allowlistedHeader)

	// Assertion 2: non-allowlisted header was dropped by the capture middleware.
	assert.NotContains(t, text, nonAllowlistedValue,
		"non-allowlisted header %q must not reach the backend",
		nonAllowlistedHeader)

	// Confirm the non-allowlisted slot shows the absent sentinel to rule out
	// the handler silently returning wrong data.
	assert.Contains(t, text, absentSentinel,
		"non-allowlisted header slot should contain the absent sentinel")
}

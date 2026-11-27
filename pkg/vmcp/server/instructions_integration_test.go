// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	mcpsdk "github.com/stacklok/toolhive-core/mcpcompat/server"
	"github.com/stacklok/toolhive/pkg/mcp/instructions"
	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/optimizer"
)

// TestIntegration_Instructions verifies that vMCP sets the configured
// instructions on its initialize result and on the server/discover result of
// both the SDK, which answers discover while the Modern gate is closed (here,
// with the optimizer enabled), and the Modern dispatcher. vMCP itself produces
// no instructions, so every mode returns the configured value alone.
func TestIntegration_Instructions(t *testing.T) {
	t.Parallel()

	tool := vmcp.Tool{Name: "echo", Description: "echoes", InputSchema: map[string]any{"type": "object"}}
	withOptimizer := func(_ context.Context, _ []mcpsdk.ServerTool) (optimizer.Optimizer, error) {
		return &fakeOptimizer{}, nil
	}
	initialize := func(t *testing.T, url string) map[string]any {
		t.Helper()
		resp := postMCP(t, url, map[string]any{
			"jsonrpc": "2.0",
			"id":      1,
			"method":  "initialize",
			"params": map[string]any{
				"protocolVersion": "2025-11-25",
				"capabilities":    map[string]any{},
				"clientInfo":      map[string]any{"name": "test", "version": "1.0"},
			},
		}, "")
		t.Cleanup(func() { _ = resp.Body.Close() })
		require.Equal(t, http.StatusOK, resp.StatusCode)
		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		var decoded map[string]any
		require.NoError(t, json.Unmarshal(body, &decoded), "body: %s", body)
		return decoded
	}
	discover := func(t *testing.T, url string) map[string]any {
		t.Helper()
		resp, decoded := postModern(t, url, "server/discover", nil, 1, "")
		t.Cleanup(func() { _ = resp.Body.Close() })
		require.Equal(t, http.StatusOK, resp.StatusCode, "decoded: %+v", decoded)
		return decoded
	}

	tests := []struct {
		name      string
		optimizer func(context.Context, []mcpsdk.ServerTool) (optimizer.Optimizer, error)
		call      func(*testing.T, string) map[string]any
	}{
		{name: "initialize", call: initialize},
		{name: "server/discover from the Modern dispatcher", call: discover},
		{name: "server/discover from the SDK", optimizer: withOptimizer, call: discover},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ts := buildTestServerWithOptions(t, newNoopMockFactory(t), serverOptions{
				tools:            []vmcp.Tool{tool},
				optimizerFactory: tt.optimizer,
				instructions:     &instructions.Config{Mode: instructions.ModeAppend, Value: "policy"},
			})

			decoded := tt.call(t, ts.URL)
			result, ok := decoded["result"].(map[string]any)
			require.True(t, ok, "expected a result, got %+v", decoded)
			assert.Equal(t, "policy", result["instructions"])
		})
	}
}

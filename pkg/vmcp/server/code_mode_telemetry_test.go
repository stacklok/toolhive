// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/telemetry"
	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/aggregator"
	"github.com/stacklok/toolhive/pkg/vmcp/codemode"
	"github.com/stacklok/toolhive/pkg/vmcp/mocks"
	"github.com/stacklok/toolhive/pkg/vmcp/optimizer"
	"github.com/stacklok/toolhive/pkg/vmcp/router"
)

func TestWithCodeModeTelemetry(t *testing.T) {
	t.Parallel()

	cfg := &codemode.Config{StepLimit: 7}

	t.Run("passes through without code mode or telemetry", func(t *testing.T) {
		t.Parallel()
		for _, tc := range []struct {
			cfg      *codemode.Config
			provider *telemetry.Provider
		}{
			{cfg: nil, provider: &telemetry.Provider{}},
			{cfg: cfg, provider: nil},
		} {
			got, err := withCodeModeTelemetry(tc.cfg, tc.provider)
			require.NoError(t, err)
			assert.Same(t, tc.cfg, got)
		}
	})

	t.Run("attaches telemetry on a copy", func(t *testing.T) {
		t.Parallel()
		got, err := withCodeModeTelemetry(cfg, &telemetry.Provider{})
		require.NoError(t, err)
		require.NotSame(t, cfg, got, "the caller's config must not be changed")
		assert.Nil(t, cfg.Telemetry)
		assert.NotNil(t, got.Telemetry)
		assert.Equal(t, uint64(7), got.StepLimit)
	})
}

// TestIntegration_CodeModeTelemetry runs a script through the real HTTP chain
// and checks the code mode metrics on /metrics, including the PromQL traffic
// split documented in docs/operator/virtualmcpserver-observability.md, with
// and without the optimizer.
//
//nolint:paralleltest // telemetry.NewProvider sets global OTel providers
func TestIntegration_CodeModeTelemetry(t *testing.T) {
	for _, tc := range []struct {
		name        string
		codeMode    *codemode.Config
		optimizer   *optimizer.Config
		wantEnabled string
	}{
		{name: "code mode enabled", codeMode: &codemode.Config{}, wantEnabled: "1"},
		{name: "code mode disabled", codeMode: nil, wantEnabled: "0"},
		{name: "code mode behind the optimizer", codeMode: &codemode.Config{}, optimizer: &optimizer.Config{},
			wantEnabled: "1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			metrics := runCodeModeServer(t, tc.codeMode, tc.optimizer)

			assert.Equal(t, tc.wantEnabled,
				metricValue(t, metrics, "toolhive_vmcp_code_mode_enabled", `server="code-mode-vmcp"`))
			if tc.codeMode == nil {
				assert.NotContains(t, metrics, "toolhive_vmcp_script_executions")
				return
			}

			assert.Equal(t, "1", metricValue(t, metrics, "toolhive_vmcp_script_executions_total", `status="success"`))
			assert.Equal(t, "1", metricValue(t, metrics, "toolhive_vmcp_script_duration_seconds_count", `status="success"`))
			assert.Equal(t, "2", metricValue(t, metrics, "toolhive_vmcp_script_tool_calls_sum", `status="success"`))
			assert.Equal(t, "2", metricValue(t, metrics, "toolhive_vmcp_script_parallel_tasks_total", `status="success"`))

			if tc.optimizer == nil {
				// The script's inner call does not pass the HTTP middleware, so the
				// request counter sees execute_tool_script only: that is the split.
				assert.Equal(t, "1", metricValue(t, metrics, "toolhive_mcp_tool_calls_total",
					`tool="execute_tool_script"`, `server="code-mode-vmcp"`))
				assert.NotContains(t, metrics, `tool="search-svc_search"`)
				assert.NotContains(t, metrics, "toolhive_vmcp_optimizer_call_tool_requests")
				return
			}

			// Behind the optimizer the request counter sees only call_tool, so
			// the split comes from the optimizer's call_tool counter, which
			// labels each call with its target. The script's inner calls run
			// below the optimizer and do not reach it.
			assert.Equal(t, "2", metricValue(t, metrics, "toolhive_mcp_tool_calls_total",
				`tool="call_tool"`, `server="code-mode-vmcp"`))
			assert.NotContains(t, metrics, `tool="execute_tool_script"`)
			assert.Equal(t, "1", metricValue(t, metrics, "toolhive_vmcp_optimizer_call_tool_requests_total",
				`tool_name="execute_tool_script"`))
			assert.Equal(t, "1", metricValue(t, metrics, "toolhive_vmcp_optimizer_call_tool_requests_total",
				`tool_name="search-svc_search"`))
		})
	}
}

// runCodeModeServer starts a vMCP server with one backend tool, opens a
// session, runs one execute_tool_script call when code mode is on, and
// returns the scraped /metrics body. With optCfg set, the optimizer is on and
// the script runs through call_tool, followed by one regular call_tool call.
func runCodeModeServer(t *testing.T, codeMode *codemode.Config, optCfg *optimizer.Config) string {
	t.Helper()
	ctx := context.Background()

	provider, err := telemetry.NewProvider(ctx, telemetry.Config{
		ServiceName:                 "vmcp-code-mode-test",
		ServiceVersion:              "1.0.0",
		EnablePrometheusMetricsPath: true,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = provider.Shutdown(ctx) })

	ctrl := gomock.NewController(t)
	backendClient := mocks.NewMockBackendClient(ctrl)
	backendClient.EXPECT().ListCapabilities(gomock.Any(), gomock.Any()).Return(&vmcp.CapabilityList{
		Tools: []vmcp.Tool{{Name: "search", Description: "Search", BackendID: "search-svc",
			InputSchema: map[string]any{"type": "object"}}},
	}, nil).AnyTimes()
	// A fresh result per call: the core writes to it, and parallel() calls run concurrently.
	backendClient.EXPECT().CallTool(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
		DoAndReturn(func(context.Context, *vmcp.BackendTarget, string, map[string]any, map[string]any, map[string]string) (*vmcp.ToolCallResult, error) {
			return &vmcp.ToolCallResult{StructuredContent: map[string]any{"result": "found"}}, nil
		}).AnyTimes()

	tools := []vmcp.Tool{{Name: "search-svc_search", Description: "Search", BackendID: "search-svc"}}
	routing := &vmcp.RoutingTable{
		Tools:     map[string]*vmcp.BackendTarget{"search-svc_search": {WorkloadID: "search-svc", WorkloadName: "Search"}},
		Resources: map[string]*vmcp.BackendTarget{},
		Prompts:   map[string]*vmcp.BackendTarget{},
	}
	backends := []vmcp.Backend{{ID: "search-svc", Name: "Search", BaseURL: "http://search-svc:8080",
		TransportType: "streamable-http", HealthStatus: vmcp.BackendHealthy}}

	srv, err := New(ctx, &Config{
		Name:              "code-mode-vmcp",
		Version:           "1.0.0",
		Host:              "127.0.0.1",
		TelemetryProvider: provider,
		SessionFactory:    newBackendAwareTestFactory(tools, routing),
		Aggregator: aggregator.NewDefaultAggregator(
			backendClient, aggregator.NewPrefixConflictResolver("{workload}_"), nil, nil),
		CodeModeConfig:  codeMode,
		OptimizerConfig: optCfg,
	}, router.NewSessionRouter(&vmcp.RoutingTable{}), backendClient, vmcp.NewImmutableRegistry(backends), nil)
	require.NoError(t, err)

	serverCtx, cancel := context.WithCancel(ctx)
	t.Cleanup(cancel)
	errCh := make(chan error, 1)
	go func() {
		if err := srv.Start(serverCtx); err != nil && !errors.Is(err, context.Canceled) {
			errCh <- err
		}
	}()
	select {
	case <-srv.Ready():
	case err := <-errCh:
		t.Fatalf("server failed to start: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for server ready")
	}

	baseURL := "http://" + srv.Address() + "/mcp"
	resp := postJSON(t, baseURL, "", map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "initialize",
		"params": map[string]any{
			"protocolVersion": "2024-11-05",
			"capabilities":    map[string]any{},
			"clientInfo":      map[string]any{"name": "code-mode-test", "version": "1.0.0"},
		},
	})
	sessionID := resp.Header.Get("Mcp-Session-Id")
	require.NotEmpty(t, sessionID)

	if codeMode != nil {
		// Let the session hooks register the tools before calling one.
		advertised := "execute_tool_script"
		if optCfg != nil {
			advertised = "call_tool"
		}
		require.Eventually(t, func() bool {
			r := postJSON(t, baseURL, sessionID, map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/list"})
			body, _ := io.ReadAll(r.Body)
			return strings.Contains(string(body), advertised)
		}, 5*time.Second, 50*time.Millisecond)

		scriptArgs := map[string]any{
			"script": `return parallel([lambda: search_svc_search(query="a"), lambda: search_svc_search(query="b")])`,
		}
		if optCfg == nil {
			callTool(t, baseURL, sessionID, 3, "execute_tool_script", scriptArgs)
		} else {
			callTool(t, baseURL, sessionID, 3, "call_tool",
				map[string]any{"tool_name": "execute_tool_script", "parameters": scriptArgs})
			callTool(t, baseURL, sessionID, 4, "call_tool",
				map[string]any{"tool_name": "search-svc_search", "parameters": map[string]any{"query": "c"}})
		}
	}

	metricsResp, err := http.Get(metricsURL(t, srv))
	require.NoError(t, err)
	t.Cleanup(func() { _ = metricsResp.Body.Close() })
	require.Equal(t, http.StatusOK, metricsResp.StatusCode)
	body, err := io.ReadAll(metricsResp.Body)
	require.NoError(t, err)
	return string(body)
}

// callTool sends one tools/call and requires a result that is not an error.
func callTool(t *testing.T, baseURL, sessionID string, id int, name string, args map[string]any) {
	t.Helper()
	r := postJSON(t, baseURL, sessionID, map[string]any{
		"jsonrpc": "2.0", "id": id, "method": "tools/call",
		"params": map[string]any{"name": name, "arguments": args},
	})
	body, err := io.ReadAll(r.Body)
	require.NoError(t, err)
	require.NotContains(t, string(body), `"isError":true`, string(body))
	require.NotContains(t, string(body), `"error":`, string(body))
}

// postJSON posts body as a JSON-RPC request and requires a 200 response.
func postJSON(t *testing.T, url, sessionID string, body map[string]any) *http.Response {
	t.Helper()
	raw, err := json.Marshal(body)
	require.NoError(t, err)
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(raw))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if sessionID != "" {
		req.Header.Set("Mcp-Session-Id", sessionID)
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	t.Cleanup(func() { _ = resp.Body.Close() })
	require.Equal(t, http.StatusOK, resp.StatusCode)
	return resp
}

// metricValue returns the value of the one sample of metric whose labels
// contain every one of labels, failing if there is not exactly one.
func metricValue(t *testing.T, metrics, metric string, labels ...string) string {
	t.Helper()
	var found []string
	for _, line := range strings.Split(metrics, "\n") {
		if !strings.HasPrefix(line, metric+"{") {
			continue
		}
		matches := true
		for _, l := range labels {
			matches = matches && strings.Contains(line, l)
		}
		if matches {
			found = append(found, line[strings.LastIndex(line, " ")+1:])
		}
	}
	require.Len(t, found, 1, "samples of %s with %v", metric, labels)
	return found[0]
}

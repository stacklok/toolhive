// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"context"
	"fmt"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	tracenoop "go.opentelemetry.io/otel/trace/noop"

	"github.com/stacklok/toolhive/pkg/vmcp/optimizer"
)

func TestMapActionToMCPMethod(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		action   string
		expected string
	}{
		{name: "call_tool maps to tools/call", action: "call_tool", expected: "tools/call"},
		{name: "read_resource maps to resources/read", action: "read_resource", expected: "resources/read"},
		{name: "get_prompt maps to prompts/get", action: "get_prompt", expected: "prompts/get"},
		{name: "unknown action passes through", action: "list_capabilities", expected: "list_capabilities"},
		{name: "empty string passes through", action: "", expected: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := mapActionToMCPMethod(tt.action)
			if got != tt.expected {
				t.Errorf("mapActionToMCPMethod(%q) = %q, want %q", tt.action, got, tt.expected)
			}
		})
	}
}

func TestMapTransportTypeToNetworkTransport(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		transportType string
		expected      string
	}{
		{name: "stdio maps to pipe", transportType: "stdio", expected: "pipe"},
		{name: "sse maps to tcp", transportType: "sse", expected: "tcp"},
		{name: "streamable-http maps to tcp", transportType: "streamable-http", expected: "tcp"},
		{name: "unknown defaults to tcp", transportType: "unknown", expected: "tcp"},
		{name: "empty defaults to tcp", transportType: "", expected: "tcp"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := mapTransportTypeToNetworkTransport(tt.transportType)
			if got != tt.expected {
				t.Errorf("mapTransportTypeToNetworkTransport(%q) = %q, want %q", tt.transportType, got, tt.expected)
			}
		})
	}
}

// fakeOptimizer implements optimizer.Optimizer for testing.
type fakeOptimizer struct {
	findToolFn func(ctx context.Context, input optimizer.FindToolInput) (*optimizer.FindToolOutput, error)
	callToolFn func(ctx context.Context, input optimizer.CallToolInput) (*mcp.CallToolResult, error)
}

func (f *fakeOptimizer) FindTool(ctx context.Context, input optimizer.FindToolInput) (*optimizer.FindToolOutput, error) {
	return f.findToolFn(ctx, input)
}

func (f *fakeOptimizer) CallTool(ctx context.Context, input optimizer.CallToolInput) (*mcp.CallToolResult, error) {
	return f.callToolFn(ctx, input)
}

// findMetric returns the first metric matching the given name from the collected resource metrics.
func findMetric(rm metricdata.ResourceMetrics, name string) *metricdata.Metrics {
	for _, sm := range rm.ScopeMetrics {
		for i := range sm.Metrics {
			if sm.Metrics[i].Name == name {
				return &sm.Metrics[i]
			}
		}
	}
	return nil
}

// counterValue returns the sum of all data points for an Int64 counter metric.
// Returns 0 if m is nil (metric not reported because it was never incremented).
func counterValue(m *metricdata.Metrics) int64 {
	if m == nil {
		return 0
	}
	sum, ok := m.Data.(metricdata.Sum[int64])
	if !ok {
		return 0
	}
	var total int64
	for _, dp := range sum.DataPoints {
		total += dp.Value
	}
	return total
}

// histogramCount returns the total count across all data points for a Float64 histogram metric.
// Returns 0 if m is nil (metric not reported because it was never recorded).
func histogramCount(m *metricdata.Metrics) uint64 {
	if m == nil {
		return 0
	}
	hist, ok := m.Data.(metricdata.Histogram[float64])
	if !ok {
		return 0
	}
	var total uint64
	for _, dp := range hist.DataPoints {
		total += dp.Count
	}
	return total
}

func TestTelemetryOptimizer(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		setup      func() *fakeOptimizer
		action     func(t *testing.T, opt optimizer.Optimizer)
		assertFunc func(t *testing.T, rm metricdata.ResourceMetrics)
	}{
		{
			name: "FindTool success records requests counter, duration, results, and savings",
			setup: func() *fakeOptimizer {
				return &fakeOptimizer{
					findToolFn: func(_ context.Context, _ optimizer.FindToolInput) (*optimizer.FindToolOutput, error) {
						return &optimizer.FindToolOutput{
							Tools: []mcp.Tool{
								{Name: "tool_a", Description: "Tool A"},
								{Name: "tool_b", Description: "Tool B"},
							},
							TokenMetrics: optimizer.TokenMetrics{
								BaselineTokens: 1000,
								ReturnedTokens: 200,
								SavingsPercent: 80.0,
							},
						}, nil
					},
				}
			},
			action: func(t *testing.T, opt optimizer.Optimizer) {
				t.Helper()
				result, err := opt.FindTool(context.Background(), optimizer.FindToolInput{
					ToolDescription: "search tools",
				})
				require.NoError(t, err)
				require.Len(t, result.Tools, 2)
			},
			assertFunc: func(t *testing.T, rm metricdata.ResourceMetrics) {
				t.Helper()
				// Requests counter
				m := findMetric(rm, "toolhive_vmcp_optimizer_find_tool_requests")
				require.NotNil(t, m, "find_tool_requests metric should exist")
				assert.Equal(t, int64(1), counterValue(m))

				// No errors (counter not reported when never incremented)
				assert.Equal(t, int64(0), counterValue(findMetric(rm, "toolhive_vmcp_optimizer_find_tool_errors")))

				// Duration histogram
				m = findMetric(rm, "toolhive_vmcp_optimizer_find_tool_duration")
				require.NotNil(t, m, "find_tool_duration metric should exist")
				assert.Equal(t, uint64(1), histogramCount(m))

				// Results histogram (2 tools returned)
				m = findMetric(rm, "toolhive_vmcp_optimizer_find_tool_results")
				require.NotNil(t, m, "find_tool_results metric should exist")
				assert.Equal(t, uint64(1), histogramCount(m))

				// Token savings histogram
				m = findMetric(rm, "toolhive_vmcp_optimizer_token_savings_percent")
				require.NotNil(t, m, "token_savings_percent metric should exist")
				assert.Equal(t, uint64(1), histogramCount(m))
			},
		},
		{
			name: "FindTool error increments error counter",
			setup: func() *fakeOptimizer {
				return &fakeOptimizer{
					findToolFn: func(_ context.Context, _ optimizer.FindToolInput) (*optimizer.FindToolOutput, error) {
						return nil, fmt.Errorf("search failed")
					},
				}
			},
			action: func(t *testing.T, opt optimizer.Optimizer) {
				t.Helper()
				_, err := opt.FindTool(context.Background(), optimizer.FindToolInput{
					ToolDescription: "search tools",
				})
				require.Error(t, err)
			},
			assertFunc: func(t *testing.T, rm metricdata.ResourceMetrics) {
				t.Helper()
				m := findMetric(rm, "toolhive_vmcp_optimizer_find_tool_requests")
				require.NotNil(t, m)
				assert.Equal(t, int64(1), counterValue(m))

				m = findMetric(rm, "toolhive_vmcp_optimizer_find_tool_errors")
				require.NotNil(t, m)
				assert.Equal(t, int64(1), counterValue(m))

				// Duration should still be recorded
				m = findMetric(rm, "toolhive_vmcp_optimizer_find_tool_duration")
				require.NotNil(t, m)
				assert.Equal(t, uint64(1), histogramCount(m))

				// Results and savings should not be recorded on error
				assert.Equal(t, uint64(0), histogramCount(findMetric(rm, "toolhive_vmcp_optimizer_find_tool_results")))
			},
		},
		{
			name: "CallTool success records requests counter and duration with tool_name attribute",
			setup: func() *fakeOptimizer {
				return &fakeOptimizer{
					callToolFn: func(_ context.Context, _ optimizer.CallToolInput) (*mcp.CallToolResult, error) {
						return &mcp.CallToolResult{
							Content: []mcp.Content{mcp.NewTextContent("result")},
						}, nil
					},
				}
			},
			action: func(t *testing.T, opt optimizer.Optimizer) {
				t.Helper()
				result, err := opt.CallTool(context.Background(), optimizer.CallToolInput{
					ToolName: "my_tool",
				})
				require.NoError(t, err)
				require.False(t, result.IsError)
			},
			assertFunc: func(t *testing.T, rm metricdata.ResourceMetrics) {
				t.Helper()
				m := findMetric(rm, "toolhive_vmcp_optimizer_call_tool_requests")
				require.NotNil(t, m, "call_tool_requests metric should exist")
				assert.Equal(t, int64(1), counterValue(m))

				// Error counters should not be reported (never incremented)
				assert.Equal(t, int64(0), counterValue(findMetric(rm, "toolhive_vmcp_optimizer_call_tool_errors")))
				assert.Equal(t, int64(0), counterValue(findMetric(rm, "toolhive_vmcp_optimizer_call_tool_not_found")))

				m = findMetric(rm, "toolhive_vmcp_optimizer_call_tool_duration")
				require.NotNil(t, m)
				assert.Equal(t, uint64(1), histogramCount(m))
			},
		},
		{
			name: "CallTool not found increments call_tool_not_found counter when IsError is true",
			setup: func() *fakeOptimizer {
				return &fakeOptimizer{
					callToolFn: func(_ context.Context, _ optimizer.CallToolInput) (*mcp.CallToolResult, error) {
						return mcp.NewToolResultError("tool not found: missing_tool"), nil
					},
				}
			},
			action: func(t *testing.T, opt optimizer.Optimizer) {
				t.Helper()
				result, err := opt.CallTool(context.Background(), optimizer.CallToolInput{
					ToolName: "missing_tool",
				})
				require.NoError(t, err)
				require.True(t, result.IsError)
			},
			assertFunc: func(t *testing.T, rm metricdata.ResourceMetrics) {
				t.Helper()
				m := findMetric(rm, "toolhive_vmcp_optimizer_call_tool_requests")
				require.NotNil(t, m)
				assert.Equal(t, int64(1), counterValue(m))

				// Go error counter should not be reported (never incremented)
				assert.Equal(t, int64(0), counterValue(findMetric(rm, "toolhive_vmcp_optimizer_call_tool_errors")))

				m = findMetric(rm, "toolhive_vmcp_optimizer_call_tool_not_found")
				require.NotNil(t, m, "not_found counter should exist")
				assert.Equal(t, int64(1), counterValue(m))

				m = findMetric(rm, "toolhive_vmcp_optimizer_call_tool_duration")
				require.NotNil(t, m)
				assert.Equal(t, uint64(1), histogramCount(m))
			},
		},
		{
			name: "CallTool Go error increments error counter",
			setup: func() *fakeOptimizer {
				return &fakeOptimizer{
					callToolFn: func(_ context.Context, _ optimizer.CallToolInput) (*mcp.CallToolResult, error) {
						return nil, fmt.Errorf("handler panic")
					},
				}
			},
			action: func(t *testing.T, opt optimizer.Optimizer) {
				t.Helper()
				_, err := opt.CallTool(context.Background(), optimizer.CallToolInput{
					ToolName: "broken_tool",
				})
				require.Error(t, err)
			},
			assertFunc: func(t *testing.T, rm metricdata.ResourceMetrics) {
				t.Helper()
				m := findMetric(rm, "toolhive_vmcp_optimizer_call_tool_requests")
				require.NotNil(t, m)
				assert.Equal(t, int64(1), counterValue(m))

				m = findMetric(rm, "toolhive_vmcp_optimizer_call_tool_errors")
				require.NotNil(t, m)
				assert.Equal(t, int64(1), counterValue(m))

				// not_found counter should not be reported (never incremented)
				assert.Equal(t, int64(0), counterValue(findMetric(rm, "toolhive_vmcp_optimizer_call_tool_not_found")))
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			reader := sdkmetric.NewManualReader()
			meterProvider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
			tracerProvider := tracenoop.NewTracerProvider()

			fake := tt.setup()

			// Create a factory that returns the fake optimizer
			factory := func(_ context.Context, _ []mcpserver.ServerTool) (optimizer.Optimizer, error) {
				return fake, nil
			}

			// Wrap with telemetry
			wrappedFactory, err := monitorOptimizer(meterProvider, tracerProvider, factory)
			require.NoError(t, err)

			// Create the telemetry-decorated optimizer
			opt, err := wrappedFactory(context.Background(), nil)
			require.NoError(t, err)

			// Execute the action
			tt.action(t, opt)

			// Collect and verify metrics
			var rm metricdata.ResourceMetrics
			err = reader.Collect(context.Background(), &rm)
			require.NoError(t, err)

			tt.assertFunc(t, rm)
		})
	}
}

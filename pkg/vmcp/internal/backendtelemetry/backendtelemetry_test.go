// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package backendtelemetry

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	tracenoop "go.opentelemetry.io/otel/trace/noop"
	"go.uber.org/mock/gomock"

	mcpparser "github.com/stacklok/toolhive/pkg/mcp"
	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/mocks"
)

// fakeRevClient embeds vmcp.BackendClient (nil — its methods are never called
// here) and adds the optional CachedRevision accessor.
type fakeRevClient struct {
	vmcp.BackendClient
	rev mcpparser.Revision
	ok  bool
}

func (f fakeRevClient) CachedRevision(string) (mcpparser.Revision, bool) { return f.rev, f.ok }

// fakeNoRevClient embeds vmcp.BackendClient but does NOT implement revisionReporter.
type fakeNoRevClient struct{ vmcp.BackendClient }

// TestTelemetryBackendClient_CachedRevisionForwarding verifies the decorator
// forwards CachedRevision to a client that reports it, and reports nothing for a
// client that doesn't.
func TestTelemetryBackendClient_CachedRevisionForwarding(t *testing.T) {
	t.Parallel()

	d := telemetryBackendClient{backendClient: fakeRevClient{rev: mcpparser.RevisionModern, ok: true}}
	rev, ok := d.CachedRevision("b")
	if !ok || rev != mcpparser.RevisionModern {
		t.Fatalf("CachedRevision = (%v, %v), want (Modern, true)", rev, ok)
	}
	if got := d.revisionLabel("b"); got != "2026-07-28" {
		t.Errorf("revisionLabel = %q, want 2026-07-28", got)
	}

	dn := telemetryBackendClient{backendClient: fakeNoRevClient{}}
	if _, ok := dn.CachedRevision("b"); ok {
		t.Error("CachedRevision should report false for a client without the accessor")
	}
	if got := dn.revisionLabel("b"); got != "" {
		t.Errorf("revisionLabel = %q, want empty for unprobed/unsupported", got)
	}
}

// TestRecordRevisionReclassification is a smoke test: the counter lazily binds to
// the global meter provider and increments without panicking (the noop provider
// makes the value unobservable here — the WARN in the same reclassify branch is
// asserted in the client package's reclassify test).
func TestRecordRevisionReclassification(t *testing.T) {
	t.Parallel()
	RecordRevisionReclassification(context.Background())
	RecordRevisionReclassification(context.Background())
}

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

// counterPoints returns the data points of the Int64 counter named name, or nil
// when the counter recorded nothing.
func counterPoints(t *testing.T, rm metricdata.ResourceMetrics, name string) []metricdata.DataPoint[int64] {
	t.Helper()
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != name {
				continue
			}
			sum, ok := m.Data.(metricdata.Sum[int64])
			require.True(t, ok, "%s is not an Int64 counter", name)
			return sum.DataPoints
		}
	}
	return nil
}

// assertToolCallDuration asserts that mcp.client.operation.duration holds one
// tools/call point over calls calls, carrying error.type only when errorType is
// not empty.
func assertToolCallDuration(t *testing.T, rm metricdata.ResourceMetrics, calls int, errorType string) {
	t.Helper()
	want := []attribute.KeyValue{
		attribute.String("mcp.method.name", "tools/call"),
		attribute.String("network.transport", "tcp"),
	}
	if errorType != "" {
		want = append(want, attribute.String("error.type", errorType))
	}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != "mcp.client.operation.duration" {
				continue
			}
			histogram, ok := m.Data.(metricdata.Histogram[float64])
			require.True(t, ok, "mcp.client.operation.duration is not a Float64 histogram")
			require.Len(t, histogram.DataPoints, 1)
			assert.EqualValues(t, calls, histogram.DataPoints[0].Count)
			assert.ElementsMatch(t, want, histogram.DataPoints[0].Attributes.ToSlice(),
				"mcp.client.operation.duration attributes")
			return
		}
	}
	t.Error("mcp.client.operation.duration recorded nothing")
}

// TestTelemetryBackendClient_CallToolRecordsToolErrors pins how the decorator
// records a tool call the backend answers with an isError result: it counts once
// in toolhive_vmcp_backend_tool_errors, under the attributes of the call's
// toolhive_vmcp_backend_requests point, and its mcp.client.operation.duration point
// carries no error.type. A successful result counts in neither error counter and
// carries no error.type either, while a Go error counts in
// toolhive_vmcp_backend_errors, not in toolhive_vmcp_backend_tool_errors, and sets
// error.type to its Go type.
func TestTelemetryBackendClient_CallToolRecordsToolErrors(t *testing.T) {
	t.Parallel()

	const (
		toolName = "search"
		workload = "logs-backend"
	)
	target := &vmcp.BackendTarget{
		WorkloadID:    workload,
		WorkloadName:  "logs",
		BaseURL:       "http://logs.example:8080/mcp",
		TransportType: "streamable-http",
	}
	toolError := &vmcp.ToolCallResult{
		IsError: true,
		Content: []vmcp.Content{{Type: vmcp.ContentTypeText, Text: "cannot parse query"}},
	}
	success := &vmcp.ToolCallResult{Content: []vmcp.Content{{Type: vmcp.ContentTypeText, Text: "ok"}}}

	tests := []struct {
		name           string
		result         *vmcp.ToolCallResult
		err            error
		calls          int
		wantToolErrors int64
		wantErrors     int64
		wantErrorType  string
	}{
		{name: "isError result counts once", result: toolError, calls: 1, wantToolErrors: 1},
		{name: "two isError results count twice", result: toolError, calls: 2, wantToolErrors: 2},
		{name: "successful result is not counted", result: success, calls: 1},
		{
			name: "Go error counts as a backend error only", err: errors.New("connection refused"), calls: 1,
			wantErrors: 1, wantErrorType: "*errors.errorString",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			backend := mocks.NewMockBackendClient(gomock.NewController(t))
			backend.EXPECT().
				CallTool(gomock.Any(), target, toolName, gomock.Any(), gomock.Any(), gomock.Any()).
				Return(tc.result, tc.err).
				Times(tc.calls)

			reader := sdkmetric.NewManualReader()
			client, err := MonitorBackends(context.Background(),
				sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)), tracenoop.NewTracerProvider(),
				nil, backend)
			require.NoError(t, err)

			for range tc.calls {
				result, callErr := client.CallTool(context.Background(), target, toolName, nil, nil, nil)
				assert.Equal(t, tc.result, result, "the result reaches the caller unchanged")
				assert.Equal(t, tc.err, callErr)
			}

			var rm metricdata.ResourceMetrics
			require.NoError(t, reader.Collect(context.Background(), &rm))

			requests := counterPoints(t, rm, "toolhive_vmcp_backend_requests")
			require.Len(t, requests, 1)
			assert.Equal(t, int64(tc.calls), requests[0].Value)

			var gotErrors int64
			for _, dp := range counterPoints(t, rm, "toolhive_vmcp_backend_errors") {
				gotErrors += dp.Value
			}
			assert.Equal(t, tc.wantErrors, gotErrors, "toolhive_vmcp_backend_errors")

			assertToolCallDuration(t, rm, tc.calls, tc.wantErrorType)

			toolErrors := counterPoints(t, rm, "toolhive_vmcp_backend_tool_errors")
			if tc.wantToolErrors == 0 {
				assert.Empty(t, toolErrors, "toolhive_vmcp_backend_tool_errors records nothing")
				return
			}
			require.Len(t, toolErrors, 1)
			assert.Equal(t, tc.wantToolErrors, toolErrors[0].Value)
			assert.True(t, requests[0].Attributes.Equals(&toolErrors[0].Attributes),
				"the tool error point carries the request point's attributes")
			for _, want := range []attribute.KeyValue{
				attribute.String("tool_name", toolName),
				attribute.String("gen_ai.tool.name", toolName),
				attribute.String("target.workload_id", workload),
				attribute.String("action", "call_tool"),
				attribute.String("mcp.method.name", "tools/call"),
			} {
				got, ok := toolErrors[0].Attributes.Value(want.Key)
				assert.True(t, ok, "attribute %s is set", want.Key)
				assert.Equal(t, want.Value, got, "attribute %s", want.Key)
			}
		})
	}
}

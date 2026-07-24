// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package backendtelemetry

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	tracenoop "go.opentelemetry.io/otel/trace/noop"
	"go.uber.org/mock/gomock"

	coremetrics "github.com/stacklok/toolhive-core/telemetry/metrics"
	"github.com/stacklok/toolhive/pkg/vmcp"
	vmcpmocks "github.com/stacklok/toolhive/pkg/vmcp/mocks"
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

// healthPoints collects stacklok.vmcp.mcp_server.health and returns, per
// backend name, the single vmcp.BackendHealthStatus value currently reporting
// 1. It also asserts every other state in healthStates reports 0 for that
// backend, so a collapse back to a two-value (healthy/unhealthy) gauge would
// fail this helper's invariant even if the "current" value alone looked right.
func healthPoints(t *testing.T, reader sdkmetric.Reader) map[string]string {
	t.Helper()
	var rm metricdata.ResourceMetrics
	require.NoError(t, reader.Collect(context.Background(), &rm))

	current := map[string]string{}
	seen := map[string]map[string]int64{}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != "stacklok.vmcp.mcp_server.health" {
				continue
			}
			gauge, ok := m.Data.(metricdata.Gauge[int64])
			require.True(t, ok, "gauge must be an int64 Gauge")
			for _, dp := range gauge.DataPoints {
				name, hasName := dp.Attributes.Value(attribute.Key(coremetrics.LabelMCPServer))
				require.True(t, hasName, "mcp_server label must be present")
				state, hasState := dp.Attributes.Value(attribute.Key(healthStateLabel))
				require.True(t, hasState, "state label must be present")

				if seen[name.AsString()] == nil {
					seen[name.AsString()] = map[string]int64{}
				}
				seen[name.AsString()][state.AsString()] = dp.Value
				if dp.Value == 1 {
					current[name.AsString()] = state.AsString()
				}
			}
		}
	}

	for backend, points := range seen {
		require.Len(t, points, len(healthStates),
			"backend %q must report one point per possible health state", backend)
		for _, state := range healthStates {
			value, ok := points[string(state)]
			require.True(t, ok, "backend %q missing a point for state %q", backend, state)
			if string(state) != current[backend] {
				assert.Equal(t, int64(0), value, "backend %q state %q must be 0 when not current", backend, state)
			}
		}
	}

	return current
}

func TestMonitorBackends_HealthGaugeReportsRegistryStatusBeyondHealthyUnhealthy(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	registry := vmcpmocks.NewMockBackendRegistry(ctrl)
	registry.EXPECT().List(gomock.Any()).Return([]vmcp.Backend{
		{ID: "be1", Name: "degraded-backend", HealthStatus: vmcp.BackendDegraded},
		{ID: "be2", Name: "unauthenticated-backend", HealthStatus: vmcp.BackendUnauthenticated},
	}).AnyTimes()

	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))

	// record() only ever distinguishes success/failure, so before any request
	// completes for these backends, the gauge must surface the registry's own
	// finer-grained status rather than collapsing it to healthy/unhealthy.
	_, unregister, err := MonitorBackends(
		context.Background(), mp, tracenoop.NewTracerProvider(), registry, vmcpmocks.NewMockBackendClient(ctrl),
	)
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, unregister()) })

	assert.Equal(t, map[string]string{
		"degraded-backend":        string(vmcp.BackendDegraded),
		"unauthenticated-backend": string(vmcp.BackendUnauthenticated),
	}, healthPoints(t, reader))
}

func TestMonitorBackends_HealthGaugeTransitionsOnRequestOutcome(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	registry := vmcpmocks.NewMockBackendRegistry(ctrl)
	registry.EXPECT().List(gomock.Any()).Return([]vmcp.Backend{
		{ID: "be1", Name: "backend-1", HealthStatus: vmcp.BackendHealthy},
	}).AnyTimes()

	baseClient := vmcpmocks.NewMockBackendClient(ctrl)
	target := &vmcp.BackendTarget{WorkloadName: "backend-1", TransportType: "sse"}

	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))

	decorated, unregister, err := MonitorBackends(
		context.Background(), mp, tracenoop.NewTracerProvider(), registry, baseClient,
	)
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, unregister()) })

	// No request yet: falls back to the registry's discovery-time HealthStatus.
	assert.Equal(t, map[string]string{"backend-1": string(vmcp.BackendHealthy)}, healthPoints(t, reader))

	baseClient.EXPECT().CallTool(gomock.Any(), target, "t", gomock.Any(), gomock.Any()).
		Return(nil, errors.New("backend unreachable"))
	_, err = decorated.CallTool(context.Background(), target, "t", nil, nil)
	require.Error(t, err)
	assert.Equal(t, map[string]string{"backend-1": string(vmcp.BackendUnhealthy)}, healthPoints(t, reader))

	baseClient.EXPECT().CallTool(gomock.Any(), target, "t", gomock.Any(), gomock.Any()).
		Return(&vmcp.ToolCallResult{}, nil)
	_, err = decorated.CallTool(context.Background(), target, "t", nil, nil)
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"backend-1": string(vmcp.BackendHealthy)}, healthPoints(t, reader))
}

func TestMonitorBackends_HealthGaugeDropsBackendRemovedFromRegistry(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	registry := vmcpmocks.NewMockBackendRegistry(ctrl)
	backends := []vmcp.Backend{
		{ID: "be1", Name: "backend-1", HealthStatus: vmcp.BackendHealthy},
		{ID: "be2", Name: "backend-2", HealthStatus: vmcp.BackendHealthy},
	}
	registry.EXPECT().List(gomock.Any()).DoAndReturn(
		func(context.Context) []vmcp.Backend { return backends },
	).AnyTimes()

	baseClient := vmcpmocks.NewMockBackendClient(ctrl)
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))

	_, unregister, err := MonitorBackends(
		context.Background(), mp, tracenoop.NewTracerProvider(), registry, baseClient,
	)
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, unregister()) })

	assert.Equal(t, map[string]string{
		"backend-1": string(vmcp.BackendHealthy),
		"backend-2": string(vmcp.BackendHealthy),
	}, healthPoints(t, reader))

	// Simulate a list_changed-driven removal: the next List call no longer
	// includes backend-2. The gauge must stop reporting it, not keep emitting
	// its last-known state indefinitely.
	backends = backends[:1]
	assert.Equal(t, map[string]string{"backend-1": string(vmcp.BackendHealthy)}, healthPoints(t, reader))
}

func TestBackendHealth_ConcurrentSetAndSnapshot(t *testing.T) {
	t.Parallel()

	health := &backendHealth{states: make(map[string]vmcp.BackendHealthStatus)}

	var wg sync.WaitGroup
	for i := range 50 {
		wg.Go(func() {
			status := vmcp.BackendUnhealthy
			if i%2 == 0 {
				status = vmcp.BackendHealthy
			}
			health.set("backend-1", status)
		})
	}
	wg.Go(func() {
		for range 50 {
			_ = health.snapshot()
		}
	})

	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for concurrent set/snapshot goroutines")
	}

	// Race-detector coverage is the point of this test; the exact final value
	// is non-deterministic, so only assert the key is present.
	_, recorded := health.snapshot()["backend-1"]
	assert.True(t, recorded)
}

// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package backendtelemetry

import (
	"context"
	"errors"
	"fmt"
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
	mcpparser "github.com/stacklok/toolhive/pkg/mcp"
	"github.com/stacklok/toolhive/pkg/vmcp"
	vmcpmocks "github.com/stacklok/toolhive/pkg/vmcp/mocks"
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
	_, _, unregister, err := MonitorBackends(
		context.Background(), mp, tracenoop.NewTracerProvider(), registry, vmcpmocks.NewMockBackendClient(ctrl),
	)
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, unregister()) })

	assert.Equal(t, map[string]string{
		"degraded-backend":        string(vmcp.BackendDegraded),
		"unauthenticated-backend": string(vmcp.BackendUnauthenticated),
	}, healthPoints(t, reader))
}

func TestMonitorBackends_HealthGaugeNormalizesEmptyHealthStatusToHealthy(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	registry := vmcpmocks.NewMockBackendRegistry(ctrl)
	// No HealthStatus set (zero value) and no request has completed yet, so
	// nothing classifies this backend — matches filterHealthyBackends's
	// "empty/zero-value: assume healthy" convention rather than reporting
	// every healthStates point as 0.
	registry.EXPECT().List(gomock.Any()).Return([]vmcp.Backend{
		{ID: "be1", Name: "unclassified-backend"},
	}).AnyTimes()

	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))

	_, _, unregister, err := MonitorBackends(
		context.Background(), mp, tracenoop.NewTracerProvider(), registry, vmcpmocks.NewMockBackendClient(ctrl),
	)
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, unregister()) })

	assert.Equal(t, map[string]string{"unclassified-backend": string(vmcp.BackendHealthy)}, healthPoints(t, reader))
}

// fakeStatusProvider is a minimal health.StatusProvider stub so tests can pin
// a specific per-backend status without constructing a full health.Monitor.
type fakeStatusProvider struct {
	statuses map[string]vmcp.BackendHealthStatus
}

func (f *fakeStatusProvider) QueryBackendStatus(backendID string) (vmcp.BackendHealthStatus, bool) {
	status, tracked := f.statuses[backendID]
	return status, tracked
}

func TestMonitorBackends_HealthGaugeMatchesFilterHealthyBackendsPrecedence(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	registry := vmcpmocks.NewMockBackendRegistry(ctrl)
	// Registry snapshot says healthy. The live provider disagrees — it must win,
	// exactly as filterHealthyBackends prefers it over the registry snapshot.
	registry.EXPECT().List(gomock.Any()).Return([]vmcp.Backend{
		{ID: "be1", Name: "backend-1", HealthStatus: vmcp.BackendHealthy},
	}).AnyTimes()

	baseClient := vmcpmocks.NewMockBackendClient(ctrl)
	target := &vmcp.BackendTarget{WorkloadID: "be1", WorkloadName: "backend-1", TransportType: "sse"}

	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))

	decorated, setter, unregister, err := MonitorBackends(
		context.Background(), mp, tracenoop.NewTracerProvider(), registry, baseClient,
	)
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, unregister()) })

	// record() sees a failure, so the request-outcome map disagrees with the
	// registry: recorded=unhealthy, registry=healthy. This divergence is what
	// makes the later "provider set but doesn't track" step unambiguous.
	baseClient.EXPECT().CallTool(gomock.Any(), target, "t", gomock.Any(), gomock.Any(), gomock.Any()).
		Return(nil, assert.AnError)
	_, err = decorated.CallTool(context.Background(), target, "t", nil, nil, nil)
	require.Error(t, err)

	// No provider set yet: falls back to the recorded (request-outcome) state,
	// per currentHealthStatus's "no provider: use recorded" branch.
	assert.Equal(t, map[string]string{"backend-1": string(vmcp.BackendUnhealthy)}, healthPoints(t, reader))

	// The health monitor (built after MonitorBackends, per core.New's ordering)
	// now reports this backend as healthy — e.g. after a successful health check.
	// The gauge must reflect the live provider immediately, not the recorded
	// request-outcome state.
	setter.Set(&fakeStatusProvider{statuses: map[string]vmcp.BackendHealthStatus{
		"be1": vmcp.BackendHealthy,
	}})
	assert.Equal(t, map[string]string{"backend-1": string(vmcp.BackendHealthy)}, healthPoints(t, reader))

	// A provider is set but doesn't track this backend (tracked=false): falls
	// straight to the registry snapshot, exactly as filterHealthyBackends does —
	// NOT to the recorded state, which still disagrees (unhealthy) at this point.
	// This is the precedence filterHealthyBackends itself uses, so the gauge and
	// capability-filtering agree even in this fallback case.
	setter.Set(&fakeStatusProvider{statuses: map[string]vmcp.BackendHealthStatus{}})
	assert.Equal(t, map[string]string{"backend-1": string(vmcp.BackendHealthy)}, healthPoints(t, reader))

	// A nil provider (monitoring disabled) falls back to the recorded state again.
	setter.Set(nil)
	assert.Equal(t, map[string]string{"backend-1": string(vmcp.BackendUnhealthy)}, healthPoints(t, reader))
}

func TestMonitorBackends_HealthGaugeTransitionsOnRequestOutcome(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	registry := vmcpmocks.NewMockBackendRegistry(ctrl)
	registry.EXPECT().List(gomock.Any()).Return([]vmcp.Backend{
		{ID: "be1", Name: "backend-1", HealthStatus: vmcp.BackendHealthy},
	}).AnyTimes()

	baseClient := vmcpmocks.NewMockBackendClient(ctrl)
	target := &vmcp.BackendTarget{WorkloadID: "be1", WorkloadName: "backend-1", TransportType: "sse"}

	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))

	decorated, _, unregister, err := MonitorBackends(
		context.Background(), mp, tracenoop.NewTracerProvider(), registry, baseClient,
	)
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, unregister()) })

	// No request yet: falls back to the registry's discovery-time HealthStatus.
	assert.Equal(t, map[string]string{"backend-1": string(vmcp.BackendHealthy)}, healthPoints(t, reader))

	baseClient.EXPECT().CallTool(gomock.Any(), target, "t", gomock.Any(), gomock.Any(), gomock.Any()).
		Return(nil, errors.New("backend unreachable"))
	_, err = decorated.CallTool(context.Background(), target, "t", nil, nil, nil)
	require.Error(t, err)
	assert.Equal(t, map[string]string{"backend-1": string(vmcp.BackendUnhealthy)}, healthPoints(t, reader))

	baseClient.EXPECT().CallTool(gomock.Any(), target, "t", gomock.Any(), gomock.Any(), gomock.Any()).
		Return(&vmcp.ToolCallResult{}, nil)
	_, err = decorated.CallTool(context.Background(), target, "t", nil, nil, nil)
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

	_, _, unregister, err := MonitorBackends(
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

// clientOperationDurationServers returns the mcp_server label value recorded
// on every mcp.client.operation.duration data point.
func clientOperationDurationServers(t *testing.T, reader sdkmetric.Reader) []string {
	t.Helper()
	var rm metricdata.ResourceMetrics
	require.NoError(t, reader.Collect(context.Background(), &rm))

	var servers []string
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != "mcp.client.operation.duration" {
				continue
			}
			hist, ok := m.Data.(metricdata.Histogram[float64])
			require.True(t, ok, "mcp.client.operation.duration must be a float64 Histogram")
			for _, dp := range hist.DataPoints {
				name, ok := dp.Attributes.Value(attribute.Key(coremetrics.LabelMCPServer))
				require.True(t, ok, "mcp_server label must be present on mcp.client.operation.duration")
				servers = append(servers, name.AsString())
			}
		}
	}
	return servers
}

func TestMonitorBackends_ClientOperationDurationCarriesBackendIdentity(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	registry := vmcpmocks.NewMockBackendRegistry(ctrl)
	registry.EXPECT().List(gomock.Any()).Return([]vmcp.Backend{
		{ID: "be1", Name: "backend-1", HealthStatus: vmcp.BackendHealthy},
	}).AnyTimes()

	baseClient := vmcpmocks.NewMockBackendClient(ctrl)
	target := &vmcp.BackendTarget{WorkloadID: "be1", WorkloadName: "backend-1", TransportType: "sse"}

	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))

	decorated, _, unregister, err := MonitorBackends(
		context.Background(), mp, tracenoop.NewTracerProvider(), registry, baseClient,
	)
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, unregister()) })

	baseClient.EXPECT().CallTool(gomock.Any(), target, "t", gomock.Any(), gomock.Any(), gomock.Any()).
		Return(&vmcp.ToolCallResult{}, nil)
	_, err = decorated.CallTool(context.Background(), target, "t", nil, nil, nil)
	require.NoError(t, err)

	baseClient.EXPECT().CallTool(gomock.Any(), target, "t", gomock.Any(), gomock.Any(), gomock.Any()).
		Return(nil, errors.New("backend unreachable"))
	_, err = decorated.CallTool(context.Background(), target, "t", nil, nil, nil)
	require.Error(t, err)

	// Both the success and error data points must carry the backend identity —
	// without it, per-backend latency/error-rate breakdown is impossible.
	assert.Equal(t, []string{"backend-1", "backend-1"}, clientOperationDurationServers(t, reader))
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

func TestHealthStatusForError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		err        error
		wantStatus vmcp.BackendHealthStatus
		wantOK     bool
	}{
		{
			// A caller that disconnects mid-call says nothing about the backend.
			name:   "context canceled leaves the gauge untouched",
			err:    context.Canceled,
			wantOK: false,
		},
		{
			name:   "deadline exceeded leaves the gauge untouched",
			err:    context.DeadlineExceeded,
			wantOK: false,
		},
		{
			name:   "wrapped cancellation is still recognized",
			err:    fmt.Errorf("call backend: %w", context.Canceled),
			wantOK: false,
		},
		{
			// The shape the real client produces: wrapBackendError wraps only the
			// domain sentinel with %w and formats the origin error with %v, so the
			// context sentinel is NOT in the chain. Matching context.Canceled alone
			// compiles and reads correctly while never firing on a real call.
			name:   "client-shaped cancellation leaves the gauge untouched",
			err:    fmt.Errorf("%w: failed to call tool for backend wl-1 (cancelled): %v", vmcp.ErrCancelled, context.Canceled),
			wantOK: false,
		},
		{
			name:   "client-shaped timeout leaves the gauge untouched",
			err:    fmt.Errorf("%w: failed to call tool for backend wl-1 (timeout): %v", vmcp.ErrTimeout, context.DeadlineExceeded),
			wantOK: false,
		},
		{
			name:       "authentication failure maps to unauthenticated",
			err:        vmcp.ErrAuthenticationFailed,
			wantStatus: vmcp.BackendUnauthenticated,
			wantOK:     true,
		},
		{
			name:       "authorization failure maps to unauthenticated",
			err:        fmt.Errorf("wrapped: %w", vmcp.ErrAuthorizationFailed),
			wantStatus: vmcp.BackendUnauthenticated,
			wantOK:     true,
		},
		{
			name:       "an unrecognized error means unhealthy",
			err:        errors.New("connection refused"),
			wantStatus: vmcp.BackendUnhealthy,
			wantOK:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			status, ok := healthStatusForError(tt.err)
			assert.Equal(t, tt.wantOK, ok)
			if tt.wantOK {
				assert.Equal(t, tt.wantStatus, status)
			}
		})
	}
}

func TestBackendHealthRetainPrunesAbsentKeys(t *testing.T) {
	t.Parallel()

	b := &backendHealth{states: make(map[string]vmcp.BackendHealthStatus)}
	b.set("live", vmcp.BackendHealthy)
	b.set("removed", vmcp.BackendUnhealthy)

	b.retain(map[string]struct{}{"live": {}})

	got := b.snapshot()
	assert.Equal(t, map[string]vmcp.BackendHealthStatus{"live": vmcp.BackendHealthy}, got,
		"entries absent from the live set must be pruned so the map cannot grow unbounded")
}

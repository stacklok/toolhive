// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package sessionmanager

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	mcpmcp "github.com/stacklok/toolhive-core/mcpcompat/mcp"
	mcpserver "github.com/stacklok/toolhive-core/mcpcompat/server"
	transportsession "github.com/stacklok/toolhive/pkg/transport/session"
	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/health"
	vmcpsession "github.com/stacklok/toolhive/pkg/vmcp/session"
)

// slowBackendDelay is the per-request delay of the simulated slow-but-working
// backend. It stands in for the real 10-25s latency in #5861, scaled down so
// the suite stays fast while remaining orders of magnitude above the healthy
// backends' sub-millisecond responses.
const slowBackendDelay = 2 * time.Second

// slowBackendLatencyBudget is the ceiling on total CreateSession latency for a
// tenant containing one known-bad slow backend. Set well below slowBackendDelay:
// if session creation blocks on the slow backend at all, elapsed time lands at
// or above slowBackendDelay and the assertion fails.
const slowBackendLatencyBudget = slowBackendDelay / 2

// startSlowMCPBackend starts an in-process MCP backend that delays every
// request by delay before responding. It is deliberately WORKING, not broken:
// #5861 is about a backend that is merely slow, which is why simulating it with
// a connection refusal or a 5xx would not reproduce the failure.
func startSlowMCPBackend(t *testing.T, backendID string, delay time.Duration) *vmcp.Backend {
	t.Helper()
	mcpSrv := mcpserver.NewMCPServer(backendID, "1.0.0")
	mcpSrv.AddTool(
		mcpmcp.NewTool("slow_tool",
			mcpmcp.WithDescription("A tool on a slow-but-working backend"),
			mcpmcp.WithString("input", mcpmcp.Required()),
		),
		func(_ context.Context, req mcpmcp.CallToolRequest) (*mcpmcp.CallToolResult, error) {
			args, _ := req.Params.Arguments.(map[string]any)
			input, _ := args["input"].(string)
			return &mcpmcp.CallToolResult{
				Content: []mcpmcp.Content{mcpmcp.NewTextContent(input)},
			}, nil
		},
	)
	streamableSrv := mcpserver.NewStreamableHTTPServer(mcpSrv)
	mux := http.NewServeMux()
	// Delay every request, including the initialize handshake — that is what
	// makes this backend slow rather than broken.
	mux.Handle("/mcp", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(delay)
		streamableSrv.ServeHTTP(w, r)
	}))
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return &vmcp.Backend{
		ID:            backendID,
		Name:          backendID,
		BaseURL:       ts.URL + "/mcp",
		TransportType: "streamable-http",
	}
}

// staticHealth is a health.StatusProvider stub returning fixed per-backend
// statuses, standing in for a live health.Monitor that has already classified
// the slow backend. #5861's premise is that the monitor ALREADY knows the
// backend is bad; the bug is that session creation never asks.
type staticHealth map[string]vmcp.BackendHealthStatus

func (s staticHealth) QueryBackendStatus(backendID string) (vmcp.BackendHealthStatus, bool) {
	status, ok := s[backendID]
	return status, ok
}

// newTestManagerWithHealth creates a Manager over backends with the given
// health.StatusProvider (nil = health monitoring disabled). Uses in-memory
// session storage; the health-gating tests do not exercise Redis.
func newTestManagerWithHealth(
	t *testing.T, backends []*vmcp.Backend, backendHealth health.StatusProvider,
) *Manager {
	t.Helper()
	backendList := make([]vmcp.Backend, len(backends))
	for i, b := range backends {
		backendList[i] = *b
	}
	registry := vmcp.NewImmutableRegistry(backendList)
	factory := vmcpsession.NewSessionFactory(newUnauthenticatedAuthRegistry(t))

	storage, err := transportsession.NewLocalSessionDataStorage(time.Hour)
	require.NoError(t, err)
	t.Cleanup(func() { _ = storage.Close() })

	sm, cleanup, err := New(
		storage,
		&FactoryConfig{Base: factory, BackendHealth: backendHealth},
		registry,
	)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, cleanup(context.Background())) })
	return sm
}

// ---------------------------------------------------------------------------
// Regression tests: #5861 — a slow backend must not inflate tenant session
// latency once the health monitor has classified it
// ---------------------------------------------------------------------------

// TestRegression_CreateSession_SkipsKnownUnhealthySlowBackend pins the fix for
// #5861: a backend the health monitor has already marked unhealthy must not be
// re-attempted during per-session backend init.
//
// Before the fix, Manager.listAllBackends returned the registry unfiltered and
// session.makeBaseSession blocked on wg.Wait() for every backend, so a single
// slow backend set the floor for the whole tenant's initialize latency — a
// 10-25s backend against a ~30s client timeout is the reported coin flip.
// Health status gated capability aggregation (core.filterHealthyBackends) but
// never connection establishment, and establishment runs first.
//
// The assertion is on elapsed time rather than on the session's backend set,
// because the user-visible symptom is latency: a fix that kept the backend in
// the set but stopped blocking on it would also be correct, while a fix that
// dropped it for an unrelated reason would not be.
func TestRegression_CreateSession_SkipsKnownUnhealthySlowBackend(t *testing.T) {
	t.Parallel()

	fast := startMCPBackend(t, "backend-fast", "echo")
	slow := startSlowMCPBackend(t, "backend-slow", slowBackendDelay)

	// The health monitor has already classified the slow backend as unhealthy
	// (its real latency exceeds the probe timeout). This is the state the
	// reporter observed; the bug is that CreateSession ignores it.
	sm := newTestManagerWithHealth(t, []*vmcp.Backend{fast, slow}, staticHealth{
		fast.ID: vmcp.BackendHealthy,
		slow.ID: vmcp.BackendUnhealthy,
	})

	start := time.Now()
	sessionID := createSession(t, sm, nil)
	elapsed := time.Since(start)

	require.NotEmpty(t, sessionID)
	assert.Less(t, elapsed, slowBackendLatencyBudget,
		"session creation must not block on a backend already known unhealthy "+
			"(#5861); took %v, budget %v — the slow backend's %v delay is on the "+
			"critical path", elapsed, slowBackendLatencyBudget, slowBackendDelay)
}

// TestRegression_CreateSession_DegradedSlowBackendDoesNotBlockTenant is the
// other half of #5861 and the reason the fix cannot simply reuse
// core.filterHealthyBackends unchanged.
//
// The reported backend oscillated unhealthy -> degraded -> unhealthy forever,
// because its 10-25s latency straddles the compiled-in 5s degraded threshold
// and 10s probe timeout (health/monitor.go) with no hysteresis.
// filterHealthyBackends INCLUDES degraded, so reusing that predicate verbatim
// would leave every degraded-phase session paying the full latency — the coin
// flip would survive the fix. Session establishment therefore uses a stricter
// predicate than aggregation: degraded is advertisable but not worth blocking
// initialize on. See health.ShouldOpenSession.
func TestRegression_CreateSession_DegradedSlowBackendDoesNotBlockTenant(t *testing.T) {
	t.Parallel()

	fast := startMCPBackend(t, "backend-fast", "echo")
	slow := startSlowMCPBackend(t, "backend-slow", slowBackendDelay)

	sm := newTestManagerWithHealth(t, []*vmcp.Backend{fast, slow}, staticHealth{
		fast.ID: vmcp.BackendHealthy,
		slow.ID: vmcp.BackendDegraded,
	})

	start := time.Now()
	sessionID := createSession(t, sm, nil)
	elapsed := time.Since(start)

	require.NotEmpty(t, sessionID)
	assert.Less(t, elapsed, slowBackendLatencyBudget,
		"a degraded (slow-but-working) backend must not block session creation "+
			"either (#5861 flapping); took %v, budget %v", elapsed, slowBackendLatencyBudget)
}

// TestRegression_CreateSession_HealthyBackendStillConnected guards against the
// fix over-filtering. Without it, the two latency tests above would pass
// vacuously if the fix skipped every backend.
func TestRegression_CreateSession_HealthyBackendStillConnected(t *testing.T) {
	t.Parallel()

	fast := startMCPBackend(t, "backend-fast", "echo")
	sm := newTestManagerWithHealth(t, []*vmcp.Backend{fast}, staticHealth{
		fast.ID: vmcp.BackendHealthy,
	})

	sessionID := createSession(t, sm, nil)

	sess, ok := sm.GetMultiSession(context.Background(), sessionID)
	require.True(t, ok)
	require.NotNil(t, sess)
	assert.NotEmpty(t, sess.BackendSessions(),
		"a healthy backend must still hold a session — the health filter must "+
			"not drop everything")
}

// TestRegression_CreateSession_NilHealthProviderConnectsAll pins that disabling
// health monitoring preserves the pre-fix behaviour exactly: every backend is
// attempted. A nil provider must not be read as "everything is unhealthy".
func TestRegression_CreateSession_NilHealthProviderConnectsAll(t *testing.T) {
	t.Parallel()

	fast := startMCPBackend(t, "backend-fast", "echo")
	sm := newTestManagerWithHealth(t, []*vmcp.Backend{fast}, nil)

	sessionID := createSession(t, sm, nil)

	sess, ok := sm.GetMultiSession(context.Background(), sessionID)
	require.True(t, ok)
	assert.NotEmpty(t, sess.BackendSessions(),
		"with health monitoring disabled every backend must be attempted")
}

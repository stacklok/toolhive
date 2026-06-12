// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	asrunner "github.com/stacklok/toolhive/pkg/authserver/runner"
	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/health"
	"github.com/stacklok/toolhive/pkg/vmcp/mocks"
	"github.com/stacklok/toolhive/pkg/vmcp/optimizer"
	"github.com/stacklok/toolhive/pkg/vmcp/server/sessionmanager"
)

// This file covers the four transport subsystems relocated under Serve in #5443:
// the embedded AS runner routes, the status reporter (+ its periodic goroutine), the
// optimizer cleanup, and the health monitor's Start/Stop. The wiring itself lives in
// ServerConfig and the carried-forward shared (*Server).Handler/Start/Stop; these tests
// prove a Serve-built *Server drives each subsystem's lifecycle the same way the
// server.New path does (whose New-path coverage lives in health_monitoring_test.go,
// status_reporting_test.go, and server_test.go). server.New behavior is unchanged.

// startServeInBackground starts srv on a fresh cancelable context and waits for it to
// become ready, failing fast on early error or timeout. It returns the cancel func and
// the error channel carrying Start's eventual return value (Start blocks until the
// context is cancelled, then returns Stop's result). Mirrors the start/ready dance the
// New-path lifecycle tests use.
func startServeInBackground(t *testing.T, srv *Server) (context.CancelFunc, <-chan error) {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- srv.Start(ctx) }()

	select {
	case <-srv.Ready():
	case err := <-errCh:
		cancel()
		t.Fatalf("server failed to start: %v", err)
	case <-time.After(2 * time.Second):
		cancel()
		t.Fatal("timeout waiting for server to become ready")
	}
	return cancel, errCh
}

// stopAndWait cancels the server and waits for Start to return, asserting a clean stop.
func stopAndWait(t *testing.T, cancel context.CancelFunc, errCh <-chan error) {
	t.Helper()

	cancel()
	select {
	case err := <-errCh:
		require.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for server to stop")
	}
}

// TestServeHealthMonitorDisabledWhenNil verifies that a nil ServerConfig.HealthMonitor
// leaves monitoring disabled on the Serve path: Serve stores no monitor and the getters
// report "disabled" without error, matching the no-monitor behavior of server.New.
func TestServeHealthMonitorDisabledWhenNil(t *testing.T) {
	t.Parallel()

	srv, err := Serve(context.Background(), &stubVMCP{}, testMinimalServeConfig())
	require.NoError(t, err)
	t.Cleanup(func() { _ = srv.Stop(context.Background()) })

	assert.Nil(t, srv.healthMonitor, "nil ServerConfig.HealthMonitor must leave the monitor unset")

	status, err := srv.GetBackendHealthStatus("backend-1")
	require.Error(t, err)
	assert.Equal(t, vmcp.BackendUnknown, status)
	assert.Contains(t, err.Error(), "health monitoring is disabled")

	assert.Equal(t, health.Summary{}, srv.GetHealthSummary())
}

// TestServeStartsAndStopsInjectedHealthMonitor verifies the health-monitor lifecycle on
// the Serve path: Serve stores the pre-built *health.Monitor it is given (it does NOT
// construct one), Start runs it (backends report healthy), and Stop stops it while
// retaining the struct so getters keep working — mirroring TestServer_Stop_StopsHealthMonitor
// for a monitor Serve was handed rather than one it built.
func TestServeStartsAndStopsInjectedHealthMonitor(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	mockBackendClient := mocks.NewMockBackendClient(ctrl)
	mockBackendClient.EXPECT().
		ListCapabilities(gomock.Any(), gomock.Any()).
		Return(&vmcp.CapabilityList{}, nil).
		AnyTimes()

	backends := []vmcp.Backend{
		{ID: "backend-1", Name: "Backend 1", BaseURL: "http://localhost:8080", TransportType: "sse"},
	}
	mon, err := health.NewMonitor(mockBackendClient, backends, health.MonitorConfig{
		CheckInterval:      50 * time.Millisecond,
		UnhealthyThreshold: 1,
		Timeout:            5 * time.Second,
	})
	require.NoError(t, err)

	cfg := testMinimalServeConfig()
	cfg.HealthMonitor = mon
	cfg.BackendRegistry = vmcp.NewImmutableRegistry(backends)

	srv, err := Serve(context.Background(), &stubVMCP{}, cfg)
	require.NoError(t, err)

	// Serve must reuse the injected instance, not build a new one (AC2: Serve does not
	// call health.NewMonitor).
	assert.Same(t, mon, srv.healthMonitor)

	cancel, errCh := startServeInBackground(t, srv)

	require.Eventually(t, func() bool {
		status, statusErr := srv.GetBackendHealthStatus("backend-1")
		return statusErr == nil && status == vmcp.BackendHealthy
	}, 2*time.Second, 10*time.Millisecond, "backend-1 should become healthy via the Serve-started monitor")

	stopAndWait(t, cancel, errCh)

	// The monitor is stopped but the struct is retained (pointer stays valid).
	srv.healthMonitorMu.RLock()
	assert.Same(t, mon, srv.healthMonitor, "health monitor should still exist after Stop")
	srv.healthMonitorMu.RUnlock()

	status, err := srv.GetBackendHealthStatus("backend-1")
	assert.NoError(t, err, "getter should not error after Stop")
	assert.NotEqual(t, vmcp.BackendUnknown, status, "should return last known status")
}

// TestServeDisablesHealthMonitorOnStartFailure verifies graceful degradation when the
// injected monitor cannot start: Start logs a WARN and sets healthMonitor to nil so the
// getters report "disabled", and Serve.Start itself does not fail. The failure is forced
// with a monitor that has already been stopped — health.Monitor.Start refuses to restart.
func TestServeDisablesHealthMonitorOnStartFailure(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	mockBackendClient := mocks.NewMockBackendClient(ctrl)

	// No backends => Start spawns no health-check goroutines (ListCapabilities is never
	// called) and Stop returns immediately, leaving the monitor in the "stopped" state.
	mon, err := health.NewMonitor(mockBackendClient, nil, health.MonitorConfig{
		CheckInterval:      time.Second,
		UnhealthyThreshold: 1,
		Timeout:            time.Second,
	})
	require.NoError(t, err)
	require.NoError(t, mon.Start(context.Background()))
	require.NoError(t, mon.Stop())

	cfg := testMinimalServeConfig()
	cfg.HealthMonitor = mon

	srv, err := Serve(context.Background(), &stubVMCP{}, cfg)
	require.NoError(t, err)

	cancel, errCh := startServeInBackground(t, srv)

	// Start must not fail; the un-restartable monitor is disabled (set to nil) instead.
	require.Eventually(t, func() bool {
		srv.healthMonitorMu.RLock()
		defer srv.healthMonitorMu.RUnlock()
		return srv.healthMonitor == nil
	}, 2*time.Second, 10*time.Millisecond, "a monitor whose Start fails must be disabled")

	_, getErr := srv.GetBackendHealthStatus("backend-1")
	assert.ErrorContains(t, getErr, "health monitoring is disabled")

	stopAndWait(t, cancel, errCh)
}

// recordingReporter is a vmcpstatus.Reporter that records whether Start and its returned
// shutdown func ran and counts ReportStatus calls, so the status-reporter lifecycle can
// be asserted on the Serve path.
type recordingReporter struct {
	mu             sync.Mutex
	started        bool
	shutdownCalled bool
	reportCount    int
}

func (r *recordingReporter) Start(context.Context) (func(context.Context) error, error) {
	r.mu.Lock()
	r.started = true
	r.mu.Unlock()
	return func(context.Context) error {
		r.mu.Lock()
		r.shutdownCalled = true
		r.mu.Unlock()
		return nil
	}, nil
}

func (r *recordingReporter) ReportStatus(context.Context, *vmcp.Status) error {
	r.mu.Lock()
	r.reportCount++
	r.mu.Unlock()
	return nil
}

func (r *recordingReporter) snapshot() (started, shutdownCalled bool, reportCount int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.started, r.shutdownCalled, r.reportCount
}

// TestServeStartsAndStopsStatusReporter verifies the status-reporter lifecycle on the
// Serve path: Start invokes ServerConfig.StatusReporter.Start, launches
// periodicStatusReporting (at least one report fires), and appends the reporter shutdown
// + the goroutine cancel to shutdownFuncs so both run on Stop.
func TestServeStartsAndStopsStatusReporter(t *testing.T) {
	t.Parallel()

	reporter := &recordingReporter{}
	cfg := testMinimalServeConfig()
	cfg.StatusReporter = reporter
	cfg.StatusReportingInterval = 20 * time.Millisecond

	srv, err := Serve(context.Background(), &stubVMCP{}, cfg)
	require.NoError(t, err)

	cancel, errCh := startServeInBackground(t, srv)

	require.Eventually(t, func() bool {
		started, _, count := reporter.snapshot()
		return started && count >= 1
	}, 2*time.Second, 10*time.Millisecond, "Start must run the reporter and emit at least one status report")

	stopAndWait(t, cancel, errCh)

	_, shutdownCalled, _ := reporter.snapshot()
	assert.True(t, shutdownCalled, "the status reporter shutdown func must run on Stop via shutdownFuncs")
}

// TestServeStopClosesOptimizerStore verifies that the optimizer cleanup returned by
// sessionmanager.New is appended to shutdownFuncs on the Serve path and runs cleanly on
// Stop. Mirrors the New-path TestServerStopClosesOptimizerStore: an empty optimizer
// config builds a (shared in-memory) SQLite store whose Close runs during shutdown.
func TestServeStopClosesOptimizerStore(t *testing.T) {
	t.Parallel()

	cfg := testMinimalServeConfig()
	cfg.SessionManagerConfig = &sessionmanager.FactoryConfig{
		Base:            testMinimalFactory(),
		OptimizerConfig: &optimizer.Config{},
	}

	srv, err := Serve(context.Background(), &stubVMCP{}, cfg)
	require.NoError(t, err)

	cancel, errCh := startServeInBackground(t, srv)

	// Stop must drain shutdownFuncs (including the optimizer store cleanup) without error.
	stopAndWait(t, cancel, errCh)
}

// TestServeWiresAuthServerIntoConfig verifies the embedded AS runner wiring on the Serve
// path: Serve carries ServerConfig.AuthServer into the *Config the shared Handler reads,
// where `if s.config.AuthServer != nil { RegisterHandlers(mux) }` registers the routes.
// The RegisterHandlers code path itself is covered by TestRegisterHandlers in
// pkg/authserver/runner — the concrete *EmbeddedAuthServer cannot be meaningfully
// constructed here (see the note in server_test.go), so this asserts the Serve-specific
// mapping with a zero-value instance rather than building the route mux.
func TestServeWiresAuthServerIntoConfig(t *testing.T) {
	t.Parallel()

	as := &asrunner.EmbeddedAuthServer{}
	cfg := testMinimalServeConfig()
	cfg.AuthServer = as

	srv, err := Serve(context.Background(), &stubVMCP{}, cfg)
	require.NoError(t, err)
	t.Cleanup(func() { _ = srv.Stop(context.Background()) })

	assert.Same(t, as, srv.config.AuthServer,
		"Serve must carry ServerConfig.AuthServer into the Config the shared Handler registers routes from")
}

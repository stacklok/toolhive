// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	asrunner "github.com/stacklok/toolhive/pkg/authserver/runner"
	"github.com/stacklok/toolhive/pkg/vmcp"
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
// become ready, failing fast on early error or timeout. It returns an idempotent stop
// function that cancels the server and waits for Start to return, yielding Start's result
// (Stop's error) so callers can assert a clean shutdown with require.NoError(t, stop()).
//
// stop is also registered with t.Cleanup, so the Start goroutine and the bound HTTP
// listener are always torn down even when a mid-test require aborts via runtime.Goexit
// before the test reaches its explicit stop (testing.md: tie resource teardown for a
// started server in a parallel test to t.Cleanup, not to a code path a failing require can
// skip). The sync.Once lets the explicit call and the cleanup safety net coexist without
// double-draining errCh.
func startServeInBackground(t *testing.T, srv *Server) (stop func() error) {
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

	var once sync.Once
	var stopErr error
	stop = func() error {
		once.Do(func() {
			cancel()
			select {
			case stopErr = <-errCh:
			case <-time.After(2 * time.Second):
				stopErr = errors.New("timeout waiting for server to stop")
			}
		})
		return stopErr
	}
	t.Cleanup(func() { _ = stop() })
	return stop
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

	stop := startServeInBackground(t, srv)

	require.Eventually(t, func() bool {
		started, _, count := reporter.snapshot()
		return started && count >= 1
	}, 2*time.Second, 10*time.Millisecond, "Start must run the reporter and emit at least one status report")

	require.NoError(t, stop())

	_, shutdownCalled, _ := reporter.snapshot()
	assert.True(t, shutdownCalled, "the status reporter shutdown func must run on Stop via shutdownFuncs")
}

// TestServeWithOptimizerStartsAndStopsCleanly exercises the optimizer-configured Serve
// path end to end: a non-nil OptimizerConfig drives sessionmanager.New into building a
// real SQLite-backed optimizer factory whose cleanup (store.Close, not the no-op) Serve
// appends to shutdownFuncs, and Start/Stop runs that construct→teardown path without
// error. The cleanup's store.Close is internal to sessionmanager/optimizer, so it is not
// observed here (asserting it would reach across the package boundary, see testing.md
// "Test Scope"); that shutdownFuncs are drained on Stop is proven observably by
// TestServeStopClosesCore. This test guards that configuring an optimizer does not break
// Serve construction or shutdown — which the no-optimizer path would not exercise.
func TestServeWithOptimizerStartsAndStopsCleanly(t *testing.T) {
	t.Parallel()

	cfg := testMinimalServeConfig()
	cfg.SessionManagerConfig = &sessionmanager.FactoryConfig{
		Base:            testMinimalFactory(),
		OptimizerConfig: &optimizer.Config{},
	}

	srv, err := Serve(context.Background(), &stubVMCP{}, cfg)
	require.NoError(t, err)

	stop := startServeInBackground(t, srv)
	require.NoError(t, stop())
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

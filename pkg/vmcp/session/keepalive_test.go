// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package session

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/metric/noop"

	"github.com/stacklok/toolhive/pkg/vmcp"
	internalbk "github.com/stacklok/toolhive/pkg/vmcp/session/internal/backend"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// fakeBackend is a minimal backend.Session that records Ping calls and can
// be configured to fail.
type fakeBackend struct {
	pingCalls atomic.Int64
	pingErr   error
	pingErrFn func(n int) error // called with call number if non-nil
	mockConnectedBackend
}

func (f *fakeBackend) Ping(_ context.Context) error {
	n := int(f.pingCalls.Add(1))
	if f.pingErrFn != nil {
		return f.pingErrFn(n)
	}
	return f.pingErr
}

func testTarget(method vmcp.KeepaliveMethod) *vmcp.BackendTarget {
	return &vmcp.BackendTarget{
		WorkloadID:      "b1",
		WorkloadName:    "b1",
		BaseURL:         "http://localhost",
		KeepaliveMethod: method,
	}
}

func testMetrics(t *testing.T) *keepaliveMetrics {
	t.Helper()
	mp := noop.NewMeterProvider()
	m, err := newKeepaliveMetrics(mp)
	require.NoError(t, err)
	return m
}

func buildBackendKeepalive(
	conn internalbk.Session,
	target *vmcp.BackendTarget,
	cfg KeepaliveConfig,
) *backendKeepalive {
	return &backendKeepalive{
		backendID: "b1",
		conn:      conn,
		target:    target,
		cfg:       cfg,
	}
}

// ---------------------------------------------------------------------------
// Tests: probe method dispatch
// ---------------------------------------------------------------------------

func TestKeepalive_PingUsedByDefault(t *testing.T) {
	t.Parallel()

	fb := &fakeBackend{}
	bk := buildBackendKeepalive(fb, testTarget(""), KeepaliveConfig{})
	bk.metrics = testMetrics(t)

	ok, reason := bk.probe(context.Background())
	assert.True(t, ok)
	assert.Empty(t, reason)
	assert.Equal(t, int64(1), fb.pingCalls.Load(), "ping should be called when method is empty (default)")
}

func TestKeepalive_ExplicitPing(t *testing.T) {
	t.Parallel()

	fb := &fakeBackend{}
	bk := buildBackendKeepalive(fb, testTarget(vmcp.KeepaliveMethodPing), KeepaliveConfig{})
	bk.metrics = testMetrics(t)

	ok, _ := bk.probe(context.Background())
	assert.True(t, ok)
	assert.Equal(t, int64(1), fb.pingCalls.Load())
}

func TestKeepalive_NoneSkipsProbe(t *testing.T) {
	t.Parallel()

	fb := &fakeBackend{}
	bk := buildBackendKeepalive(fb, testTarget(vmcp.KeepaliveMethodNone), KeepaliveConfig{})
	bk.metrics = testMetrics(t)

	ok, reason := bk.probe(context.Background())
	assert.True(t, ok, "none should always return success (no-op)")
	assert.Empty(t, reason)
	assert.Equal(t, int64(0), fb.pingCalls.Load(), "no ping should be sent when method is none")
}

func TestKeepalive_FallbackToTool(t *testing.T) {
	t.Parallel()

	var callToolCalled atomic.Bool
	fb := &fakeBackend{
		mockConnectedBackend: mockConnectedBackend{
			callToolFunc: func(_ context.Context, _ string, _, _ map[string]any) (*vmcp.ToolCallResult, error) {
				callToolCalled.Store(true)
				return &vmcp.ToolCallResult{}, nil
			},
		},
	}
	target := testTarget(vmcp.KeepaliveMethodTool)
	target.KeepaliveToolName = "health-check"
	bk := buildBackendKeepalive(fb, target, KeepaliveConfig{})
	bk.metrics = testMetrics(t)

	ok, reason := bk.probe(context.Background())
	assert.True(t, ok)
	assert.Empty(t, reason)
	assert.True(t, callToolCalled.Load(), "CallTool should be used when method is tool")
	assert.Equal(t, int64(0), fb.pingCalls.Load(), "ping must not be called when method is tool")
}

func TestKeepalive_ToolFallback_MissingToolName(t *testing.T) {
	t.Parallel()

	fb := &fakeBackend{}
	target := testTarget(vmcp.KeepaliveMethodTool)
	// KeepaliveToolName intentionally not set
	bk := buildBackendKeepalive(fb, target, KeepaliveConfig{})
	bk.metrics = testMetrics(t)

	ok, reason := bk.probe(context.Background())
	assert.False(t, ok)
	assert.Equal(t, "keepalive_tool_name_not_configured", reason)
}

// ---------------------------------------------------------------------------
// Tests: circuit breaker
// ---------------------------------------------------------------------------

func TestKeepalive_CircuitBreaker_TripsAfterNFailures(t *testing.T) {
	t.Parallel()

	pingErr := errors.New("backend gone")
	fb := &fakeBackend{pingErr: pingErr}
	cfg := KeepaliveConfig{MaxFailures: 3, ProbeAfter: time.Hour}
	bk := buildBackendKeepalive(fb, testTarget(vmcp.KeepaliveMethodPing), cfg)
	bk.metrics = testMetrics(t)

	// First two ticks: failures but circuit still open.
	bk.tick(context.Background())
	bk.mu.Lock()
	assert.False(t, bk.state.disabled, "circuit should not trip after 1 failure")
	bk.mu.Unlock()

	bk.tick(context.Background())
	bk.mu.Lock()
	assert.False(t, bk.state.disabled, "circuit should not trip after 2 failures")
	bk.mu.Unlock()

	// Third tick: trips the circuit.
	bk.tick(context.Background())
	bk.mu.Lock()
	disabled := bk.state.disabled
	bk.mu.Unlock()
	assert.True(t, disabled, "circuit should trip after MaxFailures consecutive failures")
}

func TestKeepalive_CircuitBreaker_SkipsDuringQuietWindow(t *testing.T) {
	t.Parallel()

	fb := &fakeBackend{pingErr: errors.New("down")}
	cfg := KeepaliveConfig{MaxFailures: 1, ProbeAfter: time.Hour}
	bk := buildBackendKeepalive(fb, testTarget(vmcp.KeepaliveMethodPing), cfg)
	bk.metrics = testMetrics(t)

	// Trip the circuit.
	bk.tick(context.Background())
	bk.mu.Lock()
	require.True(t, bk.state.disabled)
	bk.mu.Unlock()

	beforeCalls := fb.pingCalls.Load()

	// Another tick while inside the quiet window — must not call probe.
	bk.tick(context.Background())
	assert.Equal(t, beforeCalls, fb.pingCalls.Load(), "ping must not be sent during quiet window")
}

func TestKeepalive_CircuitBreaker_ReEnablesAfterProbeWindow(t *testing.T) {
	t.Parallel()

	// Backend starts down, then recovers on the re-probe.
	fb := &fakeBackend{
		pingErrFn: func(n int) error {
			if n == 1 {
				return errors.New("down")
			}
			return nil // recovered
		},
	}
	// Set ProbeAfter to a tiny value so we can advance state without real sleep.
	cfg := KeepaliveConfig{MaxFailures: 1, ProbeAfter: time.Millisecond}
	bk := buildBackendKeepalive(fb, testTarget(vmcp.KeepaliveMethodPing), cfg)
	bk.metrics = testMetrics(t)

	// Trip the circuit on first tick.
	bk.tick(context.Background())
	bk.mu.Lock()
	require.True(t, bk.state.disabled)
	// Back-date disabledAt so the probe window appears elapsed.
	bk.state.disabledAt = time.Now().Add(-(cfg.ProbeAfter + time.Second))
	bk.mu.Unlock()

	// Second tick: probe window elapsed → re-enable and probe.
	bk.tick(context.Background())
	bk.mu.Lock()
	disabled := bk.state.disabled
	failures := bk.state.consecutiveFailures
	bk.mu.Unlock()

	assert.False(t, disabled, "circuit should re-enable after probe window")
	assert.Equal(t, 0, failures, "consecutive failures should reset after successful probe")
	assert.Equal(t, int64(2), fb.pingCalls.Load(), "exactly two pings should have been sent")
}

// ---------------------------------------------------------------------------
// Tests: KeepaliveManager
// ---------------------------------------------------------------------------

func TestKeepaliveManager_NoneMethodExcludesBackend(t *testing.T) {
	t.Parallel()

	fb := &fakeBackend{}
	target := testTarget(vmcp.KeepaliveMethodNone)
	connections := map[string]internalbk.Session{"b1": fb}
	targets := map[string]*vmcp.BackendTarget{"b1": target}

	km := newKeepaliveManager(connections, targets, KeepaliveConfig{}, testMetrics(t))
	assert.Empty(t, km.backends, "backend with method=none must be excluded from keepalive")
}

func TestKeepaliveManager_StartStop(t *testing.T) {
	t.Parallel()

	fb := &fakeBackend{}
	target := testTarget(vmcp.KeepaliveMethodPing)
	connections := map[string]internalbk.Session{"b1": fb}
	targets := map[string]*vmcp.BackendTarget{"b1": target}

	km := newKeepaliveManager(connections, targets, KeepaliveConfig{Interval: time.Hour}, testMetrics(t))
	km.Start(context.Background())
	km.Stop() // must not hang
}

func TestKeepaliveManager_StopCancelsGoroutines(t *testing.T) {
	t.Parallel()

	fb := &fakeBackend{}
	target := testTarget(vmcp.KeepaliveMethodPing)
	connections := map[string]internalbk.Session{"b1": fb}
	targets := map[string]*vmcp.BackendTarget{"b1": target}

	km := newKeepaliveManager(connections, targets, KeepaliveConfig{Interval: time.Hour}, testMetrics(t))
	km.Start(context.Background())

	done := make(chan struct{})
	go func() {
		km.Stop()
		close(done)
	}()

	select {
	case <-done:
		// passed
	case <-time.After(2 * time.Second):
		t.Fatal("KeepaliveManager.Stop() did not return within 2s")
	}
}

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

	"github.com/stacklok/toolhive/pkg/vmcp"
)

// mockReporter is a test reporter that counts how many times ReportStatus is called.
type mockReporter struct {
	mu         sync.Mutex
	callCount  int
	lastStatus *vmcp.Status
}

func (m *mockReporter) ReportStatus(_ context.Context, status *vmcp.Status) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.callCount++
	m.lastStatus = status
	return nil
}

func (*mockReporter) Start(_ context.Context) (func(context.Context) error, error) {
	return func(_ context.Context) error { return nil }, nil
}

func (m *mockReporter) getCallCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.callCount
}

// TestPeriodicStatusReporting_InvalidInterval tests that invalid intervals are handled gracefully.
func TestPeriodicStatusReporting_InvalidInterval(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		interval time.Duration
	}{
		{
			name:     "zero interval",
			interval: 0,
		},
		{
			name:     "negative interval",
			interval: -1 * time.Second,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			reporter := &mockReporter{}
			server := &Server{}

			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()

			config := StatusReportingConfig{
				Interval: tt.interval,
				Reporter: reporter,
			}

			// Should not panic despite invalid interval
			server.periodicStatusReporting(ctx, config)

			// Should have at least one immediate report
			assert.GreaterOrEqual(t, reporter.getCallCount(), 1,
				"Should report at least once (immediate report)")
		})
	}
}

// TestPeriodicStatusReporting_ValidInterval tests normal operation with valid interval.
func TestPeriodicStatusReporting_ValidInterval(t *testing.T) {
	t.Parallel()

	reporter := &mockReporter{}
	server := &Server{}

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()

	config := StatusReportingConfig{
		Interval: 50 * time.Millisecond,
		Reporter: reporter,
	}

	server.periodicStatusReporting(ctx, config)

	// With 50ms interval and 150ms timeout, we should get at least 3 reports
	// (1 immediate + 2 from ticker)
	count := reporter.getCallCount()
	assert.GreaterOrEqual(t, count, 2, "Should get multiple reports")
}

// TestPeriodicStatusReporting_NilReporter tests that nil reporter is handled gracefully.
func TestPeriodicStatusReporting_NilReporter(t *testing.T) {
	t.Parallel()

	server := &Server{}
	ctx := context.Background()

	config := StatusReportingConfig{
		Interval: 30 * time.Second,
		Reporter: nil,
	}

	// Should return immediately without panic
	server.periodicStatusReporting(ctx, config)
}

// TestDefaultStatusReportingConfig tests the default configuration.
func TestDefaultStatusReportingConfig(t *testing.T) {
	t.Parallel()

	config := DefaultStatusReportingConfig()

	assert.Equal(t, 30*time.Second, config.Interval, "Default interval should be 30s")
	assert.Nil(t, config.Reporter, "Default reporter should be nil")
}

// testDynamicRegistry is a minimal vmcp.DynamicRegistry for testing version-change detection.
type testDynamicRegistry struct {
	mu      sync.Mutex
	version uint64
}

func (r *testDynamicRegistry) Version() uint64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.version
}

func (*testDynamicRegistry) List(_ context.Context) []vmcp.Backend         { return nil }
func (*testDynamicRegistry) Get(_ context.Context, _ string) *vmcp.Backend { return nil }
func (*testDynamicRegistry) Count() int                                    { return 0 }

func (r *testDynamicRegistry) Upsert(_ vmcp.Backend) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.version++
	return nil
}

func (r *testDynamicRegistry) Remove(_ string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.version++
	return nil
}

// TestPeriodicStatusReporting_ReactsToVersionChange verifies that when the backend
// registry version changes, an immediate status report is triggered via the version-polling
// ticker rather than waiting for the full reporting interval.
func TestPeriodicStatusReporting_ReactsToVersionChange(t *testing.T) {
	t.Parallel()

	// Speed up the version-polling ticker so the test completes in milliseconds.
	orig := versionPollInterval
	versionPollInterval = 10 * time.Millisecond
	t.Cleanup(func() { versionPollInterval = orig })

	reporter := &mockReporter{}
	reg := &testDynamicRegistry{}
	server := &Server{
		backendRegistry: reg,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	// Use a long interval so the periodic tick never fires during the test.
	config := StatusReportingConfig{
		Interval: 30 * time.Second,
		Reporter: reporter,
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		server.periodicStatusReporting(ctx, config)
	}()

	// Wait for the initial immediate report before triggering a version change.
	require.Eventually(t, func() bool {
		return reporter.getCallCount() >= 1
	}, time.Second, 5*time.Millisecond, "expected initial immediate status report")

	countAfterInit := reporter.getCallCount()

	// Trigger a version bump to simulate a backend being removed from the registry.
	require.NoError(t, reg.Remove("some-backend"))

	// The version-polling ticker fires every 10ms in tests; allow up to 200ms.
	require.Eventually(t, func() bool {
		return reporter.getCallCount() > countAfterInit
	}, 200*time.Millisecond, 5*time.Millisecond,
		"version change should trigger an immediate status report without waiting for the 30s interval")

	cancel()
	<-done
}

// TestReportStatus tests the reportStatus method.
func TestReportStatus(t *testing.T) {
	t.Parallel()

	reporter := &mockReporter{}
	server := &Server{}

	ctx := context.Background()

	// Test with no health monitor
	server.reportStatus(ctx, reporter)

	require.Equal(t, 1, reporter.getCallCount())
	require.NotNil(t, reporter.lastStatus)
	assert.Equal(t, vmcp.PhaseReady, reporter.lastStatus.Phase)
	assert.Equal(t, "Health monitoring disabled", reporter.lastStatus.Message)
}

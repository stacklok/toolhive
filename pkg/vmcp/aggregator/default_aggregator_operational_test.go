// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package aggregator

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/config"
	"github.com/stacklok/toolhive/pkg/vmcp/mocks"
)

// TestDefaultAggregator_QueryAllCapabilities_FailMode pins the contract of
// operational.failureHandling.partialFailureMode=fail: when any backend query
// fails, QueryAllCapabilities must surface an error instead of silently
// continuing with the remaining backends (the pre-wiring best-effort behavior).
func TestDefaultAggregator_QueryAllCapabilities_FailMode(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockClient := mocks.NewMockBackendClient(ctrl)
	backends := []vmcp.Backend{
		newTestBackend("backend1"),
		newTestBackend("backend2"),
	}

	caps1 := newTestCapabilityList(withTools(newTestTool("tool1", "backend1")))

	mockClient.EXPECT().ListCapabilities(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, target *vmcp.BackendTarget) (*vmcp.CapabilityList, error) {
			if target.WorkloadID == "backend1" {
				return caps1, nil
			}
			return nil, errors.New("connection timeout")
		}).Times(2)

	agg := NewDefaultAggregator(mockClient, nil, nil, nil,
		WithOperationalConfig(&config.OperationalConfig{
			FailureHandling: &config.FailureHandlingConfig{
				PartialFailureMode: "fail",
			},
		}))

	result, err := agg.QueryAllCapabilities(context.Background(), backends)
	require.Error(t, err)
	assert.Nil(t, result)
	assert.ErrorIs(t, err, ErrBackendQueryFailed)
}

// TestDefaultAggregator_QueryAllCapabilities_FailModeCancelsInflight pins the
// fail-fast contract of partialFailureMode=fail at the goroutine level: when one
// backend query fails, the errgroup must cancel the derived context so the
// remaining in-flight backend queries stop immediately instead of running to the
// outer deadline. backend2 blocks on ctx.Done() with no per-query timeout
// configured, so only errgroup cancellation can unblock it. Under a regression
// that collects the error instead of returning it, backend2 stays blocked until
// the outer deadline, blowing the elapsed upper bound below.
func TestDefaultAggregator_QueryAllCapabilities_FailModeCancelsInflight(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockClient := mocks.NewMockBackendClient(ctrl)
	backends := []vmcp.Backend{
		newTestBackend("backend1"),
		newTestBackend("backend2"),
	}

	// backend1 fails immediately; backend2 blocks until the derived context is
	// cancelled. No timeouts are configured, so nothing but fail-fast
	// cancellation can unblock backend2.
	mockClient.EXPECT().ListCapabilities(gomock.Any(), gomock.Any()).
		DoAndReturn(func(ctx context.Context, target *vmcp.BackendTarget) (*vmcp.CapabilityList, error) {
			if target.WorkloadID == "backend1" {
				return nil, errors.New("backend1 unavailable")
			}
			<-ctx.Done()
			return nil, ctx.Err()
		}).Times(2)

	agg := NewDefaultAggregator(mockClient, nil, nil, nil,
		WithOperationalConfig(&config.OperationalConfig{
			FailureHandling: &config.FailureHandlingConfig{
				PartialFailureMode: "fail",
			},
		}))

	// The outer deadline is only a safety net so the test cannot hang forever
	// under a regression: with correct fail-fast wiring backend2 unblocks in
	// milliseconds, far below the 2s upper bound (and well under the 3s net).
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	start := time.Now()
	result, err := agg.QueryAllCapabilities(ctx, backends)
	elapsed := time.Since(start)

	require.Error(t, err)
	assert.Nil(t, result)
	assert.Less(t, elapsed, 2*time.Second,
		"fail-fast must cancel in-flight queries; backend2 blocked until the outer deadline")
}

// TestDefaultAggregator_QueryAllCapabilities_BestEffortMode pins the contract of
// operational.failureHandling.partialFailureMode=best_effort: a failing backend
// is logged and skipped, and the capabilities of the healthy backends are
// returned.
func TestDefaultAggregator_QueryAllCapabilities_BestEffortMode(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockClient := mocks.NewMockBackendClient(ctrl)
	backends := []vmcp.Backend{
		newTestBackend("backend1"),
		newTestBackend("backend2"),
	}

	caps1 := newTestCapabilityList(withTools(newTestTool("tool1", "backend1")))

	mockClient.EXPECT().ListCapabilities(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, target *vmcp.BackendTarget) (*vmcp.CapabilityList, error) {
			if target.WorkloadID == "backend1" {
				return caps1, nil
			}
			return nil, errors.New("connection timeout")
		}).Times(2)

	agg := NewDefaultAggregator(mockClient, nil, nil, nil,
		WithOperationalConfig(&config.OperationalConfig{
			FailureHandling: &config.FailureHandlingConfig{
				PartialFailureMode: "best_effort",
			},
		}))

	result, err := agg.QueryAllCapabilities(context.Background(), backends)
	require.NoError(t, err)
	require.Len(t, result, 1)
	assert.Contains(t, result, "backend1")
	assert.NotContains(t, result, "backend2")
}

// TestDefaultAggregator_QueryAllCapabilities_TimeoutPropagation pins the contract
// of operational.timeouts: each backend query runs under a context whose deadline
// reflects the per-backend timeout (the default, or the perWorkload override).
func TestDefaultAggregator_QueryAllCapabilities_TimeoutPropagation(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockClient := mocks.NewMockBackendClient(ctrl)
	backends := []vmcp.Backend{
		newTestBackend("backend1"),
		newTestBackend("backend2"),
	}

	caps1 := newTestCapabilityList(withTools(newTestTool("tool1", "backend1")))
	caps2 := newTestCapabilityList(withTools(newTestTool("tool2", "backend2")))

	var mu sync.Mutex
	observed := make(map[string]time.Duration)
	recordDeadline := func(backendID string, ctx context.Context) {
		deadline, ok := ctx.Deadline()
		if !ok {
			return
		}
		mu.Lock()
		observed[backendID] = time.Until(deadline)
		mu.Unlock()
	}

	mockClient.EXPECT().ListCapabilities(gomock.Any(), gomock.Any()).
		DoAndReturn(func(ctx context.Context, target *vmcp.BackendTarget) (*vmcp.CapabilityList, error) {
			recordDeadline(target.WorkloadID, ctx)
			if target.WorkloadID == "backend1" {
				return caps1, nil
			}
			return caps2, nil
		}).Times(2)

	agg := NewDefaultAggregator(mockClient, nil, nil, nil,
		WithOperationalConfig(&config.OperationalConfig{
			Timeouts: &config.TimeoutConfig{
				Default:     config.Duration(100 * time.Millisecond),
				PerWorkload: map[string]config.Duration{"backend2": config.Duration(200 * time.Millisecond)},
			},
		}))

	result, err := agg.QueryAllCapabilities(context.Background(), backends)
	require.NoError(t, err)
	require.Len(t, result, 2)

	want := map[string]time.Duration{
		"backend1": 100 * time.Millisecond,
		"backend2": 200 * time.Millisecond,
	}
	mu.Lock()
	defer mu.Unlock()
	for backendID, wantTimeout := range want {
		got, ok := observed[backendID]
		require.Truef(t, ok, "no query deadline was recorded for backend %s", backendID)
		assert.GreaterOrEqual(t, got, wantTimeout-50*time.Millisecond,
			"deadline for %s must not be shorter than the configured timeout", backendID)
		assert.LessOrEqual(t, got, wantTimeout+50*time.Millisecond,
			"deadline for %s must not exceed the configured timeout", backendID)
	}
}

// TestDefaultAggregator_QueryAllCapabilities_FailModeWithTimeout pins the combined
// contract of partialFailureMode=fail and a per-backend timeout: a backend that
// hangs past its deadline is failed fast and the error surfaces (wrapping
// context.DeadlineExceeded) rather than blocking the whole aggregation.
func TestDefaultAggregator_QueryAllCapabilities_FailModeWithTimeout(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockClient := mocks.NewMockBackendClient(ctrl)
	backends := []vmcp.Backend{
		newTestBackend("backend1"),
		newTestBackend("backend2"),
	}

	caps1 := newTestCapabilityList(withTools(newTestTool("tool1", "backend1")))

	// backend1 answers immediately; backend2 blocks until its per-query
	// deadline fires and then reports the deadline error. Waiting on
	// ctx.Done() keeps the test deterministic (no wall-clock sleeps).
	mockClient.EXPECT().ListCapabilities(gomock.Any(), gomock.Any()).
		DoAndReturn(func(ctx context.Context, target *vmcp.BackendTarget) (*vmcp.CapabilityList, error) {
			if target.WorkloadID == "backend1" {
				return caps1, nil
			}
			<-ctx.Done()
			return nil, ctx.Err()
		}).Times(2)

	agg := NewDefaultAggregator(mockClient, nil, nil, nil,
		WithOperationalConfig(&config.OperationalConfig{
			FailureHandling: &config.FailureHandlingConfig{
				PartialFailureMode: "fail",
			},
			Timeouts: &config.TimeoutConfig{
				Default: config.Duration(10 * time.Millisecond),
			},
		}))

	// The outer deadline is only a safety net so the test cannot hang forever
	// under a regression: with the timeout wired, backend2 unblocks after ~10ms
	// and the aggregation fails fast. The elapsed upper bound below
	// distinguishes that from a regression where the per-backend timeout is not
	// wired and only the outer deadline unblocks backend2 (~3s).
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	start := time.Now()
	result, err := agg.QueryAllCapabilities(ctx, backends)
	require.Error(t, err)
	assert.Nil(t, result)
	assert.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Less(t, time.Since(start), 1*time.Second,
		"per-backend timeout must fire in ~10ms, not the outer deadline")
}

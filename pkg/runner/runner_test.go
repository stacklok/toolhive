// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package runner

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	rt "github.com/stacklok/toolhive/pkg/container/runtime"
	"github.com/stacklok/toolhive/pkg/transport/types"
	statusesmocks "github.com/stacklok/toolhive/pkg/workloads/statuses/mocks"
)

const testServerName = "test-server"

func TestNewRunner(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockStatusManager := statusesmocks.NewMockStatusManager(ctrl)

	runConfig := NewRunConfig()
	runConfig.Name = testServerName
	runConfig.Port = 8080

	runner := NewRunner(runConfig, mockStatusManager)

	require.NotNil(t, runner)
	assert.Equal(t, runConfig, runner.Config)
	assert.NotNil(t, runner.supportedMiddleware)
}

func TestRunner_AddMiddleware(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockStatusManager := statusesmocks.NewMockStatusManager(ctrl)

	runConfig := NewRunConfig()
	runner := NewRunner(runConfig, mockStatusManager)

	// Create a mock middleware
	mockMiddleware := &mockMiddlewareImpl{
		handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		}),
	}

	runner.AddMiddleware("test-middleware", mockMiddleware)

	assert.Len(t, runner.middlewares, 1)
	assert.Len(t, runner.namedMiddlewares, 1)
	assert.Equal(t, "test-middleware", runner.namedMiddlewares[0].Name)
}

func TestRunner_SetAuthInfoHandler(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockStatusManager := statusesmocks.NewMockStatusManager(ctrl)

	runConfig := NewRunConfig()
	runner := NewRunner(runConfig, mockStatusManager)

	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	runner.SetAuthInfoHandler(handler)

	assert.NotNil(t, runner.authInfoHandler)
}

func TestRunner_SetPrometheusHandler(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockStatusManager := statusesmocks.NewMockStatusManager(ctrl)

	runConfig := NewRunConfig()
	runner := NewRunner(runConfig, mockStatusManager)

	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	runner.SetPrometheusHandler(handler)

	assert.NotNil(t, runner.prometheusHandler)
}

func TestRunner_GetConfig(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockStatusManager := statusesmocks.NewMockStatusManager(ctrl)

	runConfig := NewRunConfig()
	runConfig.Name = testServerName
	runConfig.Port = 9090

	runner := NewRunner(runConfig, mockStatusManager)

	config := runner.GetConfig()

	require.NotNil(t, config)
	assert.Equal(t, testServerName, config.GetName())
	assert.Equal(t, 9090, config.GetPort())
}

func TestRunConfig_GetName(t *testing.T) {
	t.Parallel()

	runConfig := NewRunConfig()
	runConfig.Name = "my-server"

	assert.Equal(t, "my-server", runConfig.GetName())
}

func TestRunConfig_GetPort(t *testing.T) {
	t.Parallel()

	runConfig := NewRunConfig()
	runConfig.Port = 12345

	assert.Equal(t, 12345, runConfig.GetPort())
}

func TestRunner_Cleanup(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockStatusManager := statusesmocks.NewMockStatusManager(ctrl)

	runConfig := NewRunConfig()
	runner := NewRunner(runConfig, mockStatusManager)

	// Add a mock middleware that closes successfully
	mockMiddleware := &mockMiddlewareImpl{
		handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		}),
		closeErr: nil,
	}
	runner.middlewares = append(runner.middlewares, mockMiddleware)

	// Set up monitoring cancel function
	ctx, cancel := context.WithCancel(context.Background())
	runner.monitoringCtx = ctx
	runner.monitoringCancel = cancel

	err := runner.Cleanup(context.Background())
	assert.NoError(t, err)

	// Verify monitoring was cancelled
	assert.Nil(t, runner.monitoringCancel)
}

func TestRunner_CleanupWithMiddlewareError(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockStatusManager := statusesmocks.NewMockStatusManager(ctrl)

	runConfig := NewRunConfig()
	runner := NewRunner(runConfig, mockStatusManager)

	// Add a mock middleware that returns an error on close
	mockMiddleware := &mockMiddlewareImpl{
		handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		}),
		closeErr: assert.AnError,
	}
	runner.middlewares = append(runner.middlewares, mockMiddleware)

	err := runner.Cleanup(context.Background())
	assert.Error(t, err)
}

func TestStatusManagerAdapter_SetWorkloadStatus(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockStatusManager := statusesmocks.NewMockStatusManager(ctrl)
	mockStatusManager.EXPECT().
		SetWorkloadStatus(gomock.Any(), "test-workload", rt.WorkloadStatusRunning, "test reason").
		Return(nil)

	adapter := &statusManagerAdapter{sm: mockStatusManager}

	err := adapter.SetWorkloadStatus(
		context.Background(),
		"test-workload",
		rt.WorkloadStatusRunning,
		"test reason",
	)
	assert.NoError(t, err)
}

func TestWaitForInitializeSuccess(t *testing.T) {
	t.Parallel()

	t.Run("Streamable HTTP success", func(t *testing.T) {
		t.Parallel()

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodPost {
				w.WriteHeader(http.StatusOK)
				return
			}
			w.WriteHeader(http.StatusMethodNotAllowed)
		}))
		defer server.Close()

		ctx := context.Background()
		err := waitForInitializeSuccess(ctx, server.URL, "streamable-http", 5*time.Second)
		assert.NoError(t, err)
	})

	t.Run("Streamable success (alias)", func(t *testing.T) {
		t.Parallel()

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodPost {
				w.WriteHeader(http.StatusOK)
				return
			}
			w.WriteHeader(http.StatusMethodNotAllowed)
		}))
		defer server.Close()

		ctx := context.Background()
		err := waitForInitializeSuccess(ctx, server.URL, "streamable", 5*time.Second)
		assert.NoError(t, err)
	})

	t.Run("SSE success", func(t *testing.T) {
		t.Parallel()

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodGet {
				w.WriteHeader(http.StatusOK)
				return
			}
			w.WriteHeader(http.StatusMethodNotAllowed)
		}))
		defer server.Close()

		ctx := context.Background()
		err := waitForInitializeSuccess(ctx, server.URL+"#container-name", "sse", 5*time.Second)
		assert.NoError(t, err)
	})

	t.Run("Unknown transport skips check", func(t *testing.T) {
		t.Parallel()

		ctx := context.Background()
		err := waitForInitializeSuccess(ctx, "http://localhost:9999", "unknown-transport", 5*time.Second)
		assert.NoError(t, err)
	})

	t.Run("Timeout on server not ready", func(t *testing.T) {
		t.Parallel()

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusServiceUnavailable)
		}))
		defer server.Close()

		ctx := context.Background()
		err := waitForInitializeSuccess(ctx, server.URL, "streamable-http", 500*time.Millisecond)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "initialize not successful")
	})

	t.Run("Context cancelled", func(t *testing.T) {
		t.Parallel()

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusServiceUnavailable)
		}))
		defer server.Close()

		ctx, cancel := context.WithCancel(context.Background())
		cancel() // Cancel immediately

		err := waitForInitializeSuccess(ctx, server.URL, "streamable-http", 5*time.Second)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "context cancelled")
	})
}

func TestHandleRemoteAuthentication(t *testing.T) {
	t.Parallel()

	t.Run("Nil remote auth config returns nil", func(t *testing.T) {
		t.Parallel()

		ctrl := gomock.NewController(t)
		t.Cleanup(func() { ctrl.Finish() })

		mockStatusManager := statusesmocks.NewMockStatusManager(ctrl)

		runConfig := NewRunConfig()
		runConfig.RemoteAuthConfig = nil

		runner := NewRunner(runConfig, mockStatusManager)

		tokenSource, err := runner.handleRemoteAuthentication(context.Background())
		assert.NoError(t, err)
		assert.Nil(t, tokenSource)
	})
}

// mockMiddlewareImpl is a mock implementation of the types.Middleware interface
type mockMiddlewareImpl struct {
	handler  http.Handler
	closeErr error
}

func (m *mockMiddlewareImpl) Handler() types.MiddlewareFunction {
	return func(_ http.Handler) http.Handler {
		return m.handler
	}
}

func (m *mockMiddlewareImpl) Close() error {
	return m.closeErr
}

// Test_monitorTransport tests the monitorTransport helper function that is used by
// Runner.Run() to detect when a transport stops running.
//
// This test verifies the contract between Runner and Transport:
// 1. monitorTransport polls the isRunning function periodically
// 2. When isRunning() returns false, it closes the returned channel
// 3. This triggers the restart flow ("container exited, restart needed")
func Test_monitorTransport(t *testing.T) {
	t.Parallel()

	t.Run("detects transport stopped and closes channel", func(t *testing.T) {
		t.Parallel()

		callCount := 0
		isRunning := func(_ context.Context) (bool, error) {
			callCount++
			if callCount <= 2 {
				return true, nil // First two calls: still running
			}
			return false, nil // Third call: stopped
		}

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		// Use the actual monitorTransport function
		transportStoppedCh := monitorTransport(ctx, isRunning, 10*time.Millisecond)

		// Wait for the monitoring loop to detect the transport stopped
		select {
		case <-transportStoppedCh:
			// Success - monitoring loop detected transport stopped
			assert.GreaterOrEqual(t, callCount, 3, "Should have polled IsRunning at least 3 times")
		case <-time.After(1 * time.Second):
			t.Fatal("monitorTransport should have detected transport stopped")
		}
	})

	t.Run("continues polling on IsRunning error", func(t *testing.T) {
		t.Parallel()

		callCount := 0
		isRunning := func(_ context.Context) (bool, error) {
			callCount++
			if callCount <= 2 {
				return false, assert.AnError // First two calls: error
			}
			return false, nil // Third call: actually stopped
		}

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		// Use the actual monitorTransport function
		transportStoppedCh := monitorTransport(ctx, isRunning, 10*time.Millisecond)

		select {
		case <-transportStoppedCh:
			// Success - continued polling despite errors
			assert.GreaterOrEqual(t, callCount, 3, "Should have continued polling after errors")
		case <-time.After(1 * time.Second):
			t.Fatal("monitorTransport should have continued after errors and detected stopped")
		}
	})

	t.Run("respects context cancellation", func(t *testing.T) {
		t.Parallel()

		isRunning := func(_ context.Context) (bool, error) {
			return true, nil // Always running
		}

		ctx, cancel := context.WithCancel(context.Background())

		transportStoppedCh := monitorTransport(ctx, isRunning, 10*time.Millisecond)

		// Cancel the context
		cancel()

		// The channel should NOT be closed (transport is still running)
		// Give it a moment to process
		time.Sleep(50 * time.Millisecond)

		select {
		case <-transportStoppedCh:
			t.Fatal("monitorTransport should not close channel when context is cancelled (transport still running)")
		default:
			// Expected - channel is not closed
		}
	})
}

// TestHandleTransportStopped tests the handleTransportStopped function which handles
// the entire transport stopped event, including cleanup and restart decision.
func TestHandleTransportStopped(t *testing.T) {
	t.Parallel()

	// noopDeps returns deps with no-op functions for fields we don't care about in a test
	noopDeps := func() transportStoppedDeps {
		return transportStoppedDeps{
			removePIDFile:     func() error { return nil },
			resetWorkloadPID:  func(_ context.Context) error { return nil },
			workloadExists:    func(_ context.Context) (bool, error) { return true, nil },
			removeFromClients: func(_ context.Context) error { return nil },
		}
	}

	t.Run("returns restart error when workload exists", func(t *testing.T) {
		t.Parallel()

		deps := noopDeps()
		deps.workloadExists = func(_ context.Context) (bool, error) {
			return true, nil
		}

		err := handleTransportStopped(context.Background(), deps)

		require.Error(t, err)
		assert.Contains(t, err.Error(), "container exited, restart needed")
	})

	t.Run("returns nil when workload does not exist", func(t *testing.T) {
		t.Parallel()

		deps := noopDeps()
		deps.workloadExists = func(_ context.Context) (bool, error) {
			return false, nil
		}

		err := handleTransportStopped(context.Background(), deps)

		assert.NoError(t, err, "Should return nil when workload was removed")
	})

	t.Run("returns restart error when existence check fails", func(t *testing.T) {
		t.Parallel()

		deps := noopDeps()
		deps.workloadExists = func(_ context.Context) (bool, error) {
			return false, assert.AnError
		}

		err := handleTransportStopped(context.Background(), deps)

		require.Error(t, err)
		assert.Contains(t, err.Error(), "container exited, restart needed")
	})

	t.Run("calls removePIDFile", func(t *testing.T) {
		t.Parallel()

		called := false
		deps := noopDeps()
		deps.removePIDFile = func() error {
			called = true
			return nil
		}

		_ = handleTransportStopped(context.Background(), deps)

		assert.True(t, called, "Should call removePIDFile")
	})

	t.Run("calls resetWorkloadPID", func(t *testing.T) {
		t.Parallel()

		called := false
		deps := noopDeps()
		deps.resetWorkloadPID = func(_ context.Context) error {
			called = true
			return nil
		}

		_ = handleTransportStopped(context.Background(), deps)

		assert.True(t, called, "Should call resetWorkloadPID")
	})

	t.Run("calls removeFromClients when workload does not exist", func(t *testing.T) {
		t.Parallel()

		called := false
		deps := noopDeps()
		deps.workloadExists = func(_ context.Context) (bool, error) {
			return false, nil
		}
		deps.removeFromClients = func(_ context.Context) error {
			called = true
			return nil
		}

		_ = handleTransportStopped(context.Background(), deps)

		assert.True(t, called, "Should call removeFromClients when workload does not exist")
	})

	t.Run("does not call removeFromClients when workload exists", func(t *testing.T) {
		t.Parallel()

		called := false
		deps := noopDeps()
		deps.workloadExists = func(_ context.Context) (bool, error) {
			return true, nil
		}
		deps.removeFromClients = func(_ context.Context) error {
			called = true
			return nil
		}

		_ = handleTransportStopped(context.Background(), deps)

		assert.False(t, called, "Should not call removeFromClients when workload exists")
	})
}

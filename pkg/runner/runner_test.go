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

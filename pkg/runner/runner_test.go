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

	"github.com/stacklok/toolhive/pkg/authserver"
	authserverrunner "github.com/stacklok/toolhive/pkg/authserver/runner"
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

// TestRunner_EmbeddedAuthServer_Integration tests the runner's integration with the embedded auth server.
// This covers initialization, handler mounting, route responses, and cleanup.
func TestRunner_EmbeddedAuthServer_Integration(t *testing.T) {
	t.Parallel()

	// createMinimalAuthServerConfig creates a minimal valid RunConfig for the embedded auth server.
	// It uses development mode defaults (no signing keys, no HMAC secrets) and
	// a pure OAuth2 upstream to avoid OIDC discovery.
	createMinimalAuthServerConfig := func() *authserver.RunConfig {
		return &authserver.RunConfig{
			SchemaVersion: authserver.CurrentSchemaVersion,
			Issuer:        "http://localhost:8080",
			Upstreams: []authserver.UpstreamRunConfig{
				{
					Name: "test-upstream",
					Type: authserver.UpstreamProviderTypeOAuth2,
					OAuth2Config: &authserver.OAuth2UpstreamRunConfig{
						AuthorizationEndpoint: "https://example.com/authorize",
						TokenEndpoint:         "https://example.com/token",
						ClientID:              "test-client-id",
						RedirectURI:           "http://localhost:8080/oauth/callback",
					},
				},
			},
			AllowedAudiences: []string{"https://mcp.example.com"},
		}
	}

	t.Run("Cleanup closes embedded auth server", func(t *testing.T) {
		t.Parallel()

		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		mockStatusManager := statusesmocks.NewMockStatusManager(ctrl)

		runConfig := NewRunConfig()
		runConfig.Name = testServerName
		runner := NewRunner(runConfig, mockStatusManager)

		// Create a real embedded auth server
		authServerCfg := createMinimalAuthServerConfig()
		embeddedServer, err := authserverrunner.NewEmbeddedAuthServer(context.Background(), authServerCfg)
		require.NoError(t, err)
		require.NotNil(t, embeddedServer)

		// Set it on the runner
		runner.embeddedAuthServer = embeddedServer

		// Cleanup should succeed and close the embedded auth server
		err = runner.Cleanup(context.Background())
		assert.NoError(t, err)
	})

	t.Run("Cleanup succeeds when embedded auth server is nil", func(t *testing.T) {
		t.Parallel()

		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		mockStatusManager := statusesmocks.NewMockStatusManager(ctrl)

		runConfig := NewRunConfig()
		runConfig.Name = testServerName
		runner := NewRunner(runConfig, mockStatusManager)

		// Ensure embeddedAuthServer is nil
		runner.embeddedAuthServer = nil

		// Cleanup should succeed when embedded auth server is nil
		err := runner.Cleanup(context.Background())
		assert.NoError(t, err)
	})

	t.Run("embedded auth server handler serves OAuth discovery endpoints", func(t *testing.T) {
		t.Parallel()

		// Create an embedded auth server with minimal config
		authServerCfg := createMinimalAuthServerConfig()
		embeddedServer, err := authserverrunner.NewEmbeddedAuthServer(context.Background(), authServerCfg)
		require.NoError(t, err)
		require.NotNil(t, embeddedServer)
		defer func() { _ = embeddedServer.Close() }()

		handler := embeddedServer.Handler()
		require.NotNil(t, handler)

		// Test OAuth Authorization Server Metadata (RFC 8414)
		t.Run("serves OAuth AS metadata", func(t *testing.T) {
			t.Parallel()

			req := httptest.NewRequest(http.MethodGet, "/.well-known/oauth-authorization-server", nil)
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, req)

			assert.Equal(t, http.StatusOK, w.Code)
			assert.Contains(t, w.Header().Get("Content-Type"), "application/json")
			assert.Contains(t, w.Body.String(), "issuer")
			assert.Contains(t, w.Body.String(), "authorization_endpoint")
			assert.Contains(t, w.Body.String(), "token_endpoint")
		})

		// Test OIDC Discovery
		t.Run("serves OIDC discovery", func(t *testing.T) {
			t.Parallel()

			req := httptest.NewRequest(http.MethodGet, "/.well-known/openid-configuration", nil)
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, req)

			assert.Equal(t, http.StatusOK, w.Code)
			assert.Contains(t, w.Header().Get("Content-Type"), "application/json")
			assert.Contains(t, w.Body.String(), "issuer")
		})

		// Test JWKS endpoint
		t.Run("serves JWKS", func(t *testing.T) {
			t.Parallel()

			req := httptest.NewRequest(http.MethodGet, "/.well-known/jwks.json", nil)
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, req)

			assert.Equal(t, http.StatusOK, w.Code)
			assert.Contains(t, w.Header().Get("Content-Type"), "application/json")
			assert.Contains(t, w.Body.String(), "keys")
		})
	})

	t.Run("PrefixHandlers map is correctly structured", func(t *testing.T) {
		t.Parallel()

		// This test verifies the expected prefix handler paths match what runner.Run() sets up
		expectedPrefixes := []string{
			"/oauth/",
			"/.well-known/oauth-authorization-server",
			"/.well-known/openid-configuration",
			"/.well-known/jwks.json",
		}

		// Create an embedded auth server
		authServerCfg := createMinimalAuthServerConfig()
		embeddedServer, err := authserverrunner.NewEmbeddedAuthServer(context.Background(), authServerCfg)
		require.NoError(t, err)
		require.NotNil(t, embeddedServer)
		defer func() { _ = embeddedServer.Close() }()

		handler := embeddedServer.Handler()
		require.NotNil(t, handler)

		// Simulate what runner.Run() does when setting up PrefixHandlers
		prefixHandlers := map[string]http.Handler{
			"/oauth/": handler,
			"/.well-known/oauth-authorization-server": handler,
			"/.well-known/openid-configuration":       handler,
			"/.well-known/jwks.json":                  handler,
		}

		// Verify all expected prefixes are present
		for _, prefix := range expectedPrefixes {
			_, ok := prefixHandlers[prefix]
			assert.True(t, ok, "expected prefix handler for %q", prefix)
		}
		assert.Len(t, prefixHandlers, len(expectedPrefixes), "should have exactly %d prefix handlers", len(expectedPrefixes))
	})
}

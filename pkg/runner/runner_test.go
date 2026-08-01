// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package runner

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/adrg/xdg"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/auth/remote"
	"github.com/stacklok/toolhive/pkg/auth/upstreamtoken"
	"github.com/stacklok/toolhive/pkg/authserver"
	authserverrunner "github.com/stacklok/toolhive/pkg/authserver/runner"
	storagemocks "github.com/stacklok/toolhive/pkg/authserver/storage/mocks"
	rt "github.com/stacklok/toolhive/pkg/container/runtime"
	"github.com/stacklok/toolhive/pkg/secrets"
	secretsmocks "github.com/stacklok/toolhive/pkg/secrets/mocks"
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
		err := waitForInitializeSuccess(ctx, server.URL, "streamable-http", false, 5*time.Second)
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
		err := waitForInitializeSuccess(ctx, server.URL, "streamable", false, 5*time.Second)
		assert.NoError(t, err)
	})

	t.Run("Streamable sends pinned probe protocol version in body only", func(t *testing.T) {
		t.Parallel()

		var (
			mu             sync.Mutex
			capturedBody   string
			capturedHeader []string
			headerPresent  bool
		)

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodPost {
				body, err := io.ReadAll(r.Body)
				require.NoError(t, err)

				mu.Lock()
				capturedBody = string(body)
				capturedHeader, headerPresent = r.Header["Mcp-Protocol-Version"]
				mu.Unlock()

				w.WriteHeader(http.StatusOK)
				return
			}
			w.WriteHeader(http.StatusMethodNotAllowed)
		}))
		t.Cleanup(server.Close)

		ctx := context.Background()
		err := waitForInitializeSuccess(ctx, server.URL, "streamable-http", false, 5*time.Second)
		require.NoError(t, err)

		mu.Lock()
		defer mu.Unlock()
		// Assert against an independent literal (not probeProtocolVersion) so that
		// changing the pin forces a conscious test update — the const is deliberately
		// pinned and must not silently track mcp.LATEST_PROTOCOL_VERSION.
		assert.Contains(t, capturedBody, `"protocolVersion":"2025-11-25"`)
		assert.NotContains(t, capturedBody, "2024-11-05")
		// The probe intentionally does not advertise the 2026-07-28 revision, which
		// removes the initialize method the probe relies on.
		assert.NotContains(t, capturedBody, "2026-07-28")
		// The MCP-Protocol-Version header is intentionally NOT sent on the initialize
		// request: it is scoped to post-initialize requests and a server MUST 400 an
		// unsupported value, which would falsely mark an older backend as not-ready.
		assert.False(t, headerPresent, "initialize probe must not send an MCP-Protocol-Version header, got %q", capturedHeader)
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
		err := waitForInitializeSuccess(ctx, server.URL+"#container-name", "sse", false, 5*time.Second)
		assert.NoError(t, err)
	})

	t.Run("Unknown transport skips check", func(t *testing.T) {
		t.Parallel()

		ctx := context.Background()
		err := waitForInitializeSuccess(ctx, "http://localhost:9999", "unknown-transport", false, 5*time.Second)
		assert.NoError(t, err)
	})

	t.Run("Timeout on server not ready", func(t *testing.T) {
		t.Parallel()

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusServiceUnavailable)
		}))
		defer server.Close()

		ctx := context.Background()
		err := waitForInitializeSuccess(ctx, server.URL, "streamable-http", false, 500*time.Millisecond)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "initialize not successful")
	})

	t.Run("Auth expected accepts 401 as ready", func(t *testing.T) {
		t.Parallel()

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
		}))
		t.Cleanup(server.Close)

		ctx := context.Background()
		err := waitForInitializeSuccess(ctx, server.URL, "streamable-http", true, 5*time.Second)
		assert.NoError(t, err)
	})

	t.Run("Context cancelled", func(t *testing.T) {
		t.Parallel()

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusServiceUnavailable)
		}))
		defer server.Close()

		ctx, cancel := context.WithCancel(context.Background())
		cancel() // Cancel immediately

		err := waitForInitializeSuccess(ctx, server.URL, "streamable-http", false, 5*time.Second)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "context cancelled")
	})
}

func TestIsReadyStatus(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		statusCode   int
		authExpected bool
		want         bool
	}{
		{"200 ready without auth", http.StatusOK, false, true},
		{"200 ready with auth", http.StatusOK, true, true},
		{"401 not ready without auth", http.StatusUnauthorized, false, false},
		{"401 ready with auth", http.StatusUnauthorized, true, true},
		{"403 not ready without auth", http.StatusForbidden, false, false},
		{"403 ready with auth", http.StatusForbidden, true, true},
		{"500 not ready without auth", http.StatusInternalServerError, false, false},
		{"500 not ready with auth", http.StatusInternalServerError, true, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, isReadyStatus(tt.statusCode, tt.authExpected))
		})
	}
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

//nolint:paralleltest // Mutates process-wide runtime and XDG state settings.
func TestRunner_PersistClientCredentials(t *testing.T) {
	// SaveState uses process-wide runtime and XDG state settings. Keep this test
	// sequential and restore xdg's cached paths after t.Setenv restores the env.
	t.Cleanup(xdg.Reload)
	t.Setenv("TOOLHIVE_RUNTIME", "")
	t.Setenv("KUBERNETES_SERVICE_HOST", "")
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	xdg.Reload()

	ctx := context.Background()
	writableCapabilities := secrets.ProviderCapabilities{CanWrite: true}

	t.Run("initial registration creates secret names and persists metadata", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		secretManager := secretsmocks.NewMockProvider(ctrl)

		const (
			clientSecretName = "OAUTH_CLIENT_SECRET_initial-registration"
			regTokenName     = "OAUTH_REG_TOKEN_initial-registration"
		)
		secretExpiry := time.Date(2026, time.July, 15, 12, 0, 0, 0, time.UTC)

		gomock.InOrder(
			secretManager.EXPECT().GetSecret(gomock.Any(), clientSecretName).Return("", assert.AnError),
			secretManager.EXPECT().Capabilities().Return(writableCapabilities),
			secretManager.EXPECT().SetSecret(ctx, clientSecretName, "initial-client-secret").Return(nil),
			secretManager.EXPECT().GetSecret(gomock.Any(), regTokenName).Return("", assert.AnError),
			secretManager.EXPECT().Capabilities().Return(writableCapabilities),
			secretManager.EXPECT().SetSecret(ctx, regTokenName, "initial-registration-token").Return(nil),
		)

		remoteAuthConfig := &remote.Config{Issuer: "https://issuer.example.com"}
		runConfig := NewRunConfig()
		runConfig.Name = "initial-registration"
		runConfig.BaseName = "initial-registration"
		runConfig.RemoteAuthConfig = remoteAuthConfig
		runner := &Runner{Config: runConfig}

		err := runner.persistClientCredentials(
			ctx,
			secretManager,
			"initial-client-id",
			"initial-client-secret",
			secretExpiry,
			"initial-registration-token",
			"https://issuer.example.com/register/initial-client-id",
			"client_secret_basic",
			9876,
		)
		require.NoError(t, err)

		expected := remote.Config{
			Issuer:                        "https://issuer.example.com",
			CachedClientID:                "initial-client-id",
			CachedClientSecretRef:         clientSecretName,
			CachedSecretExpiry:            secretExpiry,
			CachedRegTokenRef:             regTokenName,
			CachedRegClientURI:            "https://issuer.example.com/register/initial-client-id",
			CachedTokenEndpointAuthMethod: "client_secret_basic",
			CachedDCRCallbackPort:         9876,
		}
		assert.Same(t, remoteAuthConfig, runner.Config.RemoteAuthConfig)
		assert.Equal(t, expected, *runner.Config.RemoteAuthConfig)

		persisted, err := LoadState(ctx, runConfig.BaseName)
		require.NoError(t, err)
		require.NotNil(t, persisted.RemoteAuthConfig)
		assert.Equal(t, expected, *persisted.RemoteAuthConfig)
	})

	t.Run("renewal reuses secret names and updates the existing config", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		secretManager := secretsmocks.NewMockProvider(ctrl)

		const (
			clientSecretName = "OAUTH_CLIENT_SECRET_existing"
			regTokenName     = "OAUTH_REG_TOKEN_existing"
		)
		secretExpiry := time.Date(2027, time.January, 2, 3, 4, 5, 0, time.UTC)
		secretManager.EXPECT().Capabilities().Return(writableCapabilities)
		secretManager.EXPECT().SetSecret(ctx, clientSecretName, "renewed-client-secret").Return(nil)
		secretManager.EXPECT().Capabilities().Return(writableCapabilities)
		secretManager.EXPECT().SetSecret(ctx, regTokenName, "renewed-registration-token").Return(nil)

		remoteAuthConfig := &remote.Config{
			Issuer:                        "https://issuer.example.com",
			CachedClientID:                "old-client-id",
			CachedClientSecretRef:         clientSecretName,
			CachedSecretExpiry:            time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC),
			CachedRegTokenRef:             regTokenName,
			CachedRegClientURI:            "https://issuer.example.com/register/old-client-id",
			CachedTokenEndpointAuthMethod: "none",
			CachedDCRCallbackPort:         8080,
		}
		runConfig := NewRunConfig()
		runConfig.Name = "renewal"
		runConfig.BaseName = "renewal"
		runConfig.RemoteAuthConfig = remoteAuthConfig
		runner := &Runner{Config: runConfig}

		err := runner.persistClientCredentials(
			ctx,
			secretManager,
			"renewed-client-id",
			"renewed-client-secret",
			secretExpiry,
			"renewed-registration-token",
			"https://issuer.example.com/register/renewed-client-id",
			"client_secret_post",
			9090,
		)
		require.NoError(t, err)

		expected := remote.Config{
			Issuer:                        "https://issuer.example.com",
			CachedClientID:                "renewed-client-id",
			CachedClientSecretRef:         clientSecretName,
			CachedSecretExpiry:            secretExpiry,
			CachedRegTokenRef:             regTokenName,
			CachedRegClientURI:            "https://issuer.example.com/register/renewed-client-id",
			CachedTokenEndpointAuthMethod: "client_secret_post",
			CachedDCRCallbackPort:         9090,
		}
		assert.Same(t, remoteAuthConfig, runner.Config.RemoteAuthConfig)
		assert.Equal(t, expected, *runner.Config.RemoteAuthConfig)

		persisted, err := LoadState(ctx, runConfig.BaseName)
		require.NoError(t, err)
		require.NotNil(t, persisted.RemoteAuthConfig)
		assert.Equal(t, expected, *persisted.RemoteAuthConfig)
	})

	t.Run("SaveState failure leaves the existing config untouched", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		secretManager := secretsmocks.NewMockProvider(ctrl)

		const (
			clientSecretName = "OAUTH_CLIENT_SECRET_existing-failure"
			regTokenName     = "OAUTH_REG_TOKEN_existing-failure"
		)
		secretManager.EXPECT().Capabilities().Return(writableCapabilities)
		secretManager.EXPECT().SetSecret(ctx, clientSecretName, "new-client-secret").Return(nil)
		secretManager.EXPECT().Capabilities().Return(writableCapabilities)
		secretManager.EXPECT().SetSecret(ctx, regTokenName, "new-registration-token").Return(nil)

		original := remote.Config{
			Issuer:                        "https://issuer.example.com",
			CachedClientID:                "existing-client-id",
			CachedClientSecretRef:         clientSecretName,
			CachedSecretExpiry:            time.Date(2026, time.December, 1, 0, 0, 0, 0, time.UTC),
			CachedRegTokenRef:             regTokenName,
			CachedRegClientURI:            "https://issuer.example.com/register/existing-client-id",
			CachedTokenEndpointAuthMethod: "client_secret_basic",
			CachedDCRCallbackPort:         8080,
		}
		remoteAuthConfig := &remote.Config{}
		*remoteAuthConfig = original
		runConfig := NewRunConfig()
		runConfig.Name = "failed-renewal"
		runConfig.BaseName = "../invalid"
		runConfig.RemoteAuthConfig = remoteAuthConfig
		runner := &Runner{Config: runConfig}

		err := runner.persistClientCredentials(
			ctx,
			secretManager,
			"new-client-id",
			"new-client-secret",
			time.Date(2027, time.December, 1, 0, 0, 0, 0, time.UTC),
			"new-registration-token",
			"https://issuer.example.com/register/new-client-id",
			"client_secret_post",
			9090,
		)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed to save config with client credentials")
		assert.Same(t, remoteAuthConfig, runner.Config.RemoteAuthConfig)
		assert.Equal(t, original, *runner.Config.RemoteAuthConfig)
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
}

func TestRunner_RejectsMultiUpstreamConfig(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockStatusManager := statusesmocks.NewMockStatusManager(ctrl)

	runConfig := NewRunConfig()
	runConfig.EmbeddedAuthServerConfig = &authserver.RunConfig{
		SchemaVersion: authserver.CurrentSchemaVersion,
		Issuer:        "http://localhost:8080",
		Upstreams: []authserver.UpstreamRunConfig{
			{
				Name: "github",
				Type: authserver.UpstreamProviderTypeOAuth2,
				OAuth2Config: &authserver.OAuth2UpstreamRunConfig{
					AuthorizationEndpoint: "https://github.com/authorize",
					TokenEndpoint:         "https://github.com/token",
					ClientID:              "id1",
					RedirectURI:           "http://localhost:8080/oauth/callback",
				},
			},
			{
				Name: "google",
				Type: authserver.UpstreamProviderTypeOAuth2,
				OAuth2Config: &authserver.OAuth2UpstreamRunConfig{
					AuthorizationEndpoint: "https://accounts.google.com/authorize",
					TokenEndpoint:         "https://accounts.google.com/token",
					ClientID:              "id2",
					RedirectURI:           "http://localhost:8080/oauth/callback",
				},
			},
		},
		AllowedAudiences: []string{"https://mcp.example.com"},
	}
	runConfig.Transport = types.TransportTypeStdio
	runConfig.Host = "localhost"
	runConfig.Name = "test"

	runner := NewRunner(runConfig, mockStatusManager)
	err := runner.Run(context.Background())

	require.Error(t, err)
	assert.Contains(t, err.Error(), "does not support multiple upstream providers")
}

func TestRunner_GetUpstreamTokenReader(t *testing.T) {
	t.Parallel()

	t.Run("returns nil when no auth server configured", func(t *testing.T) {
		t.Parallel()

		r := &Runner{}
		reader := r.GetUpstreamTokenReader()
		assert.Nil(t, reader)
	})

	t.Run("returns reader when auth server configured", func(t *testing.T) {
		t.Parallel()

		ctrl := gomock.NewController(t)
		mockStorage := storagemocks.NewMockUpstreamTokenStorage(ctrl)
		svc := upstreamtoken.NewInProcessService(mockStorage, nil)

		r := &Runner{
			upstreamTokenReader: svc,
		}
		reader := r.GetUpstreamTokenReader()
		assert.NotNil(t, reader)
		assert.Equal(t, svc, reader)
	})
}

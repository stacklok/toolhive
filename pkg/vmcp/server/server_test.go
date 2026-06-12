// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/audit"
	"github.com/stacklok/toolhive/pkg/authz/authorizers"
	mcpparser "github.com/stacklok/toolhive/pkg/mcp"
	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/aggregator"
	"github.com/stacklok/toolhive/pkg/vmcp/discovery"
	discoveryMocks "github.com/stacklok/toolhive/pkg/vmcp/discovery/mocks"
	"github.com/stacklok/toolhive/pkg/vmcp/mocks"
	"github.com/stacklok/toolhive/pkg/vmcp/optimizer"
	routerMocks "github.com/stacklok/toolhive/pkg/vmcp/router/mocks"
	"github.com/stacklok/toolhive/pkg/vmcp/server"
)

// stubReporter allows controlling Start/ReportStatus behavior in tests.
type stubReporter struct {
	startErr       error
	shutdownErr    error
	shutdownCalled chan struct{}
	reported       []*vmcp.Status
}

func (s *stubReporter) ReportStatus(_ context.Context, status *vmcp.Status) error {
	s.reported = append(s.reported, status)
	return nil
}

func (s *stubReporter) Start(_ context.Context) (func(context.Context) error, error) {
	if s.startErr != nil {
		return nil, s.startErr
	}
	return func(_ context.Context) error {
		if s.shutdownCalled != nil {
			select {
			case s.shutdownCalled <- struct{}{}:
			default:
			}
		}
		return s.shutdownErr
	}, nil
}

func TestServerStartFailsWhenReporterStartFails(t *testing.T) {
	t.Parallel()

	sr := &stubReporter{startErr: errors.New("boom")}

	ctrl := gomock.NewController(t)
	t.Cleanup(ctrl.Finish)
	mockRouter := routerMocks.NewMockRouter(ctrl)
	mockBackendClient := mocks.NewMockBackendClient(ctrl)
	mockDiscoveryMgr := discoveryMocks.NewMockManager(ctrl)
	mockBackendRegistry := mocks.NewMockBackendRegistry(ctrl)

	srv, err := server.New(
		context.Background(),
		&server.Config{Host: "127.0.0.1", Port: 0, StatusReporter: sr, SessionFactory: newNoopMockFactory(t)},
		mockRouter,
		mockBackendClient,
		mockDiscoveryMgr,
		mockBackendRegistry,
		nil,
	)
	require.NoError(t, err)

	err = srv.Start(context.Background())
	require.Error(t, err)
	require.Contains(t, err.Error(), "failed to start status reporter")
}

func TestServerStopRunsReporterShutdown(t *testing.T) {
	t.Parallel()

	shutdownCalled := make(chan struct{}, 1)
	sr := &stubReporter{shutdownErr: nil, shutdownCalled: shutdownCalled}

	ctrl := gomock.NewController(t)
	t.Cleanup(ctrl.Finish)
	mockRouter := routerMocks.NewMockRouter(ctrl)
	mockBackendClient := mocks.NewMockBackendClient(ctrl)
	mockDiscoveryMgr := discoveryMocks.NewMockManager(ctrl)
	mockBackendRegistry := mocks.NewMockBackendRegistry(ctrl)
	mockDiscoveryMgr.EXPECT().Stop().Times(1)

	srv, err := server.New(
		context.Background(),
		&server.Config{Host: "127.0.0.1", Port: 0, StatusReporter: sr, SessionFactory: newNoopMockFactory(t)},
		mockRouter,
		mockBackendClient,
		mockDiscoveryMgr,
		mockBackendRegistry,
		nil,
	)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- srv.Start(ctx)
	}()

	select {
	case <-srv.Ready():
	case err := <-done:
		t.Fatalf("server failed to start: %v", err)
	case <-time.After(3 * time.Second):
		t.Fatalf("server did not become ready")
	}

	cancel()

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(3 * time.Second):
		t.Fatalf("server start/stop did not complete")
	}

	select {
	case <-shutdownCalled:
	case <-time.After(time.Second):
		t.Fatalf("shutdown func was not called")
	}
}

func TestNew(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		config       *server.Config
		expectedHost string
		expectedPort int
		expectedPath string
		expectedName string
		expectedVer  string
	}{
		{
			name:         "applies all defaults",
			config:       &server.Config{SessionFactory: newNoopMockFactory(t)},
			expectedHost: "127.0.0.1",
			expectedPort: 4483,
			expectedPath: "/mcp",
			expectedName: "toolhive-vmcp",
			expectedVer:  "0.1.0",
		},
		{
			name: "uses provided configuration",
			config: &server.Config{
				Name:           "custom-vmcp",
				Version:        "1.0.0",
				Host:           "0.0.0.0",
				Port:           8080,
				EndpointPath:   "/api/mcp",
				SessionFactory: newNoopMockFactory(t),
			},
			expectedHost: "0.0.0.0",
			expectedPort: 8080,
			expectedPath: "/api/mcp",
			expectedName: "custom-vmcp",
			expectedVer:  "1.0.0",
		},
		{
			name: "applies partial defaults",
			config: &server.Config{
				Host:           "192.168.1.1",
				Port:           9000,
				SessionFactory: newNoopMockFactory(t),
			},
			expectedHost: "192.168.1.1",
			expectedPort: 9000,
			expectedPath: "/mcp",
			expectedName: "toolhive-vmcp",
			expectedVer:  "0.1.0",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			t.Cleanup(ctrl.Finish)

			mockRouter := routerMocks.NewMockRouter(ctrl)
			mockBackendClient := mocks.NewMockBackendClient(ctrl)
			mockDiscoveryMgr := discoveryMocks.NewMockManager(ctrl)

			s, err := server.New(context.Background(), tt.config, mockRouter, mockBackendClient, mockDiscoveryMgr, vmcp.NewImmutableRegistry([]vmcp.Backend{}), nil)
			require.NoError(t, err)
			require.NotNil(t, s)

			addr := s.Address()
			require.Contains(t, addr, tt.expectedHost)
		})
	}
}

func TestServer_Address(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		config   *server.Config
		expected string
	}{
		{
			name: "default host with explicit port",
			config: &server.Config{
				Port:           4483,
				SessionFactory: newNoopMockFactory(t),
			},
			expected: "127.0.0.1:4483",
		},
		{
			name: "port 0 for dynamic allocation",
			config: &server.Config{
				Port:           0,
				SessionFactory: newNoopMockFactory(t),
			},
			expected: "127.0.0.1:0",
		},
		{
			name: "custom host and port",
			config: &server.Config{
				Host:           "0.0.0.0",
				Port:           8080,
				SessionFactory: newNoopMockFactory(t),
			},
			expected: "0.0.0.0:8080",
		},
		{
			name: "localhost",
			config: &server.Config{
				Host:           "localhost",
				Port:           3000,
				SessionFactory: newNoopMockFactory(t),
			},
			expected: "localhost:3000",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			t.Cleanup(ctrl.Finish)

			mockRouter := routerMocks.NewMockRouter(ctrl)
			mockBackendClient := mocks.NewMockBackendClient(ctrl)
			mockDiscoveryMgr := discoveryMocks.NewMockManager(ctrl)

			s, err := server.New(context.Background(), tt.config, mockRouter, mockBackendClient, mockDiscoveryMgr, vmcp.NewImmutableRegistry([]vmcp.Backend{}), nil)
			require.NoError(t, err)
			addr := s.Address()
			assert.Equal(t, tt.expected, addr)
		})
	}
}

func TestServer_Stop(t *testing.T) {
	t.Parallel()

	t.Run("stop without starting is safe", func(t *testing.T) {
		t.Parallel()

		ctrl := gomock.NewController(t)
		t.Cleanup(ctrl.Finish)

		mockRouter := routerMocks.NewMockRouter(ctrl)
		mockBackendClient := mocks.NewMockBackendClient(ctrl)
		mockDiscoveryMgr := discoveryMocks.NewMockManager(ctrl)
		mockDiscoveryMgr.EXPECT().Stop().Times(1)

		s, err := server.New(context.Background(), &server.Config{SessionFactory: newNoopMockFactory(t)}, mockRouter, mockBackendClient, mockDiscoveryMgr, vmcp.NewImmutableRegistry([]vmcp.Backend{}), nil)
		require.NoError(t, err)
		err = s.Stop(context.Background())
		require.NoError(t, err)
	})
}

func TestNew_NilSessionFactory_ReturnsError(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	t.Cleanup(ctrl.Finish)

	mockRouter := routerMocks.NewMockRouter(ctrl)
	mockBackendClient := mocks.NewMockBackendClient(ctrl)
	mockDiscoveryMgr := discoveryMocks.NewMockManager(ctrl)

	_, err := server.New(
		context.Background(),
		&server.Config{
			SessionFactory: nil, // deliberately omitted
		},
		mockRouter, mockBackendClient, mockDiscoveryMgr,
		vmcp.NewImmutableRegistry([]vmcp.Backend{}), nil,
	)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "SessionFactory")
}

func TestNew_WithAuditConfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		auditConfig *audit.Config
		wantErr     bool
		errContains string
	}{
		{
			name:        "nil audit config is valid",
			auditConfig: nil,
			wantErr:     false,
		},
		{
			name: "empty audit config is valid",
			auditConfig: &audit.Config{
				Component: "vmcp-server",
			},
			wantErr: false,
		},
		{
			name: "full audit config is valid",
			auditConfig: &audit.Config{
				Component:           "vmcp-server",
				IncludeRequestData:  true,
				IncludeResponseData: true,
				MaxDataSize:         1024,
			},
			wantErr: false,
		},
		{
			name: "negative MaxDataSize is invalid",
			auditConfig: &audit.Config{
				Component:   "vmcp-server",
				MaxDataSize: -100,
			},
			wantErr:     true,
			errContains: "maxDataSize cannot be negative",
		},
		{
			name: "invalid event type is rejected",
			auditConfig: &audit.Config{
				Component:  "vmcp-server",
				EventTypes: []string{"invalid_event_type"},
			},
			wantErr:     true,
			errContains: "unknown event type: invalid_event_type",
		},
		{
			name: "invalid exclude event type is rejected",
			auditConfig: &audit.Config{
				Component:         "vmcp-server",
				ExcludeEventTypes: []string{"bad_event"},
			},
			wantErr:     true,
			errContains: "unknown exclude event type: bad_event",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			t.Cleanup(ctrl.Finish)

			mockRouter := routerMocks.NewMockRouter(ctrl)
			mockBackendClient := mocks.NewMockBackendClient(ctrl)
			mockDiscoveryMgr := discoveryMocks.NewMockManager(ctrl)

			config := &server.Config{
				AuditConfig:    tt.auditConfig,
				SessionFactory: newNoopMockFactory(t),
			}

			s, err := server.New(context.Background(), config, mockRouter, mockBackendClient, mockDiscoveryMgr, vmcp.NewImmutableRegistry([]vmcp.Backend{}), nil)

			if tt.wantErr {
				require.Error(t, err)
				if tt.errContains != "" {
					assert.Contains(t, err.Error(), tt.errContains)
				}
				return
			}

			require.NoError(t, err)
			require.NotNil(t, s)
		})
	}
}

func TestServerStopClosesOptimizerStore(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	t.Cleanup(ctrl.Finish)
	mockRouter := routerMocks.NewMockRouter(ctrl)
	mockBackendClient := mocks.NewMockBackendClient(ctrl)
	mockDiscoveryMgr := discoveryMocks.NewMockManager(ctrl)
	mockBackendRegistry := mocks.NewMockBackendRegistry(ctrl)

	mockDiscoveryMgr.EXPECT().Stop().Times(1)

	srv, err := server.New(
		context.Background(),
		&server.Config{Host: "127.0.0.1", Port: 0, OptimizerConfig: &optimizer.Config{}, SessionFactory: newNoopMockFactory(t)},
		mockRouter,
		mockBackendClient,
		mockDiscoveryMgr,
		mockBackendRegistry,
		nil,
	)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- srv.Start(ctx)
	}()

	select {
	case <-srv.Ready():
	case err := <-done:
		require.NoError(t, err, "server failed to start")
	case <-time.After(3 * time.Second):
		require.FailNow(t, "server did not become ready")
	}

	// Cancel triggers Stop which must run shutdownFuncs (including store.Close)
	cancel()

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(3 * time.Second):
		require.FailNow(t, "server start/stop did not complete")
	}
}

func TestHandler_ReturnsNonNilHandler(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	t.Cleanup(ctrl.Finish)

	mockRouter := routerMocks.NewMockRouter(ctrl)
	mockBackendClient := mocks.NewMockBackendClient(ctrl)
	mockDiscoveryMgr := discoveryMocks.NewMockManager(ctrl)
	mockBackendRegistry := mocks.NewMockBackendRegistry(ctrl)

	// Allow discovery middleware calls
	mockBackendRegistry.EXPECT().List(gomock.Any()).Return(nil).AnyTimes()
	mockDiscoveryMgr.EXPECT().Discover(gomock.Any(), gomock.Any()).Return(nil, nil).AnyTimes()

	srv, err := server.New(
		t.Context(),
		&server.Config{Host: "127.0.0.1", Port: 0, SessionFactory: newNoopMockFactory(t)},
		mockRouter,
		mockBackendClient,
		mockDiscoveryMgr,
		mockBackendRegistry,
		nil,
	)
	require.NoError(t, err)

	handler, err := srv.Handler(t.Context())
	require.NoError(t, err)
	require.NotNil(t, handler)

	// Verify handler responds to health endpoint
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	handler.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), `"status":"ok"`)
}

func TestHandler_ReturnsErrorOnInvalidAuditConfig(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	t.Cleanup(ctrl.Finish)

	mockRouter := routerMocks.NewMockRouter(ctrl)
	mockBackendClient := mocks.NewMockBackendClient(ctrl)
	mockDiscoveryMgr := discoveryMocks.NewMockManager(ctrl)
	mockBackendRegistry := mocks.NewMockBackendRegistry(ctrl)

	// AuditConfig with negative MaxDataSize fails validation inside Handler()
	srv, err := server.New(
		t.Context(),
		&server.Config{
			Host: "127.0.0.1",
			Port: 0,
			AuditConfig: &audit.Config{
				Component:   "vmcp-server",
				MaxDataSize: -1,
			},
			SessionFactory: newNoopMockFactory(t),
		},
		mockRouter,
		mockBackendClient,
		mockDiscoveryMgr,
		mockBackendRegistry,
		nil,
	)
	// New() also validates AuditConfig, so this may fail at New() level
	// If it passes New(), Handler() should catch it
	if err != nil {
		require.Contains(t, err.Error(), "maxDataSize cannot be negative")
		return
	}

	_, err = srv.Handler(t.Context())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid audit configuration")
}

func TestHandler_CanBeCalledMultipleTimes(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	t.Cleanup(ctrl.Finish)

	mockRouter := routerMocks.NewMockRouter(ctrl)
	mockBackendClient := mocks.NewMockBackendClient(ctrl)
	mockDiscoveryMgr := discoveryMocks.NewMockManager(ctrl)
	mockBackendRegistry := mocks.NewMockBackendRegistry(ctrl)

	mockBackendRegistry.EXPECT().List(gomock.Any()).Return(nil).AnyTimes()
	mockDiscoveryMgr.EXPECT().Discover(gomock.Any(), gomock.Any()).Return(nil, nil).AnyTimes()

	srv, err := server.New(
		t.Context(),
		&server.Config{Host: "127.0.0.1", Port: 0, SessionFactory: newNoopMockFactory(t)},
		mockRouter,
		mockBackendClient,
		mockDiscoveryMgr,
		mockBackendRegistry,
		nil,
	)
	require.NoError(t, err)

	h1, err := srv.Handler(t.Context())
	require.NoError(t, err)
	require.NotNil(t, h1)

	h2, err := srv.Handler(t.Context())
	require.NoError(t, err)
	require.NotNil(t, h2)

	// Both handlers should work independently
	for _, h := range []http.Handler{h1, h2} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/health", nil)
		h.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
	}
}

func TestHandler_RegistersWellKnownRoutes(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	t.Cleanup(ctrl.Finish)

	mockRouter := routerMocks.NewMockRouter(ctrl)
	mockBackendClient := mocks.NewMockBackendClient(ctrl)
	mockDiscoveryMgr := discoveryMocks.NewMockManager(ctrl)
	mockBackendRegistry := mocks.NewMockBackendRegistry(ctrl)

	mockBackendRegistry.EXPECT().List(gomock.Any()).Return(nil).AnyTimes()
	mockDiscoveryMgr.EXPECT().Discover(gomock.Any(), gomock.Any()).Return(nil, nil).AnyTimes()

	// Stub AuthInfoHandler that responds with a fixed JSON body.
	authInfoHandler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"resource":"https://mcp.example.com"}`))
	})

	srv, err := server.New(
		t.Context(),
		&server.Config{
			Host:            "127.0.0.1",
			Port:            0,
			AuthInfoHandler: authInfoHandler,
			SessionFactory:  newNoopMockFactory(t),
			// AuthServer is not set here because the concrete type
			// *asrunner.EmbeddedAuthServer cannot be easily constructed in an
			// external test without a real auth server backing it.
			// The RegisterHandlers code path on EmbeddedAuthServer is covered
			// by TestRegisterHandlers in pkg/authserver/runner.
		},
		mockRouter,
		mockBackendClient,
		mockDiscoveryMgr,
		mockBackendRegistry,
		nil,
	)
	require.NoError(t, err)

	handler, err := srv.Handler(t.Context())
	require.NoError(t, err)
	require.NotNil(t, handler)

	t.Run("oauth-protected-resource returns 200", func(t *testing.T) {
		t.Parallel()

		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/.well-known/oauth-protected-resource", nil)
		handler.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusOK, rec.Code)
		assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))
		assert.Contains(t, rec.Body.String(), `"resource"`)
	})

	t.Run("oauth-protected-resource subpath returns 200", func(t *testing.T) {
		t.Parallel()

		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/.well-known/oauth-protected-resource/mcp", nil)
		handler.ServeHTTP(rec, req)

		// The NewWellKnownHandler matches the prefix, so subpaths should also be handled.
		assert.Equal(t, http.StatusOK, rec.Code)
	})

	t.Run("unrelated well-known path is not handled by AuthInfoHandler", func(t *testing.T) {
		t.Parallel()

		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/.well-known/other", nil)
		handler.ServeHTTP(rec, req)

		// Should not be 200 from our stub handler.
		assert.NotEqual(t, http.StatusOK, rec.Code)
	})
}

func TestAcceptHeaderValidation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		method         string
		acceptHeader   string
		expectRejected bool
	}{
		{
			name:           "GET without Accept header returns 406",
			method:         http.MethodGet,
			acceptHeader:   "",
			expectRejected: true,
		},
		{
			name:           "GET with Accept application/json returns 406",
			method:         http.MethodGet,
			acceptHeader:   "application/json",
			expectRejected: true,
		},
		{
			name:           "GET with Accept text/event-stream passes through",
			method:         http.MethodGet,
			acceptHeader:   "text/event-stream",
			expectRejected: false,
		},
		{
			name:           "GET with multiple Accept types including text/event-stream passes through",
			method:         http.MethodGet,
			acceptHeader:   "text/event-stream, application/json",
			expectRejected: false,
		},
		{
			name:           "POST without Accept header passes through",
			method:         http.MethodPost,
			acceptHeader:   "",
			expectRejected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Use httptest recorder + handler directly to avoid shared server lifecycle issues.
			// Each subtest gets its own mocks and handler, making parallel execution safe.
			ctrl := gomock.NewController(t)
			t.Cleanup(ctrl.Finish)

			mockRouter := routerMocks.NewMockRouter(ctrl)
			mockBackendClient := mocks.NewMockBackendClient(ctrl)
			mockDiscoveryMgr := discoveryMocks.NewMockManager(ctrl)
			mockBackendRegistry := mocks.NewMockBackendRegistry(ctrl)

			mockBackendRegistry.EXPECT().List(gomock.Any()).Return(nil).AnyTimes()
			mockDiscoveryMgr.EXPECT().Discover(gomock.Any(), gomock.Any()).Return(nil, nil).AnyTimes()

			srv, err := server.New(
				t.Context(),
				&server.Config{Host: "127.0.0.1", Port: 0, SessionFactory: newNoopMockFactory(t)},
				mockRouter,
				mockBackendClient,
				mockDiscoveryMgr,
				mockBackendRegistry,
				nil,
			)
			require.NoError(t, err)

			handler, err := srv.Handler(t.Context())
			require.NoError(t, err)

			reqCtx, reqCancel := context.WithCancel(t.Context())
			t.Cleanup(reqCancel)

			req := httptest.NewRequest(tt.method, "/mcp", nil).WithContext(reqCtx)
			if tt.acceptHeader != "" {
				req.Header.Set("Accept", tt.acceptHeader)
			}

			rec := httptest.NewRecorder()

			if tt.expectRejected {
				// For rejected cases, ServeHTTP returns quickly with 406.
				handler.ServeHTTP(rec, req)

				resp := rec.Result()
				defer resp.Body.Close()

				body, err := io.ReadAll(resp.Body)
				require.NoError(t, err)

				assert.Equal(t, http.StatusNotAcceptable, resp.StatusCode)
				assert.Contains(t, string(body), "Not Acceptable")
				assert.Contains(t, string(body), "text/event-stream")
				assert.Equal(t, "application/json", resp.Header.Get("Content-Type"))
			} else {
				// Run the handler in a goroutine since it may block on streaming.
				// The Accept validation middleware runs before any blocking, so a
				// 406 would be written within the first 50 ms.
				done := make(chan struct{})
				go func() {
					defer close(done)
					handler.ServeHTTP(rec, req)
				}()

				// Give the middleware time to write any immediate response (like 406).
				time.Sleep(50 * time.Millisecond)
				reqCancel() // Unblock any long-running handler (e.g. SSE).

				// Require the goroutine to finish — it must exit once the context is
				// canceled. Only read rec.Code after done to avoid a data race.
				select {
				case <-done:
				case <-time.After(2 * time.Second):
					t.Fatal("handler goroutine did not return after context cancellation")
				}

				assert.NotEqual(t, http.StatusNotAcceptable, rec.Code,
					"expected request to pass Accept validation but got 406")
			}
		})
	}
}

// TestHandlerAppliesAuthzAndAnnotationOnlyWhenConfigured proves the shared
// (*Server).Handler applies BOTH the authz layer and the annotation-enrichment layer
// exactly when config.AuthzMiddleware != nil. This is the single guard that lets the
// same Handler serve both modes: the server.New path (AuthzMiddleware set) keeps
// enforcing authz with no regression, while the Serve path (AuthzMiddleware nil; see
// TestServeOmitsAuthzAndAnnotation) omits both layers and defers authorization to the
// core admission seam (#5438). The blocks are not deleted here — physical removal is
// Phase 3 (#5445).
func TestHandlerAppliesAuthzAndAnnotationOnlyWhenConfigured(t *testing.T) {
	t.Parallel()

	// Distinctive status only the observable authz layer writes, so its presence in the
	// chain is unambiguous.
	const sentinelStatus = http.StatusTeapot

	readOnly := true
	caps := &aggregator.AggregatedCapabilities{
		Tools: []vmcp.Tool{{
			Name:        "my_tool",
			Annotations: &vmcp.ToolAnnotations{ReadOnlyHint: &readOnly},
		}},
	}

	tests := []struct {
		name             string
		withAuthz        bool
		wantAuthzApplied bool
	}{
		{name: "applied when AuthzMiddleware set (server.New path)", withAuthz: true, wantAuthzApplied: true},
		{name: "omitted when AuthzMiddleware nil (Serve path)", withAuthz: false, wantAuthzApplied: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			t.Cleanup(ctrl.Finish)
			mockRouter := routerMocks.NewMockRouter(ctrl)
			mockBackendClient := mocks.NewMockBackendClient(ctrl)
			mockDiscoveryMgr := discoveryMocks.NewMockManager(ctrl)
			mockBackendRegistry := mocks.NewMockBackendRegistry(ctrl)
			// List/Discover stay permissive (AnyTimes) but do not fire on this path: with
			// WithSessionScopedRouting() and no Mcp-Session-Id, discovery.Middleware returns
			// before aggregating. Stop fires via srv.Stop in the cleanup below.
			mockBackendRegistry.EXPECT().List(gomock.Any()).Return(nil).AnyTimes()
			mockDiscoveryMgr.EXPECT().Discover(gomock.Any(), gomock.Any()).Return(nil, nil).AnyTimes()
			mockDiscoveryMgr.EXPECT().Stop().AnyTimes()

			var (
				authzApplied    bool
				annotationsSeen bool
			)
			// Observable authz layer: records that it ran AND whether the
			// annotation-enrichment layer (which executes immediately before it) injected
			// the tool's annotations into ctx, then short-circuits with the sentinel.
			authz := func(_ http.Handler) http.Handler {
				return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					authzApplied = true
					annotationsSeen = authorizers.ToolAnnotationsFromContext(r.Context()) != nil
					w.WriteHeader(sentinelStatus)
				})
			}

			cfg := &server.Config{Host: "127.0.0.1", Port: 0, SessionFactory: newNoopMockFactory(t)}
			if tt.withAuthz {
				cfg.AuthzMiddleware = authz
			}

			srv, err := server.New(t.Context(), cfg, mockRouter, mockBackendClient, mockDiscoveryMgr, mockBackendRegistry, nil)
			require.NoError(t, err)
			t.Cleanup(func() { _ = srv.Stop(context.Background()) })

			handler, err := srv.Handler(t.Context())
			require.NoError(t, err)

			// Craft a tools/call request. This chain has no auth-parser, so inject the
			// parsed request and discovered capabilities directly into ctx (as
			// ParsingMiddleware and the discovery middleware would).
			//
			// Load-bearing dependency: discovery.Middleware is built with
			// WithSessionScopedRouting(), so a request with no Mcp-Session-Id passes straight
			// through without touching the (mock) manager and WITHOUT rewriting ctx —
			// preserving the injected capabilities for the annotation-enrichment layer. If
			// that session-scoped pass-through ever changes to overwrite ctx, annotationsSeen
			// silently goes false; the positive-case assert.True below is the guard.
			ctx := context.WithValue(t.Context(), mcpparser.MCPRequestContextKey,
				&mcpparser.ParsedMCPRequest{Method: "tools/call", ResourceID: "my_tool"})
			ctx = discovery.WithDiscoveredCapabilities(ctx, caps)

			req := httptest.NewRequest(http.MethodPost, "/mcp", nil).WithContext(ctx)
			// Set Content-Type so the nil-authz request reaches a representative chain point
			// (the inner SDK handler) rather than being rejected during content negotiation;
			// both cases then exercise the chain identically.
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()

			// Both paths return synchronously: the configured case short-circuits at the
			// observable authz layer (sentinel), and the nil case falls through to the inner
			// SDK handler, which answers this POST without blocking. A direct call suffices —
			// no goroutine/timeout scaffolding needed.
			handler.ServeHTTP(rec, req)

			assert.Equal(t, tt.wantAuthzApplied, authzApplied)
			if tt.wantAuthzApplied {
				assert.Equal(t, sentinelStatus, rec.Code, "observable authz layer should have written the sentinel status")
				assert.True(t, annotationsSeen,
					"annotation-enrichment must run before authz and inject the tool annotations")
			} else {
				assert.NotEqual(t, sentinelStatus, rec.Code,
					"no authz layer should run on the Serve-style (nil AuthzMiddleware) path")
			}
		})
	}
}

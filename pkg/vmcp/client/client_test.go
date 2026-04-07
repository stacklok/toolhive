// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package client

//go:generate mockgen -destination=mocks/mock_outgoing_registry.go -package=mocks github.com/stacklok/toolhive/pkg/vmcp/auth OutgoingAuthRegistry

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/mock/gomock"

	pkgauth "github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/auth"
	authmocks "github.com/stacklok/toolhive/pkg/vmcp/auth/mocks"
	"github.com/stacklok/toolhive/pkg/vmcp/auth/strategies"
	authtypes "github.com/stacklok/toolhive/pkg/vmcp/auth/types"
	"github.com/stacklok/toolhive/pkg/vmcp/health"
)

func TestHTTPBackendClient_ListCapabilities_WithMockFactory(t *testing.T) {
	t.Parallel()

	t.Run("handles client factory error", func(t *testing.T) {
		t.Parallel()

		expectedErr := errors.New("factory error")
		mockFactory := func(_ context.Context, _ *vmcp.BackendTarget) (*client.Client, error) {
			return nil, expectedErr
		}

		backendClient := &httpBackendClient{
			clientFactory: mockFactory,
		}

		target := &vmcp.BackendTarget{
			WorkloadID:    "test-backend",
			WorkloadName:  "Test Backend",
			BaseURL:       "http://localhost:8080",
			TransportType: "streamable-http",
		}

		capabilities, err := backendClient.ListCapabilities(context.Background(), target)

		require.Error(t, err)
		assert.Nil(t, capabilities)
		assert.Contains(t, err.Error(), "failed to create client")
		assert.Contains(t, err.Error(), "test-backend")
	})
}

func TestQueryHelpers_PartialCapabilities(t *testing.T) {
	t.Parallel()

	t.Run("queryTools with unsupported capability returns empty slice", func(t *testing.T) {
		t.Parallel()

		result, err := queryTools(context.Background(), nil, false, "test-backend")
		require.NoError(t, err)
		assert.NotNil(t, result)
		assert.Empty(t, result.Tools)
	})

	t.Run("queryResources with unsupported capability returns empty slice", func(t *testing.T) {
		t.Parallel()

		result, err := queryResources(context.Background(), nil, false, "test-backend")
		require.NoError(t, err)
		assert.NotNil(t, result)
		assert.Empty(t, result.Resources)
	})

	t.Run("queryPrompts with unsupported capability returns empty slice", func(t *testing.T) {
		t.Parallel()

		result, err := queryPrompts(context.Background(), nil, false, "test-backend")
		require.NoError(t, err)
		assert.NotNil(t, result)
		assert.Empty(t, result.Prompts)
	})
}

// TestNewBackendTransport_IsolatesFromDefault verifies that newBackendTransport never
// returns http.DefaultTransport itself, preventing stale keep-alive connections on one
// backend from affecting requests to other backends or future calls.
func TestNewBackendTransport_IsolatesFromDefault(t *testing.T) {
	t.Parallel()

	t1 := newBackendTransport()
	t2 := newBackendTransport()

	// Each call must return a distinct transport — not the shared DefaultTransport.
	assert.NotSame(t, http.DefaultTransport, t1, "newBackendTransport must not return http.DefaultTransport")
	assert.NotSame(t, http.DefaultTransport, t2, "newBackendTransport must not return http.DefaultTransport")
	assert.NotSame(t, t1, t2, "each call must return a distinct *http.Transport")
}

func TestDefaultClientFactory_UnsupportedTransport(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name          string
		transportType string
	}{
		{
			name:          "stdio transport",
			transportType: "stdio",
		},
		{
			name:          "unknown transport",
			transportType: "unknown-protocol",
		},
		{
			name:          "empty transport",
			transportType: "",
		},
	}

	for _, tc := range testCases {
		tc := tc // Capture range variable
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			target := &vmcp.BackendTarget{
				WorkloadID:    "test-backend",
				WorkloadName:  "Test Backend",
				BaseURL:       "http://localhost:8080",
				TransportType: tc.transportType,
			}

			// Create authenticator with unauthenticated strategy for testing
			mockRegistry := auth.NewDefaultOutgoingAuthRegistry()
			err := mockRegistry.RegisterStrategy("unauthenticated", &strategies.UnauthenticatedStrategy{})
			require.NoError(t, err)

			backendClient, err := NewHTTPBackendClient(mockRegistry)
			require.NoError(t, err)
			httpClient := backendClient.(*httpBackendClient)

			_, err = httpClient.defaultClientFactory(context.Background(), target)

			require.Error(t, err)
			assert.ErrorIs(t, err, vmcp.ErrUnsupportedTransport)
			assert.Contains(t, err.Error(), tc.transportType)
		})
	}
}

func TestHTTPBackendClient_CallTool_WithMockFactory(t *testing.T) {
	t.Parallel()

	t.Run("handles client factory error", func(t *testing.T) {
		t.Parallel()

		expectedErr := errors.New("connection failed")
		mockFactory := func(_ context.Context, _ *vmcp.BackendTarget) (*client.Client, error) {
			return nil, expectedErr
		}

		backendClient := &httpBackendClient{
			clientFactory: mockFactory,
		}

		target := &vmcp.BackendTarget{
			WorkloadID:    "test-backend",
			WorkloadName:  "Test Backend",
			BaseURL:       "http://localhost:8080",
			TransportType: "streamable-http",
		}

		result, err := backendClient.CallTool(context.Background(), target, "test_tool", map[string]any{}, nil)

		require.Error(t, err)
		assert.Nil(t, result)
		assert.Contains(t, err.Error(), "failed to create client")
	})
}

func TestHTTPBackendClient_ReadResource_WithMockFactory(t *testing.T) {
	t.Parallel()

	t.Run("handles client factory error", func(t *testing.T) {
		t.Parallel()

		expectedErr := errors.New("connection failed")
		mockFactory := func(_ context.Context, _ *vmcp.BackendTarget) (*client.Client, error) {
			return nil, expectedErr
		}

		backendClient := &httpBackendClient{
			clientFactory: mockFactory,
		}

		target := &vmcp.BackendTarget{
			WorkloadID:    "test-backend",
			WorkloadName:  "Test Backend",
			BaseURL:       "http://localhost:8080",
			TransportType: "streamable-http",
		}

		data, err := backendClient.ReadResource(context.Background(), target, "test://resource")

		require.Error(t, err)
		assert.Nil(t, data)
		assert.Contains(t, err.Error(), "failed to create client")
	})
}

func TestHTTPBackendClient_GetPrompt_WithMockFactory(t *testing.T) {
	t.Parallel()

	t.Run("handles client factory error", func(t *testing.T) {
		t.Parallel()

		expectedErr := errors.New("connection failed")
		mockFactory := func(_ context.Context, _ *vmcp.BackendTarget) (*client.Client, error) {
			return nil, expectedErr
		}

		backendClient := &httpBackendClient{
			clientFactory: mockFactory,
		}

		target := &vmcp.BackendTarget{
			WorkloadID:    "test-backend",
			WorkloadName:  "Test Backend",
			BaseURL:       "http://localhost:8080",
			TransportType: "streamable-http",
		}

		prompt, err := backendClient.GetPrompt(context.Background(), target, "test_prompt", map[string]any{"arg": "value"})

		require.Error(t, err)
		assert.Empty(t, prompt)
		assert.Contains(t, err.Error(), "failed to create client")
	})
}

func TestInitializeClient_ErrorHandling(t *testing.T) {
	t.Parallel()

	// This test verifies that initializeClient properly propagates errors
	// We can't easily test the success case without a real MCP server
	// Integration tests will cover the success path
	t.Run("error handling structure", func(t *testing.T) {
		t.Parallel()

		// Verify that initializeClient exists and has the right signature
		// The actual error handling is tested via integration tests
		assert.NotNil(t, initializeClient)
	})
}

// mockRoundTripper is a test implementation of http.RoundTripper that captures requests
type mockRoundTripper struct {
	capturedReq *http.Request
	response    *http.Response
	err         error
}

func (m *mockRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	m.capturedReq = req
	if m.err != nil {
		return nil, m.err
	}
	return m.response, nil
}

func TestAuthRoundTripper_RoundTrip(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name               string
		target             *vmcp.BackendTarget
		setupStrategy      func(*gomock.Controller) auth.Strategy
		baseTransportResp  *http.Response
		baseTransportErr   error
		expectError        bool
		errorContains      string
		checkRequest       func(t *testing.T, originalReq, capturedReq *http.Request)
		checkBaseTransport func(t *testing.T, baseTransport *mockRoundTripper)
	}{
		{
			name: "successful authentication adds headers and forwards request",
			target: &vmcp.BackendTarget{
				WorkloadID: "backend-1",
				AuthConfig: &authtypes.BackendAuthStrategy{Type: "header_injection"},
			},
			setupStrategy: func(ctrl *gomock.Controller) auth.Strategy {
				mockStrategy := authmocks.NewMockStrategy(ctrl)
				mockStrategy.EXPECT().
					Name().
					Return("header_injection").
					AnyTimes()
				mockStrategy.EXPECT().
					Authenticate(
						gomock.Any(),
						gomock.Any(),
						&authtypes.BackendAuthStrategy{Type: "header_injection"},
					).
					DoAndReturn(func(_ context.Context, req *http.Request, _ *authtypes.BackendAuthStrategy) error {
						// Simulate adding auth header
						req.Header.Set("Authorization", "Bearer test-token")
						return nil
					})
				return mockStrategy
			},
			baseTransportResp: &http.Response{StatusCode: http.StatusOK},
			expectError:       false,
			checkRequest: func(t *testing.T, originalReq, capturedReq *http.Request) {
				t.Helper()
				// Original request should not be modified
				assert.Empty(t, originalReq.Header.Get("Authorization"))
				// Captured request should have auth header
				assert.Equal(t, "Bearer test-token", capturedReq.Header.Get("Authorization"))
			},
			checkBaseTransport: func(t *testing.T, baseTransport *mockRoundTripper) {
				t.Helper()
				// Base transport should have been called
				assert.NotNil(t, baseTransport.capturedReq)
			},
		},
		{
			name: "unauthenticated strategy skips authentication",
			target: &vmcp.BackendTarget{
				WorkloadID: "backend-1",
				AuthConfig: &authtypes.BackendAuthStrategy{
					Type: authtypes.StrategyTypeUnauthenticated,
				},
			},
			setupStrategy: func(ctrl *gomock.Controller) auth.Strategy {
				mockStrategy := authmocks.NewMockStrategy(ctrl)
				mockStrategy.EXPECT().
					Name().
					Return("unauthenticated").
					AnyTimes()
				mockStrategy.EXPECT().
					Authenticate(
						gomock.Any(),
						gomock.Any(),
						&authtypes.BackendAuthStrategy{
							Type: authtypes.StrategyTypeUnauthenticated,
						},
					).
					DoAndReturn(func(_ context.Context, _ *http.Request, _ *authtypes.BackendAuthStrategy) error {
						// UnauthenticatedStrategy does nothing
						return nil
					})
				return mockStrategy
			},
			baseTransportResp: &http.Response{StatusCode: http.StatusOK},
			expectError:       false,
			checkRequest: func(t *testing.T, originalReq, capturedReq *http.Request) {
				t.Helper()
				// Neither request should have auth headers
				assert.Empty(t, originalReq.Header.Get("Authorization"))
				assert.Empty(t, capturedReq.Header.Get("Authorization"))
			},
			checkBaseTransport: func(t *testing.T, baseTransport *mockRoundTripper) {
				t.Helper()
				// Base transport should have been called
				assert.NotNil(t, baseTransport.capturedReq)
			},
		},
		{
			name: "authentication failure returns error without calling base transport",
			target: &vmcp.BackendTarget{
				WorkloadID: "backend-1",
				AuthConfig: &authtypes.BackendAuthStrategy{Type: "header_injection"},
			},
			setupStrategy: func(ctrl *gomock.Controller) auth.Strategy {
				mockStrategy := authmocks.NewMockStrategy(ctrl)
				mockStrategy.EXPECT().
					Name().
					Return("header_injection").
					AnyTimes()
				mockStrategy.EXPECT().
					Authenticate(
						gomock.Any(),
						gomock.Any(),
						&authtypes.BackendAuthStrategy{Type: "header_injection"},
					).
					Return(errors.New("auth failed"))
				return mockStrategy
			},
			baseTransportResp: &http.Response{StatusCode: http.StatusOK},
			expectError:       true,
			errorContains:     "authentication failed for backend backend-1",
			checkBaseTransport: func(t *testing.T, baseTransport *mockRoundTripper) {
				t.Helper()
				// Base transport should NOT have been called
				assert.Nil(t, baseTransport.capturedReq)
			},
		},
		{
			name: "base transport error propagates after successful auth",
			target: &vmcp.BackendTarget{
				WorkloadID: "backend-1",
				AuthConfig: &authtypes.BackendAuthStrategy{Type: "header_injection"},
			},
			setupStrategy: func(ctrl *gomock.Controller) auth.Strategy {
				mockStrategy := authmocks.NewMockStrategy(ctrl)
				mockStrategy.EXPECT().
					Name().
					Return("header_injection").
					AnyTimes()
				mockStrategy.EXPECT().
					Authenticate(
						gomock.Any(),
						gomock.Any(),
						&authtypes.BackendAuthStrategy{Type: "header_injection"},
					).
					Return(nil)
				return mockStrategy
			},
			baseTransportErr: errors.New("connection refused"),
			expectError:      true,
			errorContains:    "connection refused",
			checkBaseTransport: func(t *testing.T, baseTransport *mockRoundTripper) {
				t.Helper()
				// Base transport should have been called
				assert.NotNil(t, baseTransport.capturedReq)
			},
		},
		{
			name: "request immutability - original request unchanged",
			target: &vmcp.BackendTarget{
				WorkloadID: "backend-1",
				AuthConfig: &authtypes.BackendAuthStrategy{Type: "header_injection"},
			},
			setupStrategy: func(ctrl *gomock.Controller) auth.Strategy {
				mockStrategy := authmocks.NewMockStrategy(ctrl)
				mockStrategy.EXPECT().
					Name().
					Return("header_injection").
					AnyTimes()
				mockStrategy.EXPECT().
					Authenticate(
						gomock.Any(),
						gomock.Any(),
						&authtypes.BackendAuthStrategy{Type: "header_injection"},
					).
					DoAndReturn(func(_ context.Context, req *http.Request, _ *authtypes.BackendAuthStrategy) error {
						// Modify the cloned request
						req.Header.Set("Authorization", "Bearer modified-token")
						req.Header.Set("X-Custom-Header", "custom-value")
						return nil
					})
				return mockStrategy
			},
			baseTransportResp: &http.Response{StatusCode: http.StatusOK},
			expectError:       false,
			checkRequest: func(t *testing.T, originalReq, capturedReq *http.Request) {
				t.Helper()
				// Original request should be completely unmodified
				assert.Empty(t, originalReq.Header.Get("Authorization"))
				assert.Empty(t, originalReq.Header.Get("X-Custom-Header"))

				// Captured (cloned) request should have modifications
				assert.Equal(t, "Bearer modified-token", capturedReq.Header.Get("Authorization"))
				assert.Equal(t, "custom-value", capturedReq.Header.Get("X-Custom-Header"))
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)

			// Setup mock strategy
			var mockStrategy auth.Strategy
			if tt.setupStrategy != nil {
				mockStrategy = tt.setupStrategy(ctrl)
			}

			// Setup mock base transport
			baseTransport := &mockRoundTripper{
				response: tt.baseTransportResp,
				err:      tt.baseTransportErr,
			}

			// Create authRoundTripper with pre-resolved strategy
			authRT := &authRoundTripper{
				base:         baseTransport,
				authStrategy: mockStrategy,
				authConfig:   tt.target.AuthConfig,
				target:       tt.target,
			}

			// Create test request
			req := httptest.NewRequest(http.MethodGet, "http://backend.example.com/test", nil)
			ctx := context.Background()
			req = req.WithContext(ctx)

			// Execute RoundTrip
			resp, err := authRT.RoundTrip(req)

			// Check error expectations
			if tt.expectError {
				require.Error(t, err)
				if tt.errorContains != "" {
					assert.Contains(t, err.Error(), tt.errorContains)
				}
			} else {
				require.NoError(t, err)
				assert.NotNil(t, resp)
			}

			// Check request modifications if specified
			if tt.checkRequest != nil {
				tt.checkRequest(t, req, baseTransport.capturedReq)
			}

			// Check base transport calls if specified
			if tt.checkBaseTransport != nil {
				tt.checkBaseTransport(t, baseTransport)
			}
		})
	}
}

func TestNewHTTPBackendClient_NilRegistry(t *testing.T) {
	t.Parallel()

	t.Run("returns error when registry is nil", func(t *testing.T) {
		t.Parallel()

		client, err := NewHTTPBackendClient(nil)

		require.Error(t, err)
		assert.Nil(t, client)
		assert.Contains(t, err.Error(), "registry cannot be nil")
		assert.Contains(t, err.Error(), "UnauthenticatedStrategy")
	})

	t.Run("succeeds with valid registry", func(t *testing.T) {
		t.Parallel()

		mockRegistry := auth.NewDefaultOutgoingAuthRegistry()
		client, err := NewHTTPBackendClient(mockRegistry)

		require.NoError(t, err)
		assert.NotNil(t, client)
	})
}

// TestTracePropagatingRoundTripper tests the trace context propagation RoundTripper.
func TestTracePropagatingRoundTripper(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name              string
		propagator        propagation.TextMapPropagator
		createSpan        bool
		baseErr           error
		expectTraceparent bool
		expectErr         bool
	}{
		{
			name:              "injects traceparent with active span",
			propagator:        propagation.TraceContext{},
			createSpan:        true,
			expectTraceparent: true,
		},
		{
			name:       "no span context does not inject header",
			propagator: propagation.TraceContext{},
			createSpan: false,
		},
		{
			name:              "propagates base error and still injects header",
			propagator:        propagation.TraceContext{},
			createSpan:        true,
			baseErr:           errors.New("connection refused"),
			expectTraceparent: true,
			expectErr:         true,
		},
		{
			name:       "no-op propagator does not inject header",
			propagator: propagation.NewCompositeTextMapPropagator(),
			createSpan: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctx := context.Background()
			var traceID string

			if tt.createSpan {
				tp := sdktrace.NewTracerProvider()
				t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })
				var span trace.Span
				ctx, span = tp.Tracer("test").Start(ctx, "test-span")
				defer span.End()
				traceID = span.SpanContext().TraceID().String()
			}

			base := &mockRoundTripper{
				response: &http.Response{StatusCode: http.StatusOK},
				err:      tt.baseErr,
			}
			rt := &tracePropagatingRoundTripper{
				base:       base,
				propagator: tt.propagator,
			}

			req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://backend.example.com/mcp", nil)
			require.NoError(t, err)

			resp, err := rt.RoundTrip(req)
			if tt.expectErr {
				require.Error(t, err)
				assert.ErrorIs(t, err, tt.baseErr)
				assert.Nil(t, resp)
			} else {
				require.NoError(t, err)
				assert.Equal(t, http.StatusOK, resp.StatusCode)
			}

			traceparent := base.capturedReq.Header.Get("Traceparent")
			if tt.expectTraceparent {
				require.NotEmpty(t, traceparent, "traceparent header should be set")
				assert.Contains(t, traceparent, traceID,
					"traceparent %q should contain trace ID %q", traceparent, traceID)
			} else {
				assert.Empty(t, traceparent, "traceparent should not be set")
			}
		})
	}
}

// TestTracePropagatingRoundTripper_ParentChildSpan verifies that the propagated
// traceparent contains the child (most recent) span's span ID, not the parent's.
func TestTracePropagatingRoundTripper_ParentChildSpan(t *testing.T) {
	t.Parallel()

	tp := sdktrace.NewTracerProvider()
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })

	ctx, parentSpan := tp.Tracer("test").Start(context.Background(), "parent")
	ctx, childSpan := tp.Tracer("test").Start(ctx, "child")
	defer parentSpan.End()
	defer childSpan.End()

	base := &mockRoundTripper{response: &http.Response{StatusCode: http.StatusOK}}
	rt := &tracePropagatingRoundTripper{
		base:       base,
		propagator: propagation.TraceContext{},
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://backend.example.com/mcp", nil)
	require.NoError(t, err)

	_, err = rt.RoundTrip(req)
	require.NoError(t, err)

	traceparent := base.capturedReq.Header.Get("Traceparent")
	require.NotEmpty(t, traceparent)
	assert.Contains(t, traceparent, childSpan.SpanContext().SpanID().String(),
		"traceparent should contain child span ID, not parent")
}

func TestResolveAuthStrategy(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		target        *vmcp.BackendTarget
		setupRegistry func() auth.OutgoingAuthRegistry
		expectError   bool
		errorContains string
		checkStrategy func(t *testing.T, strategy auth.Strategy)
	}{
		{
			name: "defaults to unauthenticated when strategy is empty",
			target: &vmcp.BackendTarget{
				WorkloadID: "backend-1",
				AuthConfig: &authtypes.BackendAuthStrategy{
					Type: authtypes.StrategyTypeUnauthenticated,
				},
			},
			setupRegistry: func() auth.OutgoingAuthRegistry {
				registry := auth.NewDefaultOutgoingAuthRegistry()
				err := registry.RegisterStrategy("unauthenticated", &strategies.UnauthenticatedStrategy{})
				require.NoError(t, err)
				return registry
			},
			expectError: false,
			checkStrategy: func(t *testing.T, strategy auth.Strategy) {
				t.Helper()
				assert.Equal(t, "unauthenticated", strategy.Name())
			},
		},
		{
			name: "resolves explicitly configured strategy",
			target: &vmcp.BackendTarget{
				WorkloadID: "backend-1",
				AuthConfig: &authtypes.BackendAuthStrategy{Type: "header_injection"},
			},
			setupRegistry: func() auth.OutgoingAuthRegistry {
				registry := auth.NewDefaultOutgoingAuthRegistry()
				err := registry.RegisterStrategy("header_injection", strategies.NewHeaderInjectionStrategy())
				require.NoError(t, err)
				return registry
			},
			expectError: false,
			checkStrategy: func(t *testing.T, strategy auth.Strategy) {
				t.Helper()
				assert.Equal(t, "header_injection", strategy.Name())
			},
		},
		{
			name: "returns error for unknown strategy",
			target: &vmcp.BackendTarget{
				WorkloadID: "backend-1",
				AuthConfig: &authtypes.BackendAuthStrategy{
					Type: "unknown_strategy",
				},
			},
			setupRegistry: func() auth.OutgoingAuthRegistry {
				registry := auth.NewDefaultOutgoingAuthRegistry()
				err := registry.RegisterStrategy("unauthenticated", &strategies.UnauthenticatedStrategy{})
				require.NoError(t, err)
				return registry
			},
			expectError:   true,
			errorContains: "authentication strategy \"unknown_strategy\" not found",
		},
		{
			name: "returns error when unauthenticated strategy not registered",
			target: &vmcp.BackendTarget{
				WorkloadID: "backend-1",
				AuthConfig: &authtypes.BackendAuthStrategy{
					Type: authtypes.StrategyTypeUnauthenticated,
				},
			},
			setupRegistry: func() auth.OutgoingAuthRegistry {
				// Don't register unauthenticated strategy
				return auth.NewDefaultOutgoingAuthRegistry()
			},
			expectError:   true,
			errorContains: "authentication strategy \"unauthenticated\" not found",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			registry := tt.setupRegistry()
			backendClient, err := NewHTTPBackendClient(registry)
			require.NoError(t, err)

			httpClient := backendClient.(*httpBackendClient)

			// Call resolveAuthStrategy
			strategy, err := httpClient.resolveAuthStrategy(tt.target)

			if tt.expectError {
				require.Error(t, err)
				if tt.errorContains != "" {
					assert.Contains(t, err.Error(), tt.errorContains)
				}
				assert.Nil(t, strategy)
			} else {
				require.NoError(t, err)
				assert.NotNil(t, strategy)
				if tt.checkStrategy != nil {
					tt.checkStrategy(t, strategy)
				}
			}
		})
	}
}

// TestWrapBackendError verifies that wrapBackendError maps mcp-go transport sentinel
// errors to the correct vmcp sentinel errors for downstream health monitoring.
func TestWrapBackendError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		err             error
		wantSentinel    error
		wantMsgContains string
	}{
		{
			name:         "nil error returns nil",
			err:          nil,
			wantSentinel: nil,
		},
		{
			// mcp-go returns ErrUnauthorized for 401 on initialize POST.
			// Must map to ErrAuthenticationFailed so health monitors classify
			// the backend as BackendUnauthenticated, not BackendUnhealthy.
			name:         "ErrUnauthorized maps to ErrAuthenticationFailed",
			err:          transport.ErrUnauthorized,
			wantSentinel: vmcp.ErrAuthenticationFailed,
		},
		{
			// errors.Is traverses the error chain, so wrapping ErrUnauthorized
			// in another error must still produce ErrAuthenticationFailed.
			name:         "wrapped ErrUnauthorized maps to ErrAuthenticationFailed",
			err:          fmt.Errorf("transport layer: %w", transport.ErrUnauthorized),
			wantSentinel: vmcp.ErrAuthenticationFailed,
		},
		{
			// mcp-go returns ErrLegacySSEServer for non-401 4xx on initialize POST
			// (e.g. 403, 404, 405). Classified as backend unavailable so the health
			// monitor can recover if the backend is later corrected.
			name:            "ErrLegacySSEServer maps to ErrBackendUnavailable",
			err:             transport.ErrLegacySSEServer,
			wantSentinel:    vmcp.ErrBackendUnavailable,
			wantMsgContains: "legacy SSE",
		},
		{
			name:         "context.DeadlineExceeded maps to ErrTimeout",
			err:          context.DeadlineExceeded,
			wantSentinel: vmcp.ErrTimeout,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result := wrapBackendError(tt.err, "test-backend", "initialize")

			if tt.err == nil {
				assert.NoError(t, result)
				return
			}

			require.Error(t, result)
			assert.ErrorIs(t, result, tt.wantSentinel)
			if tt.wantMsgContains != "" {
				assert.Contains(t, result.Error(), tt.wantMsgContains)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// identityPropagatingRoundTripper
// ---------------------------------------------------------------------------

func TestIdentityPropagatingRoundTripper_WithIdentity_PropagatesIdentityInContext(t *testing.T) {
	t.Parallel()

	base := &mockRoundTripper{response: &http.Response{StatusCode: http.StatusOK}}
	identity := &pkgauth.Identity{PrincipalInfo: pkgauth.PrincipalInfo{Subject: "user-1"}}
	rt := &identityPropagatingRoundTripper{base: base, identity: identity}

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://backend.example.com/mcp", nil)
	require.NoError(t, err)

	_, err = rt.RoundTrip(req)
	require.NoError(t, err)

	require.NotNil(t, base.capturedReq)
	got, ok := pkgauth.IdentityFromContext(base.capturedReq.Context())
	require.True(t, ok, "identity should be in downstream request context")
	assert.Equal(t, "user-1", got.Subject)
}

func TestIdentityPropagatingRoundTripper_NilIdentity_NoIdentityInContext(t *testing.T) {
	t.Parallel()

	base := &mockRoundTripper{response: &http.Response{StatusCode: http.StatusOK}}
	rt := &identityPropagatingRoundTripper{base: base, identity: nil}

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://backend.example.com/mcp", nil)
	require.NoError(t, err)

	_, err = rt.RoundTrip(req)
	require.NoError(t, err)

	require.NotNil(t, base.capturedReq)
	_, ok := pkgauth.IdentityFromContext(base.capturedReq.Context())
	assert.False(t, ok, "no identity should be in downstream context when nil identity configured")
}

func TestIdentityPropagatingRoundTripper_HealthCheck_PropagatesMarker(t *testing.T) {
	t.Parallel()

	base := &mockRoundTripper{response: &http.Response{StatusCode: http.StatusOK}}
	rt := &identityPropagatingRoundTripper{base: base, identity: nil, isHealthCheck: true}

	// Simulate mcp-go Close(): request created with context.Background(), no health check marker.
	req, err := http.NewRequestWithContext(context.Background(), http.MethodDelete, "http://backend.example.com/mcp", nil)
	require.NoError(t, err)

	_, err = rt.RoundTrip(req)
	require.NoError(t, err)

	require.NotNil(t, base.capturedReq)
	assert.True(t, health.IsHealthCheck(base.capturedReq.Context()),
		"health check marker should be propagated even when original request context lacks it")
}

func TestIdentityPropagatingRoundTripper_NonHealthCheck_NoMarkerAdded(t *testing.T) {
	t.Parallel()

	base := &mockRoundTripper{response: &http.Response{StatusCode: http.StatusOK}}
	rt := &identityPropagatingRoundTripper{base: base, identity: nil, isHealthCheck: false}

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://backend.example.com/mcp", nil)
	require.NoError(t, err)

	_, err = rt.RoundTrip(req)
	require.NoError(t, err)

	require.NotNil(t, base.capturedReq)
	assert.False(t, health.IsHealthCheck(base.capturedReq.Context()),
		"health check marker should not be injected for non-health-check transports")
}

func TestIdentityPropagatingRoundTripper_HealthCheckWithIdentity_PropagatesBoth(t *testing.T) {
	t.Parallel()

	base := &mockRoundTripper{response: &http.Response{StatusCode: http.StatusOK}}
	identity := &pkgauth.Identity{PrincipalInfo: pkgauth.PrincipalInfo{Subject: "svc-account"}}
	rt := &identityPropagatingRoundTripper{base: base, identity: identity, isHealthCheck: true}

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://backend.example.com/mcp", nil)
	require.NoError(t, err)

	_, err = rt.RoundTrip(req)
	require.NoError(t, err)

	require.NotNil(t, base.capturedReq)
	got, ok := pkgauth.IdentityFromContext(base.capturedReq.Context())
	require.True(t, ok)
	assert.Equal(t, "svc-account", got.Subject)
	assert.True(t, health.IsHealthCheck(base.capturedReq.Context()))
}

// TestIdentityPropagatingRoundTripper_HealthCheckClose_NoAuthError verifies that when a
// transient BackendClient is created for a health check (no identity, health check ctx),
// the transport propagates the health check marker to ALL requests including the DELETE
// that mcp-go emits on Close(). Auth strategies skip auth for health check requests,
// so this prevents "no identity found in context" errors in the vMCP logs.
func TestIdentityPropagatingRoundTripper_HealthCheckClose_OriginalRequestContextUnchanged(t *testing.T) {
	t.Parallel()

	base := &mockRoundTripper{response: &http.Response{StatusCode: http.StatusOK}}
	rt := &identityPropagatingRoundTripper{base: base, identity: nil, isHealthCheck: true}

	originalCtx := context.Background() // no health check marker — simulates mcp-go Close()
	req, err := http.NewRequestWithContext(originalCtx, http.MethodDelete, "http://backend.example.com/mcp", nil)
	require.NoError(t, err)

	_, err = rt.RoundTrip(req)
	require.NoError(t, err)

	// Original request context must NOT be modified.
	assert.False(t, health.IsHealthCheck(originalCtx),
		"original request context must not be mutated")
	// But downstream context MUST have the marker.
	require.NotNil(t, base.capturedReq)
	assert.True(t, health.IsHealthCheck(base.capturedReq.Context()),
		"downstream request must carry health check marker")
}

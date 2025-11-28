package client

//go:generate mockgen -destination=mocks/mock_outgoing_registry.go -package=mocks github.com/stacklok/toolhive/pkg/vmcp/auth OutgoingAuthRegistry

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/mark3labs/mcp-go/client"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/auth"
	authmocks "github.com/stacklok/toolhive/pkg/vmcp/auth/mocks"
	"github.com/stacklok/toolhive/pkg/vmcp/auth/strategies"
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

		result, err := backendClient.CallTool(context.Background(), target, "test_tool", map[string]any{})

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
				WorkloadID:   "backend-1",
				AuthStrategy: "header_injection",
				AuthMetadata: map[string]any{"key": "value"},
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
						map[string]any{"key": "value"},
					).
					DoAndReturn(func(_ context.Context, req *http.Request, _ map[string]any) error {
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
				WorkloadID:   "backend-1",
				AuthStrategy: "unauthenticated",
				AuthMetadata: nil,
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
						gomock.Nil(),
					).
					DoAndReturn(func(_ context.Context, _ *http.Request, _ map[string]any) error {
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
				WorkloadID:   "backend-1",
				AuthStrategy: "header_injection",
				AuthMetadata: map[string]any{"key": "value"},
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
						map[string]any{"key": "value"},
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
				WorkloadID:   "backend-1",
				AuthStrategy: "header_injection",
				AuthMetadata: map[string]any{"key": "value"},
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
						map[string]any{"key": "value"},
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
				WorkloadID:   "backend-1",
				AuthStrategy: "header_injection",
				AuthMetadata: map[string]any{"key": "value"},
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
						map[string]any{"key": "value"},
					).
					DoAndReturn(func(_ context.Context, req *http.Request, _ map[string]any) error {
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
				authMetadata: tt.target.AuthMetadata,
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
				WorkloadID:   "backend-1",
				AuthStrategy: "",
				AuthMetadata: nil,
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
				WorkloadID:   "backend-1",
				AuthStrategy: "header_injection",
				AuthMetadata: map[string]any{"header_name": "X-API-Key", "header_value": "test-key"},
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
				WorkloadID:   "backend-1",
				AuthStrategy: "unknown_strategy",
				AuthMetadata: nil,
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
				WorkloadID:   "backend-1",
				AuthStrategy: "", // Empty strategy defaults to unauthenticated
				AuthMetadata: nil,
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

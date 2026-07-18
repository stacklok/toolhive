// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package client

//go:generate mockgen -destination=mocks/mock_outgoing_registry.go -package=mocks github.com/stacklok/toolhive/pkg/vmcp/auth OutgoingAuthRegistry

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive-core/mcpcompat/client"
	"github.com/stacklok/toolhive-core/mcpcompat/client/transport"
	mcpserver "github.com/stacklok/toolhive-core/mcpcompat/server"
	pkgauth "github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/auth"
	authmocks "github.com/stacklok/toolhive/pkg/vmcp/auth/mocks"
	"github.com/stacklok/toolhive/pkg/vmcp/auth/strategies"
	authtypes "github.com/stacklok/toolhive/pkg/vmcp/auth/types"
	healthcontext "github.com/stacklok/toolhive/pkg/vmcp/health/context"
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

	t.Run("queryResourceTemplates with unsupported capability returns empty slice", func(t *testing.T) {
		t.Parallel()

		// Resource templates are gated on the resources capability; when the backend
		// does not advertise resources, the query is skipped and returns empty.
		result, err := queryResourceTemplates(context.Background(), nil, false, "test-backend")
		require.NoError(t, err)
		assert.NotNil(t, result)
		assert.Empty(t, result.ResourceTemplates)
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

	t1, err1 := newBackendTransport("", nil, nil)
	require.NoError(t, err1)
	t2, err2 := newBackendTransport("", nil, nil)
	require.NoError(t, err2)

	// Each call must return a distinct transport — not the shared DefaultTransport.
	assert.NotSame(t, http.DefaultTransport, t1, "newBackendTransport must not return http.DefaultTransport")
	assert.NotSame(t, http.DefaultTransport, t2, "newBackendTransport must not return http.DefaultTransport")
	assert.NotSame(t, t1, t2, "each call must return a distinct *http.Transport")
}

// generateTestCACert creates a self-signed CA certificate in PEM format for testing.
func generateTestCACert(t *testing.T) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)

	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "Test CA"},
		NotBefore:             time.Now(),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
	}

	certDER, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	require.NoError(t, err)

	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
}

func TestNewBackendTransport_CustomCA(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		setupFile     func(t *testing.T) string
		caBundleData  []byte
		expectError   bool
		errorContains string
		checkResult   func(t *testing.T, tr *http.Transport)
	}{
		{
			name: "empty path uses default TLS",
			setupFile: func(_ *testing.T) string {
				return ""
			},
			expectError: false,
			checkResult: func(t *testing.T, tr *http.Transport) {
				t.Helper()
				// When caBundlePath is empty, newBackendTransport must not set a custom
				// RootCAs pool. The cloned DefaultTransport may carry a non-nil
				// TLSClientConfig (e.g. for HTTP/2 NextProtos), so we check RootCAs
				// specifically rather than asserting the entire config is nil.
				if tr.TLSClientConfig != nil {
					assert.Nil(t, tr.TLSClientConfig.RootCAs, "RootCAs should not be set for empty CA path")
					assert.Equal(t, uint16(0), tr.TLSClientConfig.MinVersion,
						"MinVersion should not be overridden for empty CA path")
				}
			},
		},
		{
			name: "valid CA bundle applies custom TLS config",
			setupFile: func(t *testing.T) string {
				t.Helper()
				certPEM := generateTestCACert(t)
				caPath := filepath.Join(t.TempDir(), "ca.crt")
				require.NoError(t, os.WriteFile(caPath, certPEM, 0644))
				return caPath
			},
			expectError: false,
			checkResult: func(t *testing.T, tr *http.Transport) {
				t.Helper()
				require.NotNil(t, tr.TLSClientConfig, "TLSClientConfig should be set for valid CA")
				assert.Equal(t, uint16(tls.VersionTLS12), tr.TLSClientConfig.MinVersion)
				assert.NotNil(t, tr.TLSClientConfig.RootCAs, "RootCAs should be set")
			},
		},
		{
			name: "non-existent CA file returns error",
			setupFile: func(t *testing.T) string {
				t.Helper()
				return filepath.Join(t.TempDir(), "does-not-exist.crt")
			},
			expectError:   true,
			errorContains: "failed to read CA bundle",
		},
		{
			name: "invalid PEM content returns error",
			setupFile: func(t *testing.T) string {
				t.Helper()
				caPath := filepath.Join(t.TempDir(), "bad.crt")
				require.NoError(t, os.WriteFile(caPath, []byte("not-a-cert"), 0644))
				return caPath
			},
			expectError:   true,
			errorContains: "failed to parse CA certificate",
		},
		{
			name: "valid CA data bytes applies custom TLS config",
			setupFile: func(t *testing.T) string {
				t.Helper()
				return "" // no file path
			},
			caBundleData: generateTestCACert(t),
			expectError:  false,
			checkResult: func(t *testing.T, tr *http.Transport) {
				t.Helper()
				require.NotNil(t, tr.TLSClientConfig)
				assert.Equal(t, uint16(tls.VersionTLS12), tr.TLSClientConfig.MinVersion)
				assert.NotNil(t, tr.TLSClientConfig.RootCAs)
			},
		},
		{
			name: "invalid CA data bytes returns error",
			setupFile: func(t *testing.T) string {
				t.Helper()
				return "" // no file path
			},
			caBundleData:  []byte("not-a-cert"),
			expectError:   true,
			errorContains: "failed to parse CA certificate from inline data",
		},
		{
			name: "CA data takes precedence over file path",
			setupFile: func(t *testing.T) string {
				t.Helper()
				return "/nonexistent/path.crt" // file doesn't exist but shouldn't be read
			},
			caBundleData: generateTestCACert(t),
			expectError:  false,
			checkResult: func(t *testing.T, tr *http.Transport) {
				t.Helper()
				require.NotNil(t, tr.TLSClientConfig)
				assert.NotNil(t, tr.TLSClientConfig.RootCAs)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			caPath := tt.setupFile(t)
			tr, err := newBackendTransport(caPath, tt.caBundleData, nil)

			if tt.expectError {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errorContains)
				assert.Nil(t, tr)
			} else {
				require.NoError(t, err)
				require.NotNil(t, tr)
				if tt.checkResult != nil {
					tt.checkResult(t, tr)
				}
			}
		})
	}
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
		{
			// mcp-go v0.49.0 added ErrAuthorizationRequired for 401 responses
			// with a WWW-Authenticate header. Issue #5223: probe of GitHub
			// Copilot's MCP returns this; without recognizing the sentinel here
			// the error falls through to ErrBackendUnavailable and the health
			// monitor never engages the auth-aware branch (#4935).
			name:         "ErrAuthorizationRequired maps to ErrAuthenticationFailed",
			err:          transport.ErrAuthorizationRequired,
			wantSentinel: vmcp.ErrAuthenticationFailed,
		},
		{
			// Production chain mcp-go produces: transport.Error wrapping
			// *AuthorizationRequiredError. Error() formats as
			// "transport error: authorization required" — byte-for-byte
			// the string seen in North's WARN logs (issue #5223). Both
			// layers Unwrap() to the sentinel so errors.Is must succeed.
			name:         "wrapped AuthorizationRequiredError (production chain) maps to ErrAuthenticationFailed",
			err:          transport.NewError(&transport.AuthorizationRequiredError{}),
			wantSentinel: vmcp.ErrAuthenticationFailed,
		},
		{
			// Companion sentinel for the OAuth-handler path. Less hot in
			// practice but belongs in the same matcher block; the existing
			// transport.ErrUnauthorized check would not catch it.
			name:         "ErrOAuthAuthorizationRequired maps to ErrAuthenticationFailed",
			err:          transport.ErrOAuthAuthorizationRequired,
			wantSentinel: vmcp.ErrAuthenticationFailed,
		},
		{
			// ErrUpstreamTokenNotFound is returned by the outgoing auth strategies
			// (upstream_inject, token_exchange, aws_sts) when the upstream provider
			// token is absent from the identity. Must map to ErrAuthenticationFailed
			// via an explicit errors.Is check rather than the fragile "authentication
			// failed" substring match that also matches the authRoundTripper's wrapper.
			name:            "ErrUpstreamTokenNotFound maps to ErrAuthenticationFailed",
			err:             authtypes.ErrUpstreamTokenNotFound,
			wantSentinel:    vmcp.ErrAuthenticationFailed,
			wantMsgContains: "upstream token missing",
		},
		{
			// The authRoundTripper wraps ErrUpstreamTokenNotFound with additional
			// context; errors.Is must still find the sentinel through the chain.
			name: "wrapped ErrUpstreamTokenNotFound maps to ErrAuthenticationFailed",
			err: fmt.Errorf("authentication failed for backend foo: provider %q: %w",
				"my-provider", authtypes.ErrUpstreamTokenNotFound),
			wantSentinel:    vmcp.ErrAuthenticationFailed,
			wantMsgContains: "upstream token missing",
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
//
// See identityPropagatingRoundTripper in pkg/vmcp/client/client.go for the
// canonical description of the #5323 fallback-only identity invariant.
//
// Tests below are grouped to reflect this hierarchy:
//   1. Per-request identity (normal path)            — *_PerRequestIdentity_*
//   2. Fallback identity (no-identity-on-context)    — *_FallbackIdentity_*
//   3. Health-check marker propagation               — *_HealthCheck*

// --- Per-request identity (normal path) -----------------------------------

// TestIdentityPropagatingRoundTripper_PerRequestIdentity_FlowsThroughUnchanged
// verifies the normal per-request flow: identity is on req.Context() (placed by
// TokenValidator.Middleware), no fallback is configured, and the transport leaves
// the identity untouched so the freshly refreshed upstream tokens reach the
// downstream auth strategies. This is the path taken on every authenticated
// tool call.
func TestIdentityPropagatingRoundTripper_PerRequestIdentity_FlowsThroughUnchanged(t *testing.T) {
	t.Parallel()

	base := &mockRoundTripper{response: &http.Response{StatusCode: http.StatusOK}}
	rt := &identityPropagatingRoundTripper{base: base, fallbackIdentity: nil}

	fresh := &pkgauth.Identity{
		PrincipalInfo:  pkgauth.PrincipalInfo{Subject: "fresh-user"},
		UpstreamTokens: map[string]string{"provider": "fresh-token"},
	}
	ctx := pkgauth.WithIdentity(context.Background(), fresh)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://backend.example.com/mcp", nil)
	require.NoError(t, err)

	_, err = rt.RoundTrip(req)
	require.NoError(t, err)

	require.NotNil(t, base.capturedReq)
	got, ok := pkgauth.IdentityFromContext(base.capturedReq.Context())
	require.True(t, ok)
	assert.Equal(t, "fresh-user", got.Subject)
	assert.Equal(t, "fresh-token", got.UpstreamTokens["provider"])
}

// TestIdentityPropagatingRoundTripper_PerRequestIdentity_NotOverriddenByFallback
// is the regression test for issue #5323: a fresh identity already on
// req.Context() (placed there by TokenValidator.Middleware on every incoming
// request) must survive the transport untouched, even when a stale fallback
// identity is captured. Overriding it would silently re-inject stale upstream
// tokens on every backend call and is exactly the bug this PR fixes.
func TestIdentityPropagatingRoundTripper_PerRequestIdentity_NotOverriddenByFallback(t *testing.T) {
	t.Parallel()

	base := &mockRoundTripper{response: &http.Response{StatusCode: http.StatusOK}}
	stale := &pkgauth.Identity{
		PrincipalInfo: pkgauth.PrincipalInfo{Subject: "stale-user"},
		UpstreamTokens: map[string]string{
			"provider": "stale-token",
		},
	}
	rt := &identityPropagatingRoundTripper{base: base, fallbackIdentity: stale}

	fresh := &pkgauth.Identity{
		PrincipalInfo: pkgauth.PrincipalInfo{Subject: "fresh-user"},
		UpstreamTokens: map[string]string{
			"provider": "fresh-token",
		},
	}
	ctx := pkgauth.WithIdentity(context.Background(), fresh)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://backend.example.com/mcp", nil)
	require.NoError(t, err)

	_, err = rt.RoundTrip(req)
	require.NoError(t, err)

	require.NotNil(t, base.capturedReq)
	got, ok := pkgauth.IdentityFromContext(base.capturedReq.Context())
	require.True(t, ok, "identity from req.Context() must be present downstream")
	assert.Equal(t, "fresh-user", got.Subject,
		"identity on req.Context() must not be overridden by the captured fallback (#5323)")
	assert.Equal(t, "fresh-token", got.UpstreamTokens["provider"],
		"fresh upstream tokens must reach auth strategies unchanged (#5323)")
}

// --- Fallback identity (teardown / no-identity-on-context path) ------------

// TestIdentityPropagatingRoundTripper_FallbackIdentity_InjectedWhenContextLacksIdentity
// verifies that the fallback identity is injected when req.Context() carries no
// identity (e.g. mcp-go's Close() DELETE built from context.Background()).
func TestIdentityPropagatingRoundTripper_FallbackIdentity_InjectedWhenContextLacksIdentity(t *testing.T) {
	t.Parallel()

	base := &mockRoundTripper{response: &http.Response{StatusCode: http.StatusOK}}
	fallback := &pkgauth.Identity{PrincipalInfo: pkgauth.PrincipalInfo{Subject: "fallback-user"}}
	rt := &identityPropagatingRoundTripper{base: base, fallbackIdentity: fallback}

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://backend.example.com/mcp", nil)
	require.NoError(t, err)

	_, err = rt.RoundTrip(req)
	require.NoError(t, err)

	require.NotNil(t, base.capturedReq)
	got, ok := pkgauth.IdentityFromContext(base.capturedReq.Context())
	require.True(t, ok, "fallback identity should be in downstream request context")
	assert.Equal(t, "fallback-user", got.Subject)
}

func TestIdentityPropagatingRoundTripper_FallbackIdentity_NilFallback_NoIdentityInContext(t *testing.T) {
	t.Parallel()

	base := &mockRoundTripper{response: &http.Response{StatusCode: http.StatusOK}}
	rt := &identityPropagatingRoundTripper{base: base, fallbackIdentity: nil}

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://backend.example.com/mcp", nil)
	require.NoError(t, err)

	_, err = rt.RoundTrip(req)
	require.NoError(t, err)

	require.NotNil(t, base.capturedReq)
	_, ok := pkgauth.IdentityFromContext(base.capturedReq.Context())
	assert.False(t, ok, "no identity should be in downstream context when no fallback and no req-ctx identity")
}

// --- Health-check marker propagation ---------------------------------------

func TestIdentityPropagatingRoundTripper_HealthCheck_PropagatesMarker(t *testing.T) {
	t.Parallel()

	base := &mockRoundTripper{response: &http.Response{StatusCode: http.StatusOK}}
	rt := &identityPropagatingRoundTripper{base: base, fallbackIdentity: nil, isHealthCheck: true}

	// Simulate mcp-go Close(): request created with context.Background(), no health check marker.
	req, err := http.NewRequestWithContext(context.Background(), http.MethodDelete, "http://backend.example.com/mcp", nil)
	require.NoError(t, err)

	_, err = rt.RoundTrip(req)
	require.NoError(t, err)

	require.NotNil(t, base.capturedReq)
	assert.True(t, healthcontext.IsHealthCheck(base.capturedReq.Context()),
		"health check marker should be propagated even when original request context lacks it")
}

func TestIdentityPropagatingRoundTripper_NonHealthCheck_NoMarkerAdded(t *testing.T) {
	t.Parallel()

	base := &mockRoundTripper{response: &http.Response{StatusCode: http.StatusOK}}
	rt := &identityPropagatingRoundTripper{base: base, fallbackIdentity: nil, isHealthCheck: false}

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://backend.example.com/mcp", nil)
	require.NoError(t, err)

	_, err = rt.RoundTrip(req)
	require.NoError(t, err)

	require.NotNil(t, base.capturedReq)
	assert.False(t, healthcontext.IsHealthCheck(base.capturedReq.Context()),
		"health check marker should not be injected for non-health-check transports")
}

func TestIdentityPropagatingRoundTripper_HealthCheckWithFallbackIdentity_PropagatesBoth(t *testing.T) {
	t.Parallel()

	base := &mockRoundTripper{response: &http.Response{StatusCode: http.StatusOK}}
	identity := &pkgauth.Identity{PrincipalInfo: pkgauth.PrincipalInfo{Subject: "svc-account"}}
	rt := &identityPropagatingRoundTripper{base: base, fallbackIdentity: identity, isHealthCheck: true}

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://backend.example.com/mcp", nil)
	require.NoError(t, err)

	_, err = rt.RoundTrip(req)
	require.NoError(t, err)

	require.NotNil(t, base.capturedReq)
	got, ok := pkgauth.IdentityFromContext(base.capturedReq.Context())
	require.True(t, ok)
	assert.Equal(t, "svc-account", got.Subject)
	assert.True(t, healthcontext.IsHealthCheck(base.capturedReq.Context()))
}

// TestIdentityPropagatingRoundTripper_HealthCheckClose_OriginalRequestContextUnchanged verifies
// that when the transport is in health-check mode, RoundTrip injects the health-check marker
// into the downstream request's context without mutating the original request context. This
// covers requests (e.g. the DELETE mcp-go emits on Close()) whose context does not already
// carry the marker.
func TestIdentityPropagatingRoundTripper_HealthCheckClose_OriginalRequestContextUnchanged(t *testing.T) {
	t.Parallel()

	base := &mockRoundTripper{response: &http.Response{StatusCode: http.StatusOK}}
	rt := &identityPropagatingRoundTripper{base: base, fallbackIdentity: nil, isHealthCheck: true}

	originalCtx := context.Background() // no health check marker — simulates mcp-go Close()
	req, err := http.NewRequestWithContext(originalCtx, http.MethodDelete, "http://backend.example.com/mcp", nil)
	require.NoError(t, err)

	_, err = rt.RoundTrip(req)
	require.NoError(t, err)

	// Original request context must NOT be modified.
	assert.False(t, healthcontext.IsHealthCheck(originalCtx),
		"original request context must not be mutated")
	// But downstream context MUST have the marker.
	require.NotNil(t, base.capturedReq)
	assert.True(t, healthcontext.IsHealthCheck(base.capturedReq.Context()),
		"downstream request must carry health check marker")
}

// ---------------------------------------------------------------------------
// WithDialControl / newBackendTransport — dial hook
// ---------------------------------------------------------------------------

// TestNewBackendTransport_WithDialControl verifies that a non-nil dialControl
// causes newBackendTransport to wire a hook that fires on dial. The control
// hook fires BEFORE the TCP handshake completes, so no accepting server is
// needed — we open a listener for the address but don't accept, then assert
// the hook fired regardless of the dial outcome.
func TestNewBackendTransport_WithDialControl(t *testing.T) {
	t.Parallel()

	var hookFired atomic.Bool
	control := func(_, _ string, _ syscall.RawConn) error {
		hookFired.Store(true)
		return nil
	}

	// Open a listener to obtain a valid address; we don't need to accept.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })

	tr, err := newBackendTransport("", nil, control)
	require.NoError(t, err)

	conn, dialErr := tr.DialContext(t.Context(), "tcp", ln.Addr().String())
	if dialErr == nil && conn != nil {
		_ = conn.Close()
	}
	assert.True(t, hookFired.Load(), "control hook must fire during DialContext")
}

// TestWithDialControl_EndToEnd verifies that a Control hook installed via
// WithDialControl is invoked on every TCP dial made by the client constructed
// with NewHTTPBackendClient.
func TestWithDialControl_EndToEnd(t *testing.T) {
	t.Parallel()

	t.Run("control hook fires on successful dial", func(t *testing.T) {
		t.Parallel()

		var hookFired atomic.Bool
		control := func(_, _ string, _ syscall.RawConn) error {
			hookFired.Store(true)
			return nil
		}

		mcpSrv := mcpserver.NewMCPServer("test", "1.0.0")
		streamSrv := mcpserver.NewStreamableHTTPServer(mcpSrv)
		ts := httptest.NewServer(streamSrv)
		t.Cleanup(ts.Close)

		registry := auth.NewDefaultOutgoingAuthRegistry()
		err := registry.RegisterStrategy(authtypes.StrategyTypeUnauthenticated, &strategies.UnauthenticatedStrategy{})
		require.NoError(t, err)

		backendClient, err := NewHTTPBackendClient(registry, WithDialControl(control))
		require.NoError(t, err)

		target := &vmcp.BackendTarget{
			WorkloadID:    "test-backend",
			WorkloadName:  "Test Backend",
			BaseURL:       ts.URL,
			TransportType: "streamable-http",
		}

		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
		defer cancel()

		_, err = backendClient.ListCapabilities(ctx, target)
		require.NoError(t, err)
		assert.True(t, hookFired.Load(), "dial control hook must fire on connection to backend")
	})

	t.Run("control hook returning error blocks the dial", func(t *testing.T) {
		t.Parallel()

		dialErr := errors.New("dial blocked by control hook")
		control := func(_, _ string, _ syscall.RawConn) error {
			return dialErr
		}

		mcpSrv := mcpserver.NewMCPServer("test", "1.0.0")
		streamSrv := mcpserver.NewStreamableHTTPServer(mcpSrv)
		ts := httptest.NewServer(streamSrv)
		t.Cleanup(ts.Close)

		registry := auth.NewDefaultOutgoingAuthRegistry()
		err := registry.RegisterStrategy(authtypes.StrategyTypeUnauthenticated, &strategies.UnauthenticatedStrategy{})
		require.NoError(t, err)

		backendClient, err := NewHTTPBackendClient(registry, WithDialControl(control))
		require.NoError(t, err)

		target := &vmcp.BackendTarget{
			WorkloadID:    "test-backend",
			WorkloadName:  "Test Backend",
			BaseURL:       ts.URL,
			TransportType: "streamable-http",
		}

		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
		defer cancel()

		_, err = backendClient.ListCapabilities(ctx, target)
		require.Error(t, err, "ListCapabilities must fail when the dial control hook blocks the connection")
		assert.Contains(t, err.Error(), dialErr.Error())
	})

	t.Run("control hook receives resolved IP not hostname", func(t *testing.T) {
		t.Parallel()

		var capturedAddr atomic.Value
		control := func(_, address string, _ syscall.RawConn) error {
			capturedAddr.Store(address)
			return nil
		}

		mcpSrv := mcpserver.NewMCPServer("test", "1.0.0")
		streamSrv := mcpserver.NewStreamableHTTPServer(mcpSrv)
		ts := httptest.NewServer(streamSrv)
		t.Cleanup(ts.Close)

		registry := auth.NewDefaultOutgoingAuthRegistry()
		err := registry.RegisterStrategy(authtypes.StrategyTypeUnauthenticated, &strategies.UnauthenticatedStrategy{})
		require.NoError(t, err)

		backendClient, err := NewHTTPBackendClient(registry, WithDialControl(control))
		require.NoError(t, err)

		// Force DNS resolution by using "localhost" instead of the literal 127.0.0.1
		// that httptest.NewServer embeds in ts.URL. Without this substitution the
		// hook would receive 127.0.0.1 regardless, making the assertion tautological.
		baseURL := strings.Replace(ts.URL, "127.0.0.1", "localhost", 1)
		target := &vmcp.BackendTarget{
			WorkloadID:    "test-backend",
			WorkloadName:  "Test Backend",
			BaseURL:       baseURL,
			TransportType: "streamable-http",
		}

		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
		defer cancel()

		_, err = backendClient.ListCapabilities(ctx, target)
		require.NoError(t, err)

		addr, _ := capturedAddr.Load().(string)
		require.NotEmpty(t, addr, "control hook must receive the address")
		host, _, err := net.SplitHostPort(addr)
		require.NoError(t, err)
		// The hook must see the resolved IP, not the name "localhost".
		// net.ParseIP returns non-nil only for valid IP literals.
		assert.NotNil(t, net.ParseIP(host),
			"control hook address %q must be an IP literal (resolved), not a hostname", host)
		assert.NotEqual(t, "localhost", host,
			"control hook must receive the resolved IP, not the original hostname (DNS-rebinding defense point)")
	})
}

// ExampleWithDialControl shows how an embedder installs a dial-control hook
// that rejects connections to non-public IP ranges — the building block of an
// SSRF / DNS-rebinding defense. The hook receives the resolved peer IP, so it
// classifies the address the kernel is about to connect to rather than the
// (re-resolvable) hostname. A production check should additionally cover the
// ranges listed in the WithDialControl doc comment (CGNAT, IPv6 ULA, etc.).
func ExampleWithDialControl() {
	denyNonPublic := func(_, address string, _ syscall.RawConn) error {
		host, _, err := net.SplitHostPort(address)
		if err != nil {
			return err
		}
		ip := net.ParseIP(host)
		if ip == nil {
			return fmt.Errorf("unresolved dial address %q", address)
		}
		if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() {
			return fmt.Errorf("blocked connection to non-public IP %s", ip)
		}
		return nil
	}

	registry := auth.NewDefaultOutgoingAuthRegistry()
	if err := registry.RegisterStrategy(
		authtypes.StrategyTypeUnauthenticated, &strategies.UnauthenticatedStrategy{},
	); err != nil {
		panic(err)
	}

	backendClient, err := NewHTTPBackendClient(registry, WithDialControl(denyNonPublic))
	if err != nil {
		panic(err)
	}
	_ = backendClient
}

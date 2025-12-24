package health

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/mocks"
)

func TestNewHealthChecker(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	t.Cleanup(ctrl.Finish)

	mockClient := mocks.NewMockBackendClient(ctrl)

	tests := []struct {
		name    string
		timeout time.Duration
	}{
		{
			name:    "with timeout",
			timeout: 5 * time.Second,
		},
		{
			name:    "with zero timeout",
			timeout: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			checker := NewHealthChecker(mockClient, tt.timeout, 0)
			require.NotNil(t, checker)

			// Type assert to access internals for verification
			hc, ok := checker.(*healthChecker)
			require.True(t, ok)
			assert.Equal(t, mockClient, hc.client)
			assert.Equal(t, tt.timeout, hc.timeout)
		})
	}
}

func TestHealthChecker_CheckHealth_Success(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockClient := mocks.NewMockBackendClient(ctrl)
	mockClient.EXPECT().
		ListCapabilities(gomock.Any(), gomock.Any()).
		Return(&vmcp.CapabilityList{}, nil).
		Times(1)

	checker := NewHealthChecker(mockClient, 5*time.Second, 0)
	target := &vmcp.BackendTarget{
		WorkloadID:   "backend-1",
		WorkloadName: "test-backend",
		BaseURL:      "http://localhost:8080",
	}

	status, err := checker.CheckHealth(context.Background(), target)
	assert.NoError(t, err)
	assert.Equal(t, vmcp.BackendHealthy, status)
}

func TestHealthChecker_CheckHealth_ContextCancellation(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockClient := mocks.NewMockBackendClient(ctrl)
	mockClient.EXPECT().
		ListCapabilities(gomock.Any(), gomock.Any()).
		DoAndReturn(func(ctx context.Context, _ *vmcp.BackendTarget) (*vmcp.CapabilityList, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		}).
		Times(1)

	checker := NewHealthChecker(mockClient, 100*time.Millisecond, 0)
	target := &vmcp.BackendTarget{
		WorkloadID:   "backend-1",
		WorkloadName: "test-backend",
		BaseURL:      "http://localhost:8080",
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	status, err := checker.CheckHealth(ctx, target)
	assert.Error(t, err)
	assert.Equal(t, vmcp.BackendUnhealthy, status)
}

func TestHealthChecker_CheckHealth_NoTimeout(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockClient := mocks.NewMockBackendClient(ctrl)
	mockClient.EXPECT().
		ListCapabilities(gomock.Any(), gomock.Any()).
		Return(&vmcp.CapabilityList{}, nil).
		Times(1)

	// Create checker with no timeout
	checker := NewHealthChecker(mockClient, 0, 0)
	target := &vmcp.BackendTarget{
		WorkloadID:   "backend-1",
		WorkloadName: "test-backend",
		BaseURL:      "http://localhost:8080",
	}

	status, err := checker.CheckHealth(context.Background(), target)
	assert.NoError(t, err)
	assert.Equal(t, vmcp.BackendHealthy, status)
}

func TestHealthChecker_CheckHealth_ErrorCategorization(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		err            error
		expectedStatus vmcp.BackendHealthStatus
		description    string
	}{
		{
			name:           "timeout error",
			err:            fmt.Errorf("context deadline exceeded"),
			expectedStatus: vmcp.BackendUnhealthy,
			description:    "should categorize timeout as unhealthy",
		},
		{
			name:           "connection refused",
			err:            fmt.Errorf("connection refused"),
			expectedStatus: vmcp.BackendUnhealthy,
			description:    "should categorize connection error as unhealthy",
		},
		{
			name:           "authentication failed",
			err:            fmt.Errorf("authentication failed: invalid token"),
			expectedStatus: vmcp.BackendUnauthenticated,
			description:    "should categorize auth failure as unauthenticated",
		},
		{
			name:           "401 unauthorized",
			err:            fmt.Errorf("HTTP 401 unauthorized"),
			expectedStatus: vmcp.BackendUnauthenticated,
			description:    "should categorize 401 as unauthenticated",
		},
		{
			name:           "403 forbidden",
			err:            fmt.Errorf("403 forbidden"),
			expectedStatus: vmcp.BackendUnauthenticated,
			description:    "should categorize 403 as unauthenticated",
		},
		{
			name:           "status code 401",
			err:            fmt.Errorf("status code 401"),
			expectedStatus: vmcp.BackendUnauthenticated,
			description:    "should recognize status code format",
		},
		{
			name:           "request unauthenticated",
			err:            fmt.Errorf("request unauthenticated"),
			expectedStatus: vmcp.BackendUnauthenticated,
			description:    "should recognize request unauthenticated",
		},
		{
			name:           "access denied",
			err:            fmt.Errorf("access denied"),
			expectedStatus: vmcp.BackendUnauthenticated,
			description:    "should recognize access denied",
		},
		{
			name:           "generic error",
			err:            fmt.Errorf("unknown error"),
			expectedStatus: vmcp.BackendUnhealthy,
			description:    "should default unknown errors to unhealthy",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mockClient := mocks.NewMockBackendClient(ctrl)
			mockClient.EXPECT().
				ListCapabilities(gomock.Any(), gomock.Any()).
				Return(nil, tt.err).
				Times(1)

			checker := NewHealthChecker(mockClient, 5*time.Second, 0)
			target := &vmcp.BackendTarget{
				WorkloadID:   "backend-1",
				WorkloadName: "test-backend",
				BaseURL:      "http://localhost:8080",
			}

			status, err := checker.CheckHealth(context.Background(), target)
			assert.Error(t, err, tt.description)
			assert.Equal(t, tt.expectedStatus, status, tt.description)
		})
	}
}

func TestCategorizeError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		err            error
		expectedStatus vmcp.BackendHealthStatus
	}{
		{
			name:           "nil error",
			err:            nil,
			expectedStatus: vmcp.BackendHealthy,
		},
		{
			name:           "authentication failed",
			err:            errors.New("authentication failed"),
			expectedStatus: vmcp.BackendUnauthenticated,
		},
		{
			name:           "authentication error",
			err:            errors.New("authentication error: invalid credentials"),
			expectedStatus: vmcp.BackendUnauthenticated,
		},
		{
			name:           "request unauthorized",
			err:            errors.New("request unauthorized"),
			expectedStatus: vmcp.BackendUnauthenticated,
		},
		{
			name:           "HTTP 401",
			err:            errors.New("HTTP 401"),
			expectedStatus: vmcp.BackendUnauthenticated,
		},
		{
			name:           "HTTP 403",
			err:            errors.New("HTTP 403"),
			expectedStatus: vmcp.BackendUnauthenticated,
		},
		{
			name:           "timeout",
			err:            errors.New("request timeout"),
			expectedStatus: vmcp.BackendUnhealthy,
		},
		{
			name:           "deadline exceeded",
			err:            errors.New("context deadline exceeded"),
			expectedStatus: vmcp.BackendUnhealthy,
		},
		{
			name:           "connection refused",
			err:            errors.New("connection refused"),
			expectedStatus: vmcp.BackendUnhealthy,
		},
		{
			name:           "connection reset",
			err:            errors.New("connection reset by peer"),
			expectedStatus: vmcp.BackendUnhealthy,
		},
		{
			name:           "no route to host",
			err:            errors.New("no route to host"),
			expectedStatus: vmcp.BackendUnhealthy,
		},
		{
			name:           "network unreachable",
			err:            errors.New("network is unreachable"),
			expectedStatus: vmcp.BackendUnhealthy,
		},
		{
			name:           "generic error",
			err:            errors.New("something went wrong"),
			expectedStatus: vmcp.BackendUnhealthy,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			status := categorizeError(tt.err)
			assert.Equal(t, tt.expectedStatus, status)
		})
	}
}

func TestIsAuthenticationError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		err       error
		expectErr bool
	}{
		// Positive cases
		{name: "authentication failed", err: errors.New("authentication failed"), expectErr: true},
		{name: "Authentication Failed (uppercase)", err: errors.New("Authentication Failed"), expectErr: true},
		{name: "authentication error", err: errors.New("authentication error: bad token"), expectErr: true},
		{name: "401 unauthorized", err: errors.New("401 unauthorized"), expectErr: true},
		{name: "403 forbidden", err: errors.New("403 forbidden"), expectErr: true},
		{name: "HTTP 401", err: errors.New("HTTP 401"), expectErr: true},
		{name: "HTTP 403", err: errors.New("HTTP 403"), expectErr: true},
		{name: "status code 401", err: errors.New("status code 401"), expectErr: true},
		{name: "status code 403", err: errors.New("status code 403"), expectErr: true},
		{name: "request unauthenticated", err: errors.New("request unauthenticated"), expectErr: true},
		{name: "request unauthorized", err: errors.New("request unauthorized"), expectErr: true},
		{name: "access denied", err: errors.New("access denied"), expectErr: true},

		// Negative cases - should NOT be detected as auth errors
		{name: "connection refused", err: errors.New("connection refused"), expectErr: false},
		{name: "timeout", err: errors.New("request timeout"), expectErr: false},
		{name: "generic error", err: errors.New("something went wrong"), expectErr: false},
		{name: "404 not found", err: errors.New("404 not found"), expectErr: false},
		{name: "500 internal server error", err: errors.New("500 internal server error"), expectErr: false},
		{name: "hostname with 401", err: errors.New("http://backend401.example.com"), expectErr: false},
		{name: "nil error", err: nil, expectErr: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result := vmcp.IsAuthenticationError(tt.err)
			assert.Equal(t, tt.expectErr, result)
		})
	}
}

func TestIsTimeoutError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		err       error
		expectErr bool
	}{
		{name: "timeout", err: errors.New("request timeout"), expectErr: true},
		{name: "deadline exceeded", err: errors.New("deadline exceeded"), expectErr: true},
		{name: "context deadline exceeded", err: errors.New("context deadline exceeded"), expectErr: true},
		{name: "Timeout (uppercase)", err: errors.New("Request Timeout"), expectErr: true},
		{name: "connection refused", err: errors.New("connection refused"), expectErr: false},
		{name: "generic error", err: errors.New("something went wrong"), expectErr: false},
		{name: "nil error", err: nil, expectErr: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result := vmcp.IsTimeoutError(tt.err)
			assert.Equal(t, tt.expectErr, result)
		})
	}
}

func TestIsConnectionError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		err       error
		expectErr bool
	}{
		{name: "connection refused", err: errors.New("connection refused"), expectErr: true},
		{name: "connection reset", err: errors.New("connection reset by peer"), expectErr: true},
		{name: "no route to host", err: errors.New("no route to host"), expectErr: true},
		{name: "network unreachable", err: errors.New("network is unreachable"), expectErr: true},
		{name: "Connection Refused (uppercase)", err: errors.New("Connection Refused"), expectErr: true},
		{name: "timeout", err: errors.New("request timeout"), expectErr: false},
		{name: "authentication failed", err: errors.New("authentication failed"), expectErr: false},
		{name: "generic error", err: errors.New("something went wrong"), expectErr: false},
		{name: "nil error", err: nil, expectErr: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result := vmcp.IsConnectionError(tt.err)
			assert.Equal(t, tt.expectErr, result)
		})
	}
}

func TestHealthChecker_CheckHealth_Timeout(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockClient := mocks.NewMockBackendClient(ctrl)
	mockClient.EXPECT().
		ListCapabilities(gomock.Any(), gomock.Any()).
		DoAndReturn(func(ctx context.Context, _ *vmcp.BackendTarget) (*vmcp.CapabilityList, error) {
			// Simulate slow backend
			select {
			case <-time.After(2 * time.Second):
				return &vmcp.CapabilityList{}, nil
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}).
		Times(1)

	checker := NewHealthChecker(mockClient, 100*time.Millisecond, 0)
	target := &vmcp.BackendTarget{
		WorkloadID:   "backend-1",
		WorkloadName: "test-backend",
		BaseURL:      "http://localhost:8080",
	}

	status, err := checker.CheckHealth(context.Background(), target)
	assert.Error(t, err)
	assert.Equal(t, vmcp.BackendUnhealthy, status)
}

func TestHealthChecker_CheckHealth_MultipleBackends(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockClient := mocks.NewMockBackendClient(ctrl)

	// Setup different responses for different backends
	mockClient.EXPECT().
		ListCapabilities(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, target *vmcp.BackendTarget) (*vmcp.CapabilityList, error) {
			switch target.WorkloadID {
			case "backend-healthy":
				return &vmcp.CapabilityList{}, nil
			case "backend-auth-error":
				return nil, errors.New("authentication failed")
			case "backend-timeout":
				return nil, errors.New("context deadline exceeded")
			default:
				return nil, errors.New("unknown error")
			}
		}).
		Times(4)

	checker := NewHealthChecker(mockClient, 5*time.Second, 0)

	// Test healthy backend
	status, err := checker.CheckHealth(context.Background(), &vmcp.BackendTarget{
		WorkloadID:   "backend-healthy",
		WorkloadName: "Healthy Backend",
		BaseURL:      "http://localhost:8080",
	})
	assert.NoError(t, err)
	assert.Equal(t, vmcp.BackendHealthy, status)

	// Test auth error backend
	status, err = checker.CheckHealth(context.Background(), &vmcp.BackendTarget{
		WorkloadID:   "backend-auth-error",
		WorkloadName: "Auth Error Backend",
		BaseURL:      "http://localhost:8081",
	})
	assert.Error(t, err)
	assert.Equal(t, vmcp.BackendUnauthenticated, status)

	// Test timeout backend
	status, err = checker.CheckHealth(context.Background(), &vmcp.BackendTarget{
		WorkloadID:   "backend-timeout",
		WorkloadName: "Timeout Backend",
		BaseURL:      "http://localhost:8082",
	})
	assert.Error(t, err)
	assert.Equal(t, vmcp.BackendUnhealthy, status)

	// Test unknown error backend
	status, err = checker.CheckHealth(context.Background(), &vmcp.BackendTarget{
		WorkloadID:   "backend-unknown",
		WorkloadName: "Unknown Backend",
		BaseURL:      "http://localhost:8083",
	})
	assert.Error(t, err)
	assert.Equal(t, vmcp.BackendUnhealthy, status)
}

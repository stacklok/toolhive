// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

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

	status, reason, err := checker.CheckHealth(context.Background(), target)
	assert.NoError(t, err)
	assert.Equal(t, vmcp.BackendHealthy, status)
	assert.Equal(t, vmcp.ReasonHealthy, reason)
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

	status, reason, err := checker.CheckHealth(ctx, target)
	assert.Error(t, err)
	assert.Equal(t, vmcp.BackendUnhealthy, status)
	assert.Equal(t, vmcp.ReasonTimeout, reason)
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

	status, reason, err := checker.CheckHealth(context.Background(), target)
	assert.NoError(t, err)
	assert.Equal(t, vmcp.BackendHealthy, status)
	assert.Equal(t, vmcp.ReasonHealthy, reason)
}

func TestHealthChecker_CheckHealth_ErrorCategorization(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		err            error
		expectedStatus vmcp.BackendHealthStatus
		expectedReason vmcp.BackendHealthReason
		description    string
	}{
		{
			name:           "timeout error",
			err:            fmt.Errorf("context deadline exceeded"),
			expectedStatus: vmcp.BackendUnhealthy,
			expectedReason: vmcp.ReasonTimeout,
			description:    "should categorize timeout as unhealthy with timeout reason",
		},
		{
			name:           "connection refused",
			err:            fmt.Errorf("connection refused"),
			expectedStatus: vmcp.BackendUnhealthy,
			expectedReason: vmcp.ReasonConnectionRefused,
			description:    "should categorize connection error as unhealthy with connection_refused reason",
		},
		{
			name:           "authentication failed",
			err:            fmt.Errorf("authentication failed: invalid token"),
			expectedStatus: vmcp.BackendUnhealthy,
			expectedReason: vmcp.ReasonAuthenticationFailed,
			description:    "should categorize auth failure as unhealthy with authentication_failed reason",
		},
		{
			name:           "401 unauthorized",
			err:            fmt.Errorf("HTTP 401 unauthorized"),
			expectedStatus: vmcp.BackendUnhealthy,
			expectedReason: vmcp.ReasonAuthenticationFailed,
			description:    "should categorize 401 as unhealthy with authentication_failed reason",
		},
		{
			name:           "403 forbidden",
			err:            fmt.Errorf("403 forbidden"),
			expectedStatus: vmcp.BackendUnhealthy,
			expectedReason: vmcp.ReasonAuthenticationFailed,
			description:    "should categorize 403 as unhealthy with authentication_failed reason",
		},
		{
			name:           "status code 401",
			err:            fmt.Errorf("status code 401"),
			expectedStatus: vmcp.BackendUnhealthy,
			expectedReason: vmcp.ReasonAuthenticationFailed,
			description:    "should recognize status code format as authentication_failed",
		},
		{
			name:           "request unauthenticated",
			err:            fmt.Errorf("request unauthenticated"),
			expectedStatus: vmcp.BackendUnhealthy,
			expectedReason: vmcp.ReasonAuthenticationFailed,
			description:    "should recognize request unauthenticated as authentication_failed",
		},
		{
			name:           "access denied",
			err:            fmt.Errorf("access denied"),
			expectedStatus: vmcp.BackendUnhealthy,
			expectedReason: vmcp.ReasonAuthenticationFailed,
			description:    "should recognize access denied as authentication_failed",
		},
		{
			name:           "generic error",
			err:            fmt.Errorf("unknown error"),
			expectedStatus: vmcp.BackendUnhealthy,
			expectedReason: vmcp.ReasonHealthCheckFailed,
			description:    "should default unknown errors to unhealthy with health_check_failed reason",
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

			status, reason, err := checker.CheckHealth(context.Background(), target)
			assert.Error(t, err, tt.description)
			assert.Equal(t, tt.expectedStatus, status, tt.description)
			assert.Equal(t, tt.expectedReason, reason, tt.description)
		})
	}
}

func TestCategorizeError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		err            error
		expectedStatus vmcp.BackendHealthStatus
		expectedReason vmcp.BackendHealthReason
	}{
		{
			name:           "nil error",
			err:            nil,
			expectedStatus: vmcp.BackendHealthy,
			expectedReason: vmcp.ReasonHealthy,
		},
		{
			name:           "authentication failed",
			err:            errors.New("authentication failed"),
			expectedStatus: vmcp.BackendUnhealthy,
			expectedReason: vmcp.ReasonAuthenticationFailed,
		},
		{
			name:           "authentication error",
			err:            errors.New("authentication error: invalid credentials"),
			expectedStatus: vmcp.BackendUnhealthy,
			expectedReason: vmcp.ReasonAuthenticationFailed,
		},
		{
			name:           "request unauthorized",
			err:            errors.New("request unauthorized"),
			expectedStatus: vmcp.BackendUnhealthy,
			expectedReason: vmcp.ReasonAuthenticationFailed,
		},
		{
			name:           "HTTP 401",
			err:            errors.New("HTTP 401"),
			expectedStatus: vmcp.BackendUnhealthy,
			expectedReason: vmcp.ReasonAuthenticationFailed,
		},
		{
			name:           "HTTP 403",
			err:            errors.New("HTTP 403"),
			expectedStatus: vmcp.BackendUnhealthy,
			expectedReason: vmcp.ReasonAuthenticationFailed,
		},
		{
			name:           "timeout",
			err:            errors.New("request timeout"),
			expectedStatus: vmcp.BackendUnhealthy,
			expectedReason: vmcp.ReasonTimeout,
		},
		{
			name:           "deadline exceeded",
			err:            errors.New("context deadline exceeded"),
			expectedStatus: vmcp.BackendUnhealthy,
			expectedReason: vmcp.ReasonTimeout,
		},
		{
			name:           "connection refused",
			err:            errors.New("connection refused"),
			expectedStatus: vmcp.BackendUnhealthy,
			expectedReason: vmcp.ReasonConnectionRefused,
		},
		{
			name:           "connection reset",
			err:            errors.New("connection reset by peer"),
			expectedStatus: vmcp.BackendUnhealthy,
			expectedReason: vmcp.ReasonHealthCheckFailed,
		},
		{
			name:           "no route to host",
			err:            errors.New("no route to host"),
			expectedStatus: vmcp.BackendUnhealthy,
			expectedReason: vmcp.ReasonNetworkUnreachable,
		},
		{
			name:           "network unreachable",
			err:            errors.New("network is unreachable"),
			expectedStatus: vmcp.BackendUnhealthy,
			expectedReason: vmcp.ReasonNetworkUnreachable,
		},
		{
			name:           "generic error",
			err:            errors.New("something went wrong"),
			expectedStatus: vmcp.BackendUnhealthy,
			expectedReason: vmcp.ReasonHealthCheckFailed,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			status, reason := categorizeError(tt.err)
			assert.Equal(t, tt.expectedStatus, status)
			assert.Equal(t, tt.expectedReason, reason)
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

	status, reason, err := checker.CheckHealth(context.Background(), target)
	assert.Error(t, err)
	assert.Equal(t, vmcp.BackendUnhealthy, status)
	assert.Equal(t, vmcp.ReasonTimeout, reason)
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
	status, reason, err := checker.CheckHealth(context.Background(), &vmcp.BackendTarget{
		WorkloadID:   "backend-healthy",
		WorkloadName: "Healthy Backend",
		BaseURL:      "http://localhost:8080",
	})
	assert.NoError(t, err)
	assert.Equal(t, vmcp.BackendHealthy, status)
	assert.Equal(t, vmcp.ReasonHealthy, reason)

	// Test auth error backend
	status, reason, err = checker.CheckHealth(context.Background(), &vmcp.BackendTarget{
		WorkloadID:   "backend-auth-error",
		WorkloadName: "Auth Error Backend",
		BaseURL:      "http://localhost:8081",
	})
	assert.Error(t, err)
	assert.Equal(t, vmcp.BackendUnhealthy, status)
	assert.Equal(t, vmcp.ReasonAuthenticationFailed, reason)

	// Test timeout backend
	status, reason, err = checker.CheckHealth(context.Background(), &vmcp.BackendTarget{
		WorkloadID:   "backend-timeout",
		WorkloadName: "Timeout Backend",
		BaseURL:      "http://localhost:8082",
	})
	assert.Error(t, err)
	assert.Equal(t, vmcp.BackendUnhealthy, status)
	assert.Equal(t, vmcp.ReasonTimeout, reason)

	// Test unknown error backend
	status, reason, err = checker.CheckHealth(context.Background(), &vmcp.BackendTarget{
		WorkloadID:   "backend-unknown",
		WorkloadName: "Unknown Backend",
		BaseURL:      "http://localhost:8083",
	})
	assert.Error(t, err)
	assert.Equal(t, vmcp.BackendUnhealthy, status)
	assert.Equal(t, vmcp.ReasonHealthCheckFailed, reason)
}

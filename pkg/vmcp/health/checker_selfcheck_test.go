package health

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/mocks"
)

// TestHealthChecker_CheckHealth_SelfCheck tests self-check detection
func TestHealthChecker_CheckHealth_SelfCheck(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockClient := mocks.NewMockBackendClient(ctrl)
	// Should not call ListCapabilities for self-check
	mockClient.EXPECT().
		ListCapabilities(gomock.Any(), gomock.Any()).
		Times(0)

	checker := NewHealthChecker(mockClient, 5*time.Second, 0, "http://127.0.0.1:8080")
	target := &vmcp.BackendTarget{
		WorkloadID:   "backend-1",
		WorkloadName: "test-backend",
		BaseURL:      "http://127.0.0.1:8080", // Same as selfURL
	}

	status, err := checker.CheckHealth(context.Background(), target)
	assert.NoError(t, err)
	assert.Equal(t, vmcp.BackendHealthy, status)
}

// TestHealthChecker_CheckHealth_SelfCheck_Localhost tests localhost normalization
func TestHealthChecker_CheckHealth_SelfCheck_Localhost(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockClient := mocks.NewMockBackendClient(ctrl)
	mockClient.EXPECT().
		ListCapabilities(gomock.Any(), gomock.Any()).
		Times(0)

	checker := NewHealthChecker(mockClient, 5*time.Second, 0, "http://localhost:8080")
	target := &vmcp.BackendTarget{
		WorkloadID:   "backend-1",
		WorkloadName: "test-backend",
		BaseURL:      "http://127.0.0.1:8080", // localhost should match 127.0.0.1
	}

	status, err := checker.CheckHealth(context.Background(), target)
	assert.NoError(t, err)
	assert.Equal(t, vmcp.BackendHealthy, status)
}

// TestHealthChecker_CheckHealth_SelfCheck_Reverse tests reverse localhost normalization
func TestHealthChecker_CheckHealth_SelfCheck_Reverse(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockClient := mocks.NewMockBackendClient(ctrl)
	mockClient.EXPECT().
		ListCapabilities(gomock.Any(), gomock.Any()).
		Times(0)

	checker := NewHealthChecker(mockClient, 5*time.Second, 0, "http://127.0.0.1:8080")
	target := &vmcp.BackendTarget{
		WorkloadID:   "backend-1",
		WorkloadName: "test-backend",
		BaseURL:      "http://localhost:8080", // 127.0.0.1 should match localhost
	}

	status, err := checker.CheckHealth(context.Background(), target)
	assert.NoError(t, err)
	assert.Equal(t, vmcp.BackendHealthy, status)
}

// TestHealthChecker_CheckHealth_SelfCheck_DifferentPort tests different ports don't match
func TestHealthChecker_CheckHealth_SelfCheck_DifferentPort(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockClient := mocks.NewMockBackendClient(ctrl)
	mockClient.EXPECT().
		ListCapabilities(gomock.Any(), gomock.Any()).
		Return(&vmcp.CapabilityList{}, nil).
		Times(1)

	checker := NewHealthChecker(mockClient, 5*time.Second, 0, "http://127.0.0.1:8080")
	target := &vmcp.BackendTarget{
		WorkloadID:   "backend-1",
		WorkloadName: "test-backend",
		BaseURL:      "http://127.0.0.1:8081", // Different port
	}

	status, err := checker.CheckHealth(context.Background(), target)
	assert.NoError(t, err)
	assert.Equal(t, vmcp.BackendHealthy, status)
}

// TestHealthChecker_CheckHealth_SelfCheck_EmptyURL tests empty URLs
func TestHealthChecker_CheckHealth_SelfCheck_EmptyURL(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockClient := mocks.NewMockBackendClient(ctrl)
	mockClient.EXPECT().
		ListCapabilities(gomock.Any(), gomock.Any()).
		Return(&vmcp.CapabilityList{}, nil).
		Times(1)

	checker := NewHealthChecker(mockClient, 5*time.Second, 0, "")
	target := &vmcp.BackendTarget{
		WorkloadID:   "backend-1",
		WorkloadName: "test-backend",
		BaseURL:      "http://127.0.0.1:8080",
	}

	status, err := checker.CheckHealth(context.Background(), target)
	assert.NoError(t, err)
	assert.Equal(t, vmcp.BackendHealthy, status)
}

// TestHealthChecker_CheckHealth_SelfCheck_InvalidURL tests invalid URLs
func TestHealthChecker_CheckHealth_SelfCheck_InvalidURL(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockClient := mocks.NewMockBackendClient(ctrl)
	mockClient.EXPECT().
		ListCapabilities(gomock.Any(), gomock.Any()).
		Return(&vmcp.CapabilityList{}, nil).
		Times(1)

	checker := NewHealthChecker(mockClient, 5*time.Second, 0, "not-a-valid-url")
	target := &vmcp.BackendTarget{
		WorkloadID:   "backend-1",
		WorkloadName: "test-backend",
		BaseURL:      "http://127.0.0.1:8080",
	}

	status, err := checker.CheckHealth(context.Background(), target)
	assert.NoError(t, err)
	assert.Equal(t, vmcp.BackendHealthy, status)
}

// TestHealthChecker_CheckHealth_SelfCheck_WithPath tests URLs with paths are normalized
func TestHealthChecker_CheckHealth_SelfCheck_WithPath(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockClient := mocks.NewMockBackendClient(ctrl)
	mockClient.EXPECT().
		ListCapabilities(gomock.Any(), gomock.Any()).
		Times(0)

	checker := NewHealthChecker(mockClient, 5*time.Second, 0, "http://127.0.0.1:8080")
	target := &vmcp.BackendTarget{
		WorkloadID:   "backend-1",
		WorkloadName: "test-backend",
		BaseURL:      "http://127.0.0.1:8080/mcp", // Path should be ignored
	}

	status, err := checker.CheckHealth(context.Background(), target)
	assert.NoError(t, err)
	assert.Equal(t, vmcp.BackendHealthy, status)
}

// TestHealthChecker_CheckHealth_DegradedThreshold tests degraded threshold detection
func TestHealthChecker_CheckHealth_DegradedThreshold(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockClient := mocks.NewMockBackendClient(ctrl)
	mockClient.EXPECT().
		ListCapabilities(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, _ *vmcp.BackendTarget) (*vmcp.CapabilityList, error) {
			// Simulate slow response
			time.Sleep(150 * time.Millisecond)
			return &vmcp.CapabilityList{}, nil
		}).
		Times(1)

	// Set degraded threshold to 100ms
	checker := NewHealthChecker(mockClient, 5*time.Second, 100*time.Millisecond, "")
	target := &vmcp.BackendTarget{
		WorkloadID:   "backend-1",
		WorkloadName: "test-backend",
		BaseURL:      "http://localhost:8080",
	}

	status, err := checker.CheckHealth(context.Background(), target)
	assert.NoError(t, err)
	assert.Equal(t, vmcp.BackendDegraded, status, "Should mark as degraded when response time exceeds threshold")
}

// TestHealthChecker_CheckHealth_DegradedThreshold_Disabled tests disabled degraded threshold
func TestHealthChecker_CheckHealth_DegradedThreshold_Disabled(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockClient := mocks.NewMockBackendClient(ctrl)
	mockClient.EXPECT().
		ListCapabilities(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, _ *vmcp.BackendTarget) (*vmcp.CapabilityList, error) {
			// Simulate slow response
			time.Sleep(150 * time.Millisecond)
			return &vmcp.CapabilityList{}, nil
		}).
		Times(1)

	// Set degraded threshold to 0 (disabled)
	checker := NewHealthChecker(mockClient, 5*time.Second, 0, "")
	target := &vmcp.BackendTarget{
		WorkloadID:   "backend-1",
		WorkloadName: "test-backend",
		BaseURL:      "http://localhost:8080",
	}

	status, err := checker.CheckHealth(context.Background(), target)
	assert.NoError(t, err)
	assert.Equal(t, vmcp.BackendHealthy, status, "Should not mark as degraded when threshold is disabled")
}

// TestHealthChecker_CheckHealth_DegradedThreshold_FastResponse tests fast response doesn't trigger degraded
func TestHealthChecker_CheckHealth_DegradedThreshold_FastResponse(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockClient := mocks.NewMockBackendClient(ctrl)
	mockClient.EXPECT().
		ListCapabilities(gomock.Any(), gomock.Any()).
		Return(&vmcp.CapabilityList{}, nil).
		Times(1)

	// Set degraded threshold to 100ms
	checker := NewHealthChecker(mockClient, 5*time.Second, 100*time.Millisecond, "")
	target := &vmcp.BackendTarget{
		WorkloadID:   "backend-1",
		WorkloadName: "test-backend",
		BaseURL:      "http://localhost:8080",
	}

	status, err := checker.CheckHealth(context.Background(), target)
	assert.NoError(t, err)
	assert.Equal(t, vmcp.BackendHealthy, status, "Should not mark as degraded when response is fast")
}

// TestCategorizeError_SentinelErrors tests sentinel error categorization
func TestCategorizeError_SentinelErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		err            error
		expectedStatus vmcp.BackendHealthStatus
	}{
		{
			name:           "ErrAuthenticationFailed",
			err:            vmcp.ErrAuthenticationFailed,
			expectedStatus: vmcp.BackendUnauthenticated,
		},
		{
			name:           "ErrAuthorizationFailed",
			err:            vmcp.ErrAuthorizationFailed,
			expectedStatus: vmcp.BackendUnauthenticated,
		},
		{
			name:           "ErrTimeout",
			err:            vmcp.ErrTimeout,
			expectedStatus: vmcp.BackendUnhealthy,
		},
		{
			name:           "ErrCancelled",
			err:            vmcp.ErrCancelled,
			expectedStatus: vmcp.BackendUnhealthy,
		},
		{
			name:           "ErrBackendUnavailable",
			err:            vmcp.ErrBackendUnavailable,
			expectedStatus: vmcp.BackendUnhealthy,
		},
		{
			name:           "wrapped ErrAuthenticationFailed",
			err:            errors.New("wrapped: " + vmcp.ErrAuthenticationFailed.Error()),
			expectedStatus: vmcp.BackendUnauthenticated,
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

// TestNormalizeURLForComparison tests URL normalization
func TestNormalizeURLForComparison(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    string
		expected string
		wantErr  bool
	}{
		{
			name:     "localhost normalized to 127.0.0.1",
			input:    "http://localhost:8080",
			expected: "http://127.0.0.1:8080",
			wantErr:  false,
		},
		{
			name:     "127.0.0.1 stays as is",
			input:    "http://127.0.0.1:8080",
			expected: "http://127.0.0.1:8080",
			wantErr:  false,
		},
		{
			name:     "path is ignored",
			input:    "http://127.0.0.1:8080/mcp",
			expected: "http://127.0.0.1:8080",
			wantErr:  false,
		},
		{
			name:     "query is ignored",
			input:    "http://127.0.0.1:8080?param=value",
			expected: "http://127.0.0.1:8080",
			wantErr:  false,
		},
		{
			name:     "fragment is ignored",
			input:    "http://127.0.0.1:8080#fragment",
			expected: "http://127.0.0.1:8080",
			wantErr:  false,
		},
		{
			name:     "scheme is lowercased",
			input:    "HTTP://127.0.0.1:8080",
			expected: "http://127.0.0.1:8080",
			wantErr:  false,
		},
		{
			name:     "host is lowercased",
			input:    "http://EXAMPLE.COM:8080",
			expected: "http://example.com:8080",
			wantErr:  false,
		},
		{
			name:     "no port",
			input:    "http://127.0.0.1",
			expected: "http://127.0.0.1",
			wantErr:  false,
		},
		{
			name:    "invalid URL",
			input:   "not-a-url",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result, err := normalizeURLForComparison(tt.input)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.expected, result)
			}
		})
	}
}

// TestIsSelfCheck_EdgeCases tests edge cases for self-check detection
func TestIsSelfCheck_EdgeCases(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockClient := mocks.NewMockBackendClient(ctrl)

	tests := []struct {
		name     string
		selfURL  string
		backendURL string
		expected bool
	}{
		{
			name:      "both empty",
			selfURL:   "",
			backendURL: "",
			expected:  false,
		},
		{
			name:      "selfURL empty",
			selfURL:   "",
			backendURL: "http://127.0.0.1:8080",
			expected:  false,
		},
		{
			name:      "backendURL empty",
			selfURL:   "http://127.0.0.1:8080",
			backendURL: "",
			expected:  false,
		},
		{
			name:      "localhost matches 127.0.0.1",
			selfURL:   "http://localhost:8080",
			backendURL: "http://127.0.0.1:8080",
			expected:  true,
		},
		{
			name:      "127.0.0.1 matches localhost",
			selfURL:   "http://127.0.0.1:8080",
			backendURL: "http://localhost:8080",
			expected:  true,
		},
		{
			name:      "different ports",
			selfURL:   "http://127.0.0.1:8080",
			backendURL: "http://127.0.0.1:8081",
			expected:  false,
		},
		{
			name:      "different hosts",
			selfURL:   "http://127.0.0.1:8080",
			backendURL: "http://192.168.1.1:8080",
			expected:  false,
		},
		{
			name:      "path ignored",
			selfURL:   "http://127.0.0.1:8080",
			backendURL: "http://127.0.0.1:8080/mcp",
			expected:  true,
		},
		{
			name:      "query ignored",
			selfURL:   "http://127.0.0.1:8080",
			backendURL: "http://127.0.0.1:8080?param=value",
			expected:  true,
		},
		{
			name:      "invalid selfURL",
			selfURL:   "not-a-url",
			backendURL: "http://127.0.0.1:8080",
			expected:  false,
		},
		{
			name:      "invalid backendURL",
			selfURL:   "http://127.0.0.1:8080",
			backendURL: "not-a-url",
			expected:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			checker := NewHealthChecker(mockClient, 5*time.Second, 0, tt.selfURL)
			hc, ok := checker.(*healthChecker)
			require.True(t, ok)

			result := hc.isSelfCheck(tt.backendURL)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package vmcp

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const testStatusUnavailable = "unavailable"

func TestBackendHealthStatus_ToCRDStatus(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		status   BackendHealthStatus
		expected string
	}{
		{
			name:     "healthy maps to ready",
			status:   BackendHealthy,
			expected: "ready",
		},
		{
			name:     "degraded maps to degraded",
			status:   BackendDegraded,
			expected: "degraded",
		},
		{
			name:     "unhealthy maps to unavailable",
			status:   BackendUnhealthy,
			expected: testStatusUnavailable,
		},
		{
			name:     "unauthenticated maps to unavailable",
			status:   BackendUnauthenticated,
			expected: testStatusUnavailable,
		},
		{
			name:     "unknown maps to unknown",
			status:   BackendUnknown,
			expected: "unknown",
		},
		{
			name:     "empty status maps to unknown",
			status:   BackendHealthStatus(""),
			expected: "unknown",
		},
		{
			name:     "invalid status maps to unknown",
			status:   BackendHealthStatus("invalid-status"),
			expected: "unknown",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result := tt.status.ToCRDStatus()

			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestBackendHealthStatus_ToCRDStatus_AllHealthStatusesCovered(t *testing.T) {
	t.Parallel()

	// Ensure all defined BackendHealthStatus constants are tested
	allStatuses := []BackendHealthStatus{
		BackendHealthy,
		BackendDegraded,
		BackendUnhealthy,
		BackendUnknown,
		BackendUnauthenticated,
	}

	// Verify each status maps to a valid CRD status
	validCRDStatuses := map[string]bool{
		"ready":               true,
		"degraded":            true,
		testStatusUnavailable: true,
		"unknown":             true,
	}

	for _, status := range allStatuses {
		crdStatus := status.ToCRDStatus()
		assert.True(t, validCRDStatuses[crdStatus],
			"status %q should map to a valid CRD status, got %q", status, crdStatus)
	}
}

func TestDiscoveredBackend_DeepCopyInto(t *testing.T) {
	t.Parallel()

	t.Run("copies all fields correctly", func(t *testing.T) {
		t.Parallel()

		original := &DiscoveredBackend{
			Name:            "github-mcp",
			URL:             "http://localhost:8080",
			Status:          "ready",
			AuthConfigRef:   "github-auth",
			AuthType:        "oauth2",
			LastHealthCheck: metav1.NewTime(time.Date(2024, 1, 15, 10, 30, 0, 0, time.UTC)),
			Message:         "Backend is healthy",
		}

		copied := &DiscoveredBackend{}
		original.DeepCopyInto(copied)

		assert.Equal(t, original.Name, copied.Name)
		assert.Equal(t, original.URL, copied.URL)
		assert.Equal(t, original.Status, copied.Status)
		assert.Equal(t, original.AuthConfigRef, copied.AuthConfigRef)
		assert.Equal(t, original.AuthType, copied.AuthType)
		assert.Equal(t, original.LastHealthCheck, copied.LastHealthCheck)
		assert.Equal(t, original.Message, copied.Message)
	})

	t.Run("modifications to copy do not affect original", func(t *testing.T) {
		t.Parallel()

		original := &DiscoveredBackend{
			Name:            "github-mcp",
			URL:             "http://localhost:8080",
			Status:          "ready",
			LastHealthCheck: metav1.NewTime(time.Date(2024, 1, 15, 10, 30, 0, 0, time.UTC)),
		}

		copied := &DiscoveredBackend{}
		original.DeepCopyInto(copied)

		// Modify the copy
		copied.Name = "modified-name"
		copied.URL = "http://modified:9090"
		copied.Status = testStatusUnavailable
		copied.LastHealthCheck = metav1.NewTime(time.Date(2025, 12, 1, 0, 0, 0, 0, time.UTC))

		// Original should be unchanged
		assert.Equal(t, "github-mcp", original.Name)
		assert.Equal(t, "http://localhost:8080", original.URL)
		assert.Equal(t, "ready", original.Status)
		assert.Equal(t, time.Date(2024, 1, 15, 10, 30, 0, 0, time.UTC), original.LastHealthCheck.Time)
	})

	t.Run("handles empty fields", func(t *testing.T) {
		t.Parallel()

		original := &DiscoveredBackend{
			Name: "minimal",
		}

		copied := &DiscoveredBackend{}
		original.DeepCopyInto(copied)

		assert.Equal(t, "minimal", copied.Name)
		assert.Empty(t, copied.URL)
		assert.Empty(t, copied.Status)
		assert.Empty(t, copied.AuthConfigRef)
		assert.Empty(t, copied.AuthType)
		assert.True(t, copied.LastHealthCheck.IsZero())
		assert.Empty(t, copied.Message)
	})

	t.Run("handles zero time correctly", func(t *testing.T) {
		t.Parallel()

		original := &DiscoveredBackend{
			Name:            "test",
			LastHealthCheck: metav1.Time{},
		}

		copied := &DiscoveredBackend{}
		original.DeepCopyInto(copied)

		assert.True(t, copied.LastHealthCheck.IsZero())
	})
}

func TestDiscoveredBackend_DeepCopy(t *testing.T) {
	t.Parallel()

	t.Run("returns nil for nil receiver", func(t *testing.T) {
		t.Parallel()

		var original *DiscoveredBackend
		result := original.DeepCopy()

		assert.Nil(t, result)
	})

	t.Run("returns independent copy", func(t *testing.T) {
		t.Parallel()

		original := &DiscoveredBackend{
			Name:            "github-mcp",
			URL:             "http://localhost:8080",
			Status:          "ready",
			AuthConfigRef:   "github-auth",
			AuthType:        "oauth2",
			LastHealthCheck: metav1.NewTime(time.Date(2024, 1, 15, 10, 30, 0, 0, time.UTC)),
			Message:         "Backend is healthy",
		}

		copied := original.DeepCopy()

		// Verify it's a different pointer
		assert.NotSame(t, original, copied)

		// Verify all fields are equal
		assert.Equal(t, original.Name, copied.Name)
		assert.Equal(t, original.URL, copied.URL)
		assert.Equal(t, original.Status, copied.Status)
		assert.Equal(t, original.AuthConfigRef, copied.AuthConfigRef)
		assert.Equal(t, original.AuthType, copied.AuthType)
		assert.Equal(t, original.LastHealthCheck, copied.LastHealthCheck)
		assert.Equal(t, original.Message, copied.Message)
	})

	t.Run("modifications to copy do not affect original", func(t *testing.T) {
		t.Parallel()

		original := &DiscoveredBackend{
			Name:            "github-mcp",
			URL:             "http://localhost:8080",
			Status:          "ready",
			LastHealthCheck: metav1.NewTime(time.Date(2024, 1, 15, 10, 30, 0, 0, time.UTC)),
		}

		copied := original.DeepCopy()

		// Modify the copy
		copied.Name = "modified-name"
		copied.URL = "http://modified:9090"
		copied.Status = testStatusUnavailable
		copied.LastHealthCheck = metav1.NewTime(time.Date(2025, 12, 1, 0, 0, 0, 0, time.UTC))

		// Original should be unchanged
		assert.Equal(t, "github-mcp", original.Name)
		assert.Equal(t, "http://localhost:8080", original.URL)
		assert.Equal(t, "ready", original.Status)
		assert.Equal(t, time.Date(2024, 1, 15, 10, 30, 0, 0, time.UTC), original.LastHealthCheck.Time)
	})

	t.Run("handles all optional fields being empty", func(t *testing.T) {
		t.Parallel()

		original := &DiscoveredBackend{
			Name: "minimal-backend",
		}

		copied := original.DeepCopy()

		assert.NotNil(t, copied)
		assert.Equal(t, "minimal-backend", copied.Name)
		assert.Empty(t, copied.URL)
		assert.Empty(t, copied.Status)
	})
}

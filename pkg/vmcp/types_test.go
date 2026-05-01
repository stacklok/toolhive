// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package vmcp

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Shared test string constants — used across types_test.go and registry_test.go
// (both in the same package, so constants are visible to both files).
const (
	testStatusUnavailable     = BackendCRDStatusUnavailable
	testStatusUnauthenticated = BackendCRDStatusUnauthenticated

	testBackendIDGithubMCP      = "github-mcp"
	testBackendNameGithubMCP    = "GitHub MCP"
	testBackendIDJiraMCP        = "jira-mcp"
	testBackendNameJiraMCP      = "Jira MCP"
	testBackendIDBackend1       = "backend-1"
	testBackendNameBackend1     = "Backend 1"
	testBackendIDBackend2       = "backend-2"
	testBackendNameBackend2     = "Backend 2"
	testBackendIDBackend3       = "backend-3"
	testBackendNameBackend3     = "Backend 3"
	testBaseURL                 = "http://localhost:8080"
	testTransportStreamableHTTP = "streamable-http"
	testAuthTypeUnauthenticated = "unauthenticated"
	testAuthTypeTokenExchange   = "token_exchange"
	testAudienceGithubAPI       = "github-api"
	testMetaKeyEnv              = "env"
	testBackendIDMinimal        = "minimal"
	testBackendIDTest           = "test"
	testCapabilityCodeReview    = "code_review"
	testCapabilityFetch         = "fetch"
	testCapabilityGithub        = "github"
	testCapabilityCreateIssue   = "create_issue"
	testNameOriginal            = "Original"
	testNameSingleBackend       = "single backend"
	testNameMultipleBackends    = "multiple backends"
	testMetaValueProduction     = "production"
)

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
			expected: BackendReady,
		},
		{
			name:     "degraded maps to degraded",
			status:   BackendDegraded,
			expected: BackendCRDStatusDegraded,
		},
		{
			name:     "unhealthy maps to unavailable",
			status:   BackendUnhealthy,
			expected: testStatusUnavailable,
		},
		{
			name:     "unauthenticated maps to unauthenticated",
			status:   BackendUnauthenticated,
			expected: testStatusUnauthenticated,
		},
		{
			name:     "unknown maps to unknown",
			status:   BackendUnknown,
			expected: BackendCRDStatusUnknown,
		},
		{
			name:     "empty status maps to unknown",
			status:   BackendHealthStatus(""),
			expected: BackendCRDStatusUnknown,
		},
		{
			name:     "invalid status maps to unknown",
			status:   BackendHealthStatus("invalid-status"),
			expected: BackendCRDStatusUnknown,
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
		BackendReady:              true,
		BackendCRDStatusDegraded:  true,
		testStatusUnavailable:     true,
		testStatusUnauthenticated: true,
		BackendCRDStatusUnknown:   true,
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
			Name:            testBackendIDGithubMCP,
			URL:             testBaseURL,
			Status:          BackendReady,
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
			Name:            testBackendIDGithubMCP,
			URL:             testBaseURL,
			Status:          BackendReady,
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
		assert.Equal(t, testBackendIDGithubMCP, original.Name)
		assert.Equal(t, testBaseURL, original.URL)
		assert.Equal(t, BackendReady, original.Status)
		assert.Equal(t, time.Date(2024, 1, 15, 10, 30, 0, 0, time.UTC), original.LastHealthCheck.Time)
	})

	t.Run("handles empty fields", func(t *testing.T) {
		t.Parallel()

		original := &DiscoveredBackend{
			Name: testBackendIDMinimal,
		}

		copied := &DiscoveredBackend{}
		original.DeepCopyInto(copied)

		assert.Equal(t, testBackendIDMinimal, copied.Name)
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
			Name:            testBackendIDTest,
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
			Name:            testBackendIDGithubMCP,
			URL:             testBaseURL,
			Status:          BackendReady,
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
			Name:            testBackendIDGithubMCP,
			URL:             testBaseURL,
			Status:          BackendReady,
			LastHealthCheck: metav1.NewTime(time.Date(2024, 1, 15, 10, 30, 0, 0, time.UTC)),
		}

		copied := original.DeepCopy()

		// Modify the copy
		copied.Name = "modified-name"
		copied.URL = "http://modified:9090"
		copied.Status = testStatusUnavailable
		copied.LastHealthCheck = metav1.NewTime(time.Date(2025, 12, 1, 0, 0, 0, 0, time.UTC))

		// Original should be unchanged
		assert.Equal(t, testBackendIDGithubMCP, original.Name)
		assert.Equal(t, testBaseURL, original.URL)
		assert.Equal(t, BackendReady, original.Status)
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

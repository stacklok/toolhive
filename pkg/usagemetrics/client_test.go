// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package usagemetrics

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	envmocks "github.com/stacklok/toolhive-core/env/mocks"
)

// newTestClient creates a client for testing with a pre-set anonymous ID
func newTestClient(endpoint, anonymousID string) *Client {
	if endpoint == "" {
		endpoint = defaultEndpoint
	}
	return &Client{
		endpoint:    endpoint,
		anonymousID: anonymousID,
		client: &http.Client{
			Timeout: defaultTimeout,
		},
	}
}

func TestGenerateUserAgent(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		k8sEnvValue    string
		expectedPrefix string
	}{
		{
			name:           "local environment",
			k8sEnvValue:    "",
			expectedPrefix: "toolhive/local",
		},
		{
			name:           "operator environment",
			k8sEnvValue:    "https://10.0.0.1:443",
			expectedPrefix: "toolhive/operator",
		},
	}

	for _, tt := range tests {
		tt := tt // capture range variable
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Create mock environment reader
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mockEnv := envmocks.NewMockReader(ctrl)

			// Set up mock expectations
			mockEnv.EXPECT().
				Getenv("TOOLHIVE_RUNTIME").
				Return("").
				AnyTimes()

			mockEnv.EXPECT().
				Getenv("KUBERNETES_SERVICE_HOST").
				Return(tt.k8sEnvValue).
				AnyTimes()

			userAgent := generateUserAgentWithEnv(mockEnv)

			// Verify it starts with expected prefix
			assert.True(t, strings.HasPrefix(userAgent, tt.expectedPrefix),
				"User-Agent should start with %s, got: %s", tt.expectedPrefix, userAgent)

			// Verify it contains version and platform info
			assert.Contains(t, userAgent, "(")
			assert.Contains(t, userAgent, ")")
		})
	}
}

func TestNewClient(t *testing.T) {
	t.Parallel()

	// Test with default endpoint
	client := NewClient("")
	assert.NotNil(t, client)
	assert.Equal(t, defaultEndpoint, client.endpoint)

	// Test with custom endpoint
	customEndpoint := "https://custom.example.com/metrics"
	client = NewClient(customEndpoint)
	assert.NotNil(t, client)
	assert.Equal(t, customEndpoint, client.endpoint)
}

func TestSendMetrics_Non2xxStatusCode(t *testing.T) {
	t.Parallel()

	// Create test server that returns 500
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	// Create client with test anonymous ID
	client := newTestClient(server.URL, "test-anon-id")
	record := MetricRecord{
		Count:     5,
		Timestamp: "2025-01-01T00:00:00Z",
	}

	err := client.SendMetrics("test-instance-id", record)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "API returned non-2xx status code: 500")
}

func TestGenerateUserAgent_BuildType(t *testing.T) {
	t.Parallel()

	userAgent := generateUserAgent()

	// Verify user agent has expected format: toolhive/[type] [version] [build] (platform)
	assert.NotEmpty(t, userAgent)
	assert.True(t, strings.HasPrefix(userAgent, "toolhive/"), "User agent should start with 'toolhive/'")
	assert.Contains(t, userAgent, "(", "User agent should contain platform info in parentheses")
	assert.Contains(t, userAgent, ")", "User agent should contain platform info in parentheses")
}

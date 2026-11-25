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

	"github.com/stacklok/toolhive-core/env"
	envmocks "github.com/stacklok/toolhive-core/env/mocks"
	"github.com/stacklok/toolhive/pkg/updates"
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

			userAgent := generateUserAgentWithEnv(mockEnv, "")

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

	userAgent := generateUserAgentWithEnv(&env.OSReader{}, "")

	// Verify user agent has expected format: toolhive/[type] [version] [build] (platform)
	assert.NotEmpty(t, userAgent)
	assert.True(t, strings.HasPrefix(userAgent, "toolhive/"), "User agent should start with 'toolhive/'")
	assert.Contains(t, userAgent, "(", "User agent should contain platform info in parentheses")
	assert.Contains(t, userAgent, ")", "User agent should contain platform info in parentheses")
}

func TestResolveAnonymousID(t *testing.T) {
	t.Parallel()

	fromFile, _ := updates.TryGetAnonymousID()

	tests := []struct {
		name     string
		envValue string
		want     string
	}{
		{name: "environment override wins", envValue: "operator-instance-id", want: "operator-instance-id"},
		{name: "falls back to the updates file", envValue: "", want: fromFile},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			mockEnv := envmocks.NewMockReader(ctrl)
			mockEnv.EXPECT().Getenv(EnvVarAnonymousID).Return(tt.envValue)

			assert.Equal(t, tt.want, resolveAnonymousID(mockEnv))
		})
	}
}

// TestSendMetrics_LateAnonymousID covers a client created before the anonymous
// ID existed. The ID comes from the TOOLHIVE_ANONYMOUS_ID override so the test
// never reads the developer's real updates file.
//
//nolint:paralleltest // t.Setenv
func TestSendMetrics_LateAnonymousID(t *testing.T) {
	tests := []struct {
		name       string
		kubernetes bool
		wantID     string
	}{
		{name: "read again once it exists", kubernetes: false, wantID: "late-id"},
		{name: "Kubernetes keeps the placeholder", kubernetes: true, wantID: "operator-proxy"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotID string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotID = r.Header.Get(anonymousIDHeader)
				w.WriteHeader(http.StatusOK)
			}))
			t.Cleanup(server.Close)

			client := newTestClient(server.URL, "")
			runtimeEnv := ""
			if tt.kubernetes {
				runtimeEnv = "kubernetes"
			}
			t.Setenv("TOOLHIVE_RUNTIME", runtimeEnv)
			t.Setenv("KUBERNETES_SERVICE_HOST", "")
			t.Setenv(EnvVarAnonymousID, "late-id")

			require.NoError(t, client.SendMetrics("instance", MetricRecord{Count: 1, Timestamp: "2026-11-15T00:00:00Z"}))
			assert.Equal(t, tt.wantID, gotID)
		})
	}
}

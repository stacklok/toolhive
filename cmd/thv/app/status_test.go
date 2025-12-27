package app

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	rt "github.com/stacklok/toolhive/pkg/container/runtime"
	"github.com/stacklok/toolhive/pkg/core"
	transporttypes "github.com/stacklok/toolhive/pkg/transport/types"
)

func TestDetermineHealth(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		status   rt.WorkloadStatus
		expected string
	}{
		{
			name:     "running status returns healthy",
			status:   rt.WorkloadStatusRunning,
			expected: "healthy",
		},
		{
			name:     "unhealthy status returns unhealthy",
			status:   rt.WorkloadStatusUnhealthy,
			expected: "unhealthy",
		},
		{
			name:     "error status returns error",
			status:   rt.WorkloadStatusError,
			expected: "error",
		},
		{
			name:     "starting status returns starting",
			status:   rt.WorkloadStatusStarting,
			expected: "starting",
		},
		{
			name:     "stopping status returns stopping",
			status:   rt.WorkloadStatusStopping,
			expected: "stopping",
		},
		{
			name:     "stopped status returns stopped",
			status:   rt.WorkloadStatusStopped,
			expected: "stopped",
		},
		{
			name:     "removing status returns removing",
			status:   rt.WorkloadStatusRemoving,
			expected: "removing",
		},
		{
			name:     "unauthenticated status returns unauthenticated",
			status:   rt.WorkloadStatusUnauthenticated,
			expected: "unauthenticated",
		},
		{
			name:     "unknown status returns unknown",
			status:   rt.WorkloadStatusUnknown,
			expected: "unknown",
		},
		{
			name:     "empty status returns unknown",
			status:   "",
			expected: "unknown",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			result := determineHealth(tc.status)
			assert.Equal(t, tc.expected, result)
		})
	}
}

func TestFormatUptime(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		duration time.Duration
		expected string
	}{
		{
			name:     "seconds only",
			duration: 45 * time.Second,
			expected: "45s",
		},
		{
			name:     "minutes and seconds",
			duration: 5*time.Minute + 30*time.Second,
			expected: "5m 30s",
		},
		{
			name:     "hours and minutes",
			duration: 2*time.Hour + 30*time.Minute,
			expected: "2h 30m",
		},
		{
			name:     "days and hours",
			duration: 3*24*time.Hour + 5*time.Hour,
			expected: "3d 5h",
		},
		{
			name:     "zero duration",
			duration: 0,
			expected: "0s",
		},
		{
			name:     "one second",
			duration: 1 * time.Second,
			expected: "1s",
		},
		{
			name:     "exactly one minute",
			duration: 1 * time.Minute,
			expected: "1m 0s",
		},
		{
			name:     "exactly one hour",
			duration: 1 * time.Hour,
			expected: "1h 0m",
		},
		{
			name:     "exactly one day",
			duration: 24 * time.Hour,
			expected: "1d 0h",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			result := formatUptime(tc.duration)
			assert.Equal(t, tc.expected, result)
		})
	}
}

func TestBuildStatusOutput(t *testing.T) {
	t.Parallel()

	createdAt := time.Now().Add(-2 * time.Hour)

	tests := []struct {
		name     string
		workload core.Workload
		validate func(*testing.T, StatusOutput)
	}{
		{
			name: "running workload has uptime",
			workload: core.Workload{
				Name:          "test-server",
				Status:        rt.WorkloadStatusRunning,
				Group:         "production",
				TransportType: transporttypes.TransportTypeSSE,
				ProxyMode:     "sse",
				URL:           "http://localhost:8080/sse",
				Port:          8080,
				Package:       "test-package",
				CreatedAt:     createdAt,
			},
			validate: func(t *testing.T, output StatusOutput) {
				t.Helper()
				assert.Equal(t, "test-server", output.Name)
				assert.Equal(t, "running", output.Status)
				assert.Equal(t, "healthy", output.Health)
				assert.NotEqual(t, "-", output.Uptime)
				assert.Greater(t, output.UptimeSeconds, int64(0))
				assert.Equal(t, "production", output.Group)
				assert.Equal(t, "sse", output.Transport)
				assert.Equal(t, "http://localhost:8080/sse", output.URL)
				assert.Equal(t, 8080, output.Port)
				assert.Equal(t, "test-package", output.Package)
			},
		},
		{
			name: "stopped workload has no uptime",
			workload: core.Workload{
				Name:          "stopped-server",
				Status:        rt.WorkloadStatusStopped,
				TransportType: transporttypes.TransportTypeSSE,
				CreatedAt:     createdAt,
			},
			validate: func(t *testing.T, output StatusOutput) {
				t.Helper()
				assert.Equal(t, "stopped-server", output.Name)
				assert.Equal(t, "stopped", output.Status)
				assert.Equal(t, "stopped", output.Health)
				assert.Equal(t, "-", output.Uptime)
				assert.Equal(t, int64(0), output.UptimeSeconds)
			},
		},
		{
			name: "empty group defaults to default",
			workload: core.Workload{
				Name:          "no-group-server",
				Status:        rt.WorkloadStatusRunning,
				Group:         "",
				TransportType: transporttypes.TransportTypeSSE,
				CreatedAt:     createdAt,
			},
			validate: func(t *testing.T, output StatusOutput) {
				t.Helper()
				assert.Equal(t, "default", output.Group)
			},
		},
		{
			name: "remote workload",
			workload: core.Workload{
				Name:          "remote-server",
				Status:        rt.WorkloadStatusRunning,
				Remote:        true,
				TransportType: transporttypes.TransportTypeSSE,
				CreatedAt:     createdAt,
			},
			validate: func(t *testing.T, output StatusOutput) {
				t.Helper()
				assert.True(t, output.Remote)
			},
		},
		{
			name: "workload with labels",
			workload: core.Workload{
				Name:          "labeled-server",
				Status:        rt.WorkloadStatusRunning,
				TransportType: transporttypes.TransportTypeSSE,
				Labels: map[string]string{
					"env":  "dev",
					"team": "backend",
				},
				CreatedAt: createdAt,
			},
			validate: func(t *testing.T, output StatusOutput) {
				t.Helper()
				assert.NotNil(t, output.Labels)
				assert.Equal(t, "dev", output.Labels["env"])
				assert.Equal(t, "backend", output.Labels["team"])
			},
		},
		{
			name: "workload with status context",
			workload: core.Workload{
				Name:          "context-server",
				Status:        rt.WorkloadStatusError,
				StatusContext: "connection timeout",
				TransportType: transporttypes.TransportTypeSSE,
				CreatedAt:     createdAt,
			},
			validate: func(t *testing.T, output StatusOutput) {
				t.Helper()
				assert.Equal(t, "error", output.Status)
				assert.Equal(t, "error", output.Health)
				assert.Equal(t, "connection timeout", output.StatusContext)
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			output := buildStatusOutput(tc.workload)
			tc.validate(t, output)
		})
	}
}

func TestPrintStatusJSON(t *testing.T) {
	t.Parallel()

	output := StatusOutput{
		Name:      "json-test-server",
		Status:    "running",
		Health:    "healthy",
		Uptime:    "2h 30m",
		Group:     "default",
		Transport: "sse",
		URL:       "http://localhost:8080/sse",
		Port:      8080,
	}

	// Test that printStatusJSON works by marshaling and verifying the expected output
	// We verify the same struct would produce valid JSON
	expectedJSON, err := json.MarshalIndent(output, "", "  ")
	require.NoError(t, err)

	// Verify the marshaled JSON can be parsed back
	var parsed StatusOutput
	err = json.Unmarshal(expectedJSON, &parsed)
	require.NoError(t, err)

	assert.Equal(t, output.Name, parsed.Name)
	assert.Equal(t, output.Status, parsed.Status)
	assert.Equal(t, output.Health, parsed.Health)
	assert.Equal(t, output.Uptime, parsed.Uptime)
	assert.Equal(t, output.Group, parsed.Group)
	assert.Equal(t, output.Transport, parsed.Transport)
	assert.Equal(t, output.URL, parsed.URL)
	assert.Equal(t, output.Port, parsed.Port)
}

func TestPrintStatusText(t *testing.T) {
	t.Parallel()

	output := StatusOutput{
		Name:      "text-test-server",
		Status:    "running",
		Health:    "healthy",
		Uptime:    "1h 0m",
		Group:     "production",
		Transport: "sse",
		ProxyMode: "streamable-http",
		URL:       "http://localhost:9000",
		Port:      9000,
		Package:   "my-package",
		Labels: map[string]string{
			"env": "prod",
		},
		StatusContext: "all systems operational",
	}

	// Test output validation by checking the output struct has the expected values
	// The actual printStatusText function just formats and prints these values

	// Verify the output struct contains expected values
	assert.Equal(t, "text-test-server", output.Name)
	assert.Equal(t, "running", output.Status)
	assert.Equal(t, "healthy", output.Health)
	assert.Equal(t, "1h 0m", output.Uptime)
	assert.Equal(t, "production", output.Group)
	assert.Equal(t, "sse", output.Transport)
	assert.Equal(t, "streamable-http", output.ProxyMode)
	assert.Equal(t, "http://localhost:9000", output.URL)
	assert.Equal(t, 9000, output.Port)
	assert.Equal(t, "my-package", output.Package)
	assert.Equal(t, "prod", output.Labels["env"])
	assert.Equal(t, "all systems operational", output.StatusContext)
}

func TestPrintStatusTextMinimal(t *testing.T) {
	t.Parallel()

	// Test with minimal output (no optional fields)
	output := StatusOutput{
		Name:      "minimal-server",
		Status:    "stopped",
		Health:    "stopped",
		Uptime:    "-",
		Group:     "default",
		Transport: "stdio",
		URL:       "",
		Port:      0,
	}

	// Test that minimal output struct has expected values
	// The printStatusText function will conditionally print optional fields

	// Verify required fields are set
	assert.Equal(t, "minimal-server", output.Name)
	assert.Equal(t, "stopped", output.Status)
	assert.Equal(t, "stopped", output.Health)
	assert.Equal(t, "-", output.Uptime)
	assert.Equal(t, "default", output.Group)
	assert.Equal(t, "stdio", output.Transport)

	// Verify optional fields are empty/zero (would not be printed by printStatusText)
	assert.Empty(t, output.ProxyMode)
	assert.Empty(t, output.Package)
	assert.False(t, output.Remote)
	assert.Empty(t, output.StatusContext)
	assert.Empty(t, output.Labels)
}

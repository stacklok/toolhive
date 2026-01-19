package app

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stacklok/toolhive/pkg/container/runtime"
	"github.com/stacklok/toolhive/pkg/core"
	"github.com/stacklok/toolhive/pkg/transport/types"
)

// captureStdout captures stdout during function execution
func captureStdout(t *testing.T, f func()) string {
	t.Helper()

	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("failed to create pipe: %v", err)
	}
	os.Stdout = w

	f()

	w.Close()
	os.Stdout = old

	var buf bytes.Buffer
	if _, err := io.Copy(&buf, r); err != nil {
		t.Fatalf("failed to read captured output: %v", err)
	}
	return buf.String()
}

//nolint:paralleltest // Test captures os.Stdout which cannot be done in parallel
func TestPrintStatusTextOutput(t *testing.T) {
	tests := []struct {
		name     string
		workload core.Workload
		expected []string
	}{
		{
			name: "basic workload",
			workload: core.Workload{
				Name:          "test-server",
				Status:        runtime.WorkloadStatusRunning,
				Package:       "ghcr.io/test/server:latest",
				URL:           "http://localhost:8080",
				Port:          8080,
				TransportType: types.TransportTypeSSE,
				CreatedAt:     time.Date(2024, 1, 15, 10, 30, 0, 0, time.UTC),
			},
			expected: []string{
				"Name:",
				"test-server",
				"Status:",
				"running",
				"Package:",
				"ghcr.io/test/server:latest",
				"URL:",
				"http://localhost:8080",
				"Port:",
				"8080",
				"Transport:",
				"sse",
			},
		},
		{
			name: "workload with group",
			workload: core.Workload{
				Name:          "grouped-server",
				Status:        runtime.WorkloadStatusRunning,
				Package:       "test-package",
				URL:           "http://localhost:9000",
				Port:          9000,
				TransportType: types.TransportTypeStdio,
				ProxyMode:     "streamable-http",
				Group:         "my-group",
				CreatedAt:     time.Date(2024, 1, 15, 10, 30, 0, 0, time.UTC),
			},
			expected: []string{
				"Name:",
				"grouped-server",
				"Group:",
				"my-group",
				"Proxy Mode:",
				"streamable-http",
			},
		},
		{
			name: "unauthenticated workload",
			workload: core.Workload{
				Name:          "unauth-server",
				Status:        runtime.WorkloadStatusUnauthenticated,
				Package:       "test-package",
				URL:           "http://localhost:9000",
				Port:          9000,
				TransportType: types.TransportTypeSSE,
				CreatedAt:     time.Date(2024, 1, 15, 10, 30, 0, 0, time.UTC),
			},
			expected: []string{
				"Status:",
				"unauthenticated",
			},
		},
		{
			name: "remote workload",
			workload: core.Workload{
				Name:          "remote-server",
				Status:        runtime.WorkloadStatusRunning,
				Package:       "remote-package",
				URL:           "https://remote.example.com",
				Port:          443,
				TransportType: types.TransportTypeSSE,
				Remote:        true,
				CreatedAt:     time.Date(2024, 1, 15, 10, 30, 0, 0, time.UTC),
			},
			expected: []string{
				"Remote:",
				"true",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			output := captureStdout(t, func() {
				printStatusTextOutput(tt.workload)
			})

			for _, exp := range tt.expected {
				if !strings.Contains(output, exp) {
					t.Errorf("output missing expected string %q\nGot: %s", exp, output)
				}
			}
		})
	}
}

//nolint:paralleltest // Test captures os.Stdout which cannot be done in parallel
func TestPrintStatusJSONOutput(t *testing.T) {
	workload := core.Workload{
		Name:          "json-test-server",
		Status:        runtime.WorkloadStatusRunning,
		Package:       "ghcr.io/test/server:latest",
		URL:           "http://localhost:8080",
		Port:          8080,
		TransportType: types.TransportTypeSSE,
		Group:         "test-group",
		CreatedAt:     time.Date(2024, 1, 15, 10, 30, 0, 0, time.UTC),
	}

	var jsonErr error
	output := captureStdout(t, func() {
		jsonErr = printStatusJSONOutput(workload)
	})

	if jsonErr != nil {
		t.Fatalf("printStatusJSONOutput() returned error: %v", jsonErr)
	}

	// Verify it's valid JSON with the expected structure
	var parsed struct {
		Name      string `json:"name"`
		Status    string `json:"status"`
		Package   string `json:"package"`
		URL       string `json:"url"`
		Port      int    `json:"port"`
		Transport string `json:"transport"`
		Group     string `json:"group"`
	}
	if err := json.Unmarshal([]byte(output), &parsed); err != nil {
		t.Fatalf("output is not valid JSON: %v\nOutput: %s", err, output)
	}

	// Verify key fields
	if parsed.Name != workload.Name {
		t.Errorf("Name mismatch: got %q, want %q", parsed.Name, workload.Name)
	}
	if parsed.Status != string(workload.Status) {
		t.Errorf("Status mismatch: got %q, want %q", parsed.Status, workload.Status)
	}
	if parsed.URL != workload.URL {
		t.Errorf("URL mismatch: got %q, want %q", parsed.URL, workload.URL)
	}
	if parsed.Group != workload.Group {
		t.Errorf("Group mismatch: got %q, want %q", parsed.Group, workload.Group)
	}
}

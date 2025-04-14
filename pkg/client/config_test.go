// Package client provides utilities for managing client configurations
// and interacting with MCP servers.
package client

import (
	"testing"

	"github.com/StacklokLabs/toolhive/pkg/transport/ssecommon"
)

// TODO: Chris, add betters tests for config layer.

func TestGenerateMCPServerURL(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name          string
		host          string
		port          int
		containerName string
		expected      string
	}{
		{
			name:          "Standard URL",
			host:          "localhost",
			port:          12345,
			containerName: "test-container",
			expected:      "http://localhost:12345" + ssecommon.HTTPSSEEndpoint + "#test-container",
		},
		{
			name:          "Different host",
			host:          "192.168.1.100",
			port:          54321,
			containerName: "another-container",
			expected:      "http://192.168.1.100:54321" + ssecommon.HTTPSSEEndpoint + "#another-container",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			url := GenerateMCPServerURL(tt.host, tt.port, tt.containerName)
			if url != tt.expected {
				t.Errorf("GenerateMCPServerURL() = %v, want %v", url, tt.expected)
			}
		})
	}
}

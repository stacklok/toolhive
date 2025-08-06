package transport

import (
	"testing"

	"github.com/stacklok/toolhive/pkg/transport/ssecommon"
	"github.com/stacklok/toolhive/pkg/transport/streamable"
	"github.com/stacklok/toolhive/pkg/transport/types"
)

func TestGenerateMCPServerURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		transportType string
		host          string
		port          int
		containerName string
		expected      string
	}{
		{
			name:          "SSE transport",
			transportType: types.TransportTypeSSE.String(),
			host:          "localhost",
			port:          12345,
			containerName: "test-container",
			expected:      "http://localhost:12345" + ssecommon.HTTPSSEEndpoint + "#test-container",
		},
		{
			name:          "STDIO transport (uses SSE proxy)",
			transportType: types.TransportTypeStdio.String(),
			host:          "localhost",
			port:          12345,
			containerName: "test-container",
			expected:      "http://localhost:12345" + ssecommon.HTTPSSEEndpoint + "#test-container",
		},
		{
			name:          "Streamable HTTP transport",
			transportType: types.TransportTypeStreamableHTTP.String(),
			host:          "localhost",
			port:          12345,
			containerName: "test-container",
			expected:      "http://localhost:12345/" + streamable.HTTPStreamableHTTPEndpoint,
		},
		{
			name:          "Different host with SSE",
			transportType: types.TransportTypeSSE.String(),
			host:          "192.168.1.100",
			port:          54321,
			containerName: "another-container",
			expected:      "http://192.168.1.100:54321" + ssecommon.HTTPSSEEndpoint + "#another-container",
		},
		{
			name:          "Unsupported transport type",
			transportType: "unsupported",
			host:          "localhost",
			port:          12345,
			containerName: "test-container",
			expected:      "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			url := GenerateMCPServerURL(tt.transportType, tt.host, tt.port, tt.containerName)
			if url != tt.expected {
				t.Errorf("GenerateMCPServerURL() = %v, want %v", url, tt.expected)
			}
		})
	}
}

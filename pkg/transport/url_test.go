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
		targetURI     string
		expected      string
	}{
		{
			name:          "SSE transport",
			transportType: types.TransportTypeSSE.String(),
			host:          "localhost",
			port:          12345,
			containerName: "test-container",
			targetURI:     "",
			expected:      "http://localhost:12345" + ssecommon.HTTPSSEEndpoint + "#test-container",
		},
		{
			name:          "STDIO transport (uses SSE proxy)",
			transportType: types.TransportTypeStdio.String(),
			host:          "localhost",
			port:          12345,
			containerName: "test-container",
			targetURI:     "",
			expected:      "http://localhost:12345" + ssecommon.HTTPSSEEndpoint + "#test-container",
		},
		{
			name:          "Streamable HTTP transport",
			transportType: types.TransportTypeStreamableHTTP.String(),
			host:          "localhost",
			port:          12345,
			containerName: "test-container",
			targetURI:     "",
			expected:      "http://localhost:12345/" + streamable.HTTPStreamableHTTPEndpoint,
		},
		{
			name:          "Unsupported transport type",
			transportType: "unsupported",
			host:          "localhost",
			port:          12345,
			containerName: "test-container",
			targetURI:     "",
			expected:      "",
		},
		{
			name:          "SSE transport with targetURI path",
			transportType: types.TransportTypeSSE.String(),
			host:          "localhost",
			port:          12345,
			containerName: "test-container",
			targetURI:     "http://example.com/api/v1",
			expected:      "http://localhost:12345/api/v1#test-container",
		},
		{
			name:          "SSE transport with targetURI domain only",
			transportType: types.TransportTypeSSE.String(),
			host:          "localhost",
			port:          12345,
			containerName: "test-container",
			targetURI:     "http://example.com",
			expected:      "http://localhost:12345/sse#test-container",
		},
		{
			name:          "SSE transport with targetURI root path",
			transportType: types.TransportTypeSSE.String(),
			host:          "localhost",
			port:          12345,
			containerName: "test-container",
			targetURI:     "http://example.com/",
			expected:      "http://localhost:12345/sse#test-container",
		},
		// Major targetURI test cases - Streamable HTTP transport
		{
			name:          "Streamable HTTP transport with targetURI path",
			transportType: types.TransportTypeStreamableHTTP.String(),
			host:          "localhost",
			port:          12345,
			containerName: "test-container",
			targetURI:     "http://remote-server.com/path",
			expected:      "http://localhost:12345/path",
		},
		{
			name:          "Streamable HTTP transport with targetURI domain only",
			transportType: types.TransportTypeStreamableHTTP.String(),
			host:          "localhost",
			port:          12345,
			containerName: "test-container",
			targetURI:     "http://remote-server.com",
			expected:      "http://localhost:12345/mcp",
		},
		{
			name:          "Streamable HTTP transport with targetURI root path",
			transportType: types.TransportTypeStreamableHTTP.String(),
			host:          "localhost",
			port:          12345,
			containerName: "test-container",
			targetURI:     "http://remote-server.com/",
			expected:      "http://localhost:12345/mcp",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			url := GenerateMCPServerURL(tt.transportType, tt.host, tt.port, tt.containerName, tt.targetURI)
			if url != tt.expected {
				t.Errorf("GenerateMCPServerURL() = %v, want %v", url, tt.expected)
			}
		})
	}
}

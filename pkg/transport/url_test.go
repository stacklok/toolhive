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
		proxyMode     string
		host          string
		port          int
		containerName string
		targetURI     string
		expected      string
	}{
		{
			name:          "STDIO transport with streamable-http proxy",
			transportType: types.TransportTypeStdio.String(),
			proxyMode:     "streamable-http",
			host:          "localhost",
			port:          12345,
			containerName: "test-container",
			targetURI:     "",
			expected:      "http://localhost:12345/" + streamable.HTTPStreamableHTTPEndpoint,
		},
		{
			name:          "STDIO transport with sse proxy",
			transportType: types.TransportTypeStdio.String(),
			proxyMode:     "sse",
			host:          "localhost",
			port:          12345,
			containerName: "test-container",
			targetURI:     "",
			expected:      "http://localhost:12345" + ssecommon.HTTPSSEEndpoint + "#test-container",
		},
		{
			name:          "STDIO transport with empty proxyMode (defaults to streamable-http)",
			transportType: types.TransportTypeStdio.String(),

			proxyMode:     "",
			host:          "localhost",
			port:          12345,
			containerName: "test-container",
			targetURI:     "",
			expected:      "http://localhost:12345/" + streamable.HTTPStreamableHTTPEndpoint,
		},
		{
			name:          "SSE transport",
			transportType: types.TransportTypeSSE.String(),
			proxyMode:     "",
			host:          "localhost",
			port:          12345,
			containerName: "test-container",
			targetURI:     "",
			expected:      "http://localhost:12345" + ssecommon.HTTPSSEEndpoint + "#test-container",
		},
		{
			name:          "Streamable HTTP transport",
			transportType: types.TransportTypeStreamableHTTP.String(),
			proxyMode:     "",
			host:          "localhost",
			port:          12345,
			containerName: "test-container",
			targetURI:     "",
			expected:      "http://localhost:12345/" + streamable.HTTPStreamableHTTPEndpoint,
		},
		{
			name:          "Unsupported transport type",
			transportType: "unsupported",
			proxyMode:     "",
			host:          "localhost",
			port:          12345,
			containerName: "test-container",
			targetURI:     "",
			expected:      "",
		},
		{
			name:          "SSE transport with targetURI path",
			transportType: types.TransportTypeSSE.String(),
			proxyMode:     "",
			host:          "localhost",
			port:          12345,
			containerName: "test-container",
			targetURI:     "http://example.com/api/v1",
			expected:      "http://localhost:12345/api/v1#test-container",
		},
		{
			name:          "SSE transport with targetURI domain only",
			transportType: types.TransportTypeSSE.String(),
			proxyMode:     "",
			host:          "localhost",
			port:          12345,
			containerName: "test-container",
			targetURI:     "http://example.com",
			expected:      "http://localhost:12345#test-container",
		},
		{
			name:          "SSE transport with targetURI root path",
			transportType: types.TransportTypeSSE.String(),
			proxyMode:     "",
			host:          "localhost",
			port:          12345,
			containerName: "test-container",
			targetURI:     "http://example.com/",
			expected:      "http://localhost:12345#test-container",
		},
		{
			name:          "Streamable HTTP transport with targetURI path",
			transportType: types.TransportTypeStreamableHTTP.String(),
			proxyMode:     "",
			host:          "localhost",
			port:          12345,
			containerName: "test-container",
			targetURI:     "http://remote-server.com/path",
			expected:      "http://localhost:12345/path",
		},
		{
			name:          "Streamable HTTP transport with targetURI domain only",
			transportType: types.TransportTypeStreamableHTTP.String(),
			proxyMode:     "",
			host:          "localhost",
			port:          12345,
			containerName: "test-container",
			targetURI:     "http://remote-server.com",
			expected:      "http://localhost:12345",
		},
		{
			name:          "Streamable HTTP transport with targetURI root path",
			transportType: types.TransportTypeStreamableHTTP.String(),
			proxyMode:     "",
			host:          "localhost",
			port:          12345,
			containerName: "test-container",
			targetURI:     "http://remote-server.com/",
			expected:      "http://localhost:12345",
		},
		{
			name:          "STDIO with streamable-http proxy and targetURI",
			transportType: types.TransportTypeStdio.String(),
			proxyMode:     "streamable-http",
			host:          "localhost",
			port:          12345,
			containerName: "test-container",
			targetURI:     "http://remote.com/api",
			expected:      "http://localhost:12345/api",
		},
		{
			name:          "STDIO with sse proxy and targetURI",
			transportType: types.TransportTypeStdio.String(),
			proxyMode:     "sse",
			host:          "localhost",
			port:          12345,
			containerName: "test-container",
			targetURI:     "http://remote.com/api",
			expected:      "http://localhost:12345/api#test-container",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			url := GenerateMCPServerURL(tt.transportType, tt.proxyMode, tt.host, tt.port, tt.containerName, tt.targetURI)
			if url != tt.expected {
				t.Errorf("GenerateMCPServerURL() = %v, want %v", url, tt.expected)
			}
		})
	}
}

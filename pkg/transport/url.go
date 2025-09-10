// Package transport provides utilities for MCP transport operations
package transport

import (
	"fmt"
	"net/url"

	"github.com/stacklok/toolhive/pkg/logger"
	"github.com/stacklok/toolhive/pkg/transport/ssecommon"
	"github.com/stacklok/toolhive/pkg/transport/streamable"
	"github.com/stacklok/toolhive/pkg/transport/types"
)

// GenerateMCPServerURL generates the URL for an MCP server.
// If remoteURL is provided, the remote server's path will be used as the path of the proxy.
// For SSE/STDIO transports, a "#<containerName>" fragment is appended.
// For StreamableHTTP, no fragment is appended.
func GenerateMCPServerURL(transportType string, host string, port int, containerName, remoteURL string) string {
	base := fmt.Sprintf("http://%s:%d", host, port)

	isSSE := transportType == types.TransportTypeSSE.String() || transportType == types.TransportTypeStdio.String()
	isStreamable := transportType == types.TransportTypeStreamableHTTP.String()

	// ---- Remote path case ----
	if remoteURL != "" {
		targetURL, err := url.Parse(remoteURL)
		if err != nil {
			logger.Errorf("Failed to parse target URI: %v", err)
			return ""
		}

		// Use remote path as-is; treat "/" as empty
		path := targetURL.EscapedPath()
		if path == "/" {
			path = ""
		}

		if isSSE {
			return fmt.Sprintf("%s%s#%s", base, path, url.PathEscape(containerName))
		}
		if isStreamable {
			return fmt.Sprintf("%s%s", base, path)
		}
		return ""
	}

	// ---- Local path case (use constants as-is) ----
	if isSSE {
		// ssecommon.HTTPSSEEndpoint already includes "/sse"
		return fmt.Sprintf("%s%s#%s", base, ssecommon.HTTPSSEEndpoint, url.PathEscape(containerName))
	}

	if isStreamable {
		// streamable.HTTPStreamableHTTPEndpoint is "mcp"
		return fmt.Sprintf("%s/%s", base, streamable.HTTPStreamableHTTPEndpoint)
	}

	return ""
}

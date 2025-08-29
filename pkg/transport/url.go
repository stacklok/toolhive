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

// GenerateMCPServerURL generates the URL for an MCP server
// if remoteURL is provided, remote server path will be used as the path of the proxy
func GenerateMCPServerURL(transportType string, host string, port int, containerName, remoteURL string) string {
	path := ""
	if remoteURL != "" {
		targetURL, err := url.Parse(remoteURL)
		if err != nil {
			logger.Errorf("Failed to parse target URI: %v", err)
			return ""
		}
		path = targetURL.Path
	}
	// The URL format is: http://host:port/sse#container-name
	// Both SSE and STDIO transport types use an SSE proxy
	if transportType == types.TransportTypeSSE.String() || transportType == types.TransportTypeStdio.String() {
		if path == "" || path == "/" {
			path = ssecommon.HTTPSSEEndpoint
		}
		return fmt.Sprintf("http://%s:%d%s#%s", host, port, path, containerName)
	} else if transportType == types.TransportTypeStreamableHTTP.String() {
		if path == "" || path == "/" {
			path = streamable.HTTPStreamableHTTPEndpoint
		}
		return fmt.Sprintf("http://%s:%d/%s", host, port, path)
	}
	return ""
}

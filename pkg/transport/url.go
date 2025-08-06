// Package transport provides utilities for MCP transport operations
package transport

import (
	"fmt"

	"github.com/stacklok/toolhive/pkg/transport/ssecommon"
	"github.com/stacklok/toolhive/pkg/transport/streamable"
	"github.com/stacklok/toolhive/pkg/transport/types"
)

// GenerateMCPServerURL generates the URL for an MCP server
func GenerateMCPServerURL(transportType string, host string, port int, containerName string) string {
	// The URL format is: http://host:port/sse#container-name
	// Both SSE and STDIO transport types use an SSE proxy
	if transportType == types.TransportTypeSSE.String() || transportType == types.TransportTypeStdio.String() {
		return fmt.Sprintf("http://%s:%d%s#%s", host, port, ssecommon.HTTPSSEEndpoint, containerName)
	} else if transportType == types.TransportTypeStreamableHTTP.String() {
		return fmt.Sprintf("http://%s:%d/%s", host, port, streamable.HTTPStreamableHTTPEndpoint)
	}
	return ""
}

// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package transport provides utilities for MCP transport operations
package transport

import (
	"fmt"
	"log/slog"
	"net/url"

	"github.com/stacklok/toolhive/pkg/transport/ssecommon"
	"github.com/stacklok/toolhive/pkg/transport/streamable"
	"github.com/stacklok/toolhive/pkg/transport/types"
)

// GenerateMCPServerURL generates the URL for an MCP server.
// If remoteURL is provided, the remote server's path will be used as the path of the proxy.
// For SSE/STDIO transports, a "#<containerName>" fragment is appended.
// For StreamableHTTP, no fragment is appended.
func GenerateMCPServerURL(transportType string, proxyMode string, host string, port int, containerName, remoteURL string) string {
	base := fmt.Sprintf("http://%s:%d", host, port)

	var isSSE, isStreamable bool

	if transportType == types.TransportTypeStdio.String() {
		// For stdio, the proxy mode determines the HTTP endpoint
		// Default to streamable-http if proxyMode is empty (matches CRD default)
		effectiveProxyMode := proxyMode
		if effectiveProxyMode == "" {
			effectiveProxyMode = types.ProxyModeStreamableHTTP.String()
		}

		// Map proxy mode to endpoint type
		if effectiveProxyMode == types.ProxyModeSSE.String() {
			isSSE = true
		} else {
			// streamable-http or any other value
			isStreamable = true
		}
	} else if transportType == types.TransportTypeSSE.String() {
		// Native SSE transport
		isSSE = true
	} else if transportType == types.TransportTypeStreamableHTTP.String() {
		// Native streamable-http transport
		isStreamable = true
	}

	// ---- Remote path case ----
	if remoteURL != "" {
		return generateRemoteMCPServerURL(base, containerName, remoteURL, isSSE, isStreamable)
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

// generateRemoteMCPServerURL builds the proxy URL for a remote MCP server,
// using only the path from the remote URL.
//
// Query parameters are intentionally excluded from the generated client URL.
// The transparent proxy forwards them on every outbound request via
// WithRemoteRawQuery, so including them here would cause duplication —
// the upstream would receive the same parameter twice (e.g.
// "toolsets=core&toolsets=core"). Clients connect to the clean proxy
// URL; the proxy transparently appends the configured query string.
func generateRemoteMCPServerURL(base, containerName, remoteURL string, isSSE, isStreamable bool) string {
	targetURL, err := url.Parse(remoteURL)
	if err != nil {
		slog.Error("failed to parse target URI", "error", err)
		return ""
	}

	// Use remote path as-is; treat "/" as empty
	path := targetURL.EscapedPath()
	if path == "/" {
		path = ""
	}

	if isSSE {
		if path == "" {
			path = ssecommon.HTTPSSEEndpoint
		}
		return fmt.Sprintf("%s%s#%s", base, path, url.PathEscape(containerName))
	}
	if isStreamable {
		return fmt.Sprintf("%s%s", base, path)
	}
	return ""
}

// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package app

import (
	"testing"

	"github.com/stacklok/toolhive/pkg/transport/types"
)

func TestBuildInspectorURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		uiPort     int
		proxyMode  types.ProxyMode
		serverPort int
		authToken  string
		want       string
	}{
		{
			name:       "SSE proxy mode uses sse suffix",
			uiPort:     6274,
			proxyMode:  types.ProxyModeSSE,
			serverPort: 8080,
			authToken:  "abc123",
			want:       "http://localhost:6274?transport=sse&serverUrl=http://host.docker.internal:8080/sse&MCP_PROXY_AUTH_TOKEN=abc123",
		},
		{
			name:       "streamable-http proxy mode uses mcp suffix",
			uiPort:     6274,
			proxyMode:  types.ProxyModeStreamableHTTP,
			serverPort: 8080,
			authToken:  "abc123",
			want:       "http://localhost:6274?transport=streamable-http&serverUrl=http://host.docker.internal:8080/mcp&MCP_PROXY_AUTH_TOKEN=abc123",
		},
		{
			name:       "different ports and token",
			uiPort:     9000,
			proxyMode:  types.ProxyModeStreamableHTTP,
			serverPort: 3000,
			authToken:  "token-xyz-456",
			want:       "http://localhost:9000?transport=streamable-http&serverUrl=http://host.docker.internal:3000/mcp&MCP_PROXY_AUTH_TOKEN=token-xyz-456",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := buildInspectorURL(tt.uiPort, tt.proxyMode, tt.serverPort, tt.authToken)
			if got != tt.want {
				t.Errorf("buildInspectorURL() =\n  %s\nwant:\n  %s", got, tt.want)
			}
		})
	}
}

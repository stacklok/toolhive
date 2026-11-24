// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package app

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"github.com/stacklok/toolhive/pkg/container/runtime"
	"github.com/stacklok/toolhive/pkg/transport/types"
)

func TestBuildInspectorURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		uiPort      int
		proxyMode   types.ProxyMode
		hostGateway string
		serverPort  int
		authToken   string
		want        string
	}{
		{
			name:        "SSE proxy mode uses sse suffix",
			uiPort:      6274,
			proxyMode:   types.ProxyModeSSE,
			hostGateway: "host.docker.internal",
			serverPort:  8080,
			authToken:   "abc123",
			want:        "http://localhost:6274?transport=sse&serverUrl=http://host.docker.internal:8080/sse&MCP_PROXY_AUTH_TOKEN=abc123",
		},
		{
			name:        "streamable-http proxy mode uses mcp suffix",
			uiPort:      6274,
			proxyMode:   types.ProxyModeStreamableHTTP,
			hostGateway: "host.docker.internal",
			serverPort:  8080,
			authToken:   "abc123",
			want:        "http://localhost:6274?transport=streamable-http&serverUrl=http://host.docker.internal:8080/mcp&MCP_PROXY_AUTH_TOKEN=abc123",
		},
		{
			name:        "different ports and token with podman gateway",
			uiPort:      9000,
			proxyMode:   types.ProxyModeStreamableHTTP,
			hostGateway: "host.containers.internal",
			serverPort:  3000,
			authToken:   "token-xyz-456",
			want:        "http://localhost:9000?transport=streamable-http&serverUrl=http://host.containers.internal:3000/mcp&MCP_PROXY_AUTH_TOKEN=token-xyz-456",
		},
		{
			name:        "apple vmnet IPv4 gateway",
			uiPort:      6274,
			proxyMode:   types.ProxyModeStreamableHTTP,
			hostGateway: "192.168.64.1",
			serverPort:  8080,
			authToken:   "abc123",
			want:        "http://localhost:6274?transport=streamable-http&serverUrl=http://192.168.64.1:8080/mcp&MCP_PROXY_AUTH_TOKEN=abc123",
		},
		{
			name:        "empty gateway falls back to host.docker.internal",
			uiPort:      6274,
			proxyMode:   types.ProxyModeStreamableHTTP,
			hostGateway: "",
			serverPort:  8080,
			authToken:   "abc123",
			want:        "http://localhost:6274?transport=streamable-http&serverUrl=http://host.docker.internal:8080/mcp&MCP_PROXY_AUTH_TOKEN=abc123",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := buildInspectorURL(tt.uiPort, tt.proxyMode, tt.hostGateway, tt.serverPort, tt.authToken)
			if got != tt.want {
				t.Errorf("buildInspectorURL() =\n  %s\nwant:\n  %s", got, tt.want)
			}
		})
	}
}

func TestIsLoopbackURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		url  string
		want bool
	}{
		{name: "IPv4 loopback", url: "http://127.0.0.1:8080/mcp", want: true},
		{name: "localhost", url: "http://localhost:8080/sse", want: true},
		{name: "IPv6 loopback", url: "http://[::1]:8080/mcp", want: true},
		{name: "all interfaces", url: "http://0.0.0.0:8080/mcp", want: false},
		{name: "vmnet gateway", url: "http://192.168.64.1:8080/mcp", want: false},
		{name: "empty", url: "", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := isLoopbackURL(tt.url); got != tt.want {
				t.Errorf("isLoopbackURL(%q) = %v, want %v", tt.url, got, tt.want)
			}
		})
	}
}

type stubTypedRuntime struct {
	runtime.Runtime
	rtType runtime.Type
}

func (s stubTypedRuntime) RuntimeType() runtime.Type {
	return s.rtType
}

//nolint:paralleltest // slog.SetDefault mutates process-global state
func TestWarnIfAppleLoopbackServer(t *testing.T) {
	tests := []struct {
		name          string
		rtType        runtime.Type
		serverURL     string
		hostGateway   string
		wantWarn      bool
		wantSubstr    string
		wantNotSubstr string
	}{
		{
			name:          "warns for apple runtime on loopback and suggests only gateway host",
			rtType:        runtime.TypeApple,
			serverURL:     "http://127.0.0.1:8080/mcp",
			hostGateway:   "192.168.64.1",
			wantWarn:      true,
			wantSubstr:    "--host 192.168.64.1",
			wantNotSubstr: "0.0.0.0",
		},
		{
			name:        "does not warn for apple runtime bound to gateway IP",
			rtType:      runtime.TypeApple,
			serverURL:   "http://192.168.64.1:8080/mcp",
			hostGateway: "192.168.64.1",
			wantWarn:    false,
		},
		{
			name:        "does not warn for docker runtime on loopback",
			rtType:      runtime.TypeDocker,
			serverURL:   "http://127.0.0.1:8080/mcp",
			hostGateway: "host.docker.internal",
			wantWarn:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			prev := slog.Default()
			slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})))
			t.Cleanup(func() { slog.SetDefault(prev) })

			warnIfAppleLoopbackServer(stubTypedRuntime{rtType: tt.rtType}, "demo", tt.serverURL, tt.hostGateway)

			output := buf.String()
			if tt.wantWarn && output == "" {
				t.Fatalf("expected warning log, got empty output")
			}
			if !tt.wantWarn && output != "" {
				t.Fatalf("expected no warning log, got: %s", output)
			}
			if tt.wantSubstr != "" && !strings.Contains(output, tt.wantSubstr) {
				t.Errorf("warning output %q does not contain %q", output, tt.wantSubstr)
			}
			if tt.wantNotSubstr != "" && strings.Contains(output, tt.wantNotSubstr) {
				t.Errorf("warning output %q must not contain %q", output, tt.wantNotSubstr)
			}
		})
	}
}

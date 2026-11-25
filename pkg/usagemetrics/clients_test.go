// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package usagemetrics

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNormalizeClientName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		want string
	}{
		{"claude-code", "claude-code"},
		{"Claude Code", "claude-code"},
		{"claude-ai", "claude-desktop"},
		{"cursor-vscode", "cursor"},
		{"Visual Studio Code", "vscode"},
		{"Visual Studio Code - Insiders", "vscode"},
		{"windsurf-client", "windsurf"},
		{"gemini-cli-mcp-client", "gemini-cli"},
		{"toolhive-vmcp", "vmcp"},
		{"toolhive", "toolhive"},
		{"", ClientUnknown},
		{ClientUnknown, ClientUnknown},
		{"my-private-agent", ValueOther},
		// Matching is by whole token, so a known name inside a word does not match.
		{"customized", ValueOther},
		{"authorized-client", ValueOther},
	}

	for _, tt := range tests {
		t.Run(fmt.Sprintf("%q", tt.name), func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, NormalizeClientName(tt.name))
		})
	}
}

func TestUserAgentClient(t *testing.T) {
	t.Parallel()

	tests := []struct {
		userAgent string
		want      string
	}{
		{"claude-code/2.0.14", "claude-code"},
		{"claude-code/2.0.14 (external, cli)", "claude-code"},
		{"Cursor/1.7.0 (darwin arm64)", "cursor"},
		// The User-Agent vMCP and thv send for their own usage reports.
		{"toolhive/vmcp v0.60.0 release (linux/amd64)", "vmcp"},
		{"toolhive/local v0.60.0 release (darwin/arm64)", "toolhive"},
		// versions.GetUserAgent, sent by ToolHive's registry clients.
		{"ToolHive/v0.60.0 (linux/amd64) go/go1.27.0", "toolhive"},
		// Generic HTTP library agents say nothing about the MCP client.
		{"node", ClientUnknown},
		{"python-httpx/0.28.1", ClientUnknown},
		{"Go-http-client/1.1", ClientUnknown},
		{"Go-http-client/2.0", ClientUnknown},
		{"axios/1.7.7", ClientUnknown},
		{"curl/8.7.1", ClientUnknown},
		{"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko)", ClientUnknown},
		{"", ClientUnknown},
	}

	for _, tt := range tests {
		t.Run(fmt.Sprintf("%q", tt.userAgent), func(t *testing.T) {
			t.Parallel()
			r := httptest.NewRequest(http.MethodPost, "/mcp", nil)
			if tt.userAgent != "" {
				r.Header.Set(userAgentHeader, tt.userAgent)
			}
			assert.Equal(t, tt.want, userAgentClient(r))
		})
	}
}

func TestSessionClients(t *testing.T) {
	t.Parallel()

	t.Run("remembers and looks up clients", func(t *testing.T) {
		t.Parallel()
		s := newSessionClients()
		s.remember("s1", "cursor")
		s.remember("s2", "claude-code")

		assert.Equal(t, "cursor", s.lookup("s1"))
		assert.Equal(t, "claude-code", s.lookup("s2"))
		assert.Equal(t, ClientUnknown, s.lookup("missing"))
	})

	t.Run("ignores empty session ID", func(t *testing.T) {
		t.Parallel()
		s := newSessionClients()
		s.remember("", "cursor")

		assert.Zero(t, s.sessions.Len())
		assert.Equal(t, ClientUnknown, s.lookup(""))
	})

	t.Run("forgets only the least recently used session when full", func(t *testing.T) {
		t.Parallel()
		s := newSessionClients()
		for i := range maxTrackedSessions {
			s.remember(fmt.Sprintf("s%d", i), "cursor")
		}
		require.Equal(t, maxTrackedSessions, s.sessions.Len())
		// A long-lived session that keeps making calls must survive new ones.
		require.Equal(t, "cursor", s.lookup("s0"))

		s.remember("new", "zed")

		assert.Equal(t, maxTrackedSessions, s.sessions.Len())
		assert.Equal(t, "zed", s.lookup("new"))
		assert.Equal(t, "cursor", s.lookup("s0"), "a recently used session is kept")
		assert.Equal(t, ClientUnknown, s.lookup("s1"), "the least recently used session is forgotten")
	})
}

func TestRequestSessionID(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		target string
		header string
		want   string
	}{
		{name: "header", target: "/mcp", header: "h1", want: "h1"},
		{name: "header wins over query", target: "/mcp?session_id=q1", header: "h1", want: "h1"},
		{name: "sessionId query", target: "/messages?sessionId=q1", want: "q1"},
		{name: "sessionid query", target: "/messages?sessionid=q2", want: "q2"},
		{name: "session_id query", target: "/messages?session_id=q3", want: "q3"},
		{name: "none", target: "/mcp", want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := httptest.NewRequest(http.MethodPost, tt.target, nil)
			if tt.header != "" {
				r.Header.Set(sessionIDHeader, tt.header)
			}
			assert.Equal(t, tt.want, requestSessionID(r))
		})
	}
}

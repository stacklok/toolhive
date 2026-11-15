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
		{"toolhive-vmcp", "toolhive"},
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

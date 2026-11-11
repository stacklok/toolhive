// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive-core/mcpcompat/mcp"
	"github.com/stacklok/toolhive-core/mcpcompat/server"
	mcpparser "github.com/stacklok/toolhive/pkg/mcp"
	"github.com/stacklok/toolhive/pkg/vmcp"
)

// legacySession is a ClientSession that reports a negotiated protocol version
// and declared client capabilities, each with its own known flag.
type legacySession struct {
	version      string
	versionKnown bool
	caps         mcp.ClientCapabilities
	capsKnown    bool
}

func (*legacySession) SessionID() string                                   { return "legacy-session" }
func (*legacySession) Initialize()                                         {}
func (*legacySession) Initialized() bool                                   { return true }
func (*legacySession) NotificationChannel() chan<- mcp.JSONRPCNotification { return nil }
func (s *legacySession) NegotiatedProtocolVersion() (string, bool)         { return s.version, s.versionKnown }
func (s *legacySession) DeclaredClientCapabilities() (mcp.ClientCapabilities, bool) {
	return s.caps, s.capsKnown
}

// bareSession is a ClientSession with neither accessor.
type bareSession struct{}

func (bareSession) SessionID() string                                   { return "bare-session" }
func (bareSession) Initialize()                                         {}
func (bareSession) Initialized() bool                                   { return true }
func (bareSession) NotificationChannel() chan<- mcp.JSONRPCNotification { return nil }

var (
	_ server.SessionWithProtocolVersion    = (*legacySession)(nil)
	_ server.SessionWithClientCapabilities = (*legacySession)(nil)
)

func urlCapabilities() mcp.ClientCapabilities {
	return mcp.ClientCapabilities{Elicitation: &mcp.ElicitationCapability{URL: &struct{}{}}}
}

// urlDeclaringSession is a session that accepts URL mode: it negotiated
// 2025-11-25 and declared elicitation.url, both known.
func urlDeclaringSession() *legacySession {
	return &legacySession{
		version: mcpparser.MCPVersionLegacy, versionKnown: true,
		caps: urlCapabilities(), capsKnown: true,
	}
}

func twoURLAnswer(t *testing.T) *vmcp.URLElicitationRequiredError {
	t.Helper()
	first, err := vmcp.NewURLElicitation("github", "https://connect.example.com/github", "Reconnect GitHub")
	require.NoError(t, err)
	second, err := vmcp.NewURLElicitation("jira", "https://connect.example.com/jira", "Reconnect Jira")
	require.NoError(t, err)
	answer, err := vmcp.NewURLElicitationRequiredError(first, second)
	require.NoError(t, err)
	return answer
}

// assertLegacyFallback asserts the isError fallback: the elicitation messages
// and never a URL.
func assertLegacyFallback(t *testing.T, answer *vmcp.URLElicitationRequiredError, res *mcp.CallToolResult, err error) {
	t.Helper()
	require.NoError(t, err)
	require.NotNil(t, res)
	assert.True(t, res.IsError)
	require.Len(t, res.Content, 1)
	text, ok := res.Content[0].(mcp.TextContent)
	require.True(t, ok, "content: %#v", res.Content[0])
	assert.Equal(t, answer.Message(), text.Text)
	raw, mErr := json.Marshal(res)
	require.NoError(t, mErr)
	assert.NotContains(t, string(raw), "connect.example.com")
}

func TestLegacyToolCallError_URLElicitationRequired(t *testing.T) {
	t.Parallel()

	answer := twoURLAnswer(t)
	res, err := legacyToolCallError(urlDeclaringSession(), fmt.Errorf("backend call: %w", answer))
	assert.Nil(t, res)

	var wireErr *jsonrpc.Error
	require.ErrorAs(t, err, &wireErr)
	assert.Same(t, wireErr, err, "the -32042 must be returned bare, or go-sdk drops its data")
	assert.EqualValues(t, -32042, wireErr.Code)
	assert.Equal(t, "URL elicitation required", wireErr.Message)
	assert.JSONEq(t, `{"elicitations": [
		{"mode": "url", "elicitationId": "github", "url": "https://connect.example.com/github", "message": "Reconnect GitHub"},
		{"mode": "url", "elicitationId": "jira", "url": "https://connect.example.com/jira", "message": "Reconnect Jira"}
	]}`, string(wireErr.Data))
}

func TestLegacyRender_DenialOutranksURLElicitation(t *testing.T) {
	t.Parallel()

	err := errors.Join(fmt.Errorf("%w: policy", vmcp.ErrAuthorizationFailed), twoURLAnswer(t))
	res, rErr := legacyToolCallError(urlDeclaringSession(), err)
	require.NoError(t, rErr)
	require.NotNil(t, res)
	assert.True(t, res.IsError)
	require.Len(t, res.Content, 1)
	text, ok := res.Content[0].(mcp.TextContent)
	require.True(t, ok)
	assert.Equal(t, vmcp.DenyMessageToolCall, text.Text)
}

// TestLegacyRender_UnknownCapabilitiesFallBack pins #7023 AC2.2: vMCP sends
// URL mode only when the session reports both its negotiated version and its
// declared capabilities as known. Each row leaves one of them unknown or
// unavailable, as a rehydrated session does.
func TestLegacyRender_UnknownCapabilitiesFallBack(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		session server.ClientSession
	}{
		{name: "no session", session: nil},
		{name: "session without accessors", session: bareSession{}},
		{name: "capabilities unknown", session: &legacySession{
			version: mcpparser.MCPVersionLegacy, versionKnown: true, caps: urlCapabilities(),
		}},
		{name: "protocol version unknown", session: &legacySession{
			caps: urlCapabilities(), capsKnown: true,
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			answer := twoURLAnswer(t)
			res, err := legacyToolCallError(tc.session, answer)
			assertLegacyFallback(t, answer, res, err)
		})
	}
}

// TestLegacyRender_UndeclaredURLFallsBack pins the gate's known-facts side
// (#7023 AC2.1): a session that reports both facts as known still falls back
// unless it negotiated exactly 2025-11-25 and declared elicitation.url.
func TestLegacyRender_UndeclaredURLFallsBack(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		session *legacySession
	}{
		{name: "older protocol version", session: &legacySession{
			version: "2025-06-18", versionKnown: true, caps: urlCapabilities(), capsKnown: true,
		}},
		{name: "elicitation {}", session: &legacySession{
			version: mcpparser.MCPVersionLegacy, versionKnown: true,
			caps: mcp.ClientCapabilities{Elicitation: &mcp.ElicitationCapability{}}, capsKnown: true,
		}},
		{name: "form only", session: &legacySession{
			version: mcpparser.MCPVersionLegacy, versionKnown: true,
			caps:      mcp.ClientCapabilities{Elicitation: &mcp.ElicitationCapability{Form: &struct{}{}}},
			capsKnown: true,
		}},
		{name: "no elicitation", session: &legacySession{
			version: mcpparser.MCPVersionLegacy, versionKnown: true, capsKnown: true,
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			answer := twoURLAnswer(t)
			res, err := legacyToolCallError(tc.session, answer)
			assertLegacyFallback(t, answer, res, err)
		})
	}
}

func TestLegacyToolCallError_OtherErrorsUnchanged(t *testing.T) {
	t.Parallel()

	res, err := legacyToolCallError(urlDeclaringSession(), errors.New("backend unavailable"))
	require.NoError(t, err)
	require.NotNil(t, res)
	assert.True(t, res.IsError)
	text, ok := res.Content[0].(mcp.TextContent)
	require.True(t, ok)
	assert.Equal(t, "backend unavailable", text.Text)
}

// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package usagemetrics

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"sync"
	"unicode"

	"github.com/stacklok/toolhive/pkg/mcp"
)

const (
	// ClientUnknown is reported when a tool call cannot be tied to a client.
	ClientUnknown = "unknown"

	// maxTrackedSessions caps the session-to-client map. When it is full the map
	// is cleared; calls on sessions forgotten this way are reported as unknown.
	maxTrackedSessions = 4096

	sessionIDHeader = "Mcp-Session-Id"
)

// sessionQueryKeys are the query parameter spellings SSE servers use for the
// session ID (see pkg/transport/proxy/transparent/session_id.go).
var sessionQueryKeys = []string{"sessionId", "sessionid", "session_id"}

// knownClients maps clientInfo.name tokens to a normalized client value. A rule
// matches when every one of its tokens appears in the name; rules are checked
// in order, so more specific rules come first.
var knownClients = []struct {
	tokens []string
	value  string
}{
	{[]string{"claude", "code"}, "claude-code"},
	{[]string{"claude"}, "claude-desktop"},
	{[]string{"cursor"}, "cursor"},
	{[]string{"windsurf"}, "windsurf"},
	{[]string{"visual", "studio", "code"}, "vscode"},
	{[]string{"vscode"}, "vscode"},
	{[]string{"cline"}, "cline"},
	{[]string{"roo"}, "roo-code"},
	{[]string{"continue"}, "continue"},
	{[]string{"zed"}, "zed"},
	{[]string{"goose"}, "goose"},
	{[]string{"codex"}, "codex"},
	{[]string{"gemini"}, "gemini-cli"},
	{[]string{"jetbrains"}, "jetbrains"},
	{[]string{"inspector"}, "mcp-inspector"},
	{[]string{"toolhive"}, "toolhive"},
}

// NormalizeClientName maps an MCP clientInfo.name to one of a fixed set of
// client values, "other" for unrecognized names, or "unknown" for an empty name.
// The raw name is never reported.
func NormalizeClientName(name string) string {
	if name == "" || name == ClientUnknown {
		return ClientUnknown
	}
	for _, known := range knownClients {
		if known.value == name {
			return name
		}
	}
	tokens := strings.FieldsFunc(strings.ToLower(name), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	for _, known := range knownClients {
		if containsAll(tokens, known.tokens) {
			return known.value
		}
	}
	return ValueOther
}

// sessionClients remembers which client opened each session, so tool calls on
// Legacy (session-based) MCP connections can be attributed. Only normalized
// client values are stored; session IDs never leave the process.
type sessionClients struct {
	mu       sync.Mutex
	sessions map[string]string
}

func newSessionClients() *sessionClients {
	return &sessionClients{sessions: make(map[string]string)}
}

func (s *sessionClients) remember(sessionID, client string) {
	if sessionID == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.sessions) >= maxTrackedSessions {
		clear(s.sessions)
	}
	s.sessions[sessionID] = client
}

func (s *sessionClients) lookup(sessionID string) string {
	if sessionID == "" {
		return ClientUnknown
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if client, ok := s.sessions[sessionID]; ok {
		return client
	}
	return ClientUnknown
}

func containsAll(tokens, want []string) bool {
	for _, w := range want {
		if !slices.Contains(tokens, w) {
			return false
		}
	}
	return true
}

// requestSessionID returns the session ID a request carries in the
// Mcp-Session-Id header or a recognized query parameter.
func requestSessionID(r *http.Request) string {
	if id := r.Header.Get(sessionIDHeader); id != "" {
		return id
	}
	query := r.URL.Query()
	for _, key := range sessionQueryKeys {
		if id := query.Get(key); id != "" {
			return id
		}
	}
	return ""
}

// initializeClientName extracts clientInfo.name from initialize params.
func initializeClientName(params json.RawMessage) string {
	var p struct {
		ClientInfo struct {
			Name string `json:"name"`
		} `json:"clientInfo"`
	}
	if len(params) == 0 || json.Unmarshal(params, &p) != nil {
		return ""
	}
	return p.ClientInfo.Name
}

// modernClientName extracts the client name a Modern (stateless) request
// carries in its _meta clientInfo.
func modernClientName(parsed *mcp.ParsedMCPRequest) string {
	name, _ := parsed.ClientInfo["name"].(string)
	return name
}

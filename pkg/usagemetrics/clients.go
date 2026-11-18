// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package usagemetrics

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"unicode"

	lru "github.com/hashicorp/golang-lru/v2"

	"github.com/stacklok/toolhive/pkg/mcp"
)

const (
	// ClientUnknown is reported when a tool call cannot be tied to a client.
	ClientUnknown = "unknown"

	// maxTrackedSessions caps the session-to-client map. When it is full the
	// least recently used session is forgotten; later calls on it are reported
	// as unknown.
	maxTrackedSessions = 4096

	sessionIDHeader = "Mcp-Session-Id"
)

// sessionQueryKeys are the query parameter spellings SSE servers use for the
// session ID (see pkg/transport/proxy/transparent/session_id.go).
var sessionQueryKeys = []string{"sessionId", "sessionid", "session_id"}

// knownClients maps client name tokens to a normalized client value. A rule
// matches when every one of its tokens appears in the name; rules are checked
// in order, so more specific rules come first. The same rules match
// clientInfo.name and the User-Agent header.
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
	// vMCP forwarding a call to a backend proxy; it must come before "toolhive".
	{[]string{"toolhive", "vmcp"}, "vmcp"},
	{[]string{"toolhive"}, "toolhive"},
}

// NormalizeClientName maps an MCP clientInfo.name to one of a fixed set of
// client values, "other" for unrecognized names, or "unknown" for an empty name.
// The raw name is never reported.
func NormalizeClientName(name string) string {
	if name == "" || name == ClientUnknown {
		return ClientUnknown
	}
	if client, ok := knownClientName(name); ok {
		return client
	}
	return ValueOther
}

// knownClientName returns the value of the first knownClients rule whose tokens
// all appear in name, comparing whole tokens case-insensitively, or false when
// no rule matches. A name that already is a known value maps to itself.
func knownClientName(name string) (string, bool) {
	for _, known := range knownClients {
		if known.value == name {
			return name, true
		}
	}
	tokens := strings.FieldsFunc(strings.ToLower(name), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	for _, known := range knownClients {
		if containsAll(tokens, known.tokens) {
			return known.value, true
		}
	}
	return "", false
}

// userAgentClient returns the known client named by the request's User-Agent
// header, or "unknown". Unlike clientInfo.name, an unrecognized User-Agent is
// "unknown" rather than "other": generic HTTP library agents (node,
// python-httpx, Go-http-client) say nothing about the MCP client. The raw
// header is never reported.
func userAgentClient(r *http.Request) string {
	if client, ok := knownClientName(r.Header.Get(userAgentHeader)); ok {
		return client
	}
	return ClientUnknown
}

// sessionClients remembers which client opened each session, so tool calls on
// Legacy (session-based) MCP connections can be attributed. Only normalized
// client values are stored; session IDs never leave the process.
// The cache is safe for concurrent use and evicts the least recently used
// session when full, so a burst of new sessions does not forget long-lived ones
// that keep making calls.
type sessionClients struct {
	sessions *lru.Cache[string, string]
}

func newSessionClients() *sessionClients {
	// lru.New only fails for a non-positive size.
	sessions, _ := lru.New[string, string](maxTrackedSessions)
	return &sessionClients{sessions: sessions}
}

func (s *sessionClients) remember(sessionID, client string) {
	if sessionID == "" {
		return
	}
	s.sessions.Add(sessionID, client)
}

func (s *sessionClients) lookup(sessionID string) string {
	if sessionID == "" {
		return ClientUnknown
	}
	if client, ok := s.sessions.Get(sessionID); ok {
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

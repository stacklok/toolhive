// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"encoding/json"
	"errors"

	"github.com/stacklok/toolhive-core/mcpcompat/mcp"
	"github.com/stacklok/toolhive-core/mcpcompat/server"
	mcpparser "github.com/stacklok/toolhive/pkg/mcp"
	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/conversion"
	"github.com/stacklok/toolhive/pkg/vmcp/server/internal/wireerr"
)

// legacyToolCallError renders a tools/call failure on a Legacy (2025-11-25)
// session. It returns the (result, error) pair for the tool handler to hand
// straight back: either a tool result with a nil error, or a nil result with
// the bare -32042 wireerr.URLElicitationRequired builds.
//
// The order matches writeModernToolCallFailure:
//   - An authorization denial outranks the URL elicitation answer, so an
//     error that wraps ErrAuthorizationFailed renders as the deny tool result
//     even when it also carries the answer.
//   - A URL elicitation answer on a session that accepts URL mode
//     (legacySessionAcceptsURLElicitation) renders as -32042, which the
//     handler returns unwrapped (package wireerr says why).
//   - Everything else, including the answer on any other session, renders
//     through conversion.ErrorToToolResult, whose fallback carries the
//     answer's LegacyFallback: the elicitation messages and never the URL,
//     unless the embedder supplied its own text.
//
// session is the session ClientSessionFromContext returns, and may be nil.
func legacyToolCallError(session server.ClientSession, err error) (*mcp.CallToolResult, error) {
	answer, ok := vmcp.URLElicitationFromError(err)
	if !ok || errors.Is(err, vmcp.ErrAuthorizationFailed) || !legacySessionAcceptsURLElicitation(session) {
		return conversion.ErrorToToolResult(err), nil
	}
	return nil, legacyURLElicitationRequiredError(answer)
}

// legacySessionAcceptsURLElicitation reports whether a URL elicitation may be
// sent on session: it negotiated 2025-11-25, the only revision that defines
// -32042, and its client declared elicitation.url. Both facts must be known.
// A rehydrated session, whose initialize this instance did not see, reports
// neither, so it gets the fallback: MCP forbids sending an elicitation mode
// the client did not declare, and vMCP cannot tell that it did.
func legacySessionAcceptsURLElicitation(session server.ClientSession) bool {
	versioned, ok := session.(server.SessionWithProtocolVersion)
	if !ok {
		return false
	}
	if version, known := versioned.NegotiatedProtocolVersion(); !known || version != mcpparser.MCPVersionLegacy {
		return false
	}
	declaring, ok := session.(server.SessionWithClientCapabilities)
	if !ok {
		return false
	}
	caps, known := declaring.DeclaredClientCapabilities()
	return known && caps.Elicitation != nil && caps.Elicitation.URL != nil
}

// legacyURLElicitation is one entry of the -32042 error's data.elicitations:
// MCP 2025-11-25 ElicitRequestURLParams.
type legacyURLElicitation struct {
	Mode          string `json:"mode"`
	ElicitationID string `json:"elicitationId"`
	URL           string `json:"url"`
	Message       string `json:"message"`
}

// legacyURLElicitationRequiredError builds the MCP 2025-11-25
// URLElicitationRequiredError: code -32042 with data.elicitations listing
// every elicitation in the answer, in order.
func legacyURLElicitationRequiredError(answer *vmcp.URLElicitationRequiredError) error {
	elicitations := answer.Elicitations()
	entries := make([]legacyURLElicitation, 0, len(elicitations))
	for _, e := range elicitations {
		entries = append(entries, legacyURLElicitation{
			Mode:          elicitationModeURL,
			ElicitationID: e.ElicitationID(),
			URL:           e.URL(),
			Message:       e.Message(),
		})
	}
	// Marshal cannot fail: the value holds only strings.
	data, _ := json.Marshal(struct {
		Elicitations []legacyURLElicitation `json:"elicitations"`
	}{Elicitations: entries})
	return wireerr.URLElicitationRequired(data)
}

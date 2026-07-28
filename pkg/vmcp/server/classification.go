// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"net/http"

	mcpparser "github.com/stacklok/toolhive/pkg/mcp"
)

// methodServerDiscover is the Modern (2026-07-28) capability-probe method — how
// a client learns which revisions a server supports. It is special-cased in two
// places — the capability-gate branch in classifyingHandler and dispatchModern's
// method switch (dispatchModernDiscover) — because it must never be refused on
// version grounds: it is the one request a client needs answered to negotiate
// down.
const methodServerDiscover = "server/discover"

// classifyingHandler classifies a parsed MCP request as Legacy (2025-11-25) or
// Modern (2026-07-28) at the decode seam, rejects a malformed Modern request
// with the correct JSON-RPC error before it reaches dispatch, and routes a
// well-formed Modern request to dispatchModern instead of the SDK. Legacy
// traffic always falls through to next unchanged.
//
// Classification is exact-match on both the MCP-Protocol-Version header and the
// reserved io.modelcontextprotocol/* _meta keys (ClassifyRevision), so nothing
// that is not unambiguously Modern reaches dispatchModern: Legacy wire behavior
// is unaffected by this handler.
//
// Whether vMCP serves the Modern revision at all is a per-instance capability
// question, not a global switch: #5959's temporary env-var kill-switch is
// replaced by modernDispatchBlockers (modern_gate.go), which enumerates the
// enabled features the stateless dispatch path cannot serve. When that list is
// empty (the common case) a well-formed Modern request dispatches; when it is
// not, the gate branch below refuses Modern with a conformant -32022 listing
// Legacy so capable clients negotiate down and reach the SDK path, where every
// enabled feature works. A request whose _meta names some OTHER protocol
// version is refused with the same -32022 by ClassifyRevision below, which
// owns every other version-grounds rejection.
//
// ValidateHeaderConsistency (Mcp-Method/Mcp-Name) only applies to Modern
// requests: a Legacy request carrying a stray Mcp-Method/Mcp-Name header
// (e.g. from a misbehaving proxy) must not be rejected for it, since Legacy
// clients never send these headers and have no obligation to omit them.
//
// This handler makes no authentication/authorization decision of its own and
// confers no elevated trust on requests that pass it — it only validates
// protocol shape and routes. It must run after ParsingMiddleware (so
// GetParsedMCPRequest is populated) and is expected to run after any auth
// middleware in the chain, so a Modern dispatch that gets gated 403 is still
// audited as "denied".
func (s *Server) classifyingHandler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		parsed := mcpparser.GetParsedMCPRequest(r.Context())
		if parsed == nil {
			next.ServeHTTP(w, r)
			return
		}

		protoHeader := r.Header.Get("MCP-Protocol-Version")
		rev, err := mcpparser.ClassifyRevision(parsed.Method, parsed.Meta, protoHeader)
		if err != nil {
			mcpparser.WriteClassificationError(w, parsed.ID, err)
			return
		}

		if rev != mcpparser.RevisionModern {
			next.ServeHTTP(w, r)
			return
		}

		// A Modern (2026-07-28) request over HTTP MUST carry the
		// MCP-Protocol-Version header (draft Streamable HTTP "Server Validation").
		// ClassifyRevision is transport-agnostic -- it also serves header-less
		// stdio -- so it admits a Modern request signaled only by the reserved
		// io.modelcontextprotocol/* _meta keys and defers the header-presence rule
		// to the HTTP layer (see the TODO in pkg/mcp/revision.go). Without this
		// check a well-formed Modern _meta with no header would dispatch and return
		// 200 instead of the mandated -32020 rejection.
		if protoHeader == "" {
			mcpparser.WriteClassificationError(w, parsed.ID, errMissingProtocolVersionHeader)
			return
		}

		if err := mcpparser.ValidateHeaderConsistency(parsed); err != nil {
			mcpparser.WriteClassificationError(w, parsed.ID, err)
			return
		}

		// Capability gate: when this instance has enabled features the stateless
		// dispatch path cannot serve (modernDispatchBlockers, modern_gate.go),
		// vMCP does not serve the Modern revision.
		//
		// Answer that conformantly here rather than letting the request reach the
		// SDK. The draft's Streamable HTTP "Protocol Version Header" section
		// requires a server that does not implement a requested version -- "whether
		// the version is unknown to the server, or is a known version the server has
		// chosen not to support" -- to reply 400 with an UnsupportedProtocolVersionError
		// listing the versions it does support. A gated instance is exactly the
		// second case. Falling through instead yields go-sdk's stateful-server
		// rejection, which is a 400 with a PLAIN-TEXT body whose text is Go-API advice
		// for the server author ("set StreamableHTTPOptions.Stateless = true") -- not
		// parseable as a protocol error and carrying no version list.
		//
		// server/discover is deliberately exempt: it is how a client learns which
		// revisions a server supports, so rejecting it on version grounds would leave
		// the client no way to negotiate down. go-sdk exempts it for the same reason,
		// and its stateful path answers discover with the transport-filtered version
		// list -- which excludes 2026-07-28 for a stateful transport -- so the
		// fall-through is not just tolerable but the correct advertisement: a
		// Modern-first client (go-sdk v1.7+ probes discover before initialize)
		// reads it and lands on the Legacy handshake without ever seeing an error.
		if blocked := s.modernDispatchBlockers(); len(blocked) > 0 {
			if parsed.Method == methodServerDiscover {
				next.ServeHTTP(w, r)
				return
			}
			mcpparser.WriteClassificationError(w, parsed.ID, &mcpparser.UnsupportedVersionError{
				Requested: mcpparser.MCPVersionModern,
				Supported: []string{mcpparser.MCPVersionLegacy},
			})
			return
		}

		s.dispatchModern(w, r, parsed)
	})
}

// missingProtocolVersionHeaderError is returned when a request classifies Modern
// (2026-07-28) over HTTP but omits the mandatory MCP-Protocol-Version header. The
// draft Streamable HTTP "Server Validation" rules make a missing required standard
// header a -32020 (HeaderMismatch) condition, so this maps to the same wire code
// and HTTP 400 as the header/body mismatch mcp.ClassifyRevision already produces
// when the header is present but wrong.
//
// The enforcement lives here, at the HTTP layer, rather than in the
// transport-agnostic mcp.ClassifyRevision (which also serves header-less stdio and
// cannot know the header was required); see the TODO in pkg/mcp/revision.go. It
// implements mcp.CodedError so mcp.WriteClassificationError renders it correctly.
type missingProtocolVersionHeaderError struct{}

func (missingProtocolVersionHeaderError) Error() string {
	return "MCP-Protocol-Version header is required for Modern (2026-07-28) requests"
}

// Code implements mcp.CodedError.
func (missingProtocolVersionHeaderError) Code() int64 { return mcpparser.CodeHeaderMismatch }

// Data implements mcp.CodedError.
func (missingProtocolVersionHeaderError) Data() map[string]any {
	return map[string]any{"header": "MCP-Protocol-Version"}
}

// errMissingProtocolVersionHeader is the singleton rejection for a Modern HTTP
// request that omits the mandatory MCP-Protocol-Version header.
var errMissingProtocolVersionHeader = missingProtocolVersionHeaderError{}

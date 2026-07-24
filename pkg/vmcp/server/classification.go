// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"net/http"

	mcpparser "github.com/stacklok/toolhive/pkg/mcp"
)

// classifyingHandler classifies a parsed MCP request as Legacy (2025-11-25) or
// Modern (2026-07-28) at the decode seam, rejects a malformed Modern request
// with the correct JSON-RPC error before it reaches dispatch, and — when
// Config.ModernDispatchEnabled — routes a well-formed Modern request to
// dispatchModern instead of the SDK. Legacy traffic always falls through to
// next unchanged, as does a well-formed Modern request while the switch is
// off (default), matching pre-Modern-dispatch wire behavior byte for byte.
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

		// TEMPORARY kill-switch (default off): until Modern dispatch is
		// conformance-validated, a well-formed Modern request falls through to
		// the SDK path unless explicitly enabled. See issue #5959.
		if !s.config.ModernDispatchEnabled {
			next.ServeHTTP(w, r)
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

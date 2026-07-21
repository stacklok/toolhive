// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"net/http"

	mcpparser "github.com/stacklok/toolhive/pkg/mcp"
)

// classificationMiddleware classifies a parsed MCP request as Legacy
// (2025-11-25) or Modern (2026-07-28) at the decode seam and rejects a
// malformed Modern request with the correct JSON-RPC error before it reaches
// dispatch. The classified mcpparser.Revision is deliberately dropped: it is
// not stashed in context or anywhere else, since no downstream consumer reads
// it yet. Legacy traffic and well-formed Modern requests both fall through to
// the same next handler unchanged — Modern-specific dispatch is a later
// phase (toolhive issue #5756).
//
// This middleware makes no authentication/authorization decision and confers
// no elevated trust on requests that pass it — it only validates protocol
// shape. It must run after ParsingMiddleware (so GetParsedMCPRequest is
// populated) and is expected to run after any auth middleware in the chain.
func classificationMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		parsed := mcpparser.GetParsedMCPRequest(r.Context())
		if parsed == nil {
			next.ServeHTTP(w, r)
			return
		}

		protoHeader := r.Header.Get("MCP-Protocol-Version")
		if _, err := mcpparser.ClassifyRevision(parsed.Method, parsed.Meta, protoHeader); err != nil {
			mcpparser.WriteClassificationError(w, parsed.ID, err)
			return
		}

		if err := mcpparser.ValidateHeaderConsistency(parsed); err != nil {
			mcpparser.WriteClassificationError(w, parsed.ID, err)
			return
		}

		next.ServeHTTP(w, r)
	})
}

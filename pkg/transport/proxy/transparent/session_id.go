// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package transparent

import (
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/google/uuid"

	"github.com/stacklok/toolhive/pkg/auth/sessionbinding"
)

// sessionQueryKeys are the query parameter names an MCP SSE server may use to carry
// the session id in its endpoint event. The proxy does not get to choose the name,
// and the spellings in the wild genuinely differ:
//
//   - "sessionId"  the TypeScript SDK and mcp-go
//   - "sessionid"  mark3labs/mcp-go servers such as the yardstick test server,
//     whose endpoint event reads `data: /sse?sessionid=...`
//   - "session_id" ToolHive's own SSE proxy (pkg/transport/proxy/httpsse)
//
// Recognising only one spelling does not fail safe. extractSessionID treats an
// unrecognised carrier as an absent one and closes the stream, so the client never
// receives the endpoint event at all -- a total outage for that backend rather than
// a tightened check.
//
// Every site that reads, enforces or rewrites a session carrier must use this same
// list. A carrier bound under one spelling and checked under another is how a
// request gets authorized as one session and executed as another.
var sessionQueryKeys = []string{"sessionId", "sessionid", "session_id"}

// requestSessionID resolves a request's session id from the Mcp-Session-Id header or
// any recognised query spelling.
//
// Two spellings carrying different values are refused rather than resolved to one of
// them: this proxy and the backend could pick differently, and a disagreement there
// is a session-confusion bug. Returning an empty id is not an error -- it means the
// request names no session, which callers handle separately.
func requestSessionID(r *http.Request) (string, error) {
	var found string
	for _, key := range sessionQueryKeys {
		id, err := sessionbinding.RequestID(r, key)
		if err != nil {
			return "", err
		}
		if id == "" {
			continue
		}
		if found != "" && found != id {
			return "", sessionbinding.ErrNotFound
		}
		found = id
	}
	return found, nil
}

// rewriteSessionQuery modifies only existing sessionId fields, preserving the
// byte representation and order of other query parameters. An empty ID strips it.
func rewriteSessionQuery(u *url.URL, id string) {
	parts := strings.Split(u.RawQuery, "&")
	kept := parts[:0]
	for _, part := range parts {
		key, _, _ := strings.Cut(part, "=")
		key, err := url.QueryUnescape(key)
		if err == nil && slices.Contains(sessionQueryKeys, key) {
			if id != "" {
				kept = append(kept, key+"="+url.QueryEscape(id))
			}
			continue
		}
		kept = append(kept, part)
	}
	u.RawQuery = strings.Join(kept, "&")
}

// mcpSessionNamespace is the UUID v5 namespace used when normalizing non-UUID
// Mcp-Session-Id values received from upstream MCP servers.
var mcpSessionNamespace = uuid.MustParse("6ba7b810-9dad-11d1-80b4-00c04fd430c8") // RFC 4122 URL namespace

// normalizeSessionID returns id unchanged if it is already a valid UUID.
// Otherwise it returns a deterministic UUID v5 derived from id, ensuring that
// the session manager (which requires UUID-format IDs) can store sessions whose
// Mcp-Session-Id was issued by an upstream server in a non-UUID format.
//
// The mapping is stable: the same external id always produces the same UUID,
// so the proxy can look up and delete sessions without maintaining a separate
// reverse-mapping table.
func normalizeSessionID(id string) string {
	if _, err := uuid.Parse(id); err == nil {
		return id
	}
	return uuid.NewSHA1(mcpSessionNamespace, []byte(id)).String()
}

// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package transparent

import "github.com/google/uuid"

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

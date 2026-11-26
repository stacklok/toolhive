// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"net/http"
	"strings"
)

// HasContentEncoding reports whether the headers declare any encoding other
// than identity. Encodings are case-insensitive and may be listed in order
// (RFC 9110 section 8.4), so every listed token is checked.
func HasContentEncoding(h http.Header) bool {
	for _, value := range h.Values("Content-Encoding") {
		for _, token := range strings.Split(value, ",") {
			if t := strings.ToLower(strings.TrimSpace(token)); t != "" && t != "identity" {
				return true
			}
		}
	}
	return false
}

// staleEntityHeaders describe a backend's body, not a body a filter writes in
// its place, so a client or cache must not see them on the replacement.
var staleEntityHeaders = []string{
	"Content-Encoding", "Content-Length", "Content-MD5", "Content-Range",
	"Content-Digest", "Repr-Digest", "Digest", "ETag", "Last-Modified",
}

// DeleteEntityHeaders removes the headers that describe the backend's body.
// The authz response filter calls it for every response it may rewrite, before
// committing the head.
func DeleteEntityHeaders(h http.Header) {
	for _, name := range staleEntityHeaders {
		h.Del(name)
	}
}

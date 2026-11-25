// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package oauthproto

import "strings"

const joseMediaTypePrefix = "application/"

// IsJOSEType reports whether a JOSE "typ" header value names the given media
// type. Per RFC 7515 Section 4.1.9 the comparison is case-insensitive and the
// "application/" prefix is optional: a value without a "/" implies it. The
// prefix is ignored on either argument. An empty headerTyp or mediaType never
// matches, so a token with no typ header matches nothing.
func IsJOSEType(headerTyp, mediaType string) bool {
	typ := normalizeJOSEType(headerTyp)
	want := normalizeJOSEType(mediaType)
	return typ != "" && typ == want
}

func normalizeJOSEType(v string) string {
	v = strings.ToLower(v)
	if strings.HasPrefix(v, joseMediaTypePrefix) {
		return v[len(joseMediaTypePrefix):]
	}
	return v
}

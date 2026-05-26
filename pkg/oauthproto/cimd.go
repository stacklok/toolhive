// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package oauthproto

import (
	"net/url"
	"strings"
)

// ToolHiveClientMetadataDocumentURL is the stable HTTPS URL where ToolHive's
// client metadata document is hosted. ToolHive presents this URL as its
// client_id to remote authorization servers that support CIMD. The URL must
// be live and serving the client metadata document before this feature can
// be used in production.
const ToolHiveClientMetadataDocumentURL = "https://toolhive.dev/oauth/client-metadata.json"

// IsClientIDMetadataDocumentURL returns true if clientID looks like a CIMD URL.
// In production this means any HTTPS URL; in local development http://localhost,
// http://127.0.0.1, and http://[::1] URLs are also accepted so that integration
// tests can use plain httptest.Server instances without TLS setup. DCR-issued IDs
// are always opaque strings that never begin with a URL scheme, so there is no
// risk of false positives. Do not tighten this to an exact match against
// ToolHiveClientMetadataDocumentURL — the embedded AS must accept CIMD URLs
// from third-party clients too.
func IsClientIDMetadataDocumentURL(clientID string) bool {
	if strings.HasPrefix(clientID, "https://") {
		return true
	}
	// Allow loopback HTTP URLs (localhost, 127.0.0.1, [::1]) for local
	// development and integration testing. These are the only HTTP URLs that
	// FetchClientMetadataDocument / validateCIMDClientURL also accept.
	if strings.HasPrefix(clientID, "http://") {
		parsed, err := url.Parse(clientID)
		if err != nil {
			return false
		}
		return IsLoopbackHost(parsed.Host)
	}
	return false
}

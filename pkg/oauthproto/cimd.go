// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package oauthproto

import "strings"

// ToolHiveClientMetadataDocumentURL is the stable HTTPS URL where ToolHive's
// client metadata document is hosted. ToolHive presents this URL as its
// client_id to remote authorization servers that support CIMD. The URL must
// be live and serving the client metadata document before this feature can
// be used in production.
const ToolHiveClientMetadataDocumentURL = "https://toolhive.dev/oauth/client-metadata.json"

// IsClientIDMetadataDocumentURL returns true if clientID is an HTTPS URL.
// Any HTTPS URL is treated as a CIMD client_id; DCR-issued IDs are always
// opaque strings that never begin with "https://". Do not tighten this to an
// exact match against ToolHiveClientMetadataDocumentURL — the embedded AS
// (Phase 2) must accept CIMD URLs from third-party clients too.
//
// TODO(phase2): tighten per draft-ietf-oauth-client-id-metadata-document §3
// (require host+path, reject fragment/userinfo/dot-segments) before wiring
// into the AS GetClient decorator.
func IsClientIDMetadataDocumentURL(clientID string) bool {
	return strings.HasPrefix(clientID, "https://")
}

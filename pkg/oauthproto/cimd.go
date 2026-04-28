// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package oauthproto

import "strings"

// ToolHiveClientMetadataDocumentURL is the stable HTTPS URL where ToolHive's
// client metadata document is hosted. This URL is the client_id ToolHive
// presents to remote authorization servers that support CIMD.
// ToolHiveClientMetadataDocumentURL is the stable HTTPS URL where ToolHive's
// client metadata document is hosted. This URL must be live and serving
// toolhive-client-metadata.json before this feature can be used in production.
const ToolHiveClientMetadataDocumentURL = "https://toolhive.dev/oauth/client-metadata.json"

// IsClientIDMetadataDocumentURL returns true if clientID is an HTTPS URL,
// indicating it should be treated as a CIMD client_id rather than a DCR-issued UUID.
func IsClientIDMetadataDocumentURL(clientID string) bool {
	return strings.HasPrefix(clientID, "https://")
}

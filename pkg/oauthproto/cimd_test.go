// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package oauthproto

import (
	"testing"
)

func TestToolHiveClientMetadataDocumentURL(t *testing.T) {
	t.Parallel()

	const want = "https://toolhive.dev/oauth/client-metadata.json"
	if ToolHiveClientMetadataDocumentURL != want {
		t.Errorf("ToolHiveClientMetadataDocumentURL = %q, want %q", ToolHiveClientMetadataDocumentURL, want)
	}
}

func TestIsClientIDMetadataDocumentURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		clientID string
		want     bool
	}{
		{"CIMD URL (toolhive)", ToolHiveClientMetadataDocumentURL, true},
		{"arbitrary HTTPS URL", "https://example.com/client-metadata.json", true},
		{"HTTPS URL no path", "https://example.com", true},
		{"DCR-issued UUID", "some-uuid-client-id", false},
		{"HTTP URL", "http://example.com/metadata.json", false},
		{"empty string", "", false},
		{"partial match", "xhttps://example.com", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := IsClientIDMetadataDocumentURL(tt.clientID); got != tt.want {
				t.Errorf("IsClientIDMetadataDocumentURL(%q) = %v, want %v", tt.clientID, got, tt.want)
			}
		})
	}
}

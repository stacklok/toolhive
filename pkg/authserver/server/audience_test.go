// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestValidateAudienceURI(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		resource string
		wantErr  bool
	}{
		{"empty is valid", "", false},
		{"valid https", "https://api.example.com", false},
		{"valid http", "http://localhost:8080", false},
		{"relative URI", "/api/resource", true},
		{"has fragment", "https://api.example.com#section", true},
		{"wrong scheme", "ftp://files.example.com", true},
		{"no host", "https://", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := ValidateAudienceURI(tt.resource)
			if tt.wantErr {
				assert.Error(t, err, "resource: %q", tt.resource)
			} else {
				assert.NoError(t, err, "resource: %q", tt.resource)
			}
		})
	}
}

func TestValidateAudienceAllowed(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		resource  string
		allowlist []string
		wantErr   bool
	}{
		{"empty resource always valid", "", nil, false},
		{"nil allowlist rejects all", "https://a.com", nil, true},
		{"empty allowlist rejects all", "https://a.com", []string{}, true},
		{"exact match", "https://a.com", []string{"https://a.com"}, false},
		{"not in list", "https://a.com", []string{"https://b.com"}, true},
		{"case sensitive", "https://a.com", []string{"https://A.com"}, true},
		{"multiple with match", "https://b.com", []string{"https://a.com", "https://b.com"}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := ValidateAudienceAllowed(tt.resource, tt.allowlist)
			if tt.wantErr {
				assert.Error(t, err, "resource: %q, allowlist: %v", tt.resource, tt.allowlist)
			} else {
				assert.NoError(t, err, "resource: %q, allowlist: %v", tt.resource, tt.allowlist)
			}
		})
	}
}

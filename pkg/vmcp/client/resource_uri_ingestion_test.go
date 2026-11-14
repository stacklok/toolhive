// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package client

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive-core/mcpcompat/mcp"
)

// TestNewCapabilityListFromMCP_DropsUnparseableResourceURI pins that a backend
// resource whose URI url.Parse rejects is dropped at ingestion. go-sdk's
// AddResource panics on such a URI, which would crash the whole vMCP process,
// so only the offending resource may be dropped and its siblings kept.
func TestNewCapabilityListFromMCP_DropsUnparseableResourceURI(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		uri      string
		wantKept bool
	}{
		{name: "http URI is kept", uri: "http://example.com/a", wantKept: true},
		{name: "file URI is kept", uri: "file:///a/b", wantKept: true},
		{name: "opaque scheme URI is kept", uri: "custom://thing", wantKept: true},
		{name: "unterminated IPv6 host is dropped", uri: "http://[::1", wantKept: false},
		{name: "invalid percent escape is dropped", uri: "file:///a%zz", wantKept: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			resources := []mcp.Resource{
				{URI: tt.uri, Name: "under-test"},
				{URI: "file:///sibling", Name: "sibling"},
			}
			caps := newCapabilityListFromMCP("backend-1", nil, resources, nil, nil)
			require.NotNil(t, caps)

			uris := make([]string, 0, len(caps.Resources))
			for _, r := range caps.Resources {
				uris = append(uris, r.URI)
			}
			assert.Contains(t, uris, "file:///sibling", "sibling resource must survive")
			if tt.wantKept {
				assert.Contains(t, uris, tt.uri)
			} else {
				assert.NotContains(t, uris, tt.uri)
			}
		})
	}
}

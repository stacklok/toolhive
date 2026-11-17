// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package tokenexchange

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestActIssuerProblem(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		issuer  string
		wantErr string
	}{
		{name: "https URL", issuer: "https://registry.example.com/agents"},
		{name: "spiffe trust domain", issuer: "spiffe://example.org"},
		{name: "urn", issuer: "urn:example:agents"},
		{name: "longest URI", issuer: "urn:" + strings.Repeat("a", 2044)},
		{name: "empty", issuer: "", wantErr: "length 0 is outside 1 to 2048 bytes"},
		{name: "too long", issuer: "urn:" + strings.Repeat("a", 2045), wantErr: "length 2049 is outside 1 to 2048 bytes"},
		{name: "no scheme", issuer: "example.org", wantErr: "has no scheme"},
		{name: "userinfo", issuer: "https://u@example.org", wantErr: "URI contains userinfo"},
		{name: "fragment", issuer: "urn:a#b", wantErr: "URI contains '#' at offset 5"},
		{name: "query", issuer: "https://example.org?a=b", wantErr: "URI contains a query"},
		{name: "empty query", issuer: "https://example.org?", wantErr: "URI contains a query"},
		{name: "opaque query", issuer: "urn:a?b", wantErr: "URI contains a query"},
		{name: "nothing after scheme", issuer: "urn:", wantErr: "URI has nothing after the scheme"},
		{name: "non-ASCII byte", issuer: "urn:é", wantErr: "byte 0xc3 at offset 4 is not allowed in a URI"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := ActIssuerProblem(tt.issuer)
			if tt.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.EqualError(t, err, tt.wantErr)
			assert.NotContains(t, err.Error(), "agent ID")
		})
	}
}

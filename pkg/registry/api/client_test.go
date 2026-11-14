// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package api

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

const validSpecInfo = "info:\n  version: 1.0.0\n  description: See https://github.com/modelcontextprotocol/registry\n"

// manyDistinctKeys returns size bytes of distinct block mapping keys, each
// line prefixed by indent. Decoding such a mapping into a Go value costs time
// quadratic in the number of keys.
func manyDistinctKeys(size int, indent string) string {
	var b strings.Builder
	for i := 0; b.Len() < size; i++ {
		fmt.Fprintf(&b, "%sk%d: \n", indent, i)
	}
	return b.String()
}

func TestClient_ValidateEndpoint(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		body    string
		wantErr string
	}{
		{
			name: "valid registry spec",
			body: validSpecInfo,
		},
		{
			name:    "wrong version",
			body:    "info:\n  version: 2.0.0\n  description: See https://github.com/modelcontextprotocol/registry\n",
			wantErr: "version is 2.0.0",
		},
		{
			name:    "missing info section",
			body:    "openapi: 3.0.0\n",
			wantErr: "missing 'info' section",
		},
		{
			name: "info via alias",
			body: "base: &b {version: 1.0.0, description: See https://github.com/modelcontextprotocol/registry}\ninfo: *b\n",
		},
		{
			name:    "non-string version",
			body:    "info:\n  version: 1.0\n  description: See https://github.com/modelcontextprotocol/registry\n",
			wantErr: "missing 'version' field",
		},
		{
			name:    "missing description",
			body:    "info:\n  version: 1.0.0\n",
			wantErr: "missing 'description' field",
		},
		{
			name:    "info is not a mapping",
			body:    "info: [a]\n",
			wantErr: "missing 'info' section",
		},
		{
			name:    "empty body",
			body:    "",
			wantErr: "missing 'info' section",
		},
		{
			name:    "duplicate key",
			body:    validSpecInfo + "info: {}\n",
			wantErr: `mapping key "info" already defined`,
		},
		{
			// Repeated keys took minutes through yaml.Unmarshal.
			name:    "many repeated keys at size limit",
			body:    strings.Repeat("a: \n", maxOpenAPISpecSize/4),
			wantErr: `mapping key "a" already defined`,
		},
		{
			name: "many distinct top-level keys near size limit",
			body: validSpecInfo + manyDistinctKeys(maxOpenAPISpecSize-len(validSpecInfo)-16, ""),
		},
		{
			name: "many distinct keys inside info near size limit",
			body: validSpecInfo + manyDistinctKeys(maxOpenAPISpecSize-len(validSpecInfo)-32, "  "),
		},
		{
			name:    "body over size limit",
			body:    validSpecInfo + "#" + strings.Repeat("x", maxOpenAPISpecSize),
			wantErr: "exceeds maximum size",
		},
		{
			// Mapping-valued key combined with a merge key panicked gopkg.in/yaml.v3.
			name:    "merge key with mapping-valued key",
			body:    "? {a: b}\n: c\n<<: {name: x}\n",
			wantErr: "missing 'info' section",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				require.Equal(t, "/openapi.yaml", r.URL.Path)
				_, _ = w.Write([]byte(tt.body))
			}))
			t.Cleanup(server.Close)

			client, err := NewClient(server.URL, true, nil)
			require.NoError(t, err)

			err = client.ValidateEndpoint(t.Context())
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
		})
	}
}

// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestClient_ValidateEndpoint(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		body    string
		wantErr bool
	}{
		{
			name: "valid registry spec",
			body: "info:\n  version: 1.0.0\n  description: See https://github.com/modelcontextprotocol/registry\n",
		},
		{
			name:    "wrong version",
			body:    "info:\n  version: 2.0.0\n  description: See https://github.com/modelcontextprotocol/registry\n",
			wantErr: true,
		},
		{
			name:    "missing info section",
			body:    "openapi: 3.0.0\n",
			wantErr: true,
		},
		{
			// Mapping-valued key combined with a merge key panicked gopkg.in/yaml.v3.
			name:    "merge key with mapping-valued key",
			body:    "? {a: b}\n: c\n<<: {name: x}\n",
			wantErr: true,
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
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
		})
	}
}

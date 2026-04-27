// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package registry

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	types "github.com/stacklok/toolhive-core/registry/types"
)

const legacyContainerJSON = `{
  "version": "1.0.0",
  "last_updated": "2026-01-15T10:00:00Z",
  "servers": {
    "filesystem": {
      "description": "A filesystem MCP server",
      "tier": "Official",
      "status": "active",
      "transport": "stdio",
      "image": "ghcr.io/example/filesystem:v1.0.0",
      "tools": ["read_file", "write_file"],
      "tags": ["filesystem", "productivity"],
      "metadata": {
        "stars": 42,
        "pulls": 1234
      }
    }
  }
}`

const legacyRemoteJSON = `{
  "version": "1.0.0",
  "last_updated": "2026-01-15T10:00:00Z",
  "servers": {},
  "remote_servers": {
    "example-api": {
      "description": "A remote MCP server",
      "tier": "Community",
      "status": "active",
      "transport": "streamable-http",
      "url": "https://api.example.com/mcp",
      "tools": ["query"],
      "tags": ["api"]
    }
  }
}`

const upstreamJSON = `{
  "$schema": "https://example.com/schema.json",
  "version": "1.0.0",
  "meta": {"last_updated": "2026-01-15T10:00:00Z"},
  "data": {"servers": []}
}`

func TestConvertJSON(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		input       string
		wantErr     error
		wantServers int
		assertOut   func(t *testing.T, out *types.UpstreamRegistry)
	}{
		{
			name:        "container server in legacy format converts to upstream",
			input:       legacyContainerJSON,
			wantServers: 1,
			assertOut: func(t *testing.T, out *types.UpstreamRegistry) {
				t.Helper()
				assert.Equal(t, "1.0.0", out.Version)
				assert.Equal(t, "2026-01-15T10:00:00Z", out.Meta.LastUpdated)
				require.Len(t, out.Data.Servers, 1)
				assert.Equal(t, "io.github.stacklok/filesystem", out.Data.Servers[0].Name)
				assert.Equal(t, "A filesystem MCP server", out.Data.Servers[0].Description)
			},
		},
		{
			name:        "remote server in legacy format converts to upstream",
			input:       legacyRemoteJSON,
			wantServers: 1,
			assertOut: func(t *testing.T, out *types.UpstreamRegistry) {
				t.Helper()
				require.Len(t, out.Data.Servers, 1)
				assert.Equal(t, "io.github.stacklok/example-api", out.Data.Servers[0].Name)
				require.Len(t, out.Data.Servers[0].Remotes, 1)
				assert.Equal(t, "https://api.example.com/mcp", out.Data.Servers[0].Remotes[0].URL)
			},
		},
		{
			name:    "upstream input returns ErrAlreadyUpstream",
			input:   upstreamJSON,
			wantErr: ErrAlreadyUpstream,
		},
		{
			name:    "invalid JSON returns error",
			input:   "not json",
			wantErr: errParseFailed,
		},
		{
			name:    "empty input returns error",
			input:   "",
			wantErr: errParseFailed,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			out, err := ConvertJSON([]byte(tt.input))

			switch tt.wantErr {
			case ErrAlreadyUpstream:
				assert.ErrorIs(t, err, ErrAlreadyUpstream)
				assert.Nil(t, out)
				return
			case errParseFailed:
				assert.Error(t, err)
				assert.Nil(t, out)
				return
			}

			require.NoError(t, err)
			require.NotNil(t, out)

			var parsed types.UpstreamRegistry
			require.NoError(t, json.Unmarshal(out, &parsed))
			assert.Len(t, parsed.Data.Servers, tt.wantServers)
			if tt.assertOut != nil {
				tt.assertOut(t, &parsed)
			}
		})
	}
}

// errParseFailed is a sentinel used by TestConvertJSON to signal "any non-nil
// error other than ErrAlreadyUpstream is acceptable". It is never returned by
// production code.
var errParseFailed = assert.AnError

func TestIsUpstreamJSON(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		want bool
	}{
		{name: "upstream format with data wrapper", in: upstreamJSON, want: true},
		{name: "legacy format without data wrapper", in: legacyContainerJSON, want: false},
		{name: "empty input", in: "", want: false},
		{name: "invalid JSON", in: "not json", want: false},
		{name: "data field is array, not object", in: `{"data": []}`, want: false},
		{name: "data field is null", in: `{"data": null}`, want: false},
		{name: "no data field", in: `{"version": "1.0"}`, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, isUpstreamJSON([]byte(tt.in)))
		})
	}
}

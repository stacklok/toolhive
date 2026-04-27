// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package registry

import (
	"encoding/json"
	"testing"

	v0 "github.com/modelcontextprotocol/registry/pkg/api/v0"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	catalog "github.com/stacklok/toolhive-catalog/pkg/catalog/toolhive"
	"github.com/stacklok/toolhive-core/registry/converters"
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
		name             string
		input            string
		wantAlreadyUpstr bool
		wantParseErr     bool
		wantServers      int
		assertOut        func(t *testing.T, out *types.UpstreamRegistry)
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
			name:             "upstream input returns ErrAlreadyUpstream",
			input:            upstreamJSON,
			wantAlreadyUpstr: true,
		},
		{
			name:         "invalid JSON returns error",
			input:        "not json",
			wantParseErr: true,
		},
		{
			name:         "empty input returns error",
			input:        "",
			wantParseErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			out, err := ConvertJSON([]byte(tt.input))

			switch {
			case tt.wantAlreadyUpstr:
				assert.ErrorIs(t, err, ErrAlreadyUpstream)
				assert.Nil(t, out)
				return
			case tt.wantParseErr:
				assert.Error(t, err)
				assert.NotErrorIs(t, err, ErrAlreadyUpstream)
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

// legacyContainerWithExtensionsJSON exercises the fields the converter must
// place under the publisher-provided extension on an upstream server entry.
// If any of these fields are dropped, "the conversion is lossless" is false.
const legacyContainerWithExtensionsJSON = `{
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
      "args": ["--root", "/data"],
      "docker_tags": ["v1.0.0", "latest"],
      "target_port": 8080,
      "env_vars": [
        {"name": "API_KEY", "description": "auth token", "required": true, "secret": true},
        {"name": "LOG_LEVEL", "description": "log level", "default": "info"}
      ],
      "metadata": {
        "stars": 42,
        "last_updated": "2026-01-15T10:00:00Z"
      },
      "permissions": {
        "network": {"outbound": {"insecure_allow_all": true}}
      },
      "custom_metadata": {
        "vendor": "example",
        "purpose": "testing"
      }
    }
  }
}`

// publisherExtension extracts the publisher-provided extension block for a
// single server entry. Returns the inner ServerExtensions map keyed by the
// server image/url under "io.github.stacklok".
func publisherExtension(t *testing.T, server v0.ServerJSON) map[string]any {
	t.Helper()
	require.NotNil(t, server.Meta, "server must carry _meta when ToolHive fields are present")
	publisher := server.Meta.PublisherProvided
	require.NotNil(t, publisher)
	stacklok, ok := publisher[types.ToolHivePublisherNamespace].(map[string]any)
	require.True(t, ok, "publisher-provided block must contain %q", types.ToolHivePublisherNamespace)
	require.Len(t, stacklok, 1, "expected exactly one inner key under %q", types.ToolHivePublisherNamespace)
	for _, v := range stacklok {
		inner, ok := v.(map[string]any)
		require.True(t, ok, "inner extension entry must be an object")
		return inner
	}
	t.Fatal("unreachable: stacklok block was empty")
	return nil
}

// TestConvertJSON_LosslessExtensions asserts that ToolHive-specific fields on
// a legacy container server entry survive the conversion and land in the
// publisher-provided extension on the upstream server. This is the converter's
// main user-facing claim and the test that backs the "lossless" wording in the
// PR description.
func TestConvertJSON_LosslessExtensions(t *testing.T) {
	t.Parallel()

	out, err := ConvertJSON([]byte(legacyContainerWithExtensionsJSON))
	require.NoError(t, err)

	var parsed types.UpstreamRegistry
	require.NoError(t, json.Unmarshal(out, &parsed))
	require.Len(t, parsed.Data.Servers, 1)
	server := parsed.Data.Servers[0]

	// Top-level upstream fields.
	assert.Equal(t, "io.github.stacklok/filesystem", server.Name)
	assert.Equal(t, "A filesystem MCP server", server.Description)
	require.Len(t, server.Packages, 1)
	pkg := server.Packages[0]
	assert.Equal(t, "ghcr.io/example/filesystem:v1.0.0", pkg.Identifier)
	assert.Equal(t, "stdio", string(pkg.Transport.Type))

	// env_vars land on the package, not the publisher extension.
	require.Len(t, pkg.EnvironmentVariables, 2)
	envByName := map[string]string{}
	for _, ev := range pkg.EnvironmentVariables {
		envByName[ev.Name] = ev.Description
	}
	assert.Equal(t, "auth token", envByName["API_KEY"])
	assert.Equal(t, "log level", envByName["LOG_LEVEL"])

	// Everything else lives under publisher-provided extensions.
	ext := publisherExtension(t, server)
	assert.Equal(t, "Official", ext["tier"])
	assert.Equal(t, "active", ext["status"])
	assert.ElementsMatch(t, []any{"read_file", "write_file"}, ext["tools"])
	assert.ElementsMatch(t, []any{"filesystem", "productivity"}, ext["tags"])
	assert.ElementsMatch(t, []any{"--root", "/data"}, ext["args"])
	assert.ElementsMatch(t, []any{"v1.0.0", "latest"}, ext["docker_tags"])

	require.Contains(t, ext, "metadata")
	meta, ok := ext["metadata"].(map[string]any)
	require.True(t, ok, "metadata must be an object")
	assert.EqualValues(t, 42, meta["stars"])

	require.Contains(t, ext, "permissions")
	perms, ok := ext["permissions"].(map[string]any)
	require.True(t, ok)
	assert.NotEmpty(t, perms["network"], "permissions.network must round-trip")

	require.Contains(t, ext, "custom_metadata")
	custom, ok := ext["custom_metadata"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "example", custom["vendor"])
	assert.Equal(t, "testing", custom["purpose"])
}

// TestConvertJSON_RemoteServerExtensions asserts that remote server fields land
// where the upstream format expects them — URL on the remote transport entry,
// ToolHive-specific fields on the publisher-provided extension.
func TestConvertJSON_RemoteServerExtensions(t *testing.T) {
	t.Parallel()

	const legacy = `{
	  "version": "1.0.0",
	  "last_updated": "2026-01-15T10:00:00Z",
	  "remote_servers": {
	    "example-api": {
	      "description": "A remote MCP server",
	      "tier": "Community",
	      "status": "active",
	      "transport": "streamable-http",
	      "url": "https://api.example.com/mcp",
	      "tools": ["query"],
	      "tags": ["api"],
	      "oauth_config": {
	        "issuer": "https://accounts.example.com",
	        "client_id": "test-client",
	        "scopes": ["openid", "email"]
	      }
	    }
	  }
	}`

	out, err := ConvertJSON([]byte(legacy))
	require.NoError(t, err)

	var parsed types.UpstreamRegistry
	require.NoError(t, json.Unmarshal(out, &parsed))
	require.Len(t, parsed.Data.Servers, 1)
	server := parsed.Data.Servers[0]

	require.Len(t, server.Remotes, 1)
	assert.Equal(t, "https://api.example.com/mcp", server.Remotes[0].URL)
	assert.Equal(t, "streamable-http", string(server.Remotes[0].Type))

	ext := publisherExtension(t, server)
	assert.Equal(t, "Community", ext["tier"])
	assert.ElementsMatch(t, []any{"query"}, ext["tools"])
	assert.ElementsMatch(t, []any{"api"}, ext["tags"])

	require.Contains(t, ext, "oauth_config")
	oauth, ok := ext["oauth_config"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "https://accounts.example.com", oauth["issuer"])
	assert.Equal(t, "test-client", oauth["client_id"])
}

// TestConvertJSON_OutputPassesSchemaValidation makes the schema-validation
// invariant explicit: ConvertJSON must never return bytes that fail the
// upstream registry schema. This guards the on-disk file `thv registry convert`
// produces.
func TestConvertJSON_OutputPassesSchemaValidation(t *testing.T) {
	t.Parallel()

	out, err := ConvertJSON([]byte(legacyContainerWithExtensionsJSON))
	require.NoError(t, err)
	assert.NoError(t, types.ValidateUpstreamRegistryBytes(out),
		"converter output must conform to the upstream registry schema")
}

// TestConvertJSON_RoundTripEmbeddedCatalog runs the embedded upstream catalog
// through the full upstream → toolhive → upstream pipeline and verifies the
// server set is preserved. The legacy types.Registry used as the intermediate
// representation maps each server by canonical name, so collisions there
// would manifest as a server count drop.
func TestConvertJSON_RoundTripEmbeddedCatalog(t *testing.T) {
	t.Parallel()

	original, _, err := parseUpstreamRegistry(catalog.Upstream())
	require.NoError(t, err)

	roundTripped, err := converters.NewUpstreamRegistryFromToolhiveRegistry(original)
	require.NoError(t, err)

	// Server count is preserved across the round trip.
	want := len(original.Servers) + len(original.RemoteServers)
	assert.Equal(t, want, len(roundTripped.Data.Servers),
		"round trip must preserve every server")

	// Spot-check that descriptions survive — a regression here means the
	// converter dropped a non-trivial field somewhere.
	descriptions := map[string]struct{}{}
	for _, s := range roundTripped.Data.Servers {
		descriptions[s.Description] = struct{}{}
	}
	for name, srv := range original.Servers {
		if srv.Description == "" {
			continue
		}
		_, ok := descriptions[srv.Description]
		assert.True(t, ok, "server %q description was lost in the round trip", name)
	}
}

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

func TestLooksLikeLegacyJSON(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		want bool
	}{
		{name: "top-level servers", in: legacyContainerJSON, want: true},
		{name: "top-level remote_servers", in: `{"remote_servers": {"r": {}}}`, want: true},
		{name: "top-level groups", in: `{"groups": [{"name": "g"}]}`, want: true},
		{name: "upstream wraps under data", in: upstreamJSON, want: false},
		{name: "empty object", in: `{}`, want: false},
		{name: "malformed JSON returns false", in: "not json", want: false},
		{name: "empty input returns false", in: "", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, looksLikeLegacyJSON([]byte(tt.in)))
		})
	}
}

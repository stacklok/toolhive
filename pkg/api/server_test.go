// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package api

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"regexp"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	regtypes "github.com/stacklok/toolhive-core/registry/types"
	pluginsmocks "github.com/stacklok/toolhive/pkg/plugins/mocks"
	"github.com/stacklok/toolhive/pkg/plugins/pluginsvc"
	skillsmocks "github.com/stacklok/toolhive/pkg/skills/mocks"
)

func TestGenerateNonce(t *testing.T) {
	t.Parallel()

	t.Run("returns valid 32-char hex string", func(t *testing.T) {
		t.Parallel()

		nonce, err := GenerateNonce()
		require.NoError(t, err)

		assert.Len(t, nonce, 32)
		assert.Regexp(t, regexp.MustCompile(`^[0-9a-f]{32}$`), nonce)
	})

	t.Run("returns unique values on successive calls", func(t *testing.T) {
		t.Parallel()

		nonce1, err := GenerateNonce()
		require.NoError(t, err)

		nonce2, err := GenerateNonce()
		require.NoError(t, err)

		assert.NotEqual(t, nonce1, nonce2)
	})
}

func TestListenURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		server   func(t *testing.T) *Server
		expected func(s *Server) string
	}{
		{
			name: "TCP returns http URL with actual port",
			server: func(t *testing.T) *Server {
				t.Helper()
				listener, err := net.Listen("tcp", "127.0.0.1:0")
				require.NoError(t, err)
				t.Cleanup(func() { listener.Close() })
				return &Server{
					listener:     listener,
					isUnixSocket: false,
					address:      "127.0.0.1:0",
				}
			},
			expected: func(s *Server) string {
				return fmt.Sprintf("http://%s", s.listener.Addr().String())
			},
		},
		{
			name: "Unix socket returns unix URL",
			server: func(_ *testing.T) *Server {
				return &Server{
					isUnixSocket: true,
					address:      "/tmp/test.sock",
				}
			},
			expected: func(_ *Server) string {
				return "unix:///tmp/test.sock"
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s := tt.server(t)
			assert.Equal(t, tt.expected(s), s.ListenURL())
		})
	}
}

// TestServerBuilderExtensionPoints exercises WithMiddleware and WithRoute so
// they remain reachable to deadcode analysis. Both methods form the public
// surface for ApplyServerExtensions consumers, whose callers may live in
// downstream repositories that this module's analyzer cannot see. Without
// this test, a future deadcode pass would flag them as unreachable (as
// happened in #5355) even though external callers depend on them.
func TestSecurityHeaders(t *testing.T) {
	t.Parallel()

	b := NewServerBuilder()
	router, err := b.Build(context.Background())
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	assert.Equal(t, "nosniff", rec.Header().Get("X-Content-Type-Options"))
	assert.Equal(t, "same-origin", rec.Header().Get("Cross-Origin-Resource-Policy"))
}

func TestServerBuilderExtensionPoints(t *testing.T) {
	t.Parallel()

	t.Run("WithMiddleware appends to middleware chain", func(t *testing.T) {
		t.Parallel()

		b := NewServerBuilder()
		mw := func(next http.Handler) http.Handler { return next }
		b.WithMiddleware(mw, mw)

		assert.Len(t, b.middlewares, 2)
	})

	t.Run("WithRoute registers handler at prefix", func(t *testing.T) {
		t.Parallel()

		b := NewServerBuilder()
		b.WithRoute("/ext", chi.NewRouter())

		_, ok := b.customRoutes["/ext"]
		assert.True(t, ok, "expected /ext to be registered")
	})

	t.Run("methods chain on the builder", func(t *testing.T) {
		t.Parallel()

		b := NewServerBuilder().
			WithMiddleware(func(next http.Handler) http.Handler { return next }).
			WithRoute("/ext", chi.NewRouter())

		assert.NotNil(t, b)
	})
}

// TestNewServer_ReadTimeoutConfigured verifies the management API http.Server is
// created with ReadTimeout set (bounding slow uploads) and WriteTimeout left
// unset, since the workload router serves multi-minute responses (image pulls).
func TestNewServer_ReadTimeoutConfigured(t *testing.T) {
	t.Parallel()

	// Inject mock skill and plugin managers so Build() skips creating the default
	// SQLite stores, which share a DB file on disk and race under parallel tests
	// (SQLITE_BUSY).
	ctrl := gomock.NewController(t)
	b := NewServerBuilder().WithAddress("127.0.0.1:0")
	b.skillManager = skillsmocks.NewMockSkillService(ctrl)
	b.pluginManager = pluginsmocks.NewMockPluginService(ctrl)

	s, err := NewServer(context.Background(), b)
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.listener.Close() })

	require.NotNil(t, s.httpServer)
	assert.Equal(t, readTimeout, s.httpServer.ReadTimeout)
	assert.Zero(t, s.httpServer.WriteTimeout)
}

func TestPluginHitsFromRegistry(t *testing.T) {
	t.Parallel()

	skillPkg := func(identifier, registryType, digest string) regtypes.SkillPackage {
		return regtypes.SkillPackage{Identifier: identifier, RegistryType: registryType, Digest: digest}
	}

	tests := []struct {
		name   string
		input  []regtypes.Plugin
		assert func(t *testing.T, hits []pluginsvc.PluginSearchHit)
	}{
		{
			name: "single plugin with one oci package",
			input: []regtypes.Plugin{
				{
					Name:        "code-reviewer",
					Namespace:   "io.github.user",
					Version:     "1.0.0",
					Description: "Reviews code for bugs",
					Packages: []regtypes.SkillPackage{
						skillPkg("ghcr.io/org/code-reviewer:v1", "oci", "sha256:abc"),
					},
				},
			},
			assert: func(t *testing.T, hits []pluginsvc.PluginSearchHit) {
				t.Helper()
				require.Len(t, hits, 1)
				assert.Equal(t, "code-reviewer", hits[0].Name)
				assert.Equal(t, "io.github.user", hits[0].Namespace)
				assert.Equal(t, "1.0.0", hits[0].Version)
				assert.Equal(t, "Reviews code for bugs", hits[0].Description)
				require.Len(t, hits[0].Packages, 1)
				assert.Equal(t, "ghcr.io/org/code-reviewer:v1", hits[0].Packages[0].Reference)
				assert.Equal(t, "oci", hits[0].Packages[0].Type)
				assert.Equal(t, "sha256:abc", hits[0].Packages[0].Digest)
			},
		},
		{
			name: "multiple packages (oci + git)",
			input: []regtypes.Plugin{
				{
					Name:        "multi-pkg",
					Namespace:   "io.github.acme",
					Version:     "2.1.0",
					Description: "Has multiple packages",
					Packages: []regtypes.SkillPackage{
						skillPkg("ghcr.io/org/multi:latest", "oci", "sha256:def"),
						skillPkg("https://github.com/org/repo.git", "git", ""),
					},
				},
			},
			assert: func(t *testing.T, hits []pluginsvc.PluginSearchHit) {
				t.Helper()
				require.Len(t, hits, 1)
				assert.Equal(t, "io.github.acme", hits[0].Namespace)
				assert.Equal(t, "2.1.0", hits[0].Version)
				require.Len(t, hits[0].Packages, 2)
				assert.Equal(t, "ghcr.io/org/multi:latest", hits[0].Packages[0].Reference)
				assert.Equal(t, "oci", hits[0].Packages[0].Type)
				assert.Equal(t, "sha256:def", hits[0].Packages[0].Digest)
				assert.Equal(t, "https://github.com/org/repo.git", hits[0].Packages[1].Reference)
				assert.Equal(t, "git", hits[0].Packages[1].Type)
				assert.Empty(t, hits[0].Packages[1].Digest)
			},
		},
		{
			name: "plugin with zero packages",
			input: []regtypes.Plugin{
				{Name: "no-pkgs", Namespace: "io.github.bare", Version: "0.1.0", Description: "No packages"},
			},
			assert: func(t *testing.T, hits []pluginsvc.PluginSearchHit) {
				t.Helper()
				require.Len(t, hits, 1)
				assert.Equal(t, "no-pkgs", hits[0].Name)
				assert.Equal(t, "io.github.bare", hits[0].Namespace)
				assert.Equal(t, "0.1.0", hits[0].Version)
				assert.NotNil(t, hits[0].Packages)
				assert.Empty(t, hits[0].Packages)
			},
		},
		{
			name:  "empty input returns non-nil empty slice",
			input: []regtypes.Plugin{},
			assert: func(t *testing.T, hits []pluginsvc.PluginSearchHit) {
				t.Helper()
				assert.NotNil(t, hits)
				assert.Empty(t, hits)
			},
		},
		{
			name: "Description empty maps to empty string",
			input: []regtypes.Plugin{
				{Name: "bare-plugin"},
			},
			assert: func(t *testing.T, hits []pluginsvc.PluginSearchHit) {
				t.Helper()
				require.Len(t, hits, 1)
				assert.Equal(t, "bare-plugin", hits[0].Name)
				assert.Empty(t, hits[0].Description)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			hits := pluginHitsFromRegistry(tt.input)
			tt.assert(t, hits)
		})
	}
}

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

	pluginsmocks "github.com/stacklok/toolhive/pkg/plugins/mocks"
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

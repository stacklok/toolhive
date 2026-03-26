// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package api

import (
	"fmt"
	"net"
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGenerateNonce(t *testing.T) {
	t.Parallel()

	t.Run("returns valid 32-char hex string", func(t *testing.T) {
		t.Parallel()

		nonce, err := generateNonce()
		require.NoError(t, err)

		assert.Len(t, nonce, 32)
		assert.Regexp(t, regexp.MustCompile(`^[0-9a-f]{32}$`), nonce)
	})

	t.Run("returns unique values on successive calls", func(t *testing.T) {
		t.Parallel()

		nonce1, err := generateNonce()
		require.NoError(t, err)

		nonce2, err := generateNonce()
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

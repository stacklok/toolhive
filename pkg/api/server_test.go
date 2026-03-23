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

func TestListenURL_TCP(t *testing.T) {
	t.Parallel()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer listener.Close()

	s := &Server{
		listener:     listener,
		isUnixSocket: false,
		address:      "127.0.0.1:0",
	}

	got := s.ListenURL()
	expected := fmt.Sprintf("http://%s", listener.Addr().String())
	assert.Equal(t, expected, got)
}

func TestListenURL_UnixSocket(t *testing.T) {
	t.Parallel()

	const sockPath = "/tmp/test.sock"
	s := &Server{
		isUnixSocket: true,
		address:      sockPath,
	}

	got := s.ListenURL()
	assert.Equal(t, "unix:///tmp/test.sock", got)
}

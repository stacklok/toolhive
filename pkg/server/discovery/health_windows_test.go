// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

//go:build windows

package discovery

import (
	"context"
	"fmt"
	"net/http"
	"sync/atomic"
	"testing"

	"github.com/Microsoft/go-winio"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// pipeNameSeq disambiguates concurrent test pipes so parallel runs don't
// collide on the global pipe namespace.
var pipeNameSeq atomic.Uint64

func TestCheckHealth_NamedPipe_Success(t *testing.T) {
	t.Parallel()

	pipeName := fmt.Sprintf("thv-test-%d", pipeNameSeq.Add(1))
	pipePath := `\\.\pipe\` + pipeName

	listener, err := winio.ListenPipe(pipePath, &winio.PipeConfig{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = listener.Close() })

	expectedNonce := "pipe-nonce"
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set(NonceHeader, expectedNonce)
		w.WriteHeader(http.StatusNoContent)
	})
	srv := &http.Server{Handler: mux} //nolint:gosec // test server, ReadHeaderTimeout not relevant
	go func() { _ = srv.Serve(listener) }()
	t.Cleanup(func() { _ = srv.Close() })

	err = CheckHealth(context.Background(), "npipe://"+pipeName, expectedNonce)
	require.NoError(t, err)
}

func TestCheckHealth_NamedPipe_NotFound(t *testing.T) {
	t.Parallel()
	err := CheckHealth(context.Background(), "npipe://nonexistent-pipe-thv-test", "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "health check failed")
}

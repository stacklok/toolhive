// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package discovery

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDiscover_NotFound(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	result, err := discover(context.Background(), dir)
	require.NoError(t, err)
	assert.Equal(t, StateNotFound, result.State)
	assert.Nil(t, result.Info)
}

func TestDiscover_Running(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	nonce := "running-nonce"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set(NonceHeader, nonce)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	info := &ServerInfo{
		URL:       srv.URL,
		PID:       os.Getpid(),
		Nonce:     nonce,
		StartedAt: time.Now().UTC(),
	}
	require.NoError(t, writeServerInfoTo(dir, info))

	result, err := discover(context.Background(), dir)
	require.NoError(t, err)
	assert.Equal(t, StateRunning, result.State)
	assert.Equal(t, nonce, result.Info.Nonce)
}

func TestDiscover_Stale_DeadProcess(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	info := &ServerInfo{
		URL:       "http://127.0.0.1:1",
		PID:       999999999,
		Nonce:     "stale-nonce",
		StartedAt: time.Now().UTC(),
	}
	require.NoError(t, writeServerInfoTo(dir, info))

	result, err := discover(context.Background(), dir)
	require.NoError(t, err)
	assert.Equal(t, StateStale, result.State)
	assert.NotNil(t, result.Info)
}

func TestDiscover_Unhealthy_AliveButNotResponding(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	// Server that returns 503 (unhealthy) — process is alive (our own PID)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	info := &ServerInfo{
		URL:       srv.URL,
		PID:       os.Getpid(),
		Nonce:     "unhealthy-nonce",
		StartedAt: time.Now().UTC(),
	}
	require.NoError(t, writeServerInfoTo(dir, info))

	result, err := discover(context.Background(), dir)
	require.NoError(t, err)
	assert.Equal(t, StateUnhealthy, result.State)
	assert.NotNil(t, result.Info)
}

func TestDiscover_NonceMismatch_TreatedAsUnhealthy(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	// Server returns wrong nonce — simulates PID reuse scenario
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set(NonceHeader, "different-server-nonce")
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	info := &ServerInfo{
		URL:       srv.URL,
		PID:       os.Getpid(),
		Nonce:     "original-nonce",
		StartedAt: time.Now().UTC(),
	}
	require.NoError(t, writeServerInfoTo(dir, info))

	result, err := discover(context.Background(), dir)
	require.NoError(t, err)
	// Nonce mismatch means health check fails, but process is alive
	assert.Equal(t, StateUnhealthy, result.State)
}

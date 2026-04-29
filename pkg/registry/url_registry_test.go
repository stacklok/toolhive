// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package registry

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewURLRegistry(t *testing.T) {
	t.Parallel()

	t.Run("loads upstream-format response", func(t *testing.T) {
		t.Parallel()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, upstreamWithServersAndSkills)
		}))
		t.Cleanup(srv.Close)

		r, err := NewURLRegistry(context.Background(), "remote", srv.URL, srv.Client())
		require.NoError(t, err)
		assert.Equal(t, "remote", r.Name())

		entries, err := r.List(Filter{})
		require.NoError(t, err)
		assert.Len(t, entries, 4)
	})

	t.Run("rejects empty url", func(t *testing.T) {
		t.Parallel()
		_, err := NewURLRegistry(context.Background(), "remote", "", nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "url must not be empty")
	})

	t.Run("returns UnavailableError on non-200", func(t *testing.T) {
		t.Parallel()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusServiceUnavailable)
		}))
		t.Cleanup(srv.Close)

		_, err := NewURLRegistry(context.Background(), "remote", srv.URL, srv.Client())
		require.Error(t, err)
		var ue *UnavailableError
		require.True(t, errors.As(err, &ue), "want UnavailableError, got %v", err)
		assert.Equal(t, srv.URL, ue.URL)
	})

	t.Run("returns UnavailableError on connection error", func(t *testing.T) {
		t.Parallel()
		// Server that closes connections immediately.
		srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {}))
		srv.Close() // start, then close, so URL is unreachable

		_, err := NewURLRegistry(context.Background(), "remote", srv.URL, srv.Client())
		require.Error(t, err)
		var ue *UnavailableError
		assert.True(t, errors.As(err, &ue))
	})

	t.Run("rejects oversized response", func(t *testing.T) {
		t.Parallel()
		// Build a response just over the size limit. Has to be valid JSON
		// up to size limit so the size check, not the parser, fires.
		big := strings.Repeat("x", defaultURLFetchSizeLimit+10)
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			fmt.Fprint(w, big)
		}))
		t.Cleanup(srv.Close)

		_, err := NewURLRegistry(context.Background(), "remote", srv.URL, srv.Client())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "size limit")
	})

	t.Run("uses default client when nil passed", func(t *testing.T) {
		t.Parallel()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			fmt.Fprint(w, upstreamEmpty)
		}))
		t.Cleanup(srv.Close)

		r, err := NewURLRegistry(context.Background(), "remote", srv.URL, nil)
		require.NoError(t, err)
		assert.Equal(t, "remote", r.Name())
	})

	t.Run("returns ErrLegacyFormat for legacy input", func(t *testing.T) {
		t.Parallel()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			fmt.Fprint(w, legacyFormat)
		}))
		t.Cleanup(srv.Close)

		_, err := NewURLRegistry(context.Background(), "remote", srv.URL, srv.Client())
		require.Error(t, err)
		assert.True(t, errors.Is(err, ErrLegacyFormat))
	})

	t.Run("respects context cancellation", func(t *testing.T) {
		t.Parallel()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			fmt.Fprint(w, upstreamEmpty)
		}))
		t.Cleanup(srv.Close)

		ctx, cancel := context.WithCancel(context.Background())
		cancel() // already cancelled

		_, err := NewURLRegistry(ctx, "remote", srv.URL, srv.Client())
		require.Error(t, err)
	})
}

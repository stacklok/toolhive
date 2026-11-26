// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package useragent

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/usagemetrics"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

type closeTracker struct {
	http.RoundTripper
	closed bool
}

func (c *closeTracker) CloseIdleConnections() { c.closed = true }

// sentUserAgent sends req through the round tripper and returns the
// User-Agent the base transport received.
func sentUserAgent(t *testing.T, req *http.Request) string {
	t.Helper()
	var got string
	rt := NewRoundTripper(roundTripFunc(func(r *http.Request) (*http.Response, error) {
		got = r.Header.Get("User-Agent")
		return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody}, nil
	}))
	resp, err := rt.RoundTrip(req)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	return got
}

func TestRoundTripper(t *testing.T) {
	t.Parallel()

	t.Run("sets the vMCP User-Agent when none is set", func(t *testing.T) {
		t.Parallel()
		req, err := http.NewRequest(http.MethodPost, "http://backend.example/mcp", http.NoBody)
		require.NoError(t, err)

		assert.Equal(t, Value(), sentUserAgent(t, req))
		assert.Empty(t, req.Header.Get("User-Agent"), "caller's request must not be mutated")
	})

	t.Run("keeps a User-Agent already on the request", func(t *testing.T) {
		t.Parallel()
		req, err := http.NewRequest(http.MethodPost, "http://backend.example/mcp", http.NoBody)
		require.NoError(t, err)
		req.Header.Set("User-Agent", "claude-code/2.0")

		assert.Equal(t, "claude-code/2.0", sentUserAgent(t, req))
	})

	t.Run("sets the header on a request with nil headers", func(t *testing.T) {
		t.Parallel()
		req, err := http.NewRequest(http.MethodPost, "http://backend.example/mcp", http.NoBody)
		require.NoError(t, err)
		req.Header = nil

		assert.Equal(t, Value(), sentUserAgent(t, req))
	})

	t.Run("forwards CloseIdleConnections", func(t *testing.T) {
		t.Parallel()
		base := &closeTracker{}
		NewRoundTripper(base).CloseIdleConnections()
		assert.True(t, base.closed)
	})
}

// TestValue_AttributedToVMCP pins the contract with pkg/usagemetrics: a
// backend proxy attributes a sessionless call carrying this User-Agent to vmcp.
func TestValue_AttributedToVMCP(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "vmcp", usagemetrics.NormalizeClientName(Value()))
}

// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"context"
	"testing"

	"github.com/mark3labs/mcp-go/server"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/vmcp/headerforward"
)

// TestInjectCapturedHeaders exercises all three branches of injectCapturedHeaders:
// a map hit, a map miss, and a wrong-type entry (type-assertion fallback).
func TestInjectCapturedHeaders(t *testing.T) {
	t.Parallel()

	t.Run("hit: stored headers are injected into ctx", func(t *testing.T) {
		t.Parallel()
		s := &Server{}
		s.capturedPassthroughHeaders.Store("s1", map[string]string{"X-Api-Key": "key-123"})
		ctx := s.injectCapturedHeaders(context.Background(), "s1")
		got := headerforward.ForwardedHeadersFromContext(ctx)
		assert.Equal(t, map[string]string{"X-Api-Key": "key-123"}, got,
			"stored headers must be injected into the context")
	})

	t.Run("miss: absent session ID leaves ctx unchanged", func(t *testing.T) {
		t.Parallel()
		s := &Server{}
		ctx := s.injectCapturedHeaders(context.Background(), "missing")
		assert.Nil(t, headerforward.ForwardedHeadersFromContext(ctx),
			"a missing session ID must leave the context's forwarded headers unchanged")
	})

	t.Run("type fallback: wrong type stored silently degrades to no-op", func(t *testing.T) {
		t.Parallel()
		s := &Server{}
		s.capturedPassthroughHeaders.Store("s2", "not-a-map")
		ctx := s.injectCapturedHeaders(context.Background(), "s2")
		assert.Nil(t, headerforward.ForwardedHeadersFromContext(ctx),
			"a non-map entry must not be injected (type-assertion fallback)")
	})
}

// stubClientSession is a minimal server.ClientSession that returns a fixed ID.
// Used to exercise the OnUnregisterSession hook closure without running a full server.
type stubClientSession struct {
	server.ClientSession // embed for unused interface methods
	id                   string
}

func (s *stubClientSession) SessionID() string { return s.id }

// TestOnUnregisterSessionHookDeletesCapturedHeaders verifies that the closure
// registered with AddOnUnregisterSession in serve.go deletes the session's entry
// from capturedPassthroughHeaders. The test constructs a minimal *Server with a
// pre-populated entry and fires the hook closure directly, avoiding the need to
// bring up an HTTP server.
func TestOnUnregisterSessionHookDeletesCapturedHeaders(t *testing.T) {
	t.Parallel()

	srv, err := Serve(context.Background(), &stubVMCP{}, testMinimalServeConfig())
	require.NoError(t, err)
	t.Cleanup(func() { _ = srv.Stop(context.Background()) })

	const sessionID = "test-session-hook"
	srv.capturedPassthroughHeaders.Store(sessionID, map[string]string{"X-Key": "v"})

	// Confirm the entry is present before firing.
	_, ok := srv.capturedPassthroughHeaders.Load(sessionID)
	require.True(t, ok, "entry must be present before hook fires")

	// Fire the hook by calling UnregisterSession on the mcp-go hooks object.
	// The hooks object is embedded in mcpServer; UnregisterSession triggers all
	// AddOnUnregisterSession callbacks registered during Serve, including our cleanup.
	srv.mcpServer.GetHooks().UnregisterSession(context.Background(), &stubClientSession{id: sessionID})

	// After the hook fires the entry must be gone.
	_, stillPresent := srv.capturedPassthroughHeaders.Load(sessionID)
	assert.False(t, stillPresent,
		"capturedPassthroughHeaders entry must be deleted by OnUnregisterSession hook")
}

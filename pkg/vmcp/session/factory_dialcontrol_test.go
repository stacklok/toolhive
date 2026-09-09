// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package session

import (
	"context"
	"errors"
	"syscall"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/vmcp"
)

// denyAllResolver returns a WithDialControlResolver argument that resolves a
// deny-all hook for every workload, standing in for an embedder's per-backend
// SSRF/private-address guard that happens to block all of this test's backends.
func denyAllResolver() func(workloadID string) func(network, address string, c syscall.RawConn) error {
	return func(_ string) func(network, address string, c syscall.RawConn) error {
		return func(_, _ string, _ syscall.RawConn) error {
			return errors.New("dial blocked by test policy")
		}
	}
}

// TestSessionFactory_WithDialControlResolver_GuardsSessionInit proves the option
// is threaded from the public NewSessionFactory API down to the per-backend
// session-init dial: with a deny-all hook the real backend is never reached, so
// the (partial-failure) session is created but holds no capabilities. The
// unguarded TestSessionFactory_Integration_CapabilityDiscovery shows the same
// backend IS reached and discovered without the option.
func TestSessionFactory_WithDialControlResolver_GuardsSessionInit(t *testing.T) {
	t.Parallel()

	baseURL := startInProcessMCPServer(t)
	backend := &vmcp.Backend{
		ID:            "guarded-backend",
		Name:          "guarded-backend",
		BaseURL:       baseURL,
		TransportType: "streamable-http",
	}

	factory := NewSessionFactory(newUnauthenticatedRegistry(t), WithDialControlResolver(denyAllResolver()))

	// A blocked backend is a partial failure: the backend is excluded and the
	// session is still created, just with no held connection or capabilities.
	sess, err := factory.MakeSessionWithID(context.Background(), uuid.New().String(), nil, []*vmcp.Backend{backend}, nil)
	require.NoError(t, err)
	require.NotNil(t, sess)
	t.Cleanup(func() { require.NoError(t, sess.Close()) })

	assert.Empty(t, sess.Tools(), "guarded session-init dial must not discover backend tools")
	assert.Empty(t, sess.Resources(), "guarded session-init dial must not discover backend resources")
	assert.Empty(t, sess.Prompts(), "guarded session-init dial must not discover backend prompts")
}

// TestSessionFactory_WithDialControlResolver_GuardsRestoreSession proves the
// option also covers the RestoreSession path (which has its own filtering and
// identity-binding restore logic), not just MakeSessionWithID. A guarded factory
// restoring from valid stored metadata must not reach the backend.
func TestSessionFactory_WithDialControlResolver_GuardsRestoreSession(t *testing.T) {
	t.Parallel()

	baseURL := startInProcessMCPServer(t)
	backend := &vmcp.Backend{
		ID:            "guarded-backend",
		Name:          "guarded-backend",
		BaseURL:       baseURL,
		TransportType: "streamable-http",
	}

	// Produce valid stored metadata (backend IDs + identity binding) from a real
	// unguarded session.
	seed := NewSessionFactory(newUnauthenticatedRegistry(t))
	orig, err := seed.MakeSessionWithID(context.Background(), uuid.New().String(), nil, []*vmcp.Backend{backend}, nil)
	require.NoError(t, err)
	require.NotEmpty(t, orig.Tools(), "sanity: the unguarded seed session must reach the backend")
	storedMeta := orig.GetMetadata()
	require.NoError(t, orig.Close())

	// A guarded factory restoring from that metadata must not reach the backend.
	guarded := NewSessionFactory(newUnauthenticatedRegistry(t), WithDialControlResolver(denyAllResolver()))
	restored, err := guarded.RestoreSession(context.Background(), uuid.New().String(), storedMeta, []*vmcp.Backend{backend})
	require.NoError(t, err)
	require.NotNil(t, restored)
	t.Cleanup(func() { require.NoError(t, restored.Close()) })

	assert.Empty(t, restored.Tools(), "guarded restore must not discover backend tools")
	assert.Empty(t, restored.BackendSessions(), "guarded restore must hold no backend connection")
}

// TestSessionFactory_WithDialControlResolver_AppliesPerBackend proves the hook is
// resolved per-backend within a single session: with two backends and a resolver
// that returns a deny-all hook for one workload ID and nil for the other, the
// allowed backend connects and the blocked one is excluded. This is the
// production scenario the resolver exists for — a per-backend dial policy the
// single address-blind hook could not express.
func TestSessionFactory_WithDialControlResolver_AppliesPerBackend(t *testing.T) {
	t.Parallel()

	allowedURL := startInProcessMCPServer(t)
	blockedURL := startInProcessMCPServer(t)

	allowed := &vmcp.Backend{
		ID: "allowed-backend", Name: "allowed-backend", BaseURL: allowedURL, TransportType: "streamable-http",
	}
	blocked := &vmcp.Backend{
		ID: "blocked-backend", Name: "blocked-backend", BaseURL: blockedURL, TransportType: "streamable-http",
	}

	factory := NewSessionFactory(newUnauthenticatedRegistry(t),
		WithDialControlResolver(func(workloadID string) func(network, address string, c syscall.RawConn) error {
			if workloadID != blocked.ID {
				return nil
			}
			return func(_, _ string, _ syscall.RawConn) error {
				return errors.New("dial blocked by test policy")
			}
		}))

	sess, err := factory.MakeSessionWithID(
		context.Background(), uuid.New().String(), nil, []*vmcp.Backend{allowed, blocked}, nil)
	require.NoError(t, err)
	require.NotNil(t, sess)
	t.Cleanup(func() { require.NoError(t, sess.Close()) })

	conns := sess.BackendSessions()
	assert.Contains(t, conns, "allowed-backend", "the workload the resolver returns nil for must be connected")
	assert.NotContains(t, conns, "blocked-backend", "the workload with a deny-all hook must be excluded")
	assert.NotEmpty(t, sess.Tools(), "the allowed backend's capabilities must still be discovered")
}

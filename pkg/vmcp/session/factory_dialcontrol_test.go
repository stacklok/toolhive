// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package session

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

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

// TestSessionFactory_WithDialControlResolver_AppliesPerBackendOnRestore is the
// RestoreSession counterpart to _AppliesPerBackend. It proves the resolver is keyed
// on each backend's own workload ID during restore, not only under a blanket
// deny-all: a bug threading the wrong workload ID into the resolver on the restore
// path (e.g. always the first backend's) would still pass GuardsRestoreSession but
// fail here.
func TestSessionFactory_WithDialControlResolver_AppliesPerBackendOnRestore(t *testing.T) {
	t.Parallel()

	allowedURL := startInProcessMCPServer(t)
	blockedURL := startInProcessMCPServer(t)
	allowed := &vmcp.Backend{
		ID: "allowed-backend", Name: "allowed-backend", BaseURL: allowedURL, TransportType: "streamable-http",
	}
	blocked := &vmcp.Backend{
		ID: "blocked-backend", Name: "blocked-backend", BaseURL: blockedURL, TransportType: "streamable-http",
	}
	backends := []*vmcp.Backend{allowed, blocked}

	// Seed valid stored metadata (backend IDs + identity binding) from a real
	// unguarded session over both backends.
	seed := NewSessionFactory(newUnauthenticatedRegistry(t))
	orig, err := seed.MakeSessionWithID(context.Background(), uuid.New().String(), nil, backends, nil)
	require.NoError(t, err)
	require.NotEmpty(t, orig.Tools(), "sanity: the unguarded seed session must reach both backends")
	storedMeta := orig.GetMetadata()
	require.NoError(t, orig.Close())

	// Restore with a resolver that denies only the blocked workload ID.
	guarded := NewSessionFactory(newUnauthenticatedRegistry(t),
		WithDialControlResolver(func(workloadID string) func(network, address string, c syscall.RawConn) error {
			if workloadID != blocked.ID {
				return nil
			}
			return func(_, _ string, _ syscall.RawConn) error {
				return errors.New("dial blocked by test policy")
			}
		}))
	restored, err := guarded.RestoreSession(context.Background(), uuid.New().String(), storedMeta, backends)
	require.NoError(t, err)
	require.NotNil(t, restored)
	t.Cleanup(func() { require.NoError(t, restored.Close()) })

	conns := restored.BackendSessions()
	assert.Contains(t, conns, "allowed-backend", "the workload the resolver returns nil for must survive restore")
	assert.NotContains(t, conns, "blocked-backend", "the workload the resolver denies must be excluded on restore")
}

// TestSessionFactory_WithDialControlResolver_IsolatesPanickingResolver proves an
// embedder resolver that panics for one workload is isolated to that backend: the
// backend is excluded like any other init failure and the session is still created
// with the surviving backend, rather than the panic crashing the per-backend init
// goroutine (and the process).
func TestSessionFactory_WithDialControlResolver_IsolatesPanickingResolver(t *testing.T) {
	t.Parallel()

	allowedURL := startInProcessMCPServer(t)
	panicURL := startInProcessMCPServer(t)
	allowed := &vmcp.Backend{
		ID: "allowed-backend", Name: "allowed-backend", BaseURL: allowedURL, TransportType: "streamable-http",
	}
	panicky := &vmcp.Backend{
		ID: "panic-backend", Name: "panic-backend", BaseURL: panicURL, TransportType: "streamable-http",
	}

	factory := NewSessionFactory(newUnauthenticatedRegistry(t),
		WithDialControlResolver(func(workloadID string) func(network, address string, c syscall.RawConn) error {
			if workloadID == panicky.ID {
				panic("resolver boom for " + workloadID)
			}
			return nil
		}))

	sess, err := factory.MakeSessionWithID(
		context.Background(), uuid.New().String(), nil, []*vmcp.Backend{allowed, panicky}, nil)
	require.NoError(t, err, "a resolver panic for one backend must not fail whole-session creation")
	require.NotNil(t, sess)
	t.Cleanup(func() { require.NoError(t, sess.Close()) })

	conns := sess.BackendSessions()
	assert.Contains(t, conns, "allowed-backend", "the backend whose resolver did not panic must connect")
	assert.NotContains(t, conns, "panic-backend", "the backend whose resolver panicked must be excluded")
}

// TestSessionFactory_WithDialControlResolver_InvokedConcurrently pins the documented
// contract that the resolver is called from the per-backend init goroutines
// concurrently (up to maxConcurrency). A barrier proves it deterministically: every
// invocation announces arrival and then blocks until all backends' resolvers have
// arrived, which can only complete if the invocations overlap in time — a serial
// caller would leave the first invocation waiting and the test would hit its timeout.
// Run under -race, the shared arrival state also exercises the safe-for-concurrent-use
// claim.
func TestSessionFactory_WithDialControlResolver_InvokedConcurrently(t *testing.T) {
	t.Parallel()

	const n = 3
	backends := make([]*vmcp.Backend, n)
	for i := range backends {
		url := startInProcessMCPServer(t)
		id := fmt.Sprintf("backend-%d", i)
		backends[i] = &vmcp.Backend{ID: id, Name: id, BaseURL: url, TransportType: "streamable-http"}
	}

	var arrived sync.WaitGroup
	arrived.Add(n)
	release := make(chan struct{})
	var inFlight, maxObserved atomic.Int32

	factory := NewSessionFactory(newUnauthenticatedRegistry(t),
		WithDialControlResolver(func(_ string) func(network, address string, c syscall.RawConn) error {
			cur := inFlight.Add(1)
			for {
				old := maxObserved.Load()
				if cur <= old || maxObserved.CompareAndSwap(old, cur) {
					break
				}
			}
			arrived.Done()
			// Block until every backend's resolver has also arrived — only reachable
			// if the invocations run concurrently.
			select {
			case <-release:
			case <-time.After(5 * time.Second):
			}
			inFlight.Add(-1)
			return nil
		}))

	// Release the barrier once all n resolvers are concurrently in flight.
	barrierMet := make(chan struct{})
	go func() {
		arrived.Wait()
		close(release)
		close(barrierMet)
	}()

	sess, err := factory.MakeSessionWithID(context.Background(), uuid.New().String(), nil, backends, nil)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sess.Close()) })

	select {
	case <-barrierMet:
	case <-time.After(10 * time.Second):
		t.Fatal("resolvers were not invoked concurrently: barrier never reached n arrivals")
	}
	assert.GreaterOrEqual(t, int(maxObserved.Load()), 2,
		"the resolver must be invoked concurrently for multiple backends")
}

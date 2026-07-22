// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package sessionmanager

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
	corev1 "k8s.io/api/core/v1"

	transportsession "github.com/stacklok/toolhive/pkg/transport/session"
	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/session/binding"
	sessionfactorymocks "github.com/stacklok/toolhive/pkg/vmcp/session/mocks"
	sessiontypes "github.com/stacklok/toolhive/pkg/vmcp/session/types"
	sessionmocks "github.com/stacklok/toolhive/pkg/vmcp/session/types/mocks"
	"github.com/stacklok/toolhive/pkg/vmcp/session/untrusted"
)

// fakeLifecycle records DeletePod calls.
type fakeLifecycle struct {
	deleted []string
}

func (*fakeLifecycle) EnsurePod(_ context.Context, _ untrusted.EnsurePodRequest) (*corev1.Pod, error) {
	return nil, nil
}
func (*fakeLifecycle) WaitReady(_ context.Context, _ *corev1.Pod, _ time.Duration) error {
	return nil
}
func (f *fakeLifecycle) DeletePod(_ context.Context, name string) error {
	f.deleted = append(f.deleted, name)
	return nil
}

//nolint:unparam // uid kept as a parameter for readability; callers consistently use "uid-1"
func untrustedBackendMeta(uid string) map[string]string {
	return map[string]string{
		untrusted.MetadataKeyUntrusted:    "true",
		untrusted.MetadataKeyMCPServerUID: uid,
	}
}

func TestRestoreBudgetFor(t *testing.T) {
	t.Parallel()

	newManager := func(t *testing.T, untrustedCfg *UntrustedConfig, backends ...vmcp.Backend) *Manager {
		t.Helper()
		ctrl := gomock.NewController(t)
		sess := newMockSession(t, ctrl, "s", nil)
		factory := sessionfactorymocks.NewMockMultiSessionFactory(ctrl)
		factory.EXPECT().MakeSessionWithID(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
			Return(sess, nil).AnyTimes()
		registry := &fakeBackendRegistry{backends: backends}
		storage := newTestSessionDataStorage(t)
		sm, cleanup, err := New(storage, &FactoryConfig{Base: factory, CacheCapacity: 100, Untrusted: untrustedCfg}, registry)
		require.NoError(t, err)
		t.Cleanup(func() { _ = cleanup(context.Background()) })
		return sm
	}

	metadata := func(ids string) map[string]string {
		return map[string]string{"vmcp.backend.ids": ids}
	}

	t.Run("untrusted backend in restored set selects 60s budget", func(t *testing.T) {
		t.Parallel()
		sm := newManager(t, &UntrustedConfig{}, vmcp.Backend{ID: "b1", Metadata: untrustedBackendMeta("uid-1")})
		assert.Equal(t, restoreSessionTimeoutUntrusted, sm.restoreBudgetFor(metadata("b1"), sm.listAllBackends(context.Background())))
	})

	t.Run("trusted-only set keeps 15s budget", func(t *testing.T) {
		t.Parallel()
		sm := newManager(t, &UntrustedConfig{}, vmcp.Backend{ID: "b1", Metadata: map[string]string{}})
		assert.Equal(t, restoreSessionTimeout, sm.restoreBudgetFor(metadata("b1"), sm.listAllBackends(context.Background())))
	})

	t.Run("untrusted config nil keeps 15s even with untrusted metadata", func(t *testing.T) {
		t.Parallel()
		sm := newManager(t, nil, vmcp.Backend{ID: "b1", Metadata: untrustedBackendMeta("uid-1")})
		assert.Equal(t, restoreSessionTimeout, sm.restoreBudgetFor(metadata("b1"), sm.listAllBackends(context.Background())))
	})

	t.Run("untrusted backend not in restored set keeps 15s", func(t *testing.T) {
		t.Parallel()
		sm := newManager(t, &UntrustedConfig{},
			vmcp.Backend{ID: "b1", Metadata: map[string]string{}},
			vmcp.Backend{ID: "b2", Metadata: untrustedBackendMeta("uid-1")})
		assert.Equal(t, restoreSessionTimeout, sm.restoreBudgetFor(metadata("b1"), sm.listAllBackends(context.Background())))
	})
}

func TestTerminateDeletesUntrustedSessionPods(t *testing.T) {
	t.Parallel()

	bound, err := binding.Format("https://iss", "sub-1")
	require.NoError(t, err)

	setup := func(t *testing.T, lifecycle *fakeLifecycle, backends ...vmcp.Backend) (*Manager, transportsession.DataStorage) {
		t.Helper()
		ctrl := gomock.NewController(t)
		sess := newMockSession(t, ctrl, "sess", nil)
		factory := newMockFactory(t, ctrl, sess)
		registry := &fakeBackendRegistry{backends: backends}
		storage := newTestSessionDataStorage(t)
		sm, cleanup, err := New(storage, &FactoryConfig{
			Base:          factory,
			CacheCapacity: 100,
			Untrusted:     &UntrustedConfig{Lifecycle: lifecycle},
		}, registry)
		require.NoError(t, err)
		t.Cleanup(func() { _ = cleanup(context.Background()) })
		return sm, storage
	}

	t.Run("full session terminate deletes deterministic pod names", func(t *testing.T) {
		t.Parallel()
		lifecycle := &fakeLifecycle{}
		backend := vmcp.Backend{ID: "b1", Metadata: untrustedBackendMeta("uid-1")}
		sm, storage := setup(t, lifecycle, backend)

		sessionID := sm.Generate()
		// Upgrade to full session metadata: identity binding + backend IDs.
		_, err := storage.Update(context.Background(), sessionID, map[string]string{
			sessiontypes.MetadataKeyIdentityBinding: bound,
			"vmcp.backend.ids":                      "b1",
		})
		require.NoError(t, err)

		isNotAllowed, err := sm.Terminate(sessionID)
		require.NoError(t, err)
		assert.False(t, isNotAllowed)

		userKey, err := binding.Format("https://iss", "sub-1")
		require.NoError(t, err)
		want := untrusted.PodNameFor("uid-1", userKey, sessionID)
		assert.Equal(t, []string{want}, lifecycle.deleted)
	})

	t.Run("trusted backends produce no pod deletes", func(t *testing.T) {
		t.Parallel()
		lifecycle := &fakeLifecycle{}
		backend := vmcp.Backend{ID: "b1", Metadata: map[string]string{}}
		sm, storage := setup(t, lifecycle, backend)

		sessionID := sm.Generate()
		_, err := storage.Update(context.Background(), sessionID, map[string]string{
			sessiontypes.MetadataKeyIdentityBinding: bound,
			"vmcp.backend.ids":                      "b1",
		})
		require.NoError(t, err)

		_, err = sm.Terminate(sessionID)
		require.NoError(t, err)
		assert.Empty(t, lifecycle.deleted)
	})

	t.Run("anonymous session produces no pod deletes", func(t *testing.T) {
		t.Parallel()
		lifecycle := &fakeLifecycle{}
		backend := vmcp.Backend{ID: "b1", Metadata: untrustedBackendMeta("uid-1")}
		sm, storage := setup(t, lifecycle, backend)

		sessionID := sm.Generate()
		_, err := storage.Update(context.Background(), sessionID, map[string]string{
			sessiontypes.MetadataKeyIdentityBinding: binding.UnauthenticatedSentinel,
			"vmcp.backend.ids":                      "b1",
		})
		require.NoError(t, err)

		_, err = sm.Terminate(sessionID)
		require.NoError(t, err)
		assert.Empty(t, lifecycle.deleted)
	})
}

// TestEvictionDeletesUntrustedSessionPods pins the onEvict behavior: an
// LRU-evicted untrusted session must release its per-session pods through the
// lifecycle, same as Terminate (the reaper's idle TTL is the backstop, not
// the primary teardown).
func TestEvictionDeletesUntrustedSessionPods(t *testing.T) {
	t.Parallel()

	bound, err := binding.Format("https://iss", "sub-1")
	require.NoError(t, err)
	metadata := map[string]string{
		sessiontypes.MetadataKeyIdentityBinding: bound,
		"vmcp.backend.ids":                      "b1",
	}

	ctrl := gomock.NewController(t)
	lifecycle := &fakeLifecycle{}
	registry := &fakeBackendRegistry{backends: []vmcp.Backend{{ID: "b1", Metadata: untrustedBackendMeta("uid-1")}}}
	storage := newTestSessionDataStorage(t)

	// Sessions are inserted directly: only the LRU eviction path is under
	// test, not placeholder/storage ceremony. checkSession is bypassed (no
	// Get) so the sessions are never validated against storage.
	sessFor := func(id string) *sessionmocks.MockMultiSession {
		sess := sessionmocks.NewMockMultiSession(ctrl)
		sess.EXPECT().ID().Return(id).AnyTimes()
		// GetMetadata must return the session's full metadata: eviction reads
		// the identity binding + backend IDs from it. Copy per call — the map
		// is shared across two sessions here.
		sess.EXPECT().GetMetadata().DoAndReturn(func() map[string]string {
			m := make(map[string]string, len(metadata))
			for k, v := range metadata {
				m[k] = v
			}
			return m
		}).AnyTimes()
		sess.EXPECT().Close().Return(nil).AnyTimes()
		return sess
	}
	factory := sessionfactorymocks.NewMockMultiSessionFactory(ctrl)
	factory.EXPECT().MakeSessionWithID(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
		Return(sessFor("unused"), nil).AnyTimes()

	// Capacity 1: inserting the second session evicts the first (LRU).
	sm, cleanup, err := New(storage, &FactoryConfig{
		Base:          factory,
		CacheCapacity: 1,
		Untrusted:     &UntrustedConfig{Lifecycle: lifecycle},
	}, registry)
	require.NoError(t, err)
	t.Cleanup(func() { _ = cleanup(context.Background()) })

	first := sm.Generate()
	second := sm.Generate()

	sm.sessions.Set(first, sessFor(first))
	require.Empty(t, lifecycle.deleted, "no eviction before the cache is full")

	sm.sessions.Set(second, sessFor(second))

	userKey, err := binding.Format("https://iss", "sub-1")
	require.NoError(t, err)
	want := untrusted.PodNameFor("uid-1", userKey, first)
	require.Equal(t, []string{want}, lifecycle.deleted,
		"evicting the LRU session must delete its untrusted pods")
}

// TestSessionExists pins the storage-seam liveness probe the untrusted reaper
// consumes: live session → true; terminated placeholder, unknown session, or
// storage error → false (fail closed).
func TestSessionExists(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	setup := func(t *testing.T) (*Manager, transportsession.DataStorage) {
		t.Helper()
		ctrl := gomock.NewController(t)
		sess := newMockSession(t, ctrl, "sess", nil)
		sm, cleanup, err := New(newTestSessionDataStorage(t),
			&FactoryConfig{Base: newMockFactory(t, ctrl, sess), CacheCapacity: 10},
			&fakeBackendRegistry{})
		require.NoError(t, err)
		t.Cleanup(func() { _ = cleanup(context.Background()) })
		return sm, sm.storage
	}

	t.Run("live session exists", func(t *testing.T) {
		t.Parallel()
		sm, _ := setup(t)
		sessionID := sm.Generate()
		assert.True(t, sm.SessionExists(ctx, sessionID))
	})

	t.Run("terminated placeholder does not exist", func(t *testing.T) {
		t.Parallel()
		sm, storage := setup(t)
		sessionID := sm.Generate()
		_, err := storage.Update(ctx, sessionID, map[string]string{MetadataKeyTerminated: MetadataValTrue})
		require.NoError(t, err)
		assert.False(t, sm.SessionExists(ctx, sessionID))
	})

	t.Run("unknown session does not exist", func(t *testing.T) {
		t.Parallel()
		sm, _ := setup(t)
		assert.False(t, sm.SessionExists(ctx, "never-created"))
	})

	t.Run("empty session ID does not exist", func(t *testing.T) {
		t.Parallel()
		sm, _ := setup(t)
		assert.False(t, sm.SessionExists(ctx, ""))
	})

	t.Run("storage error fails closed", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		sess := newMockSession(t, ctrl, "sess", nil)
		sm, cleanup, err := New(alwaysFailDataStorage{},
			&FactoryConfig{Base: newMockFactory(t, ctrl, sess), CacheCapacity: 10},
			&fakeBackendRegistry{})
		require.NoError(t, err)
		t.Cleanup(func() { _ = cleanup(context.Background()) })
		assert.False(t, sm.SessionExists(ctx, "any"))
	})
}

// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package sessionmanager

import (
	"context"
	"errors"
	"maps"
	"strings"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/cache"
	transportsession "github.com/stacklok/toolhive/pkg/transport/session"
	"github.com/stacklok/toolhive/pkg/vmcp"
	vmcpsession "github.com/stacklok/toolhive/pkg/vmcp/session"
	sessionfactorymocks "github.com/stacklok/toolhive/pkg/vmcp/session/mocks"
	sessiontypes "github.com/stacklok/toolhive/pkg/vmcp/session/types"
	sessionmocks "github.com/stacklok/toolhive/pkg/vmcp/session/types/mocks"
)

// ---------------------------------------------------------------------------
// Test helpers / mocks
// ---------------------------------------------------------------------------

// newMockSession creates a MockMultiSession with AnyTimes expectations for all
// methods that tests don't explicitly care about. Methods that tests DO care
// about (Tools, Resources, CallTool, ReadResource) are left unconfigured so
// each test can set them up as needed.
func newMockSession(t *testing.T, ctrl *gomock.Controller, sessionID string, tools []vmcp.Tool) *sessionmocks.MockMultiSession {
	t.Helper()
	sess := sessionmocks.NewMockMultiSession(ctrl)

	// transportsession.Session methods — set up with AnyTimes for zero values
	sess.EXPECT().ID().Return(sessionID).AnyTimes()
	sess.EXPECT().Type().Return(transportsession.SessionType("")).AnyTimes()
	sess.EXPECT().CreatedAt().Return(time.Time{}).AnyTimes()
	sess.EXPECT().UpdatedAt().Return(time.Time{}).AnyTimes()
	sess.EXPECT().GetData().Return(nil).AnyTimes()
	sess.EXPECT().SetData(gomock.Any()).AnyTimes()
	sess.EXPECT().GetMetadata().Return(map[string]string{}).AnyTimes()
	sess.EXPECT().SetMetadata(gomock.Any(), gomock.Any()).AnyTimes()

	// MultiSession-specific methods that tests don't care about
	sess.EXPECT().BackendSessions().Return(nil).AnyTimes()
	sess.EXPECT().GetRoutingTable().Return(nil).AnyTimes()
	sess.EXPECT().Prompts().Return(nil).AnyTimes()

	// Tools — return the provided list by default
	sess.EXPECT().Tools().Return(tools).AnyTimes()

	return sess
}

// newMockFactory creates a MockMultiSessionFactory that returns the given session
// for every MakeSessionWithID call.
func newMockFactory(t *testing.T, ctrl *gomock.Controller, sess vmcpsession.MultiSession) *sessionfactorymocks.MockMultiSessionFactory {
	t.Helper()
	factory := sessionfactorymocks.NewMockMultiSessionFactory(ctrl)
	factory.EXPECT().
		MakeSessionWithID(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
		Return(sess, nil).AnyTimes()
	return factory
}

// newMockFactoryWithError creates a MockMultiSessionFactory that always returns an error.
func newMockFactoryWithError(t *testing.T, ctrl *gomock.Controller, err error) *sessionfactorymocks.MockMultiSessionFactory {
	t.Helper()
	factory := sessionfactorymocks.NewMockMultiSessionFactory(ctrl)
	factory.EXPECT().
		MakeSessionWithID(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
		Return(nil, err).AnyTimes()
	return factory
}

// alwaysFailDataStorage is a DataStorage whose Create/Update always return an
// error. It is used to exercise the Generate() double-failure path (UUID collision
// simulation — both attempts to Create fail, so Generate() must return "").
type alwaysFailDataStorage struct{}

func (alwaysFailDataStorage) Load(_ context.Context, _ string) (map[string]string, error) {
	return nil, transportsession.ErrSessionNotFound
}
func (alwaysFailDataStorage) Create(_ context.Context, _ string, _ map[string]string) (bool, error) {
	return false, errors.New("storage unavailable")
}
func (alwaysFailDataStorage) Update(_ context.Context, _ string, _ map[string]string) (bool, error) {
	return false, errors.New("storage unavailable")
}
func (alwaysFailDataStorage) Delete(_ context.Context, _ string) error { return nil }
func (alwaysFailDataStorage) Close() error                             { return nil }

// configurableFailDataStorage wraps a real SessionDataStorage and allows injecting
// failures for specific operations. Used to test fallback behavior in Terminate().
type configurableFailDataStorage struct {
	transportsession.DataStorage
	storeCallCount int
	failStoreAfter int // fail Create/Update after this many successful calls (0 = never fail, -1 = always fail)
	failDelete     bool
}

func (s *configurableFailDataStorage) shouldFail() bool {
	s.storeCallCount++
	return s.failStoreAfter == -1 || (s.failStoreAfter >= 0 && s.storeCallCount > s.failStoreAfter)
}

func (s *configurableFailDataStorage) Create(ctx context.Context, id string, metadata map[string]string) (bool, error) {
	if s.shouldFail() {
		return false, errors.New("injected Create failure")
	}
	return s.DataStorage.Create(ctx, id, metadata)
}

func (s *configurableFailDataStorage) Update(ctx context.Context, id string, metadata map[string]string) (bool, error) {
	if s.shouldFail() {
		return false, errors.New("injected Update failure")
	}
	return s.DataStorage.Update(ctx, id, metadata)
}

func (s *configurableFailDataStorage) Delete(ctx context.Context, id string) error {
	if s.failDelete {
		return errors.New("injected Delete failure")
	}
	return s.DataStorage.Delete(ctx, id)
}

// fakeBackendRegistry is a simple BackendRegistry for tests.
type fakeBackendRegistry struct {
	backends []vmcp.Backend
}

// newFakeRegistry creates a BackendRegistry with no backends.
// Tests that need backends should set the backends field directly.
func newFakeRegistry() *fakeBackendRegistry {
	return &fakeBackendRegistry{}
}

func (r *fakeBackendRegistry) Get(_ context.Context, id string) *vmcp.Backend {
	for i, b := range r.backends {
		if b.ID == id {
			return &r.backends[i]
		}
	}
	return nil
}

func (r *fakeBackendRegistry) List(_ context.Context) []vmcp.Backend {
	return r.backends
}

func (r *fakeBackendRegistry) Count() int {
	return len(r.backends)
}

// newTestSessionDataStorage creates a LocalSessionDataStorage with a long TTL.
// The storage is closed via t.Cleanup.
func newTestSessionDataStorage(t *testing.T) transportsession.DataStorage {
	t.Helper()
	storage, err := transportsession.NewLocalSessionDataStorage(30 * time.Minute)
	require.NoError(t, err)
	t.Cleanup(func() { _ = storage.Close() })
	return storage
}

// newTestSessionManager is a convenience constructor for tests.
func newTestSessionManager(
	t *testing.T,
	factory vmcpsession.MultiSessionFactory,
	registry vmcp.BackendRegistry,
) (*Manager, transportsession.DataStorage) {
	t.Helper()
	storage := newTestSessionDataStorage(t)
	sm, cleanup, err := New(storage, &FactoryConfig{Base: factory, CacheCapacity: 1000}, registry)
	require.NoError(t, err)
	t.Cleanup(func() { _ = cleanup(context.Background()) })
	return sm, storage
}

// ---------------------------------------------------------------------------
// Tests: Generate
// ---------------------------------------------------------------------------

func TestSessionManager_Generate(t *testing.T) {
	t.Parallel()

	t.Run("stores placeholder and returns valid UUID", func(t *testing.T) {
		t.Parallel()

		ctrl := gomock.NewController(t)
		sess := newMockSession(t, ctrl, "placeholder", nil)
		factory := newMockFactory(t, ctrl, sess)
		registry := newFakeRegistry()
		sm, storage := newTestSessionManager(t, factory, registry)

		sessionID := sm.Generate()

		require.NotEmpty(t, sessionID, "expected non-empty session ID")
		assert.Contains(t, sessionID, "-", "expected UUID format")

		// Placeholder must exist in storage.
		_, loadErr := storage.Load(context.Background(), sessionID)
		assert.NoError(t, loadErr, "placeholder should be stored in storage")
	})

	t.Run("returns empty string when storage always fails", func(t *testing.T) {
		t.Parallel()

		// Use a storage that always fails StoreIfAbsent(), forcing both
		// UUID attempts inside Generate() to fail so it must return "".
		ctrl := gomock.NewController(t)
		sess := newMockSession(t, ctrl, "placeholder", nil)
		factory := newMockFactory(t, ctrl, sess)
		sm, cleanup, err := New(alwaysFailDataStorage{}, &FactoryConfig{Base: factory, CacheCapacity: 1000}, newFakeRegistry())
		require.NoError(t, err)
		t.Cleanup(func() { _ = cleanup(context.Background()) })

		id := sm.Generate()
		assert.Empty(t, id, "Generate() should return '' when storage is unavailable")
	})

	t.Run("returns unique IDs on each call", func(t *testing.T) {
		t.Parallel()

		ctrl := gomock.NewController(t)
		sess := newMockSession(t, ctrl, "placeholder", nil)
		factory := newMockFactory(t, ctrl, sess)
		registry := newFakeRegistry()
		sm, _ := newTestSessionManager(t, factory, registry)

		id1 := sm.Generate()
		id2 := sm.Generate()
		id3 := sm.Generate()

		assert.NotEmpty(t, id1)
		assert.NotEmpty(t, id2)
		assert.NotEmpty(t, id3)
		assert.NotEqual(t, id1, id2)
		assert.NotEqual(t, id2, id3)
		assert.NotEqual(t, id1, id3)
	})
}

// ---------------------------------------------------------------------------
// Tests: CreateSession
// ---------------------------------------------------------------------------

func TestSessionManager_CreateSession(t *testing.T) {
	t.Parallel()

	t.Run("replaces placeholder with MultiSession", func(t *testing.T) {
		t.Parallel()

		tools := []vmcp.Tool{{Name: "my-tool", Description: "does stuff"}}
		ctrl := gomock.NewController(t)

		// We need ID() to return the actual session ID after it's known.
		// Since the session ID is generated by sm.Generate(), we use a DoAndReturn
		// to capture the ID at creation time.
		factory := sessionfactorymocks.NewMockMultiSessionFactory(ctrl)
		var createdSess *sessionmocks.MockMultiSession
		factory.EXPECT().
			MakeSessionWithID(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
			DoAndReturn(func(_ context.Context, id string, _ *auth.Identity, _ bool, _ []*vmcp.Backend) (vmcpsession.MultiSession, error) {
				createdSess = newMockSession(t, ctrl, id, tools)
				return createdSess, nil
			}).AnyTimes()

		registry := newFakeRegistry()
		sm, storage := newTestSessionManager(t, factory, registry)

		// Generate placeholder.
		sessionID := sm.Generate()
		require.NotEmpty(t, sessionID)

		// Upgrade to full MultiSession.
		multiSess, err := sm.CreateSession(context.Background(), sessionID)
		require.NoError(t, err)
		require.NotNil(t, multiSess)
		assert.Equal(t, sessionID, multiSess.ID())

		// Storage must still hold the session metadata after CreateSession.
		_, loadErr := storage.Load(context.Background(), sessionID)
		assert.NoError(t, loadErr, "session should still exist in storage after CreateSession")
	})

	t.Run("returns error for empty session ID", func(t *testing.T) {
		t.Parallel()

		ctrl := gomock.NewController(t)
		sess := newMockSession(t, ctrl, "", nil)
		factory := newMockFactory(t, ctrl, sess)
		registry := newFakeRegistry()
		sm, _ := newTestSessionManager(t, factory, registry)

		_, err := sm.CreateSession(context.Background(), "")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "session ID must not be empty")
	})

	t.Run("propagates factory error", func(t *testing.T) {
		t.Parallel()

		factoryErr := errors.New("backend unreachable")
		ctrl := gomock.NewController(t)
		factory := newMockFactoryWithError(t, ctrl, factoryErr)
		registry := newFakeRegistry()
		sm, _ := newTestSessionManager(t, factory, registry)

		// Generate a valid placeholder so the fast-fail guards pass and the
		// error comes from the factory, not from a missing session entry.
		sessionID := sm.Generate()
		require.NotEmpty(t, sessionID)

		_, err := sm.CreateSession(context.Background(), sessionID)
		require.Error(t, err)
		assert.ErrorContains(t, err, "failed to create multi-session")
	})

	t.Run("returns error without calling factory when placeholder has been deleted", func(t *testing.T) {
		t.Parallel()

		tools := []vmcp.Tool{{Name: "tool-a"}}
		ctrl := gomock.NewController(t)

		// Track whether the factory was called
		factoryCalled := false
		factory := sessionfactorymocks.NewMockMultiSessionFactory(ctrl)
		factory.EXPECT().
			MakeSessionWithID(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
			DoAndReturn(func(_ context.Context, id string, _ *auth.Identity, _ bool, _ []*vmcp.Backend) (vmcpsession.MultiSession, error) {
				factoryCalled = true
				sess := newMockSession(t, ctrl, id, tools)
				return sess, nil
			}).AnyTimes()

		registry := newFakeRegistry()
		sm, storage := newTestSessionManager(t, factory, registry)

		// Generate a placeholder and then delete it entirely — simulates a concurrent
		// TTL expiry or a client DELETE that removes the record before the hook fires.
		sessionID := sm.Generate()
		require.NotEmpty(t, sessionID)
		require.NoError(t, storage.Delete(context.Background(), sessionID))

		// CreateSession must fail fast before opening any backend connections.
		_, createErr := sm.CreateSession(context.Background(), sessionID)
		require.Error(t, createErr)
		assert.ErrorContains(t, createErr, "not found")

		// The factory must not have been called: no backend connections were opened.
		assert.False(t, factoryCalled, "factory should not be called when placeholder is absent")
	})

	t.Run("returns error without calling factory when placeholder is marked terminated", func(t *testing.T) {
		t.Parallel()

		tools := []vmcp.Tool{{Name: "tool-a"}}
		ctrl := gomock.NewController(t)

		// Track whether the factory was called
		factoryCalled := false
		factory := sessionfactorymocks.NewMockMultiSessionFactory(ctrl)
		factory.EXPECT().
			MakeSessionWithID(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
			DoAndReturn(func(_ context.Context, id string, _ *auth.Identity, _ bool, _ []*vmcp.Backend) (vmcpsession.MultiSession, error) {
				factoryCalled = true
				sess := newMockSession(t, ctrl, id, tools)
				return sess, nil
			}).AnyTimes()

		registry := newFakeRegistry()
		sm, _ := newTestSessionManager(t, factory, registry)

		// Generate a placeholder and terminate it — simulates a client DELETE
		// arriving before the OnRegisterSession hook fires. The placeholder
		// remains in storage but is marked terminated=true.
		sessionID := sm.Generate()
		require.NotEmpty(t, sessionID)
		_, err := sm.Terminate(sessionID)
		require.NoError(t, err)

		// CreateSession must fail fast (terminated=true) before opening any
		// backend connections.
		_, createErr := sm.CreateSession(context.Background(), sessionID)
		require.Error(t, createErr)
		assert.ErrorContains(t, createErr, "was terminated")

		// The factory must not have been called.
		assert.False(t, factoryCalled, "factory should not be called when placeholder is terminated")
	})

	t.Run("returns error when session is terminated during backend initialization", func(t *testing.T) {
		t.Parallel()

		ctrl := gomock.NewController(t)

		// We need a session that expects Close() to be called exactly once
		// (the second terminated check closes the session)
		var createdSess *sessionmocks.MockMultiSession
		factory := sessionfactorymocks.NewMockMultiSessionFactory(ctrl)
		factory.EXPECT().
			MakeSessionWithID(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
			DoAndReturn(func(_ context.Context, id string, _ *auth.Identity, _ bool, _ []*vmcp.Backend) (vmcpsession.MultiSession, error) {
				// Sleep to simulate slow backend initialization, creating a window
				// where the client can terminate the session after the first check passes.
				time.Sleep(50 * time.Millisecond)
				createdSess = newMockSession(t, ctrl, id, []vmcp.Tool{{Name: "tool-a"}})
				// Close() will be called exactly once when the second terminated check fails
				createdSess.EXPECT().Close().Return(nil).Times(1)
				return createdSess, nil
			}).Times(1)

		registry := newFakeRegistry()
		sm, _ := newTestSessionManager(t, factory, registry)

		// Generate a placeholder.
		sessionID := sm.Generate()
		require.NotEmpty(t, sessionID)

		// Start CreateSession in a goroutine — it will pass the first terminated
		// check and then sleep during MakeSessionWithID.
		errChan := make(chan error, 1)
		go func() {
			_, err := sm.CreateSession(context.Background(), sessionID)
			errChan <- err
		}()

		// Give the goroutine time to pass the first check and enter MakeSessionWithID.
		time.Sleep(10 * time.Millisecond)

		// Terminate the session while MakeSessionWithID is running. This sets
		// terminated=true on the placeholder (does not delete it).
		_, terminateErr := sm.Terminate(sessionID)
		require.NoError(t, terminateErr)

		// Wait for CreateSession to complete. The second terminated check (after
		// MakeSessionWithID) should detect terminated=true and fail.
		createErr := <-errChan
		require.Error(t, createErr)
		assert.ErrorContains(t, createErr, "was terminated during backend init")
	})
}

// ---------------------------------------------------------------------------
// Tests: Validate
// ---------------------------------------------------------------------------

func TestSessionManager_Validate(t *testing.T) {
	t.Parallel()

	t.Run("returns error for empty session ID", func(t *testing.T) {
		t.Parallel()

		ctrl := gomock.NewController(t)
		sess := newMockSession(t, ctrl, "", nil)
		factory := newMockFactory(t, ctrl, sess)
		registry := newFakeRegistry()
		sm, _ := newTestSessionManager(t, factory, registry)

		isTerminated, err := sm.Validate("")
		require.Error(t, err)
		assert.False(t, isTerminated)
		assert.Contains(t, err.Error(), "empty session ID")
	})

	t.Run("returns error for unknown session", func(t *testing.T) {
		t.Parallel()

		ctrl := gomock.NewController(t)
		sess := newMockSession(t, ctrl, "", nil)
		factory := newMockFactory(t, ctrl, sess)
		registry := newFakeRegistry()
		sm, _ := newTestSessionManager(t, factory, registry)

		isTerminated, err := sm.Validate("non-existent-id")
		require.Error(t, err)
		assert.False(t, isTerminated)
		assert.Contains(t, err.Error(), "session not found")
	})

	t.Run("returns false for active session", func(t *testing.T) {
		t.Parallel()

		ctrl := gomock.NewController(t)
		sess := newMockSession(t, ctrl, "", nil)
		factory := newMockFactory(t, ctrl, sess)
		registry := newFakeRegistry()
		sm, _ := newTestSessionManager(t, factory, registry)

		sessionID := sm.Generate()
		require.NotEmpty(t, sessionID)

		isTerminated, err := sm.Validate(sessionID)
		require.NoError(t, err)
		assert.False(t, isTerminated)
	})

	t.Run("returns isTerminated=true for terminated placeholder session", func(t *testing.T) {
		t.Parallel()

		ctrl := gomock.NewController(t)
		sess := newMockSession(t, ctrl, "", nil)
		factory := newMockFactory(t, ctrl, sess)
		registry := newFakeRegistry()
		sm, _ := newTestSessionManager(t, factory, registry)

		sessionID := sm.Generate()
		require.NotEmpty(t, sessionID)

		// Terminate via the phase-1 path (placeholder → set metadata).
		isNotAllowed, err := sm.Terminate(sessionID)
		require.NoError(t, err)
		assert.False(t, isNotAllowed)

		// Now Validate should report terminated.
		isTerminated, err := sm.Validate(sessionID)
		require.NoError(t, err)
		assert.True(t, isTerminated)
	})
}

// ---------------------------------------------------------------------------
// Tests: Terminate
// ---------------------------------------------------------------------------

func TestSessionManager_Terminate(t *testing.T) {
	t.Parallel()

	t.Run("returns error for empty session ID", func(t *testing.T) {
		t.Parallel()

		ctrl := gomock.NewController(t)
		sess := newMockSession(t, ctrl, "", nil)
		factory := newMockFactory(t, ctrl, sess)
		registry := newFakeRegistry()
		sm, _ := newTestSessionManager(t, factory, registry)

		isNotAllowed, err := sm.Terminate("")
		require.Error(t, err)
		assert.False(t, isNotAllowed)
		assert.Contains(t, err.Error(), "empty session ID")
	})

	t.Run("on unknown session returns no error", func(t *testing.T) {
		t.Parallel()

		ctrl := gomock.NewController(t)
		sess := newMockSession(t, ctrl, "", nil)
		factory := newMockFactory(t, ctrl, sess)
		registry := newFakeRegistry()
		sm, _ := newTestSessionManager(t, factory, registry)

		isNotAllowed, err := sm.Terminate("ghost-session")
		require.NoError(t, err)
		assert.False(t, isNotAllowed)
	})

	t.Run("closes MultiSession backend connections", func(t *testing.T) {
		t.Parallel()

		// After Terminate deletes the session from storage, the next GetMultiSession
		// call triggers checkSession → ErrExpired → onEvict → Close(). This verifies
		// that backend connections are eventually closed via lazy eviction.
		tools := []vmcp.Tool{{Name: "t1", Description: "tool 1"}}
		ctrl := gomock.NewController(t)

		// tokenHashMeta is carried by the session so CreateSession writes it to
		// storage and Terminate takes the Phase 2 (storage.Delete) path.
		tokenHashMeta := map[string]string{sessiontypes.MetadataKeyTokenHash: ""}

		var createdSess *sessionmocks.MockMultiSession
		factory := sessionfactorymocks.NewMockMultiSessionFactory(ctrl)
		factory.EXPECT().
			MakeSessionWithID(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
			DoAndReturn(func(_ context.Context, id string, _ *auth.Identity, _ bool, _ []*vmcp.Backend) (vmcpsession.MultiSession, error) {
				createdSess = sessionmocks.NewMockMultiSession(ctrl)
				createdSess.EXPECT().ID().Return(id).AnyTimes()
				createdSess.EXPECT().GetMetadata().Return(tokenHashMeta).AnyTimes()
				createdSess.EXPECT().Tools().Return(tools).AnyTimes()
				createdSess.EXPECT().Type().Return(transportsession.SessionType("")).AnyTimes()
				createdSess.EXPECT().CreatedAt().Return(time.Time{}).AnyTimes()
				createdSess.EXPECT().UpdatedAt().Return(time.Time{}).AnyTimes()
				createdSess.EXPECT().GetData().Return(nil).AnyTimes()
				createdSess.EXPECT().SetData(gomock.Any()).AnyTimes()
				createdSess.EXPECT().SetMetadata(gomock.Any(), gomock.Any()).AnyTimes()
				createdSess.EXPECT().BackendSessions().Return(nil).AnyTimes()
				createdSess.EXPECT().GetRoutingTable().Return(nil).AnyTimes()
				createdSess.EXPECT().Prompts().Return(nil).AnyTimes()
				// Close() is called by onEvict when checkSession detects the session
				// is gone from storage on the next GetMultiSession call.
				createdSess.EXPECT().Close().Return(nil).Times(1)
				return createdSess, nil
			}).Times(1)

		registry := newFakeRegistry()
		sm, _ := newTestSessionManager(t, factory, registry)

		sessionID := sm.Generate()
		require.NotEmpty(t, sessionID)

		_, err := sm.CreateSession(context.Background(), sessionID)
		require.NoError(t, err)
		require.NotNil(t, createdSess)

		// CreateSession already persists tokenHashMeta via sess.GetMetadata(),
		// so Terminate will take the Phase 2 path (storage.Delete) without
		// any additional seeding.

		// Terminate deletes from storage; the cache entry is evicted lazily on
		// the next GetMultiSession call when checkSession detects ErrSessionNotFound.
		isNotAllowed, err := sm.Terminate(sessionID)
		require.NoError(t, err)
		assert.False(t, isNotAllowed)

		// The next GetMultiSession triggers checkSession: storage returns
		// ErrSessionNotFound → ErrExpired → onEvict → Close().
		_, ok := sm.GetMultiSession(sessionID)
		assert.False(t, ok, "terminated session must not be returned")
		// gomock verifies Close() was called exactly once via Times(1)
	})

	t.Run("removes MultiSession from storage on Terminate", func(t *testing.T) {
		t.Parallel()

		ctrl := gomock.NewController(t)
		factory := sessionfactorymocks.NewMockMultiSessionFactory(ctrl)
		factory.EXPECT().
			MakeSessionWithID(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
			DoAndReturn(func(_ context.Context, id string, _ *auth.Identity, _ bool, _ []*vmcp.Backend) (vmcpsession.MultiSession, error) {
				sess := newMockSession(t, ctrl, id, nil)
				// Close is called by onEvict when Terminate removes the cache entry.
				sess.EXPECT().Close().Return(nil).AnyTimes()
				return sess, nil
			}).Times(1)

		registry := newFakeRegistry()
		sm, storage := newTestSessionManager(t, factory, registry)

		sessionID := sm.Generate()
		require.NotEmpty(t, sessionID)

		_, err := sm.CreateSession(context.Background(), sessionID)
		require.NoError(t, err)

		// Seed MetadataKeyTokenHash into storage so Terminate recognises this
		// as a Phase 2 (full MultiSession) and deletes rather than marks terminated.
		_, err = storage.Update(context.Background(), sessionID, map[string]string{
			sessiontypes.MetadataKeyTokenHash: "",
		})
		require.NoError(t, err)

		// Session must exist before termination.
		_, loadErr := storage.Load(context.Background(), sessionID)
		assert.NoError(t, loadErr, "session should exist in storage before Terminate")

		_, err = sm.Terminate(sessionID)
		require.NoError(t, err)

		// Session must be removed from storage.
		_, loadErrAfter := storage.Load(context.Background(), sessionID)
		assert.ErrorIs(t, loadErrAfter, transportsession.ErrSessionNotFound,
			"session should be deleted from storage after Terminate")
	})

	t.Run("placeholder session is marked terminated (not deleted)", func(t *testing.T) {
		t.Parallel()

		ctrl := gomock.NewController(t)
		sess := newMockSession(t, ctrl, "", nil)
		factory := newMockFactory(t, ctrl, sess)
		registry := newFakeRegistry()
		sm, storage := newTestSessionManager(t, factory, registry)

		// Generate a placeholder (no CreateSession called).
		sessionID := sm.Generate()
		require.NotEmpty(t, sessionID)

		isNotAllowed, err := sm.Terminate(sessionID)
		require.NoError(t, err)
		assert.False(t, isNotAllowed)

		// Placeholder should still be in storage but marked terminated.
		metadata, loadErr := storage.Load(context.Background(), sessionID)
		require.NoError(t, loadErr, "placeholder should remain in storage (TTL will clean it)")
		assert.Equal(t, MetadataValTrue, metadata[MetadataKeyTerminated])
	})

	t.Run("placeholder termination falls back to delete when upsert fails", func(t *testing.T) {
		t.Parallel()

		ctrl := gomock.NewController(t)
		sess := newMockSession(t, ctrl, "", nil)
		factory := newMockFactory(t, ctrl, sess)
		registry := newFakeRegistry()

		// Create a storage that succeeds on the first StoreIfAbsent (Generate creates
		// placeholder) but fails on the second Store (Terminate tries to upsert).
		// Delete succeeds. This tests the fallback path in Terminate().
		baseStorage, err := transportsession.NewLocalSessionDataStorage(time.Hour)
		require.NoError(t, err)
		t.Cleanup(func() { _ = baseStorage.Close() })
		failingStorage := &configurableFailDataStorage{
			DataStorage:    baseStorage,
			failStoreAfter: 1, // fail after 1 successful call (Generate's Create)
			failDelete:     false,
		}
		sm, cleanup, err := New(failingStorage, &FactoryConfig{Base: factory, CacheCapacity: 1000}, registry)
		require.NoError(t, err)
		t.Cleanup(func() { _ = cleanup(context.Background()) })

		// Generate a placeholder (first Create, succeeds).
		sessionID := sm.Generate()
		require.NotEmpty(t, sessionID)

		// Terminate should succeed via the delete fallback (second Store fails, Delete succeeds).
		isNotAllowed, err := sm.Terminate(sessionID)
		require.NoError(t, err)
		assert.False(t, isNotAllowed)

		// Placeholder should be deleted (not just marked terminated).
		_, loadErr := baseStorage.Load(context.Background(), sessionID)
		assert.ErrorIs(t, loadErr, transportsession.ErrSessionNotFound,
			"placeholder should be deleted when upsert fails")
	})

	t.Run("placeholder termination fails when both upsert and delete fail", func(t *testing.T) {
		t.Parallel()

		ctrl := gomock.NewController(t)
		sess := newMockSession(t, ctrl, "", nil)
		factory := newMockFactory(t, ctrl, sess)
		registry := newFakeRegistry()

		// Create a storage that succeeds on the first StoreIfAbsent (Generate creates
		// placeholder) but fails on the second Store (Terminate tries to upsert)
		// and also fails on Delete. This forces the error path.
		baseStorage, err := transportsession.NewLocalSessionDataStorage(time.Hour)
		require.NoError(t, err)
		t.Cleanup(func() { _ = baseStorage.Close() })
		failingStorage := &configurableFailDataStorage{
			DataStorage:    baseStorage,
			failStoreAfter: 1, // fail after 1 successful call (Generate's Create)
			failDelete:     true,
		}
		sm, cleanup, err := New(failingStorage, &FactoryConfig{Base: factory, CacheCapacity: 1000}, registry)
		require.NoError(t, err)
		t.Cleanup(func() { _ = cleanup(context.Background()) })

		// Generate a placeholder (first Create, succeeds).
		sessionID := sm.Generate()
		require.NotEmpty(t, sessionID)

		// Terminate should fail when both upsert and delete fail.
		isNotAllowed, err := sm.Terminate(sessionID)
		require.Error(t, err)
		assert.False(t, isNotAllowed)
		assert.ErrorContains(t, err, "failed to persist terminated flag and delete placeholder")
		assert.ErrorContains(t, err, "storeErr=")
		assert.ErrorContains(t, err, "deleteErr=")
	})
}

// ---------------------------------------------------------------------------
// Tests: GetMultiSession
// ---------------------------------------------------------------------------

func TestSessionManager_GetMultiSession(t *testing.T) {
	t.Parallel()

	t.Run("returns nil for unknown session", func(t *testing.T) {
		t.Parallel()

		ctrl := gomock.NewController(t)
		sess := newMockSession(t, ctrl, "", nil)
		factory := newMockFactory(t, ctrl, sess)
		registry := newFakeRegistry()
		sm, _ := newTestSessionManager(t, factory, registry)

		multiSess, ok := sm.GetMultiSession("ghost")
		assert.False(t, ok)
		assert.Nil(t, multiSess)
	})

	t.Run("returns nil for placeholder session (not yet upgraded)", func(t *testing.T) {
		t.Parallel()

		ctrl := gomock.NewController(t)
		sess := newMockSession(t, ctrl, "", nil)
		factory := newMockFactory(t, ctrl, sess)
		registry := newFakeRegistry()
		sm, _ := newTestSessionManager(t, factory, registry)

		sessionID := sm.Generate()
		require.NotEmpty(t, sessionID)

		// Placeholder has not been upgraded yet.
		multiSess, ok := sm.GetMultiSession(sessionID)
		assert.False(t, ok, "placeholder should not satisfy MultiSession type assertion")
		assert.Nil(t, multiSess)
	})

	t.Run("returns MultiSession after CreateSession", func(t *testing.T) {
		t.Parallel()

		tools := []vmcp.Tool{{Name: "hello", Description: "says hello"}}
		ctrl := gomock.NewController(t)
		factory := sessionfactorymocks.NewMockMultiSessionFactory(ctrl)
		factory.EXPECT().
			MakeSessionWithID(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
			DoAndReturn(func(_ context.Context, id string, _ *auth.Identity, _ bool, _ []*vmcp.Backend) (vmcpsession.MultiSession, error) {
				sess := newMockSession(t, ctrl, id, tools)
				return sess, nil
			}).Times(1)

		registry := newFakeRegistry()
		sm, _ := newTestSessionManager(t, factory, registry)

		sessionID := sm.Generate()
		require.NotEmpty(t, sessionID)

		_, err := sm.CreateSession(context.Background(), sessionID)
		require.NoError(t, err)

		multiSess, ok := sm.GetMultiSession(sessionID)
		require.True(t, ok)
		require.NotNil(t, multiSess)
		assert.Equal(t, sessionID, multiSess.ID())
		require.Len(t, multiSess.Tools(), 1)
		assert.Equal(t, "hello", multiSess.Tools()[0].Name)
	})

	// Cross-pod restore path: session is in storage but not in the in-memory
	// cache (simulates pod restart or eviction). loadSession is called on Get.

	t.Run("restore path: placeholder in storage (absent token hash) is treated as not found", func(t *testing.T) {
		t.Parallel()

		ctrl := gomock.NewController(t)
		factory := sessionfactorymocks.NewMockMultiSessionFactory(ctrl)
		// RestoreSession must NOT be called for placeholders.
		factory.EXPECT().RestoreSession(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Times(0)

		sm, _ := newTestSessionManager(t, factory, newFakeRegistry())

		sessionID := "restore-placeholder-session"
		// Write placeholder metadata directly to storage, bypassing the cache.
		// Generate() stores an empty map with no token hash.
		_, err := sm.storage.Create(context.Background(), sessionID, map[string]string{})
		require.NoError(t, err)

		// loadSession detects absent MetadataKeyTokenHash → ErrSessionNotFound.
		multiSess, ok := sm.GetMultiSession(sessionID)
		assert.False(t, ok, "placeholder should not be restorable")
		assert.Nil(t, multiSess)
	})

	t.Run("restore path: fully-initialized zero-backend session (has token hash) is restored", func(t *testing.T) {
		t.Parallel()

		tools := []vmcp.Tool{{Name: "zero-backend-tool", Description: "tool with no backends"}}
		ctrl := gomock.NewController(t)
		factory := sessionfactorymocks.NewMockMultiSessionFactory(ctrl)
		// MakeSessionWithID is only for Phase 2; unused in the restore path.
		factory.EXPECT().MakeSessionWithID(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
			Times(0)

		sessionID := "restore-zero-backend-session"
		restored := newMockSession(t, ctrl, sessionID, tools)

		factory.EXPECT().
			RestoreSession(gomock.Any(), sessionID, gomock.Any(), gomock.Any()).
			Return(restored, nil).Times(1)

		sm, _ := newTestSessionManager(t, factory, newFakeRegistry())

		// Metadata matching what populateBackendMetadata now writes for a
		// Phase-2-complete session with zero backends: MetadataKeyBackendIDs
		// is always written (empty string for zero backends).
		initializedMeta := map[string]string{
			sessiontypes.MetadataKeyTokenHash: "", // anonymous sentinel — present but empty
			vmcpsession.MetadataKeyBackendIDs: "", // always written; empty = zero backends
		}
		_, err := sm.storage.Create(context.Background(), sessionID, initializedMeta)
		require.NoError(t, err)

		// loadSession should call RestoreSession, not treat it as a placeholder.
		multiSess, ok := sm.GetMultiSession(sessionID)
		require.True(t, ok, "initialized zero-backend session should be restorable")
		require.NotNil(t, multiSess)
		assert.Equal(t, sessionID, multiSess.ID())
	})

	t.Run("restore path: legacy record missing MetadataKeyBackendIDs is still restorable", func(t *testing.T) {
		t.Parallel()

		// Legacy sessions written before populateBackendMetadata was changed to
		// always write MetadataKeyBackendIDs may omit the key entirely.
		// filterBackendsByStoredIDs treats an absent key (single-value lookup → "")
		// identically to an explicit empty string: zero backends are passed to
		// RestoreSession. This test documents that backward-compat behaviour.
		ctrl := gomock.NewController(t)
		factory := sessionfactorymocks.NewMockMultiSessionFactory(ctrl)
		factory.EXPECT().MakeSessionWithID(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
			Times(0)

		sessionID := "restore-legacy-session"
		restored := newMockSession(t, ctrl, sessionID, nil)

		factory.EXPECT().
			RestoreSession(gomock.Any(), sessionID, gomock.Any(), gomock.Any()).
			Return(restored, nil).Times(1)

		sm, _ := newTestSessionManager(t, factory, newFakeRegistry())

		// Legacy metadata: token hash present but MetadataKeyBackendIDs absent.
		legacyMeta := map[string]string{
			sessiontypes.MetadataKeyTokenHash: "", // Phase 2 completion marker
			// MetadataKeyBackendIDs intentionally absent (legacy record)
		}
		_, err := sm.storage.Create(context.Background(), sessionID, legacyMeta)
		require.NoError(t, err)

		multiSess, ok := sm.GetMultiSession(sessionID)
		require.True(t, ok, "legacy record without MetadataKeyBackendIDs must still be restorable")
		require.NotNil(t, multiSess)
		assert.Equal(t, sessionID, multiSess.ID())
	})
}

// ---------------------------------------------------------------------------
// Tests: GetAdaptedTools
// ---------------------------------------------------------------------------

func TestSessionManager_GetAdaptedTools(t *testing.T) {
	t.Parallel()

	t.Run("returns error for unknown session", func(t *testing.T) {
		t.Parallel()

		ctrl := gomock.NewController(t)
		sess := newMockSession(t, ctrl, "", nil)
		factory := newMockFactory(t, ctrl, sess)
		registry := newFakeRegistry()
		sm, _ := newTestSessionManager(t, factory, registry)

		_, err := sm.GetAdaptedTools("no-such-session")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "not found or not a multi-session")
	})

	t.Run("returns tools with correct names and schemas", func(t *testing.T) {
		t.Parallel()

		tools := []vmcp.Tool{
			{
				Name:        "alpha",
				Description: "first tool",
				InputSchema: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"input": map[string]any{"type": "string"},
					},
				},
			},
			{Name: "beta", Description: "second tool"},
		}
		ctrl := gomock.NewController(t)
		factory := sessionfactorymocks.NewMockMultiSessionFactory(ctrl)
		factory.EXPECT().
			MakeSessionWithID(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
			DoAndReturn(func(_ context.Context, id string, _ *auth.Identity, _ bool, _ []*vmcp.Backend) (vmcpsession.MultiSession, error) {
				return newMockSession(t, ctrl, id, tools), nil
			}).Times(1)

		registry := newFakeRegistry()
		sm, _ := newTestSessionManager(t, factory, registry)

		sessionID := sm.Generate()
		_, err := sm.CreateSession(context.Background(), sessionID)
		require.NoError(t, err)

		adaptedTools, err := sm.GetAdaptedTools(sessionID)
		require.NoError(t, err)
		require.Len(t, adaptedTools, 2)

		byName := map[string]mcp.Tool{}
		for _, st := range adaptedTools {
			byName[st.Tool.Name] = st.Tool
		}

		require.Contains(t, byName, "alpha")
		require.Contains(t, byName, "beta")

		// InputSchema must be marshalled into RawInputSchema so clients
		// receive the full parameter schema.
		assert.NotEmpty(t, byName["alpha"].RawInputSchema)
		assert.Contains(t, string(byName["alpha"].RawInputSchema), `"type"`)
	})

	t.Run("preserves annotations and output schema", func(t *testing.T) {
		t.Parallel()

		boolPtr := func(b bool) *bool { return &b }
		tools := []vmcp.Tool{
			{
				Name:        "annotated",
				Description: "tool with annotations",
				InputSchema: map[string]any{"type": "object"},
				OutputSchema: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"result": map[string]any{"type": "string"},
					},
				},
				Annotations: &vmcp.ToolAnnotations{
					Title:           "Annotated Tool",
					ReadOnlyHint:    boolPtr(true),
					DestructiveHint: boolPtr(false),
				},
			},
			{
				Name:        "plain",
				Description: "tool without annotations or output schema",
				InputSchema: map[string]any{"type": "object"},
			},
		}
		ctrl := gomock.NewController(t)
		factory := sessionfactorymocks.NewMockMultiSessionFactory(ctrl)
		factory.EXPECT().
			MakeSessionWithID(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
			DoAndReturn(func(_ context.Context, id string, _ *auth.Identity, _ bool, _ []*vmcp.Backend) (vmcpsession.MultiSession, error) {
				return newMockSession(t, ctrl, id, tools), nil
			}).Times(1)

		registry := newFakeRegistry()
		sm, _ := newTestSessionManager(t, factory, registry)

		sessionID := sm.Generate()
		_, err := sm.CreateSession(context.Background(), sessionID)
		require.NoError(t, err)

		adaptedTools, err := sm.GetAdaptedTools(sessionID)
		require.NoError(t, err)
		require.Len(t, adaptedTools, 2)

		byName := map[string]mcp.Tool{}
		for _, st := range adaptedTools {
			byName[st.Tool.Name] = st.Tool
		}

		// Verify annotations are preserved on the annotated tool.
		annotated := byName["annotated"]
		assert.Equal(t, "Annotated Tool", annotated.Annotations.Title)
		require.NotNil(t, annotated.Annotations.ReadOnlyHint)
		assert.True(t, *annotated.Annotations.ReadOnlyHint)
		require.NotNil(t, annotated.Annotations.DestructiveHint)
		assert.False(t, *annotated.Annotations.DestructiveHint)
		assert.Nil(t, annotated.Annotations.IdempotentHint)
		assert.Nil(t, annotated.Annotations.OpenWorldHint)

		// Verify output schema is preserved.
		assert.NotNil(t, annotated.RawOutputSchema)
		assert.Contains(t, string(annotated.RawOutputSchema), `"result"`)

		// Verify nil annotations produce zero-valued annotations and nil output schema.
		plain := byName["plain"]
		assert.Empty(t, plain.Annotations.Title)
		assert.Nil(t, plain.Annotations.ReadOnlyHint)
		assert.Nil(t, plain.RawOutputSchema)
	})

	t.Run("handlers delegate to session CallTool", func(t *testing.T) {
		t.Parallel()

		tools := []vmcp.Tool{{Name: "greet", Description: "greets user"}}
		ctrl := gomock.NewController(t)

		callToolResult := &vmcp.ToolCallResult{
			Content: []vmcp.Content{{Type: vmcp.ContentTypeText, Text: "Hello, world!"}},
		}
		factory := sessionfactorymocks.NewMockMultiSessionFactory(ctrl)
		factory.EXPECT().
			MakeSessionWithID(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
			DoAndReturn(func(_ context.Context, id string, _ *auth.Identity, _ bool, _ []*vmcp.Backend) (vmcpsession.MultiSession, error) {
				sess := newMockSession(t, ctrl, id, tools)
				sess.EXPECT().CallTool(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
					Return(callToolResult, nil).Times(1)
				return sess, nil
			}).Times(1)

		registry := newFakeRegistry()
		sm, _ := newTestSessionManager(t, factory, registry)

		sessionID := sm.Generate()
		_, err := sm.CreateSession(context.Background(), sessionID)
		require.NoError(t, err)

		adaptedTools, err := sm.GetAdaptedTools(sessionID)
		require.NoError(t, err)
		require.Len(t, adaptedTools, 1)

		// Invoke the handler.
		handler := adaptedTools[0].Handler
		require.NotNil(t, handler)

		result, handlerErr := handler(context.Background(), newCallToolRequest("greet", nil))
		require.NoError(t, handlerErr)
		require.NotNil(t, result)
		require.Len(t, result.Content, 1)
		// mcp.Content is an interface; assert the concrete TextContent type.
		textContent, ok := result.Content[0].(mcp.TextContent)
		require.True(t, ok, "expected TextContent")
		assert.Equal(t, "Hello, world!", textContent.Text)
		assert.False(t, result.IsError)
	})

	t.Run("handler returns tool error when CallTool fails", func(t *testing.T) {
		t.Parallel()

		tools := []vmcp.Tool{{Name: "boom", Description: "always fails"}}
		ctrl := gomock.NewController(t)
		factory := sessionfactorymocks.NewMockMultiSessionFactory(ctrl)
		factory.EXPECT().
			MakeSessionWithID(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
			DoAndReturn(func(_ context.Context, id string, _ *auth.Identity, _ bool, _ []*vmcp.Backend) (vmcpsession.MultiSession, error) {
				sess := newMockSession(t, ctrl, id, tools)
				sess.EXPECT().CallTool(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
					Return(nil, errors.New("backend exploded")).Times(1)
				return sess, nil
			}).Times(1)

		registry := newFakeRegistry()
		sm, _ := newTestSessionManager(t, factory, registry)

		sessionID := sm.Generate()
		_, err := sm.CreateSession(context.Background(), sessionID)
		require.NoError(t, err)

		adaptedTools, err := sm.GetAdaptedTools(sessionID)
		require.NoError(t, err)
		require.Len(t, adaptedTools, 1)

		result, handlerErr := adaptedTools[0].Handler(context.Background(), newCallToolRequest("boom", nil))
		require.NoError(t, handlerErr, "handler should not return an error — it should wrap it in a tool result")
		require.NotNil(t, result)
		assert.True(t, result.IsError, "IsError should be set for failed tool calls")
	})

	t.Run("handler returns error result for non-object arguments", func(t *testing.T) {
		t.Parallel()

		tools := []vmcp.Tool{{Name: "strict", Description: "requires object args"}}
		ctrl := gomock.NewController(t)
		factory := sessionfactorymocks.NewMockMultiSessionFactory(ctrl)
		factory.EXPECT().
			MakeSessionWithID(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
			DoAndReturn(func(_ context.Context, id string, _ *auth.Identity, _ bool, _ []*vmcp.Backend) (vmcpsession.MultiSession, error) {
				return newMockSession(t, ctrl, id, tools), nil
			}).Times(1)

		registry := newFakeRegistry()
		sm, _ := newTestSessionManager(t, factory, registry)

		sessionID := sm.Generate()
		_, err := sm.CreateSession(context.Background(), sessionID)
		require.NoError(t, err)

		adaptedTools, err := sm.GetAdaptedTools(sessionID)
		require.NoError(t, err)
		require.Len(t, adaptedTools, 1)

		// Pass a non-object argument (string instead of map).
		req := mcp.CallToolRequest{}
		req.Params.Name = "strict"
		req.Params.Arguments = "not-an-object"

		result, handlerErr := adaptedTools[0].Handler(context.Background(), req)
		require.NoError(t, handlerErr, "handler must not return a Go error")
		require.NotNil(t, result)
		assert.True(t, result.IsError, "non-object arguments should produce an error tool result")
	})

	t.Run("handler forwards request meta to CallTool", func(t *testing.T) {
		t.Parallel()

		tools := []vmcp.Tool{{Name: "meta-tool", Description: "checks meta forwarding"}}
		ctrl := gomock.NewController(t)

		var capturedMeta map[string]any
		factory := sessionfactorymocks.NewMockMultiSessionFactory(ctrl)
		factory.EXPECT().
			MakeSessionWithID(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
			DoAndReturn(func(_ context.Context, id string, _ *auth.Identity, _ bool, _ []*vmcp.Backend) (vmcpsession.MultiSession, error) {
				sess := newMockSession(t, ctrl, id, tools)
				sess.EXPECT().CallTool(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
					DoAndReturn(func(_ context.Context, _ *auth.Identity, _ string, _ map[string]any, meta map[string]any) (*vmcp.ToolCallResult, error) {
						capturedMeta = meta
						return &vmcp.ToolCallResult{}, nil
					}).Times(1)
				return sess, nil
			}).Times(1)

		registry := newFakeRegistry()
		sm, _ := newTestSessionManager(t, factory, registry)

		sessionID := sm.Generate()
		_, err := sm.CreateSession(context.Background(), sessionID)
		require.NoError(t, err)

		adaptedTools, err := sm.GetAdaptedTools(sessionID)
		require.NoError(t, err)
		require.Len(t, adaptedTools, 1)

		// Build a request with a progress token in _meta.
		req := mcp.CallToolRequest{}
		req.Params.Name = "meta-tool"
		req.Params.Arguments = map[string]any{}
		req.Params.Meta = &mcp.Meta{ProgressToken: mcp.ProgressToken("tok-1")}

		_, handlerErr := adaptedTools[0].Handler(context.Background(), req)
		require.NoError(t, handlerErr)

		// The meta must have been forwarded to CallTool.
		require.NotNil(t, capturedMeta, "meta should be forwarded to CallTool")
		assert.Equal(t, "tok-1", capturedMeta["progressToken"])
	})

	t.Run("handler terminates session on authorization errors", func(t *testing.T) {
		t.Parallel()

		// Test both ErrUnauthorizedCaller and ErrNilCaller
		testCases := []struct {
			name        string
			authError   error
			expectError string
		}{
			{
				name:        "ErrUnauthorizedCaller",
				authError:   sessiontypes.ErrUnauthorizedCaller,
				expectError: "Unauthorized",
			},
			{
				name:        "ErrNilCaller",
				authError:   sessiontypes.ErrNilCaller,
				expectError: "Unauthorized",
			},
		}

		for _, tc := range testCases {
			t.Run(tc.name, func(t *testing.T) {
				t.Parallel()

				tools := []vmcp.Tool{{Name: "auth-tool", Description: "requires authorization"}}
				ctrl := gomock.NewController(t)
				authErr := tc.authError
				factory := sessionfactorymocks.NewMockMultiSessionFactory(ctrl)
				factory.EXPECT().
					MakeSessionWithID(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
					DoAndReturn(func(_ context.Context, id string, _ *auth.Identity, _ bool, _ []*vmcp.Backend) (vmcpsession.MultiSession, error) {
						sess := newMockSession(t, ctrl, id, tools)
						sess.EXPECT().CallTool(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
							Return(nil, authErr).Times(1)
						// Close() is called when the session is terminated after auth failure
						sess.EXPECT().Close().Return(nil).Times(1)
						return sess, nil
					}).Times(1)

				registry := newFakeRegistry()
				sm, _ := newTestSessionManager(t, factory, registry)

				sessionID := sm.Generate()
				_, err := sm.CreateSession(context.Background(), sessionID)
				require.NoError(t, err)

				adaptedTools, err := sm.GetAdaptedTools(sessionID)
				require.NoError(t, err)
				require.Len(t, adaptedTools, 1)

				// Call the tool - should return an error result
				req := newCallToolRequest("auth-tool", map[string]any{})
				result, handlerErr := adaptedTools[0].Handler(context.Background(), req)
				require.NoError(t, handlerErr, "handler should not return Go error")
				require.NotNil(t, result)

				// Verify error result contains "Unauthorized"
				assert.True(t, result.IsError, "result should indicate error")
				require.Len(t, result.Content, 1, "result should have content")
				textContent, ok := result.Content[0].(mcp.TextContent)
				require.True(t, ok, "expected TextContent")
				assert.Contains(t, textContent.Text, tc.expectError)

				// Verify subsequent GetAdaptedTools fails (session no longer exists)
				_, err = sm.GetAdaptedTools(sessionID)
				assert.Error(t, err, "GetAdaptedTools should fail after session termination")
				// gomock verifies Close() was called exactly once via Times(1)
			})
		}
	})
}

// ---------------------------------------------------------------------------
// Tests: GetAdaptedResources
// ---------------------------------------------------------------------------

func TestSessionManager_GetAdaptedResources(t *testing.T) {
	t.Parallel()

	t.Run("returns error for unknown session", func(t *testing.T) {
		t.Parallel()

		ctrl := gomock.NewController(t)
		sess := newMockSession(t, ctrl, "", nil)
		factory := newMockFactory(t, ctrl, sess)
		registry := newFakeRegistry()
		sm, _ := newTestSessionManager(t, factory, registry)

		_, err := sm.GetAdaptedResources("no-such-session")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "not found or not a multi-session")
	})

	t.Run("returns resources with correct fields", func(t *testing.T) {
		t.Parallel()

		resources := []vmcp.Resource{
			{
				Name:        "config",
				URI:         "file:///etc/config.json",
				Description: "Configuration file",
				MimeType:    "application/json",
			},
			{
				Name:        "readme",
				URI:         "file:///README.md",
				Description: "Readme",
				MimeType:    "text/markdown",
			},
		}

		ctrl := gomock.NewController(t)
		factory := sessionfactorymocks.NewMockMultiSessionFactory(ctrl)
		factory.EXPECT().
			MakeSessionWithID(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
			DoAndReturn(func(_ context.Context, id string, _ *auth.Identity, _ bool, _ []*vmcp.Backend) (vmcpsession.MultiSession, error) {
				sess := newMockSession(t, ctrl, id, nil)
				// Override default Resources() AnyTimes with a specific return
				sess.EXPECT().Resources().Return(resources).AnyTimes()
				return sess, nil
			}).Times(1)

		registry := newFakeRegistry()
		sm, _ := newTestSessionManager(t, factory, registry)

		sessionID := sm.Generate()
		_, err := sm.CreateSession(context.Background(), sessionID)
		require.NoError(t, err)

		adaptedResources, err := sm.GetAdaptedResources(sessionID)
		require.NoError(t, err)
		require.Len(t, adaptedResources, 2)

		byURI := map[string]mcp.Resource{}
		for _, sr := range adaptedResources {
			byURI[sr.Resource.URI] = sr.Resource
		}

		require.Contains(t, byURI, "file:///etc/config.json")
		require.Contains(t, byURI, "file:///README.md")

		assert.Equal(t, "config", byURI["file:///etc/config.json"].Name)
		assert.Equal(t, "application/json", byURI["file:///etc/config.json"].MIMEType)
		assert.Equal(t, "readme", byURI["file:///README.md"].Name)
		assert.Equal(t, "text/markdown", byURI["file:///README.md"].MIMEType)
	})

	t.Run("handler delegates to session ReadResource", func(t *testing.T) {
		t.Parallel()

		resources := []vmcp.Resource{
			{
				Name:     "data",
				URI:      "file:///data.txt",
				MimeType: "text/plain",
			},
		}
		readResult := &vmcp.ResourceReadResult{
			Contents: []vmcp.ResourceContent{
				{URI: "file:///data.txt", MimeType: "text/plain", Text: "hello resource"},
			},
		}

		ctrl := gomock.NewController(t)
		factory := sessionfactorymocks.NewMockMultiSessionFactory(ctrl)
		factory.EXPECT().
			MakeSessionWithID(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
			DoAndReturn(func(_ context.Context, id string, _ *auth.Identity, _ bool, _ []*vmcp.Backend) (vmcpsession.MultiSession, error) {
				sess := newMockSession(t, ctrl, id, nil)
				sess.EXPECT().Resources().Return(resources).AnyTimes()
				sess.EXPECT().ReadResource(gomock.Any(), gomock.Any(), "file:///data.txt").
					Return(readResult, nil).Times(1)
				return sess, nil
			}).Times(1)

		registry := newFakeRegistry()
		sm, _ := newTestSessionManager(t, factory, registry)

		sessionID := sm.Generate()
		_, err := sm.CreateSession(context.Background(), sessionID)
		require.NoError(t, err)

		adaptedResources, err := sm.GetAdaptedResources(sessionID)
		require.NoError(t, err)
		require.Len(t, adaptedResources, 1)

		req := mcp.ReadResourceRequest{}
		req.Params.URI = "file:///data.txt"
		contents, handlerErr := adaptedResources[0].Handler(context.Background(), req)
		require.NoError(t, handlerErr)
		require.Len(t, contents, 1)

		textContents, ok := contents[0].(mcp.TextResourceContents)
		require.True(t, ok, "expected TextResourceContents")
		assert.Equal(t, "file:///data.txt", textContents.URI)
		assert.Equal(t, "text/plain", textContents.MIMEType)
		assert.Equal(t, "hello resource", textContents.Text)
	})

	t.Run("handler returns error when ReadResource fails", func(t *testing.T) {
		t.Parallel()

		resources := []vmcp.Resource{
			{
				Name:     "broken",
				URI:      "file:///broken.txt",
				MimeType: "text/plain",
			},
		}
		readErr := errors.New("read failed")

		ctrl := gomock.NewController(t)
		factory := sessionfactorymocks.NewMockMultiSessionFactory(ctrl)
		factory.EXPECT().
			MakeSessionWithID(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
			DoAndReturn(func(_ context.Context, id string, _ *auth.Identity, _ bool, _ []*vmcp.Backend) (vmcpsession.MultiSession, error) {
				sess := newMockSession(t, ctrl, id, nil)
				sess.EXPECT().Resources().Return(resources).AnyTimes()
				sess.EXPECT().ReadResource(gomock.Any(), gomock.Any(), "file:///broken.txt").
					Return(nil, readErr).Times(1)
				return sess, nil
			}).Times(1)

		registry := newFakeRegistry()
		sm, _ := newTestSessionManager(t, factory, registry)

		sessionID := sm.Generate()
		_, err := sm.CreateSession(context.Background(), sessionID)
		require.NoError(t, err)

		adaptedResources, err := sm.GetAdaptedResources(sessionID)
		require.NoError(t, err)
		require.Len(t, adaptedResources, 1)

		req := mcp.ReadResourceRequest{}
		req.Params.URI = "file:///broken.txt"
		contents, handlerErr := adaptedResources[0].Handler(context.Background(), req)
		require.Error(t, handlerErr)
		assert.Nil(t, contents)
		assert.ErrorContains(t, handlerErr, "read failed")
	})

	t.Run("handler preserves empty MimeType from backend", func(t *testing.T) {
		t.Parallel()

		resources := []vmcp.Resource{
			{
				Name: "binary",
				URI:  "file:///binary.bin",
				// MimeType intentionally empty
			},
		}
		readResult := &vmcp.ResourceReadResult{
			Contents: []vmcp.ResourceContent{
				{URI: "file:///binary.bin", MimeType: "", Text: "binary data"},
			},
		}

		ctrl := gomock.NewController(t)
		factory := sessionfactorymocks.NewMockMultiSessionFactory(ctrl)
		factory.EXPECT().
			MakeSessionWithID(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
			DoAndReturn(func(_ context.Context, id string, _ *auth.Identity, _ bool, _ []*vmcp.Backend) (vmcpsession.MultiSession, error) {
				sess := newMockSession(t, ctrl, id, nil)
				sess.EXPECT().Resources().Return(resources).AnyTimes()
				sess.EXPECT().ReadResource(gomock.Any(), gomock.Any(), "file:///binary.bin").
					Return(readResult, nil).Times(1)
				return sess, nil
			}).Times(1)

		registry := newFakeRegistry()
		sm, _ := newTestSessionManager(t, factory, registry)

		sessionID := sm.Generate()
		_, err := sm.CreateSession(context.Background(), sessionID)
		require.NoError(t, err)

		adaptedResources, err := sm.GetAdaptedResources(sessionID)
		require.NoError(t, err)
		require.Len(t, adaptedResources, 1)

		req := mcp.ReadResourceRequest{}
		req.Params.URI = "file:///binary.bin"
		contents, handlerErr := adaptedResources[0].Handler(context.Background(), req)
		require.NoError(t, handlerErr)
		require.Len(t, contents, 1)

		textContents, ok := contents[0].(mcp.TextResourceContents)
		require.True(t, ok, "expected TextResourceContents")
		assert.Equal(t, "", textContents.MIMEType)
	})

	t.Run("handler terminates session on authorization errors", func(t *testing.T) {
		t.Parallel()

		testCases := []struct {
			name      string
			authError error
		}{
			{
				name:      "ErrUnauthorizedCaller",
				authError: sessiontypes.ErrUnauthorizedCaller,
			},
			{
				name:      "ErrNilCaller",
				authError: sessiontypes.ErrNilCaller,
			},
		}

		for _, tc := range testCases {
			t.Run(tc.name, func(t *testing.T) {
				t.Parallel()

				resources := []vmcp.Resource{
					{
						Name: "protected",
						URI:  "file:///protected.txt",
					},
				}
				authErr := tc.authError

				ctrl := gomock.NewController(t)
				factory := sessionfactorymocks.NewMockMultiSessionFactory(ctrl)
				factory.EXPECT().
					MakeSessionWithID(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
					DoAndReturn(func(_ context.Context, id string, _ *auth.Identity, _ bool, _ []*vmcp.Backend) (vmcpsession.MultiSession, error) {
						sess := newMockSession(t, ctrl, id, nil)
						sess.EXPECT().Resources().Return(resources).AnyTimes()
						sess.EXPECT().ReadResource(gomock.Any(), gomock.Any(), "file:///protected.txt").
							Return(nil, authErr).Times(1)
						// Close() is called when the session is terminated after auth failure
						sess.EXPECT().Close().Return(nil).Times(1)
						return sess, nil
					}).Times(1)

				registry := newFakeRegistry()
				sm, _ := newTestSessionManager(t, factory, registry)

				sessionID := sm.Generate()
				_, err := sm.CreateSession(context.Background(), sessionID)
				require.NoError(t, err)

				adaptedResources, err := sm.GetAdaptedResources(sessionID)
				require.NoError(t, err)
				require.Len(t, adaptedResources, 1)

				req := mcp.ReadResourceRequest{}
				req.Params.URI = "file:///protected.txt"
				contents, handlerErr := adaptedResources[0].Handler(context.Background(), req)
				require.Error(t, handlerErr, "handler should return an error for auth failures")
				assert.Nil(t, contents)
				assert.ErrorContains(t, handlerErr, "unauthorized")

				// Verify subsequent GetAdaptedResources fails (session no longer exists)
				_, err = sm.GetAdaptedResources(sessionID)
				assert.Error(t, err, "GetAdaptedResources should fail after session termination")
				// gomock verifies Close() was called exactly once via Times(1)
			})
		}
	})
}

// ---------------------------------------------------------------------------
// Tests: GetAdaptedPrompts
// ---------------------------------------------------------------------------

func TestSessionManager_GetAdaptedPrompts(t *testing.T) {
	t.Parallel()

	t.Run("returns error for unknown session", func(t *testing.T) {
		t.Parallel()

		ctrl := gomock.NewController(t)
		sess := newMockSession(t, ctrl, "", nil)
		factory := newMockFactory(t, ctrl, sess)
		registry := newFakeRegistry()
		sm, _ := newTestSessionManager(t, factory, registry)

		_, err := sm.GetAdaptedPrompts("no-such-session")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "not found or not a multi-session")
	})

	t.Run("returns prompts with correct fields and arguments", func(t *testing.T) {
		t.Parallel()

		prompts := []vmcp.Prompt{
			{
				Name:        "greet",
				Description: "Greet someone",
				Arguments: []vmcp.PromptArgument{
					{Name: "name", Description: "Who to greet", Required: true},
					{Name: "language", Description: "Language to use", Required: false},
				},
			},
			{
				Name:        "summarize",
				Description: "Summarize text",
			},
		}

		ctrl := gomock.NewController(t)
		factory := sessionfactorymocks.NewMockMultiSessionFactory(ctrl)
		factory.EXPECT().
			MakeSessionWithID(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
			DoAndReturn(func(_ context.Context, id string, _ *auth.Identity, _ bool, _ []*vmcp.Backend) (vmcpsession.MultiSession, error) {
				// Create mock directly (without newMockSession) so there is no
				// pre-existing Prompts().Return(nil).AnyTimes() that would win
				// the FIFO expectation race over our specific prompts list.
				sess := sessionmocks.NewMockMultiSession(ctrl)
				sess.EXPECT().ID().Return(id).AnyTimes()
				sess.EXPECT().GetMetadata().Return(map[string]string{}).AnyTimes()
				sess.EXPECT().Prompts().Return(prompts).AnyTimes()
				return sess, nil
			}).Times(1)

		registry := newFakeRegistry()
		sm, _ := newTestSessionManager(t, factory, registry)

		sessionID := sm.Generate()
		_, err := sm.CreateSession(context.Background(), sessionID)
		require.NoError(t, err)

		adaptedPrompts, err := sm.GetAdaptedPrompts(sessionID)
		require.NoError(t, err)
		require.Len(t, adaptedPrompts, 2)

		byName := map[string]mcp.Prompt{}
		for _, sp := range adaptedPrompts {
			byName[sp.Prompt.Name] = sp.Prompt
		}

		require.Contains(t, byName, "greet")
		assert.Equal(t, "Greet someone", byName["greet"].Description)
		require.Len(t, byName["greet"].Arguments, 2)
		assert.Equal(t, "name", byName["greet"].Arguments[0].Name)
		assert.True(t, byName["greet"].Arguments[0].Required)
		assert.Equal(t, "language", byName["greet"].Arguments[1].Name)
		assert.False(t, byName["greet"].Arguments[1].Required)

		require.Contains(t, byName, "summarize")
		assert.Equal(t, "Summarize text", byName["summarize"].Description)
		assert.Empty(t, byName["summarize"].Arguments)
	})

	t.Run("handler delegates to session GetPrompt", func(t *testing.T) {
		t.Parallel()

		prompts := []vmcp.Prompt{
			{
				Name:        "hello",
				Description: "Say hello",
				Arguments:   []vmcp.PromptArgument{{Name: "name", Required: true}},
			},
		}
		getResult := &vmcp.PromptGetResult{
			Description: "A greeting",
			Messages: []vmcp.PromptMessage{
				{Role: "assistant", Content: vmcp.Content{Type: vmcp.ContentTypeText, Text: "Hello, world!"}},
			},
		}

		ctrl := gomock.NewController(t)
		factory := sessionfactorymocks.NewMockMultiSessionFactory(ctrl)
		factory.EXPECT().
			MakeSessionWithID(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
			DoAndReturn(func(_ context.Context, id string, _ *auth.Identity, _ bool, _ []*vmcp.Backend) (vmcpsession.MultiSession, error) {
				sess := sessionmocks.NewMockMultiSession(ctrl)
				sess.EXPECT().ID().Return(id).AnyTimes()
				sess.EXPECT().GetMetadata().Return(map[string]string{}).AnyTimes()
				sess.EXPECT().Prompts().Return(prompts).AnyTimes()
				sess.EXPECT().GetPrompt(gomock.Any(), gomock.Any(), "hello", gomock.Any()).
					Return(getResult, nil).Times(1)
				return sess, nil
			}).Times(1)

		registry := newFakeRegistry()
		sm, _ := newTestSessionManager(t, factory, registry)

		sessionID := sm.Generate()
		_, err := sm.CreateSession(context.Background(), sessionID)
		require.NoError(t, err)

		adaptedPrompts, err := sm.GetAdaptedPrompts(sessionID)
		require.NoError(t, err)
		require.Len(t, adaptedPrompts, 1)

		req := mcp.GetPromptRequest{}
		req.Params.Name = "hello"
		req.Params.Arguments = map[string]string{"name": "Alice"}
		result, handlerErr := adaptedPrompts[0].Handler(context.Background(), req)
		require.NoError(t, handlerErr)
		require.NotNil(t, result)
		assert.Equal(t, "A greeting", result.Description)
		require.Len(t, result.Messages, 1)
		assert.Equal(t, mcp.RoleAssistant, result.Messages[0].Role)
	})

	t.Run("handler returns error when GetPrompt fails", func(t *testing.T) {
		t.Parallel()

		prompts := []vmcp.Prompt{{Name: "broken"}}
		getErr := errors.New("prompt backend error")

		ctrl := gomock.NewController(t)
		factory := sessionfactorymocks.NewMockMultiSessionFactory(ctrl)
		factory.EXPECT().
			MakeSessionWithID(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
			DoAndReturn(func(_ context.Context, id string, _ *auth.Identity, _ bool, _ []*vmcp.Backend) (vmcpsession.MultiSession, error) {
				sess := sessionmocks.NewMockMultiSession(ctrl)
				sess.EXPECT().ID().Return(id).AnyTimes()
				sess.EXPECT().GetMetadata().Return(map[string]string{}).AnyTimes()
				sess.EXPECT().Prompts().Return(prompts).AnyTimes()
				sess.EXPECT().GetPrompt(gomock.Any(), gomock.Any(), "broken", gomock.Any()).
					Return(nil, getErr).Times(1)
				return sess, nil
			}).Times(1)

		registry := newFakeRegistry()
		sm, _ := newTestSessionManager(t, factory, registry)

		sessionID := sm.Generate()
		_, err := sm.CreateSession(context.Background(), sessionID)
		require.NoError(t, err)

		adaptedPrompts, err := sm.GetAdaptedPrompts(sessionID)
		require.NoError(t, err)
		require.Len(t, adaptedPrompts, 1)

		req := mcp.GetPromptRequest{}
		req.Params.Name = "broken"
		result, handlerErr := adaptedPrompts[0].Handler(context.Background(), req)
		require.Error(t, handlerErr)
		assert.Nil(t, result)
		assert.ErrorContains(t, handlerErr, "prompt backend error")
	})

	t.Run("handler terminates session on authorization errors", func(t *testing.T) {
		t.Parallel()

		testCases := []struct {
			name      string
			authError error
		}{
			{name: "ErrUnauthorizedCaller", authError: sessiontypes.ErrUnauthorizedCaller},
			{name: "ErrNilCaller", authError: sessiontypes.ErrNilCaller},
		}

		for _, tc := range testCases {
			t.Run(tc.name, func(t *testing.T) {
				t.Parallel()

				prompts := []vmcp.Prompt{{Name: "secret"}}
				authErr := tc.authError

				ctrl := gomock.NewController(t)
				factory := sessionfactorymocks.NewMockMultiSessionFactory(ctrl)
				factory.EXPECT().
					MakeSessionWithID(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
					DoAndReturn(func(_ context.Context, id string, _ *auth.Identity, _ bool, _ []*vmcp.Backend) (vmcpsession.MultiSession, error) {
						sess := sessionmocks.NewMockMultiSession(ctrl)
						sess.EXPECT().ID().Return(id).AnyTimes()
						sess.EXPECT().GetMetadata().Return(map[string]string{}).AnyTimes()
						sess.EXPECT().Prompts().Return(prompts).AnyTimes()
						sess.EXPECT().GetPrompt(gomock.Any(), gomock.Any(), "secret", gomock.Any()).
							Return(nil, authErr).Times(1)
						// Close() is called when the session is terminated after auth failure.
						sess.EXPECT().Close().Return(nil).Times(1)
						return sess, nil
					}).Times(1)

				registry := newFakeRegistry()
				sm, _ := newTestSessionManager(t, factory, registry)

				sessionID := sm.Generate()
				_, err := sm.CreateSession(context.Background(), sessionID)
				require.NoError(t, err)

				adaptedPrompts, err := sm.GetAdaptedPrompts(sessionID)
				require.NoError(t, err)
				require.Len(t, adaptedPrompts, 1)

				req := mcp.GetPromptRequest{}
				req.Params.Name = "secret"
				result, handlerErr := adaptedPrompts[0].Handler(context.Background(), req)
				require.Error(t, handlerErr, "handler should return an error for auth failures")
				assert.Nil(t, result)
				assert.ErrorContains(t, handlerErr, "unauthorized")

				// Verify subsequent GetAdaptedPrompts fails (session no longer exists).
				_, err = sm.GetAdaptedPrompts(sessionID)
				assert.Error(t, err, "GetAdaptedPrompts should fail after session termination")
				// gomock verifies Close() was called exactly once via Times(1)
			})
		}
	})
}

// ---------------------------------------------------------------------------
// Tests: DecorateSession
// ---------------------------------------------------------------------------

func TestSessionManager_DecorateSession(t *testing.T) {
	t.Parallel()

	t.Run("replaces session with decorated result", func(t *testing.T) {
		t.Parallel()

		tools := []vmcp.Tool{{Name: "hello", Description: "says hello"}}
		ctrl := gomock.NewController(t)
		factory := sessionfactorymocks.NewMockMultiSessionFactory(ctrl)
		factory.EXPECT().
			MakeSessionWithID(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
			DoAndReturn(func(_ context.Context, id string, _ *auth.Identity, _ bool, _ []*vmcp.Backend) (vmcpsession.MultiSession, error) {
				return newMockSession(t, ctrl, id, tools), nil
			}).Times(1)

		registry := newFakeRegistry()
		sm, _ := newTestSessionManager(t, factory, registry)

		sessionID := sm.Generate()
		require.NotEmpty(t, sessionID)
		_, err := sm.CreateSession(context.Background(), sessionID)
		require.NoError(t, err)

		// Apply a decorator that wraps with an extra tool.
		extraTool := vmcp.Tool{Name: "extra", Description: "extra tool"}
		err = sm.DecorateSession(sessionID, func(sess sessiontypes.MultiSession) sessiontypes.MultiSession {
			decorated := sessionmocks.NewMockMultiSession(ctrl)
			// Delegate everything to base session
			decorated.EXPECT().ID().Return(sess.ID()).AnyTimes()
			decorated.EXPECT().Tools().Return(append(sess.Tools(), extraTool)).AnyTimes()
			// other methods delegated via AnyTimes
			decorated.EXPECT().Type().Return(sess.Type()).AnyTimes()
			decorated.EXPECT().CreatedAt().Return(sess.CreatedAt()).AnyTimes()
			decorated.EXPECT().UpdatedAt().Return(sess.UpdatedAt()).AnyTimes()
			decorated.EXPECT().GetData().Return(nil).AnyTimes()
			decorated.EXPECT().SetData(gomock.Any()).AnyTimes()
			decorated.EXPECT().GetMetadata().Return(map[string]string{}).AnyTimes()
			decorated.EXPECT().SetMetadata(gomock.Any(), gomock.Any()).AnyTimes()
			decorated.EXPECT().BackendSessions().Return(nil).AnyTimes()
			decorated.EXPECT().GetRoutingTable().Return(nil).AnyTimes()
			decorated.EXPECT().Prompts().Return(nil).AnyTimes()
			return decorated
		})
		require.NoError(t, err)

		// After decoration, GetMultiSession returns the decorated session with both tools.
		multiSess, ok := sm.GetMultiSession(sessionID)
		require.True(t, ok)
		require.Len(t, multiSess.Tools(), 2)
		assert.Equal(t, "hello", multiSess.Tools()[0].Name)
		assert.Equal(t, "extra", multiSess.Tools()[1].Name)
	})

	t.Run("returns error for unknown session", func(t *testing.T) {
		t.Parallel()

		ctrl := gomock.NewController(t)
		sm, _ := newTestSessionManager(t, newMockFactory(t, ctrl, newMockSession(t, ctrl, "", nil)), newFakeRegistry())

		err := sm.DecorateSession("ghost-session", func(sess sessiontypes.MultiSession) sessiontypes.MultiSession {
			return sess
		})
		require.Error(t, err)
	})

	t.Run("returns error if session terminated during decoration", func(t *testing.T) {
		t.Parallel()

		// Simulate the race: Terminate() is called between GetMultiSession and
		// UpsertSession. We do this by terminating the session inside the
		// decorator fn, so the re-check that follows fn() sees it is gone.
		ctrl := gomock.NewController(t)
		factory := sessionfactorymocks.NewMockMultiSessionFactory(ctrl)
		// The mock session carries MetadataKeyTokenHash so that:
		// 1. CreateSession stores it in storage (via sess.GetMetadata()), keeping
		//    cache and storage in sync for checkSession's maps.Equal comparison.
		// 2. Terminate sees the key and takes the Phase 2 path (storage.Delete).
		tokenHashMeta := map[string]string{sessiontypes.MetadataKeyTokenHash: ""}
		factory.EXPECT().
			MakeSessionWithID(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
			DoAndReturn(func(_ context.Context, id string, _ *auth.Identity, _ bool, _ []*vmcp.Backend) (vmcpsession.MultiSession, error) {
				sess := sessionmocks.NewMockMultiSession(ctrl)
				sess.EXPECT().ID().Return(id).AnyTimes()
				sess.EXPECT().GetMetadata().Return(tokenHashMeta).AnyTimes()
				sess.EXPECT().Close().Return(nil).AnyTimes()
				// Other methods called by the session manager infrastructure.
				sess.EXPECT().Type().Return(transportsession.SessionType("")).AnyTimes()
				sess.EXPECT().CreatedAt().Return(time.Time{}).AnyTimes()
				sess.EXPECT().UpdatedAt().Return(time.Time{}).AnyTimes()
				sess.EXPECT().GetData().Return(nil).AnyTimes()
				sess.EXPECT().SetData(gomock.Any()).AnyTimes()
				sess.EXPECT().SetMetadata(gomock.Any(), gomock.Any()).AnyTimes()
				sess.EXPECT().BackendSessions().Return(nil).AnyTimes()
				sess.EXPECT().GetRoutingTable().Return(nil).AnyTimes()
				sess.EXPECT().Prompts().Return(nil).AnyTimes()
				sess.EXPECT().Tools().Return(nil).AnyTimes()
				return sess, nil
			}).Times(1)

		sm, _ := newTestSessionManager(t, factory, newFakeRegistry())

		sessionID := sm.Generate()
		require.NotEmpty(t, sessionID)
		_, err := sm.CreateSession(context.Background(), sessionID)
		require.NoError(t, err)

		err = sm.DecorateSession(sessionID, func(sess sessiontypes.MultiSession) sessiontypes.MultiSession {
			// Simulate concurrent Terminate() completing during decoration.
			_, _ = sm.Terminate(sessionID)
			return sess
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "was deleted during decoration")

		// The session must not be resurrected.
		_, ok := sm.GetMultiSession(sessionID)
		assert.False(t, ok, "terminated session must not be resurrected by DecorateSession")
	})
}

// ---------------------------------------------------------------------------
// Tests: checkSession liveness
// ---------------------------------------------------------------------------

// TestSessionManager_CheckSession verifies that checkSession correctly
// distinguishes alive, terminated, and deleted sessions.
func TestSessionManager_CheckSession(t *testing.T) {
	t.Parallel()

	makeFactory := func(t *testing.T) *sessionfactorymocks.MockMultiSessionFactory {
		t.Helper()
		ctrl := gomock.NewController(t)
		f := sessionfactorymocks.NewMockMultiSessionFactory(ctrl)
		f.EXPECT().MakeSessionWithID(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
			AnyTimes().Return(nil, nil)
		f.EXPECT().RestoreSession(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
			AnyTimes().Return(nil, nil)
		return f
	}

	makeEmptySess := func(t *testing.T) vmcpsession.MultiSession {
		t.Helper()
		ctrl := gomock.NewController(t)
		m := sessionmocks.NewMockMultiSession(ctrl)
		m.EXPECT().GetMetadata().Return(map[string]string{}).AnyTimes()
		return m
	}

	t.Run("alive session returns nil", func(t *testing.T) {
		t.Parallel()
		sm, storage := newTestSessionManager(t, makeFactory(t), newFakeRegistry())
		sessionID := "alive-session"
		_, err := storage.Create(context.Background(), sessionID, map[string]string{})
		require.NoError(t, err)

		err = sm.checkSession(sessionID, makeEmptySess(t))
		assert.NoError(t, err, "alive session must return nil")
	})

	t.Run("deleted session returns ErrExpired", func(t *testing.T) {
		t.Parallel()
		sm, _ := newTestSessionManager(t, makeFactory(t), newFakeRegistry())

		err := sm.checkSession("nonexistent-session", makeEmptySess(t))
		assert.ErrorIs(t, err, cache.ErrExpired, "deleted session must return ErrExpired")
	})

	t.Run("terminated session returns ErrExpired", func(t *testing.T) {
		t.Parallel()
		// A session terminated on another pod: storage entry exists but
		// MetadataKeyTerminated is set. checkSession must return ErrExpired
		// so the cache evicts the entry and onEvict closes backend connections.
		sm, storage := newTestSessionManager(t, makeFactory(t), newFakeRegistry())
		sessionID := "terminated-session"
		_, err := storage.Create(context.Background(), sessionID, map[string]string{
			MetadataKeyTerminated: MetadataValTrue,
		})
		require.NoError(t, err)

		err = sm.checkSession(sessionID, makeEmptySess(t))
		assert.ErrorIs(t, err, cache.ErrExpired, "terminated session must return ErrExpired")
	})

	t.Run("stale backend list triggers cross-pod eviction", func(t *testing.T) {
		t.Parallel()
		// Simulate pod B holding a cached session with backends [A, B] while
		// pod A has already written updated metadata with only [B] to storage.
		// checkSession must return ErrExpired so the stale entry is evicted and
		// the next GetMultiSession triggers RestoreSession with the fresh list.
		sm, storage := newTestSessionManager(t, makeFactory(t), newFakeRegistry())
		sessionID := "stale-session"

		// Seed storage with the up-to-date backend list (backend-a expired).
		_, err := storage.Create(context.Background(), sessionID, map[string]string{
			vmcpsession.MetadataKeyBackendIDs: "backend-b",
		})
		require.NoError(t, err)

		// Inject a cached session whose metadata still lists both backends,
		// simulating what this pod had before it learned about the expiry.
		ctrl := gomock.NewController(t)
		cached := sessionmocks.NewMockMultiSession(ctrl)
		cached.EXPECT().GetMetadata().Return(map[string]string{
			vmcpsession.MetadataKeyBackendIDs: "backend-a,backend-b",
		}).AnyTimes()
		sm.sessions.Set(sessionID, cached)

		err = sm.checkSession(sessionID, cached)
		assert.ErrorIs(t, err, cache.ErrExpired,
			"stale backend list must return ErrExpired to trigger cross-pod eviction")
	})

	t.Run("matching backend list returns nil", func(t *testing.T) {
		t.Parallel()
		sm, storage := newTestSessionManager(t, makeFactory(t), newFakeRegistry())
		sessionID := "fresh-session"

		_, err := storage.Create(context.Background(), sessionID, map[string]string{
			vmcpsession.MetadataKeyBackendIDs: "backend-a",
		})
		require.NoError(t, err)

		ctrl := gomock.NewController(t)
		cached := sessionmocks.NewMockMultiSession(ctrl)
		cached.EXPECT().GetMetadata().Return(map[string]string{
			vmcpsession.MetadataKeyBackendIDs: "backend-a",
		}).AnyTimes()
		sm.sessions.Set(sessionID, cached)

		err = sm.checkSession(sessionID, cached)
		assert.NoError(t, err, "matching backend list must return nil")
	})

	t.Run("matching metadata with no MetadataKeyBackendIDs does not evict", func(t *testing.T) {
		t.Parallel()
		// Sessions whose cached metadata exactly matches storage — including
		// having no MetadataKeyBackendIDs — must not trigger eviction.
		sm, storage := newTestSessionManager(t, makeFactory(t), newFakeRegistry())
		sessionID := "no-ids-session"

		_, err := storage.Create(context.Background(), sessionID, map[string]string{})
		require.NoError(t, err)

		ctrl := gomock.NewController(t)
		cached := sessionmocks.NewMockMultiSession(ctrl)
		cached.EXPECT().GetMetadata().Return(map[string]string{}).AnyTimes()
		sm.sessions.Set(sessionID, cached)

		err = sm.checkSession(sessionID, cached)
		assert.NoError(t, err, "matching empty metadata must not cause eviction")
	})
}

// ---------------------------------------------------------------------------
// NotifyBackendExpired tests
// ---------------------------------------------------------------------------

func TestNotifyBackendExpired(t *testing.T) {
	t.Parallel()

	// seedBackendMetadata stores backend metadata directly in storage so that
	// NotifyBackendExpired has something to operate on. This simulates what
	// populateBackendMetadata writes during session creation. It returns the
	// metadata map so callers can pass it directly to NotifyBackendExpired.
	seedBackendMetadata := func(t *testing.T, storage transportsession.DataStorage, sessionID string, ids []string, sessionIDs map[string]string) map[string]string {
		t.Helper()
		meta := map[string]string{
			vmcpsession.MetadataKeyBackendIDs: strings.Join(ids, ","),
		}
		for workloadID, sessID := range sessionIDs {
			meta[vmcpsession.MetadataKeyBackendSessionPrefix+workloadID] = sessID
		}
		updated, err := storage.Update(context.Background(), sessionID, meta)
		require.NoError(t, err)
		require.True(t, updated, "session must exist before seeding backend metadata")
		return meta
	}

	t.Run("clears backend session key and removes from MetadataKeyBackendIDs", func(t *testing.T) {
		t.Parallel()

		ctrl := gomock.NewController(t)
		registry := newFakeRegistry()
		sess := newMockSession(t, ctrl, "s", nil)
		sess.EXPECT().Close().Return(nil).AnyTimes()
		factory := newMockFactory(t, ctrl, sess)
		sm, storage := newTestSessionManager(t, factory, registry)

		sessionID := sm.Generate()
		_, err := sm.CreateSession(t.Context(), sessionID)
		require.NoError(t, err)

		meta := seedBackendMetadata(t, storage, sessionID,
			[]string{"workload-a", "workload-b"},
			map[string]string{"workload-a": "sess-a", "workload-b": "sess-b"},
		)

		metaBefore := maps.Clone(meta)
		sm.NotifyBackendExpired(sessionID, "workload-a", meta)
		assert.Equal(t, metaBefore, meta, "NotifyBackendExpired must not mutate the caller's metadata map")

		got, loadErr := storage.Load(context.Background(), sessionID)
		require.NoError(t, loadErr)
		assert.Equal(t, "workload-b", got[vmcpsession.MetadataKeyBackendIDs])
		assert.Empty(t, got[vmcpsession.MetadataKeyBackendSessionPrefix+"workload-a"],
			"per-backend session key must be cleared")
		assert.Equal(t, "sess-b", got[vmcpsession.MetadataKeyBackendSessionPrefix+"workload-b"],
			"survivor backend session key must be unchanged")
	})

	t.Run("removes last backend: MetadataKeyBackendIDs becomes empty", func(t *testing.T) {
		t.Parallel()

		ctrl := gomock.NewController(t)
		registry := newFakeRegistry()
		sess := newMockSession(t, ctrl, "s", nil)
		sess.EXPECT().Close().Return(nil).AnyTimes()
		factory := newMockFactory(t, ctrl, sess)
		sm, storage := newTestSessionManager(t, factory, registry)

		sessionID := sm.Generate()
		_, err := sm.CreateSession(t.Context(), sessionID)
		require.NoError(t, err)

		meta := seedBackendMetadata(t, storage, sessionID,
			[]string{"workload-a"},
			map[string]string{"workload-a": "sess-a"},
		)

		metaBefore := maps.Clone(meta)
		sm.NotifyBackendExpired(sessionID, "workload-a", meta)
		assert.Equal(t, metaBefore, meta, "NotifyBackendExpired must not mutate the caller's metadata map")

		got, loadErr := storage.Load(context.Background(), sessionID)
		require.NoError(t, loadErr)
		backendIDs, present := got[vmcpsession.MetadataKeyBackendIDs]
		assert.True(t, present, "MetadataKeyBackendIDs must be present even when no backends remain")
		assert.Empty(t, backendIDs, "MetadataKeyBackendIDs must be empty string when no backends remain")
		_, sessionKeyPresent := got[vmcpsession.MetadataKeyBackendSessionPrefix+"workload-a"]
		assert.False(t, sessionKeyPresent, "per-backend session key must be absent after expiry")
	})

	t.Run("absent MetadataKeyBackendIDs is a no-op (corrupted metadata)", func(t *testing.T) {
		t.Parallel()

		ctrl := gomock.NewController(t)
		factory := sessionfactorymocks.NewMockMultiSessionFactory(ctrl)
		sm, storage := newTestSessionManager(t, factory, newFakeRegistry())

		sessionID := sm.Generate()
		// Seed metadata that is missing MetadataKeyBackendIDs — simulates
		// corrupted or partially-written storage.
		corruptedMeta := map[string]string{
			vmcpsession.MetadataKeyBackendSessionPrefix + "workload-a": "sess-a",
			// MetadataKeyBackendIDs intentionally absent
		}
		_, err := storage.Update(context.Background(), sessionID, corruptedMeta)
		require.NoError(t, err)

		corruptedMetaBefore := maps.Clone(corruptedMeta)
		sm.NotifyBackendExpired(sessionID, "workload-a", corruptedMeta)
		assert.Equal(t, corruptedMetaBefore, corruptedMeta, "NotifyBackendExpired must not mutate the caller's metadata map")

		// Storage must be unchanged — clobbering with "" would drop all backends.
		got, loadErr := storage.Load(context.Background(), sessionID)
		require.NoError(t, loadErr)
		_, present := got[vmcpsession.MetadataKeyBackendIDs]
		assert.False(t, present, "MetadataKeyBackendIDs must remain absent when it was not present")
		assert.Equal(t, "sess-a", got[vmcpsession.MetadataKeyBackendSessionPrefix+"workload-a"],
			"storage must not be modified when MetadataKeyBackendIDs is absent")
	})

	t.Run("unknown session is silently ignored", func(t *testing.T) {
		t.Parallel()

		ctrl := gomock.NewController(t)
		factory := sessionfactorymocks.NewMockMultiSessionFactory(ctrl)
		sm, _ := newTestSessionManager(t, factory, newFakeRegistry())

		sm.NotifyBackendExpired("nonexistent-session", "workload-a", nil) // must not panic
	})

	t.Run("placeholder session (no backend IDs) is a no-op", func(t *testing.T) {
		t.Parallel()

		ctrl := gomock.NewController(t)
		factory := sessionfactorymocks.NewMockMultiSessionFactory(ctrl)
		sm, storage := newTestSessionManager(t, factory, newFakeRegistry())

		// Generate creates a placeholder with empty metadata.
		sessionID := sm.Generate()
		sm.NotifyBackendExpired(sessionID, "workload-a", map[string]string{})

		// Placeholder must still exist and be unmodified.
		got, loadErr := storage.Load(context.Background(), sessionID)
		require.NoError(t, loadErr)
		assert.Empty(t, got[vmcpsession.MetadataKeyBackendIDs])
	})

	t.Run("terminated session is not resurrected", func(t *testing.T) {
		t.Parallel()

		ctrl := gomock.NewController(t)
		registry := newFakeRegistry()
		sess := newMockSession(t, ctrl, "s", nil)
		sess.EXPECT().Close().Return(nil).AnyTimes()
		factory := newMockFactory(t, ctrl, sess)
		sm, storage := newTestSessionManager(t, factory, registry)

		sessionID := sm.Generate()
		_, err := sm.CreateSession(t.Context(), sessionID)
		require.NoError(t, err)

		// Seed MetadataKeyTokenHash into storage so Terminate recognises this
		// as a Phase 2 (full MultiSession) and deletes rather than marks terminated.
		_, err = storage.Update(context.Background(), sessionID, map[string]string{
			sessiontypes.MetadataKeyTokenHash: "",
		})
		require.NoError(t, err)

		_, err = sm.Terminate(sessionID)
		require.NoError(t, err)

		// Caller holds the metadata it observed before termination; updateMetadata's
		// SET XX is a no-op because Terminate already deleted the key.
		sm.NotifyBackendExpired(sessionID, "workload-a", map[string]string{
			vmcpsession.MetadataKeyBackendIDs: "workload-a",
		})

		// Session must remain absent — Load after Terminate deletes from storage.
		_, loadErr := storage.Load(context.Background(), sessionID)
		assert.ErrorIs(t, loadErr, transportsession.ErrSessionNotFound,
			"terminated session must not be resurrected by NotifyBackendExpired")
	})

	t.Run("same-pod termination: storage.Update returns false, no resurrection", func(t *testing.T) {
		t.Parallel()

		// Verify that updateMetadata's storage.Update (SET XX) prevents
		// resurrection even when Terminate runs concurrently on the same pod.
		// We model Terminate completing (key deleted) before updateMetadata
		// reaches its storage.Update call.
		ctrl := gomock.NewController(t)
		registry := newFakeRegistry()
		sess := newMockSession(t, ctrl, "s", nil)
		sess.EXPECT().Close().Return(nil).AnyTimes()
		factory := newMockFactory(t, ctrl, sess)
		sm, storage := newTestSessionManager(t, factory, registry)

		sessionID := sm.Generate()
		_, err := sm.CreateSession(t.Context(), sessionID)
		require.NoError(t, err)

		meta := seedBackendMetadata(t, storage, sessionID,
			[]string{"workload-a"},
			map[string]string{"workload-a": "sess-a"},
		)

		// Simulate Terminate having completed its storage.Delete already.
		require.NoError(t, storage.Delete(context.Background(), sessionID))

		// storage.Update (SET XX) in updateMetadata returns (false, nil) because
		// the key no longer exists — NotifyBackendExpired must bail without
		// recreating the record.
		metaBefore := maps.Clone(meta)
		sm.NotifyBackendExpired(sessionID, "workload-a", meta)
		assert.Equal(t, metaBefore, meta, "NotifyBackendExpired must not mutate the caller's metadata map")

		_, loadErr := storage.Load(context.Background(), sessionID)
		assert.ErrorIs(t, loadErr, transportsession.ErrSessionNotFound,
			"NotifyBackendExpired must not resurrect a session whose storage key was deleted by Terminate")
	})

	t.Run("cross-pod termination: absent storage key is a no-op (no resurrection)", func(t *testing.T) {
		t.Parallel()

		ctrl := gomock.NewController(t)
		registry := newFakeRegistry()
		sess := newMockSession(t, ctrl, "s", nil)
		sess.EXPECT().Close().Return(nil).AnyTimes()
		factory := newMockFactory(t, ctrl, sess)
		sm, storage := newTestSessionManager(t, factory, registry)

		sessionID := sm.Generate()
		_, err := sm.CreateSession(t.Context(), sessionID)
		require.NoError(t, err)

		meta := seedBackendMetadata(t, storage, sessionID,
			[]string{"workload-a"},
			map[string]string{"workload-a": "sess-a"},
		)

		// Simulate cross-pod termination: another pod called storage.Delete while
		// this pod was inside NotifyBackendExpired (before the Upsert).
		// We delete the key here to represent that state.
		require.NoError(t, storage.Delete(context.Background(), sessionID))

		// updateMetadata's SET XX sees the absent key and bails without recreating.
		metaBefore := maps.Clone(meta)
		sm.NotifyBackendExpired(sessionID, "workload-a", meta)
		assert.Equal(t, metaBefore, meta, "NotifyBackendExpired must not mutate the caller's metadata map")

		_, loadErr := storage.Load(context.Background(), sessionID)
		assert.ErrorIs(t, loadErr, transportsession.ErrSessionNotFound,
			"NotifyBackendExpired must not resurrect a session terminated by another pod")
	})

	t.Run("lazy eviction: session stays in cache immediately after NotifyBackendExpired", func(t *testing.T) {
		t.Parallel()

		ctrl := gomock.NewController(t)
		registry := newFakeRegistry()
		sess := newMockSession(t, ctrl, "s", nil)
		sess.EXPECT().Close().Return(nil).AnyTimes()
		factory := newMockFactory(t, ctrl, sess)
		sm, storage := newTestSessionManager(t, factory, registry)

		sessionID := sm.Generate()
		_, err := sm.CreateSession(t.Context(), sessionID)
		require.NoError(t, err)

		// Session must be in cache after CreateSession.
		assert.Equal(t, 1, sm.sessions.Len(), "session must be in node-local cache after CreateSession")

		meta := seedBackendMetadata(t, storage, sessionID,
			[]string{"workload-a"},
			map[string]string{"workload-a": "sess-a"},
		)

		metaBefore := maps.Clone(meta)
		sm.NotifyBackendExpired(sessionID, "workload-a", meta)
		assert.Equal(t, metaBefore, meta, "NotifyBackendExpired must not mutate the caller's metadata map")

		// With lazy eviction, session is still in cache immediately after NotifyBackendExpired.
		// checkSession detects drift on the next GetMultiSession call.
		assert.Equal(t, 1, sm.sessions.Len(),
			"session must still be in cache immediately after NotifyBackendExpired (eviction is lazy)")
	})
}

// ---------------------------------------------------------------------------
// Helper
// ---------------------------------------------------------------------------

// newCallToolRequest builds a minimal mcp.CallToolRequest for handler tests.
func newCallToolRequest(name string, args map[string]any) mcp.CallToolRequest {
	req := mcp.CallToolRequest{}
	req.Params.Name = name
	req.Params.Arguments = args
	return req
}

// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package sessionmanager

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/auth"
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

// alwaysFailDataStorage is a SessionDataStorage whose Store() always returns an
// error. It is used to exercise the Generate() double-failure path (UUID collision
// simulation — both attempts to Store fail, so Generate() must return "").
type alwaysFailDataStorage struct{}

func (alwaysFailDataStorage) Store(_ context.Context, _ string, _ map[string]string) error {
	return errors.New("storage unavailable")
}
func (alwaysFailDataStorage) Load(_ context.Context, _ string) (map[string]string, error) {
	return nil, transportsession.ErrSessionNotFound
}
func (alwaysFailDataStorage) Exists(_ context.Context, _ string) (bool, error) {
	return false, errors.New("storage unavailable")
}
func (alwaysFailDataStorage) StoreIfAbsent(_ context.Context, _ string, _ map[string]string) (bool, error) {
	return false, errors.New("storage unavailable")
}
func (alwaysFailDataStorage) Delete(_ context.Context, _ string) error { return nil }
func (alwaysFailDataStorage) Close() error                             { return nil }

// configurableFailDataStorage wraps a real SessionDataStorage and allows injecting
// failures for specific operations. Used to test fallback behavior in Terminate().
type configurableFailDataStorage struct {
	transportsession.DataStorage
	storeCallCount int
	failStoreAfter int // fail Store/StoreIfAbsent after this many successful calls (0 = never fail, -1 = always fail)
	failDelete     bool
}

func (s *configurableFailDataStorage) shouldFail() bool {
	s.storeCallCount++
	return s.failStoreAfter == -1 || (s.failStoreAfter >= 0 && s.storeCallCount > s.failStoreAfter)
}

func (s *configurableFailDataStorage) Store(ctx context.Context, id string, metadata map[string]string) error {
	if s.shouldFail() {
		return errors.New("injected Store failure")
	}
	return s.DataStorage.Store(ctx, id, metadata)
}

func (s *configurableFailDataStorage) StoreIfAbsent(ctx context.Context, id string, metadata map[string]string) (bool, error) {
	if s.shouldFail() {
		return false, errors.New("injected Store failure")
	}
	return s.DataStorage.StoreIfAbsent(ctx, id, metadata)
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
// The cleanup goroutine is stopped via t.Cleanup.
func newTestSessionDataStorage(t *testing.T) *transportsession.LocalSessionDataStorage {
	t.Helper()
	storage := transportsession.NewLocalSessionDataStorage(30 * time.Minute)
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
	sm, stop, err := New(storage, 30*time.Minute, &FactoryConfig{Base: factory}, registry)
	require.NoError(t, err)
	t.Cleanup(func() { _ = stop(context.Background()) })
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
		assert.NoError(t, loadErr, "placeholder should be stored in transport manager")
	})

	t.Run("returns empty string when storage always fails", func(t *testing.T) {
		t.Parallel()

		// Use a Manager backed by storage that always fails Store(), forcing both
		// UUID attempts inside Generate() to fail so it must return "".
		ctrl := gomock.NewController(t)
		sess := newMockSession(t, ctrl, "placeholder", nil)
		factory := newMockFactory(t, ctrl, sess)
		sm, stop, err := New(alwaysFailDataStorage{}, time.Hour, &FactoryConfig{Base: factory}, newFakeRegistry())
		require.NoError(t, err)
		t.Cleanup(func() { _ = stop(context.Background()) })

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

		// Session metadata must still exist in storage and the live session
		// must be accessible via GetMultiSession.
		_, loadErr := storage.Load(context.Background(), sessionID)
		require.NoError(t, loadErr, "session metadata should still exist in storage")
		_, isMulti := sm.GetMultiSession(sessionID)
		assert.True(t, isMulti, "session should be retrievable as a MultiSession")
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

		tools := []vmcp.Tool{{Name: "t1", Description: "tool 1"}}
		ctrl := gomock.NewController(t)

		var createdSess *sessionmocks.MockMultiSession
		factory := sessionfactorymocks.NewMockMultiSessionFactory(ctrl)
		factory.EXPECT().
			MakeSessionWithID(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
			DoAndReturn(func(_ context.Context, id string, _ *auth.Identity, _ bool, _ []*vmcp.Backend) (vmcpsession.MultiSession, error) {
				createdSess = newMockSession(t, ctrl, id, tools)
				// Close() will be called exactly once during Terminate
				createdSess.EXPECT().Close().Return(nil).Times(1)
				return createdSess, nil
			}).Times(1)

		registry := newFakeRegistry()
		sm, _ := newTestSessionManager(t, factory, registry)

		sessionID := sm.Generate()
		require.NotEmpty(t, sessionID)

		// Upgrade to full MultiSession.
		_, err := sm.CreateSession(context.Background(), sessionID)
		require.NoError(t, err)
		require.NotNil(t, createdSess)

		// Terminate should close the backend connections.
		isNotAllowed, err := sm.Terminate(sessionID)
		require.NoError(t, err)
		assert.False(t, isNotAllowed)
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
				sess.EXPECT().Close().Return(nil).Times(1)
				return sess, nil
			}).Times(1)

		registry := newFakeRegistry()
		sm, storage := newTestSessionManager(t, factory, registry)

		sessionID := sm.Generate()
		require.NotEmpty(t, sessionID)

		_, err := sm.CreateSession(context.Background(), sessionID)
		require.NoError(t, err)

		// Session must exist before termination.
		_, loadErr := storage.Load(context.Background(), sessionID)
		assert.NoError(t, loadErr, "session should exist before termination")

		_, err = sm.Terminate(sessionID)
		require.NoError(t, err)

		// Session must be removed from storage.
		_, loadErr2 := storage.Load(context.Background(), sessionID)
		assert.ErrorIs(t, loadErr2, transportsession.ErrSessionNotFound, "session should be deleted from storage after Terminate")
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

		// Create a storage that succeeds on the first Store (Generate creates
		// placeholder) but fails on the second Store (Terminate tries to store
		// terminated flag). Delete succeeds. This tests the fallback path in Terminate().
		baseStorage := transportsession.NewLocalSessionDataStorage(time.Hour)
		t.Cleanup(func() { _ = baseStorage.Close() })
		failingStorage := &configurableFailDataStorage{
			DataStorage:    baseStorage,
			failStoreAfter: 1, // fail after 1 successful Store
			failDelete:     false,
		}
		sm, stop, err := New(failingStorage, time.Hour, &FactoryConfig{Base: factory}, registry)
		require.NoError(t, err)
		t.Cleanup(func() { _ = stop(context.Background()) })

		// Generate a placeholder (first Store, succeeds).
		sessionID := sm.Generate()
		require.NotEmpty(t, sessionID)

		// Terminate should succeed via the delete fallback (second Store fails, Delete succeeds).
		isNotAllowed, err := sm.Terminate(sessionID)
		require.NoError(t, err)
		assert.False(t, isNotAllowed)

		// Placeholder should be deleted (not just marked terminated).
		_, loadErr := baseStorage.Load(context.Background(), sessionID)
		assert.ErrorIs(t, loadErr, transportsession.ErrSessionNotFound, "placeholder should be deleted when upsert fails")
	})

	t.Run("placeholder termination fails when both upsert and delete fail", func(t *testing.T) {
		t.Parallel()

		ctrl := gomock.NewController(t)
		sess := newMockSession(t, ctrl, "", nil)
		factory := newMockFactory(t, ctrl, sess)
		registry := newFakeRegistry()

		// Create a storage that succeeds on the first Store (Generate creates
		// placeholder) but fails on the second Store (Terminate tries to store
		// terminated flag) and also fails on Delete. This forces the error path.
		baseStorage := transportsession.NewLocalSessionDataStorage(time.Hour)
		t.Cleanup(func() { _ = baseStorage.Close() })
		failingStorage := &configurableFailDataStorage{
			DataStorage:    baseStorage,
			failStoreAfter: 1, // fail after 1 successful Store
			failDelete:     true,
		}
		sm, stop, err := New(failingStorage, time.Hour, &FactoryConfig{Base: factory}, registry)
		require.NoError(t, err)
		t.Cleanup(func() { _ = stop(context.Background()) })

		// Generate a placeholder (first Store, succeeds).
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

	// The following tests cover the cache-miss path — the scenario that was
	// broken before this fix. When Redis is the storage backend and a request
	// arrives on a pod that has never seen the session (cold local cache),
	// GetMultiSession must fall back to storage.Load + factory.RestoreSession
	// to reconstruct the live MultiSession. Without this path, every session
	// dies on its second request when served by a different pod.

	t.Run("cold cache: restores session from storage via RestoreSession", func(t *testing.T) {
		t.Parallel()

		// Simulate the "other pod wrote this session" scenario: inject metadata
		// directly into storage, bypassing Generate/CreateSession, and create a
		// fresh manager whose multiSessions cache is empty.
		storage := newTestSessionDataStorage(t)
		const sessionID = "cold-cache-session"
		storedMeta := map[string]string{
			vmcpsession.MetadataKeyBackendIDs: "backend-1",
		}
		require.NoError(t, storage.Store(context.Background(), sessionID, storedMeta))

		tools := []vmcp.Tool{{Name: "restored-tool", Description: "from restore"}}
		ctrl := gomock.NewController(t)
		restoredSess := newMockSession(t, ctrl, sessionID, tools)

		factory := sessionfactorymocks.NewMockMultiSessionFactory(ctrl)
		// MakeSessionWithID must NOT be called — this is a cold-cache restore, not a new session.
		factory.EXPECT().
			MakeSessionWithID(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
			Times(0)
		factory.EXPECT().
			RestoreSession(gomock.Any(), sessionID, storedMeta, gomock.Any()).
			Return(restoredSess, nil).
			Times(1)

		sm, stop, err := New(storage, 30*time.Minute, &FactoryConfig{Base: factory}, newFakeRegistry())
		require.NoError(t, err)
		t.Cleanup(func() { _ = stop(context.Background()) })

		multiSess, ok := sm.GetMultiSession(sessionID)
		require.True(t, ok, "should restore session from storage on cache miss")
		require.NotNil(t, multiSess)
		assert.Equal(t, sessionID, multiSess.ID())
		require.Len(t, multiSess.Tools(), 1)
		assert.Equal(t, "restored-tool", multiSess.Tools()[0].Name)
	})

	t.Run("cold cache: restored session is cached so RestoreSession is only called once", func(t *testing.T) {
		t.Parallel()

		storage := newTestSessionDataStorage(t)
		const sessionID = "cache-after-restore-session"
		storedMeta := map[string]string{
			vmcpsession.MetadataKeyBackendIDs: "backend-1",
		}
		require.NoError(t, storage.Store(context.Background(), sessionID, storedMeta))

		ctrl := gomock.NewController(t)
		restoredSess := newMockSession(t, ctrl, sessionID, nil)

		factory := sessionfactorymocks.NewMockMultiSessionFactory(ctrl)
		factory.EXPECT().
			RestoreSession(gomock.Any(), sessionID, storedMeta, gomock.Any()).
			Return(restoredSess, nil).
			Times(1) // must be called exactly once despite multiple GetMultiSession calls

		sm, stop, err := New(storage, 30*time.Minute, &FactoryConfig{Base: factory}, newFakeRegistry())
		require.NoError(t, err)
		t.Cleanup(func() { _ = stop(context.Background()) })

		// First call: cache miss, RestoreSession is invoked.
		sess1, ok1 := sm.GetMultiSession(sessionID)
		require.True(t, ok1)

		// Second call: hits the node-local cache, RestoreSession must NOT be called again.
		sess2, ok2 := sm.GetMultiSession(sessionID)
		require.True(t, ok2)
		assert.Same(t, sess1, sess2, "second lookup should return the cached instance")
	})

	t.Run("cold cache: placeholder (no BackendIDs) is not restored", func(t *testing.T) {
		t.Parallel()

		storage := newTestSessionDataStorage(t)
		const sessionID = "placeholder-session"
		// Placeholder: metadata exists but BackendIDs is empty.
		require.NoError(t, storage.Store(context.Background(), sessionID, map[string]string{}))

		ctrl := gomock.NewController(t)
		factory := sessionfactorymocks.NewMockMultiSessionFactory(ctrl)
		factory.EXPECT().RestoreSession(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Times(0)

		sm, stop, err := New(storage, 30*time.Minute, &FactoryConfig{Base: factory}, newFakeRegistry())
		require.NoError(t, err)
		t.Cleanup(func() { _ = stop(context.Background()) })

		multiSess, ok := sm.GetMultiSession(sessionID)
		assert.False(t, ok, "placeholder should not be restored")
		assert.Nil(t, multiSess)
	})

	t.Run("cold cache: terminated session is not restored", func(t *testing.T) {
		t.Parallel()

		storage := newTestSessionDataStorage(t)
		const sessionID = "terminated-session"
		require.NoError(t, storage.Store(context.Background(), sessionID, map[string]string{
			vmcpsession.MetadataKeyBackendIDs: "backend-1",
			MetadataKeyTerminated:             MetadataValTrue,
		}))

		ctrl := gomock.NewController(t)
		factory := sessionfactorymocks.NewMockMultiSessionFactory(ctrl)
		factory.EXPECT().RestoreSession(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Times(0)

		sm, stop, err := New(storage, 30*time.Minute, &FactoryConfig{Base: factory}, newFakeRegistry())
		require.NoError(t, err)
		t.Cleanup(func() { _ = stop(context.Background()) })

		multiSess, ok := sm.GetMultiSession(sessionID)
		assert.False(t, ok, "terminated session should not be restored")
		assert.Nil(t, multiSess)
	})

	t.Run("cold cache: RestoreSession error returns nil", func(t *testing.T) {
		t.Parallel()

		storage := newTestSessionDataStorage(t)
		const sessionID = "restore-error-session"
		storedMeta := map[string]string{
			vmcpsession.MetadataKeyBackendIDs: "backend-1",
		}
		require.NoError(t, storage.Store(context.Background(), sessionID, storedMeta))

		ctrl := gomock.NewController(t)
		factory := sessionfactorymocks.NewMockMultiSessionFactory(ctrl)
		factory.EXPECT().
			RestoreSession(gomock.Any(), sessionID, storedMeta, gomock.Any()).
			Return(nil, errors.New("backend unreachable")).
			Times(1)

		sm, stop, err := New(storage, 30*time.Minute, &FactoryConfig{Base: factory}, newFakeRegistry())
		require.NoError(t, err)
		t.Cleanup(func() { _ = stop(context.Background()) })

		multiSess, ok := sm.GetMultiSession(sessionID)
		assert.False(t, ok, "RestoreSession error should result in (nil, false)")
		assert.Nil(t, multiSess)
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
		factory.EXPECT().
			MakeSessionWithID(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
			DoAndReturn(func(_ context.Context, id string, _ *auth.Identity, _ bool, _ []*vmcp.Backend) (vmcpsession.MultiSession, error) {
				sess := newMockSession(t, ctrl, id, nil)
				sess.EXPECT().Close().Return(nil).AnyTimes()
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
		assert.Contains(t, err.Error(), "terminated during decoration")

		// The session must not be resurrected.
		_, ok := sm.GetMultiSession(sessionID)
		assert.False(t, ok, "terminated session must not be resurrected by DecorateSession")
	})
}

// ---------------------------------------------------------------------------
// Tests: evictExpiredMultiSessions
// ---------------------------------------------------------------------------

func TestSessionManager_EvictExpiredMultiSessions(t *testing.T) {
	t.Parallel()

	t.Run("evicts session absent from storage and calls Close", func(t *testing.T) {
		t.Parallel()

		ctrl := gomock.NewController(t)
		factory := sessionfactorymocks.NewMockMultiSessionFactory(ctrl)
		factory.EXPECT().
			MakeSessionWithID(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
			DoAndReturn(func(_ context.Context, id string, _ *auth.Identity, _ bool, _ []*vmcp.Backend) (vmcpsession.MultiSession, error) {
				sess := newMockSession(t, ctrl, id, nil)
				sess.EXPECT().Close().Return(nil).Times(1)
				return sess, nil
			}).Times(1)

		sm, storage := newTestSessionManager(t, factory, newFakeRegistry())

		sessionID := sm.Generate()
		require.NotEmpty(t, sessionID)
		_, err := sm.CreateSession(context.Background(), sessionID)
		require.NoError(t, err)

		// Simulate TTL expiry: remove from storage while leaving in multiSessions.
		require.NoError(t, storage.Delete(context.Background(), sessionID))

		sm.evictExpiredMultiSessions()

		_, stillPresent := sm.multiSessions.Load(sessionID)
		assert.False(t, stillPresent, "evicted session must be removed from node-local map")
	})

	t.Run("retains session present in storage", func(t *testing.T) {
		t.Parallel()

		ctrl := gomock.NewController(t)
		factory := sessionfactorymocks.NewMockMultiSessionFactory(ctrl)
		factory.EXPECT().
			MakeSessionWithID(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
			DoAndReturn(func(_ context.Context, id string, _ *auth.Identity, _ bool, _ []*vmcp.Backend) (vmcpsession.MultiSession, error) {
				return newMockSession(t, ctrl, id, nil), nil
			}).Times(1)

		sm, _ := newTestSessionManager(t, factory, newFakeRegistry())

		sessionID := sm.Generate()
		require.NotEmpty(t, sessionID)
		_, err := sm.CreateSession(context.Background(), sessionID)
		require.NoError(t, err)

		// Storage still contains the session — eviction must leave it alone.
		sm.evictExpiredMultiSessions()

		_, stillPresent := sm.multiSessions.Load(sessionID)
		assert.True(t, stillPresent, "live session must remain in node-local map")
	})

	t.Run("skips session when storage returns an error", func(t *testing.T) {
		t.Parallel()

		ctrl := gomock.NewController(t)
		// alwaysFailDataStorage.Exists returns an error, so eviction must skip the entry.
		// Close() is intentionally not configured — any call would fail the test.
		sess := newMockSession(t, ctrl, "error-sess", nil)
		sm, stop, err := New(alwaysFailDataStorage{}, time.Hour, &FactoryConfig{Base: newMockFactory(t, ctrl, sess)}, newFakeRegistry())
		require.NoError(t, err)
		t.Cleanup(func() { _ = stop(context.Background()) })

		// Directly plant the session to bypass Generate/CreateSession
		// (both of which would fail against alwaysFailDataStorage).
		sm.multiSessions.Store("error-sess", sess)

		sm.evictExpiredMultiSessions()

		_, stillPresent := sm.multiSessions.Load("error-sess")
		assert.True(t, stillPresent, "session must not be evicted when storage.Exists returns an error")
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

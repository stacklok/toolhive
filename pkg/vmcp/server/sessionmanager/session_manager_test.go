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
	sess.EXPECT().Touch().AnyTimes()
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

// alwaysFailStorage is a transportsession.Storage whose Store() always returns an
// error. It is used to exercise the Generate() double-failure path (UUID collision
// simulation — both attempts to AddWithID fail, so Generate() must return "").
type alwaysFailStorage struct{}

func (alwaysFailStorage) Store(_ context.Context, _ transportsession.Session) error {
	return errors.New("storage unavailable")
}
func (alwaysFailStorage) Load(_ context.Context, _ string) (transportsession.Session, error) {
	return nil, errors.New("not found")
}
func (alwaysFailStorage) Delete(_ context.Context, _ string) error           { return nil }
func (alwaysFailStorage) DeleteExpired(_ context.Context, _ time.Time) error { return nil }
func (alwaysFailStorage) Close() error                                       { return nil }

// configurableFailStorage wraps a real storage and allows injecting failures
// for specific operations. Used to test fallback behavior in Terminate().
type configurableFailStorage struct {
	transportsession.Storage
	storeCallCount int
	failStoreAfter int // fail Store after this many successful calls (0 = never fail, -1 = always fail)
	failDelete     bool
}

func (s *configurableFailStorage) Store(ctx context.Context, sess transportsession.Session) error {
	s.storeCallCount++
	if s.failStoreAfter == -1 || (s.failStoreAfter >= 0 && s.storeCallCount > s.failStoreAfter) {
		return errors.New("injected Store failure")
	}
	return s.Storage.Store(ctx, sess)
}

func (s *configurableFailStorage) Delete(ctx context.Context, id string) error {
	if s.failDelete {
		return errors.New("injected Delete failure")
	}
	return s.Storage.Delete(ctx, id)
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

// newTestTransportManager creates a transportsession.Manager backed by local storage
// with a long TTL. The cleanup goroutine is stopped via t.Cleanup.
func newTestTransportManager(t *testing.T) *transportsession.Manager {
	t.Helper()
	mgr := transportsession.NewTypedManager(30*time.Minute, transportsession.SessionTypeStreamable)
	t.Cleanup(func() { _ = mgr.Stop() })
	return mgr
}

// newTestSessionManager is a convenience constructor for tests.
func newTestSessionManager(
	t *testing.T,
	factory vmcpsession.MultiSessionFactory,
	registry vmcp.BackendRegistry,
) (*Manager, *transportsession.Manager) {
	t.Helper()
	storage := newTestTransportManager(t)
	return New(storage, factory, registry), storage
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
		_, exists := storage.Get(sessionID)
		assert.True(t, exists, "placeholder should be stored in transport manager")
	})

	t.Run("returns empty string when storage always fails", func(t *testing.T) {
		t.Parallel()

		// Use a Manager backed by storage that always fails Store(), forcing both
		// UUID attempts inside Generate() to fail so it must return "".
		failingMgr := transportsession.NewManagerWithStorage(
			time.Hour,
			func(id string) transportsession.Session { return transportsession.NewStreamableSession(id) },
			alwaysFailStorage{},
		)
		t.Cleanup(func() { _ = failingMgr.Stop() })

		ctrl := gomock.NewController(t)
		sess := newMockSession(t, ctrl, "placeholder", nil)
		factory := newMockFactory(t, ctrl, sess)
		sm := New(failingMgr, factory, newFakeRegistry())

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

		// Storage must now hold the MultiSession (not just a placeholder).
		stored, exists := storage.Get(sessionID)
		require.True(t, exists, "session should still exist in storage")
		_, isMulti := stored.(vmcpsession.MultiSession)
		assert.True(t, isMulti, "stored session should be a MultiSession")
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
		require.NoError(t, storage.Delete(sessionID))

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
		_, existsBefore := storage.Get(sessionID)
		assert.True(t, existsBefore)

		_, err = sm.Terminate(sessionID)
		require.NoError(t, err)

		// Session must be removed from storage.
		_, existsAfter := storage.Get(sessionID)
		assert.False(t, existsAfter, "session should be deleted from storage after Terminate")
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
		sess2, exists := storage.Get(sessionID)
		require.True(t, exists, "placeholder should remain in storage (TTL will clean it)")
		assert.Equal(t, MetadataValTrue, sess2.GetMetadata()[MetadataKeyTerminated])
	})

	t.Run("placeholder termination falls back to delete when upsert fails", func(t *testing.T) {
		t.Parallel()

		ctrl := gomock.NewController(t)
		sess := newMockSession(t, ctrl, "", nil)
		factory := newMockFactory(t, ctrl, sess)
		registry := newFakeRegistry()

		// Create a storage that succeeds on the first Store (Generate creates
		// placeholder) but fails on the second Store (Terminate tries to upsert).
		// Delete succeeds. This tests the fallback path in Terminate().
		baseStorage := transportsession.NewLocalStorage()
		failingStorage := &configurableFailStorage{
			Storage:        baseStorage,
			failStoreAfter: 1, // fail after 1 successful Store
			failDelete:     false,
		}
		storage := transportsession.NewManagerWithStorage(
			time.Hour,
			func(id string) transportsession.Session { return transportsession.NewStreamableSession(id) },
			failingStorage,
		)
		t.Cleanup(func() { _ = storage.Stop() })
		sm := New(storage, factory, registry)

		// Generate a placeholder (first Store, succeeds).
		sessionID := sm.Generate()
		require.NotEmpty(t, sessionID)

		// Terminate should succeed via the delete fallback (second Store fails, Delete succeeds).
		isNotAllowed, err := sm.Terminate(sessionID)
		require.NoError(t, err)
		assert.False(t, isNotAllowed)

		// Placeholder should be deleted (not just marked terminated).
		_, exists := storage.Get(sessionID)
		assert.False(t, exists, "placeholder should be deleted when upsert fails")
	})

	t.Run("placeholder termination fails when both upsert and delete fail", func(t *testing.T) {
		t.Parallel()

		ctrl := gomock.NewController(t)
		sess := newMockSession(t, ctrl, "", nil)
		factory := newMockFactory(t, ctrl, sess)
		registry := newFakeRegistry()

		// Create a storage that succeeds on the first Store (Generate creates
		// placeholder) but fails on the second Store (Terminate tries to upsert)
		// and also fails on Delete. This forces the error path.
		baseStorage := transportsession.NewLocalStorage()
		failingStorage := &configurableFailStorage{
			Storage:        baseStorage,
			failStoreAfter: 1, // fail after 1 successful Store
			failDelete:     true,
		}
		storage := transportsession.NewManagerWithStorage(
			time.Hour,
			func(id string) transportsession.Session { return transportsession.NewStreamableSession(id) },
			failingStorage,
		)
		t.Cleanup(func() { _ = storage.Stop() })
		sm := New(storage, factory, registry)

		// Generate a placeholder (first Store, succeeds).
		sessionID := sm.Generate()
		require.NotEmpty(t, sessionID)

		// Terminate should fail when both upsert and delete fail.
		isNotAllowed, err := sm.Terminate(sessionID)
		require.Error(t, err)
		assert.False(t, isNotAllowed)
		assert.ErrorContains(t, err, "failed to persist terminated flag and delete placeholder")
		assert.ErrorContains(t, err, "upsertErr=")
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

	t.Run("handlers delegate to session CallTool", func(t *testing.T) {
		t.Parallel()

		tools := []vmcp.Tool{{Name: "greet", Description: "greets user"}}
		ctrl := gomock.NewController(t)

		callToolResult := &vmcp.ToolCallResult{
			Content: []vmcp.Content{{Type: "text", Text: "Hello, world!"}},
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
			Contents: []byte("hello resource"),
			MimeType: "text/plain",
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

	t.Run("handler uses application/octet-stream fallback when MimeType is empty", func(t *testing.T) {
		t.Parallel()

		resources := []vmcp.Resource{
			{
				Name: "binary",
				URI:  "file:///binary.bin",
				// MimeType intentionally empty
			},
		}
		readResult := &vmcp.ResourceReadResult{
			Contents: []byte("binary data"),
			MimeType: "", // empty — should fall back to application/octet-stream
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
		assert.Equal(t, "application/octet-stream", textContents.MIMEType)
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
// Helper
// ---------------------------------------------------------------------------

// newCallToolRequest builds a minimal mcp.CallToolRequest for handler tests.
func newCallToolRequest(name string, args map[string]any) mcp.CallToolRequest {
	req := mcp.CallToolRequest{}
	req.Params.Name = name
	req.Params.Arguments = args
	return req
}

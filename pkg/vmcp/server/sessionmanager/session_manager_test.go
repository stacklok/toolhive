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

// deleteBeforeUpdateStorage wraps a real DataStorage and deletes the key
// from the underlying store on the first Update call, simulating a concurrent
// Terminate / TTL expiry that races with loadSession's metadata write-back.
// The Update then returns (false, nil) because the key no longer exists.
type deleteBeforeUpdateStorage struct {
	transportsession.DataStorage
	deleted bool
}

func (s *deleteBeforeUpdateStorage) Update(ctx context.Context, id string, metadata map[string]string) (bool, error) {
	if !s.deleted {
		s.deleted = true
		_ = s.Delete(ctx, id)
	}
	return s.DataStorage.Update(ctx, id, metadata)
}

// errorOnUpdateStorage wraps a real DataStorage and returns an error on the
// first Update call, simulating a transient Redis write failure during
// loadSession's metadata write-back.
type errorOnUpdateStorage struct {
	transportsession.DataStorage
	errored bool
}

func (s *errorOnUpdateStorage) Update(_ context.Context, _ string, _ map[string]string) (bool, error) {
	if !s.errored {
		s.errored = true
		return false, errors.New("injected Update failure")
	}
	return true, nil
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
			DoAndReturn(func(_ context.Context, id string, _ *auth.Identity, _ []*vmcp.Backend, _ vmcpsession.ListChangedSink) (vmcpsession.MultiSession, error) {
				createdSess = newMockSession(t, ctrl, id, tools)
				return createdSess, nil
			}).AnyTimes()

		registry := newFakeRegistry()
		sm, storage := newTestSessionManager(t, factory, registry)

		// Generate placeholder.
		sessionID := sm.Generate()
		require.NotEmpty(t, sessionID)

		// Upgrade to full MultiSession.
		multiSess, err := sm.CreateSession(context.Background(), sessionID, nil)
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

		_, err := sm.CreateSession(context.Background(), "", nil)
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

		_, err := sm.CreateSession(context.Background(), sessionID, nil)
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
			DoAndReturn(func(_ context.Context, id string, _ *auth.Identity, _ []*vmcp.Backend, _ vmcpsession.ListChangedSink) (vmcpsession.MultiSession, error) {
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
		_, createErr := sm.CreateSession(context.Background(), sessionID, nil)
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
			DoAndReturn(func(_ context.Context, id string, _ *auth.Identity, _ []*vmcp.Backend, _ vmcpsession.ListChangedSink) (vmcpsession.MultiSession, error) {
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
		_, createErr := sm.CreateSession(context.Background(), sessionID, nil)
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
			DoAndReturn(func(_ context.Context, id string, _ *auth.Identity, _ []*vmcp.Backend, _ vmcpsession.ListChangedSink) (vmcpsession.MultiSession, error) {
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
			_, err := sm.CreateSession(context.Background(), sessionID, nil)
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

	t.Run("reports unknown session as terminated", func(t *testing.T) {
		t.Parallel()

		ctrl := gomock.NewController(t)
		sess := newMockSession(t, ctrl, "", nil)
		factory := newMockFactory(t, ctrl, sess)
		registry := newFakeRegistry()
		sm, _ := newTestSessionManager(t, factory, registry)

		// A session that is absent from storage (deleted on Terminate, TTL-expired,
		// or never existed) must be reported as terminated (isTerminated=true, nil),
		// NOT as an error. The transport maps terminated -> 404 so the client
		// re-initializes; reporting an error would surface as a retryable 503 and a
		// client whose session was terminated on another replica would retry the
		// dead session forever (regression guarded here).
		isTerminated, err := sm.Validate("non-existent-id")
		require.NoError(t, err)
		assert.True(t, isTerminated, "an absent session must report as terminated, not as a transient error")
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

		// bindingMeta is carried by the session so CreateSession writes it to
		// storage and Terminate takes the Phase 2 (storage.Delete) path.
		bindingMeta := map[string]string{sessiontypes.MetadataKeyIdentityBinding: "unauthenticated"}

		var createdSess *sessionmocks.MockMultiSession
		factory := sessionfactorymocks.NewMockMultiSessionFactory(ctrl)
		factory.EXPECT().
			MakeSessionWithID(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
			DoAndReturn(func(_ context.Context, id string, _ *auth.Identity, _ []*vmcp.Backend, _ vmcpsession.ListChangedSink) (vmcpsession.MultiSession, error) {
				createdSess = sessionmocks.NewMockMultiSession(ctrl)
				createdSess.EXPECT().ID().Return(id).AnyTimes()
				createdSess.EXPECT().GetMetadata().Return(bindingMeta).AnyTimes()
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

		_, err := sm.CreateSession(context.Background(), sessionID, nil)
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
		_, ok := sm.GetMultiSession(t.Context(), sessionID)
		assert.False(t, ok, "terminated session must not be returned")
		// gomock verifies Close() was called exactly once via Times(1)
	})

	t.Run("removes MultiSession from storage on Terminate", func(t *testing.T) {
		t.Parallel()

		ctrl := gomock.NewController(t)
		factory := sessionfactorymocks.NewMockMultiSessionFactory(ctrl)
		factory.EXPECT().
			MakeSessionWithID(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
			DoAndReturn(func(_ context.Context, id string, _ *auth.Identity, _ []*vmcp.Backend, _ vmcpsession.ListChangedSink) (vmcpsession.MultiSession, error) {
				sess := newMockSession(t, ctrl, id, nil)
				// Close is called by onEvict when Terminate removes the cache entry.
				sess.EXPECT().Close().Return(nil).AnyTimes()
				return sess, nil
			}).Times(1)

		registry := newFakeRegistry()
		sm, storage := newTestSessionManager(t, factory, registry)

		sessionID := sm.Generate()
		require.NotEmpty(t, sessionID)

		_, err := sm.CreateSession(context.Background(), sessionID, nil)
		require.NoError(t, err)

		// Seed MetadataKeyIdentityBinding into storage so Terminate recognises this
		// as a Phase 2 (full MultiSession) and deletes rather than marks terminated.
		_, err = storage.Update(context.Background(), sessionID, map[string]string{
			sessiontypes.MetadataKeyIdentityBinding: "unauthenticated",
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

		multiSess, ok := sm.GetMultiSession(t.Context(), "ghost")
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
		multiSess, ok := sm.GetMultiSession(t.Context(), sessionID)
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
			DoAndReturn(func(_ context.Context, id string, _ *auth.Identity, _ []*vmcp.Backend, _ vmcpsession.ListChangedSink) (vmcpsession.MultiSession, error) {
				sess := newMockSession(t, ctrl, id, tools)
				return sess, nil
			}).Times(1)

		registry := newFakeRegistry()
		sm, _ := newTestSessionManager(t, factory, registry)

		sessionID := sm.Generate()
		require.NotEmpty(t, sessionID)

		_, err := sm.CreateSession(context.Background(), sessionID, nil)
		require.NoError(t, err)

		multiSess, ok := sm.GetMultiSession(t.Context(), sessionID)
		require.True(t, ok)
		require.NotNil(t, multiSess)
		assert.Equal(t, sessionID, multiSess.ID())
		require.Len(t, multiSess.Tools(), 1)
		assert.Equal(t, "hello", multiSess.Tools()[0].Name)
	})

	// Cross-pod restore path: session is in storage but not in the in-memory
	// cache (simulates pod restart or eviction). loadSession is called on Get.

	t.Run("restore path: placeholder in storage (absent identity binding) is treated as not found", func(t *testing.T) {
		t.Parallel()

		ctrl := gomock.NewController(t)
		factory := sessionfactorymocks.NewMockMultiSessionFactory(ctrl)
		// RestoreSession must NOT be called for placeholders.
		factory.EXPECT().RestoreSession(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Times(0)

		sm, _ := newTestSessionManager(t, factory, newFakeRegistry())

		sessionID := "restore-placeholder-session"
		// Write placeholder metadata directly to storage, bypassing the cache.
		// Generate() stores an empty map with no identity binding.
		_, err := sm.storage.Create(context.Background(), sessionID, map[string]string{})
		require.NoError(t, err)

		// loadSession detects absent MetadataKeyIdentityBinding → ErrSessionNotFound.
		multiSess, ok := sm.GetMultiSession(t.Context(), sessionID)
		assert.False(t, ok, "placeholder should not be restorable")
		assert.Nil(t, multiSess)
	})

	t.Run("restore path: fully-initialized zero-backend session (has identity binding) is restored", func(t *testing.T) {
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

		// Metadata matching what BindSession and populateBackendMetadata write
		// for a Phase-2-complete anonymous session with zero backends:
		// MetadataKeyIdentityBinding holds the unauthenticated sentinel;
		// MetadataKeyBackendIDs is always written (empty string for zero backends).
		initializedMeta := map[string]string{
			sessiontypes.MetadataKeyIdentityBinding: "unauthenticated", // anonymous sentinel
			vmcpsession.MetadataKeyBackendIDs:       "",                // always written; empty = zero backends
		}
		_, err := sm.storage.Create(context.Background(), sessionID, initializedMeta)
		require.NoError(t, err)

		// loadSession should call RestoreSession, not treat it as a placeholder.
		multiSess, ok := sm.GetMultiSession(t.Context(), sessionID)
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

		// Metadata with identity binding but MetadataKeyBackendIDs absent
		// (sessions written before populateBackendMetadata always wrote the key).
		legacyMeta := map[string]string{
			sessiontypes.MetadataKeyIdentityBinding: "unauthenticated", // Phase 2 completion marker
			// MetadataKeyBackendIDs intentionally absent (legacy record)
		}
		_, err := sm.storage.Create(context.Background(), sessionID, legacyMeta)
		require.NoError(t, err)

		multiSess, ok := sm.GetMultiSession(t.Context(), sessionID)
		require.True(t, ok, "legacy record without MetadataKeyBackendIDs must still be restorable")
		require.NotNil(t, multiSess)
		assert.Equal(t, sessionID, multiSess.ID())
	})

	t.Run("restore path: restored metadata is persisted back to storage", func(t *testing.T) {
		t.Parallel()

		// Simulate a backend that doesn't honor Mcp-Session-Id hints (e.g. SSE
		// transport): RestoreSession assigns a fresh per-backend session ID.
		// loadSession must write the restored session's metadata back to Redis so
		// that stale per-backend session IDs do not persist indefinitely in storage.
		ctrl := gomock.NewController(t)
		factory := sessionfactorymocks.NewMockMultiSessionFactory(ctrl)
		factory.EXPECT().MakeSessionWithID(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
			Times(0)

		sessionID := "restore-metadata-persist-session"

		// The restored session returns fresh per-backend session metadata.
		freshMeta := map[string]string{
			sessiontypes.MetadataKeyIdentityBinding:                   "unauthenticated",
			vmcpsession.MetadataKeyBackendIDs:                         "backend-a",
			vmcpsession.MetadataKeyBackendSessionPrefix + "backend-a": "fresh-session-id",
		}
		restored := sessionmocks.NewMockMultiSession(ctrl)
		restored.EXPECT().ID().Return(sessionID).AnyTimes()
		restored.EXPECT().GetMetadata().Return(freshMeta).AnyTimes()

		factory.EXPECT().
			RestoreSession(gomock.Any(), sessionID, gomock.Any(), gomock.Any()).
			Return(restored, nil).Times(1)

		sm, storage := newTestSessionManager(t, factory, newFakeRegistry())

		// Seed storage with stale per-backend session ID.
		staleMeta := map[string]string{
			sessiontypes.MetadataKeyIdentityBinding:                   "unauthenticated",
			vmcpsession.MetadataKeyBackendIDs:                         "backend-a",
			vmcpsession.MetadataKeyBackendSessionPrefix + "backend-a": "stale-session-id",
		}
		_, err := sm.storage.Create(context.Background(), sessionID, staleMeta)
		require.NoError(t, err)

		multiSess, ok := sm.GetMultiSession(t.Context(), sessionID)
		require.True(t, ok, "session must be restored")
		require.NotNil(t, multiSess)

		// Verify storage now contains the fresh metadata written by loadSession.
		storedMeta, loadErr := storage.Load(context.Background(), sessionID)
		require.NoError(t, loadErr)
		assert.Equal(t, freshMeta, storedMeta,
			"loadSession must persist restored session metadata back to storage")
	})

	t.Run("restore path: concurrent delete between RestoreSession and Update returns ErrSessionNotFound", func(t *testing.T) {
		t.Parallel()

		// Simulate a Terminate / TTL expiry that races with loadSession's
		// metadata write-back: deleteBeforeUpdateStorage deletes the key just
		// before the first Update, so Update returns (false, nil).
		// loadSession must treat this as ErrSessionNotFound and NOT cache the
		// restored session.
		ctrl := gomock.NewController(t)
		factory := sessionfactorymocks.NewMockMultiSessionFactory(ctrl)
		factory.EXPECT().MakeSessionWithID(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
			Times(0)

		sessionID := "restore-concurrent-delete-session"
		restored := sessionmocks.NewMockMultiSession(ctrl)
		restored.EXPECT().ID().Return(sessionID).AnyTimes()
		restored.EXPECT().GetMetadata().Return(map[string]string{
			sessiontypes.MetadataKeyIdentityBinding: "unauthenticated",
		}).AnyTimes()

		factory.EXPECT().
			RestoreSession(gomock.Any(), sessionID, gomock.Any(), gomock.Any()).
			Return(restored, nil).Times(1)
		// loadSession calls Close on the restored session when a concurrent
		// delete is detected (Update returns false, nil).
		restored.EXPECT().Close().Return(nil).Times(1)

		// Build Manager with the wrapping storage.
		innerStorage := newTestSessionDataStorage(t)
		racyStorage := &deleteBeforeUpdateStorage{DataStorage: innerStorage}
		sm, cleanup, err := New(racyStorage, &FactoryConfig{Base: factory, CacheCapacity: 1000}, newFakeRegistry())
		require.NoError(t, err)
		t.Cleanup(func() { _ = cleanup(context.Background()) })

		// Seed the inner storage with a valid session record.
		_, err = innerStorage.Create(context.Background(), sessionID, map[string]string{
			sessiontypes.MetadataKeyIdentityBinding: "unauthenticated",
		})
		require.NoError(t, err)

		// GetMultiSession triggers loadSession; the racing delete causes
		// Update to return (false, nil) → ErrSessionNotFound → (nil, false).
		multiSess, ok := sm.GetMultiSession(t.Context(), sessionID)
		assert.False(t, ok, "session deleted before metadata write-back must not be cached")
		assert.Nil(t, multiSess)
	})

	t.Run("restore path: transient Update error is non-fatal, session is still returned", func(t *testing.T) {
		t.Parallel()

		// A transient Redis write failure during loadSession's metadata write-back
		// must not prevent the restored session from being cached and served.
		// The session is still usable on this pod; checkSession will detect any
		// metadata drift on the next liveness check and evict if necessary.
		ctrl := gomock.NewController(t)
		factory := sessionfactorymocks.NewMockMultiSessionFactory(ctrl)
		factory.EXPECT().MakeSessionWithID(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
			Times(0)

		sessionID := "restore-update-error-session"
		restored := newMockSession(t, ctrl, sessionID, nil)

		factory.EXPECT().
			RestoreSession(gomock.Any(), sessionID, gomock.Any(), gomock.Any()).
			Return(restored, nil).Times(1)

		innerStorage := newTestSessionDataStorage(t)
		faultyStorage := &errorOnUpdateStorage{DataStorage: innerStorage}
		sm, cleanup, err := New(faultyStorage, &FactoryConfig{Base: factory, CacheCapacity: 1000}, newFakeRegistry())
		require.NoError(t, err)
		t.Cleanup(func() { _ = cleanup(context.Background()) })

		_, err = innerStorage.Create(context.Background(), sessionID, map[string]string{
			sessiontypes.MetadataKeyIdentityBinding: "unauthenticated",
		})
		require.NoError(t, err)

		// Write failure must be non-fatal: session is still returned and cached.
		multiSess, ok := sm.GetMultiSession(t.Context(), sessionID)
		assert.True(t, ok, "transient Update error must not prevent session from being served")
		assert.NotNil(t, multiSess)
		assert.Equal(t, sessionID, multiSess.ID())
	})
}

// ---------------------------------------------------------------------------
// Tests: identity propagation through loadSession (issue #5336 Layer B)
// ---------------------------------------------------------------------------

// TestGetMultiSession_PropagatesIdentityToRestoreSession verifies that the
// *auth.Identity present on the context passed to GetMultiSession reaches the
// RestoreSession call inside loadSession. This pins the context.WithoutCancel
// propagation contract: reversing that call to context.Background() would
// cause zero test failures without this test.
func TestGetMultiSession_PropagatesIdentityToRestoreSession(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)

	// An identity with a non-empty UpstreamTokens map — the concrete value
	// any outgoing-auth strategy would need at restore time.
	wantIdentity := &auth.Identity{
		PrincipalInfo: auth.PrincipalInfo{
			Subject: "alice",
			Claims:  map[string]any{"iss": "https://idp.example", "sub": "alice"},
		},
		UpstreamTokens: map[string]string{"provider": "live-upstream-token"},
	}

	sessionID := "restore-identity-propagation-session"
	restored := newMockSession(t, ctrl, sessionID, nil)

	// Capture the context RestoreSession receives.
	var capturedCtx context.Context
	factory := sessionfactorymocks.NewMockMultiSessionFactory(ctrl)
	factory.EXPECT().
		RestoreSession(gomock.Any(), sessionID, gomock.Any(), gomock.Any()).
		DoAndReturn(func(ctx context.Context, _ string, _ map[string]string, _ []*vmcp.Backend) (vmcpsession.MultiSession, error) {
			capturedCtx = ctx
			return restored, nil
		}).Times(1)

	sm, _ := newTestSessionManager(t, factory, newFakeRegistry())

	// Write fully-initialized metadata directly to storage (bypassing cache)
	// so GetMultiSession triggers the cache-miss → loadSession → RestoreSession path.
	_, err := sm.storage.Create(context.Background(), sessionID, map[string]string{
		sessiontypes.MetadataKeyIdentityBinding: "https://idp.example\x00alice",
		vmcpsession.MetadataKeyBackendIDs:       "",
	})
	require.NoError(t, err)

	// Call GetMultiSession with an identity-bearing context.
	ctxWithIdentity := auth.WithIdentity(t.Context(), wantIdentity)
	multiSess, ok := sm.GetMultiSession(ctxWithIdentity, sessionID)
	require.True(t, ok)
	require.NotNil(t, multiSess)

	// The identity must have propagated to RestoreSession via context.WithoutCancel.
	require.NotNil(t, capturedCtx, "RestoreSession must have been called")
	gotIdentity, hasIdentity := auth.IdentityFromContext(capturedCtx)
	require.True(t, hasIdentity, "identity must be present on the context RestoreSession received")
	assert.Equal(t, wantIdentity.Subject, gotIdentity.Subject,
		"Subject must propagate through loadSession's context.WithoutCancel")
	assert.Equal(t, "live-upstream-token", gotIdentity.UpstreamTokens["provider"],
		"UpstreamTokens must propagate — these are the credentials backend Initialize needs")
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
			DoAndReturn(func(_ context.Context, id string, _ *auth.Identity, _ []*vmcp.Backend, _ vmcpsession.ListChangedSink) (vmcpsession.MultiSession, error) {
				return newMockSession(t, ctrl, id, tools), nil
			}).Times(1)

		registry := newFakeRegistry()
		sm, _ := newTestSessionManager(t, factory, registry)

		sessionID := sm.Generate()
		require.NotEmpty(t, sessionID)
		_, err := sm.CreateSession(context.Background(), sessionID, nil)
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
		multiSess, ok := sm.GetMultiSession(t.Context(), sessionID)
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
		// The mock session carries MetadataKeyIdentityBinding so that:
		// 1. CreateSession stores it in storage (via sess.GetMetadata()), keeping
		//    cache and storage in sync for checkSession's maps.Equal comparison.
		// 2. Terminate sees the key and takes the Phase 2 path (storage.Delete).
		bindingMeta := map[string]string{sessiontypes.MetadataKeyIdentityBinding: "unauthenticated"}
		factory.EXPECT().
			MakeSessionWithID(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
			DoAndReturn(func(_ context.Context, id string, _ *auth.Identity, _ []*vmcp.Backend, _ vmcpsession.ListChangedSink) (vmcpsession.MultiSession, error) {
				sess := sessionmocks.NewMockMultiSession(ctrl)
				sess.EXPECT().ID().Return(id).AnyTimes()
				sess.EXPECT().GetMetadata().Return(bindingMeta).AnyTimes()
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
		_, err := sm.CreateSession(context.Background(), sessionID, nil)
		require.NoError(t, err)

		err = sm.DecorateSession(sessionID, func(sess sessiontypes.MultiSession) sessiontypes.MultiSession {
			// Simulate concurrent Terminate() completing during decoration.
			_, _ = sm.Terminate(sessionID)
			return sess
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "was deleted during decoration")

		// The session must not be resurrected.
		_, ok := sm.GetMultiSession(t.Context(), sessionID)
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

		err = sm.checkSession(t.Context(), sessionID, makeEmptySess(t))
		assert.NoError(t, err, "alive session must return nil")
	})

	t.Run("deleted session returns ErrExpired", func(t *testing.T) {
		t.Parallel()
		sm, _ := newTestSessionManager(t, makeFactory(t), newFakeRegistry())

		err := sm.checkSession(t.Context(), "nonexistent-session", makeEmptySess(t))
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

		err = sm.checkSession(t.Context(), sessionID, makeEmptySess(t))
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

		err = sm.checkSession(t.Context(), sessionID, cached)
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

		err = sm.checkSession(t.Context(), sessionID, cached)
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

		err = sm.checkSession(t.Context(), sessionID, cached)
		assert.NoError(t, err, "matching empty metadata must not cause eviction")
	})

	t.Run("differing per-backend session IDs do not evict", func(t *testing.T) {
		t.Parallel()
		// In multi-pod deployments, each pod's RestoreSession independently
		// negotiates its own per-backend session IDs with backends that do not
		// honor Mcp-Session-Id hints (e.g. SSE transports). Each pod then
		// writes its own IDs back to Redis via loadSession. checkSession must
		// NOT evict when only per-backend session IDs differ — only when the
		// backend ID list (MetadataKeyBackendIDs) changes. Evicting on per-
		// backend ID drift would cause each pod's write-back to invalidate all
		// other pods' sessions, creating an infinite eviction storm.
		sm, storage := newTestSessionManager(t, makeFactory(t), newFakeRegistry())
		sessionID := "multi-pod-per-backend-ids"

		// Storage holds IDs written by another pod's RestoreSession.
		_, err := storage.Create(context.Background(), sessionID, map[string]string{
			vmcpsession.MetadataKeyBackendIDs:                         "backend-a",
			vmcpsession.MetadataKeyBackendSessionPrefix + "backend-a": "pod-a-session-id",
		})
		require.NoError(t, err)

		// This pod cached different per-backend IDs from its own RestoreSession.
		ctrl := gomock.NewController(t)
		cached := sessionmocks.NewMockMultiSession(ctrl)
		cached.EXPECT().GetMetadata().Return(map[string]string{
			vmcpsession.MetadataKeyBackendIDs:                         "backend-a",
			vmcpsession.MetadataKeyBackendSessionPrefix + "backend-a": "pod-b-session-id",
		}).AnyTimes()
		sm.sessions.Set(sessionID, cached)

		err = sm.checkSession(t.Context(), sessionID, cached)
		assert.NoError(t, err,
			"differing per-backend session IDs must not evict to avoid cross-pod eviction storms")
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
		_, err := sm.CreateSession(t.Context(), sessionID, nil)
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
		_, err := sm.CreateSession(t.Context(), sessionID, nil)
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
		_, err := sm.CreateSession(t.Context(), sessionID, nil)
		require.NoError(t, err)

		// Seed MetadataKeyIdentityBinding into storage so Terminate recognises this
		// as a Phase 2 (full MultiSession) and deletes rather than marks terminated.
		_, err = storage.Update(context.Background(), sessionID, map[string]string{
			sessiontypes.MetadataKeyIdentityBinding: "unauthenticated",
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
		_, err := sm.CreateSession(t.Context(), sessionID, nil)
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
		_, err := sm.CreateSession(t.Context(), sessionID, nil)
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
		_, err := sm.CreateSession(t.Context(), sessionID, nil)
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
// Tests: Phase 2 marker migration (#5306)
// ---------------------------------------------------------------------------

// TestLoadSession_Phase2Marker_UsesIdentityBindingKey documents that the
// Phase-2 detection key for the restore path is MetadataKeyIdentityBinding,
// not the legacy MetadataKeyTokenHash. Sessions stored with only the legacy
// key are treated as not found (ErrSessionNotFound) and the client must
// re-initialize. Sessions stored with MetadataKeyIdentityBinding are restored
// normally.
func TestLoadSession_Phase2Marker_UsesIdentityBindingKey(t *testing.T) {
	t.Parallel()

	t.Run("legacy session (only MetadataKeyTokenHash) returns not found", func(t *testing.T) {
		t.Parallel()

		ctrl := gomock.NewController(t)
		factory := sessionfactorymocks.NewMockMultiSessionFactory(ctrl)
		// RestoreSession must NOT be called for legacy sessions on the restore path.
		factory.EXPECT().RestoreSession(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Times(0)

		sm, _ := newTestSessionManager(t, factory, newFakeRegistry())

		sessionID := "legacy-only-token-hash-session"
		// Seed storage with only the legacy key — no MetadataKeyIdentityBinding.
		_, err := sm.storage.Create(context.Background(), sessionID, map[string]string{
			sessiontypes.MetadataKeyTokenHash: "",
		})
		require.NoError(t, err)

		// loadSession must treat absent MetadataKeyIdentityBinding as a legacy session
		// and return (nil, false) — not attempting RestoreSession.
		multiSess, ok := sm.GetMultiSession(t.Context(), sessionID)
		assert.False(t, ok, "legacy session with only MetadataKeyTokenHash must not be restored")
		assert.Nil(t, multiSess)
	})

	t.Run("session with MetadataKeyIdentityBinding is restored normally", func(t *testing.T) {
		t.Parallel()

		tools := []vmcp.Tool{{Name: "restored-tool", Description: "a restored tool"}}
		ctrl := gomock.NewController(t)
		factory := sessionfactorymocks.NewMockMultiSessionFactory(ctrl)
		factory.EXPECT().MakeSessionWithID(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
			Times(0)

		sessionID := "identity-binding-session"
		restored := newMockSession(t, ctrl, sessionID, tools)

		factory.EXPECT().
			RestoreSession(gomock.Any(), sessionID, gomock.Any(), gomock.Any()).
			Return(restored, nil).Times(1)

		sm, _ := newTestSessionManager(t, factory, newFakeRegistry())

		_, err := sm.storage.Create(context.Background(), sessionID, map[string]string{
			sessiontypes.MetadataKeyIdentityBinding: "unauthenticated",
			vmcpsession.MetadataKeyBackendIDs:       "",
		})
		require.NoError(t, err)

		multiSess, ok := sm.GetMultiSession(t.Context(), sessionID)
		require.True(t, ok, "session with MetadataKeyIdentityBinding must be restorable")
		require.NotNil(t, multiSess)
		assert.Equal(t, sessionID, multiSess.ID())
	})
}

// TestTerminate_Phase2DetectionUsesIdentityBindingKey verifies that Terminate
// uses MetadataKeyIdentityBinding (not the legacy MetadataKeyTokenHash) to
// distinguish Phase 2 sessions (full MultiSession → Delete) from Phase 1
// placeholders (→ mark terminated).
func TestTerminate_Phase2DetectionUsesIdentityBindingKey(t *testing.T) {
	t.Parallel()

	t.Run("session with MetadataKeyIdentityBinding takes delete path", func(t *testing.T) {
		t.Parallel()

		ctrl := gomock.NewController(t)
		sess := newMockSession(t, ctrl, "s", nil)
		sess.EXPECT().Close().Return(nil).AnyTimes()
		factory := newMockFactory(t, ctrl, sess)
		registry := newFakeRegistry()
		sm, storage := newTestSessionManager(t, factory, registry)

		sessionID := sm.Generate()
		require.NotEmpty(t, sessionID)

		// Write MetadataKeyIdentityBinding into storage to simulate a Phase 2 session.
		_, err := storage.Update(context.Background(), sessionID, map[string]string{
			sessiontypes.MetadataKeyIdentityBinding: "unauthenticated",
		})
		require.NoError(t, err)

		// Terminate must take the Phase 2 path: storage.Delete (not marked terminated).
		isNotAllowed, err := sm.Terminate(sessionID)
		require.NoError(t, err)
		assert.False(t, isNotAllowed)

		// Session must be deleted from storage, not just marked terminated.
		_, loadErr := storage.Load(context.Background(), sessionID)
		assert.ErrorIs(t, loadErr, transportsession.ErrSessionNotFound,
			"Phase 2 Terminate must delete the session from storage")
	})

	t.Run("placeholder without MetadataKeyIdentityBinding takes mark-terminated path", func(t *testing.T) {
		t.Parallel()

		ctrl := gomock.NewController(t)
		sess := newMockSession(t, ctrl, "", nil)
		factory := newMockFactory(t, ctrl, sess)
		registry := newFakeRegistry()
		sm, storage := newTestSessionManager(t, factory, registry)

		sessionID := sm.Generate()
		require.NotEmpty(t, sessionID)

		// No MetadataKeyIdentityBinding in storage — this is a Phase 1 placeholder.
		isNotAllowed, err := sm.Terminate(sessionID)
		require.NoError(t, err)
		assert.False(t, isNotAllowed)

		// Session must remain in storage but marked as terminated (not deleted).
		metadata, loadErr := storage.Load(context.Background(), sessionID)
		require.NoError(t, loadErr, "placeholder must remain in storage (TTL will clean it)")
		assert.Equal(t, MetadataValTrue, metadata[MetadataKeyTerminated],
			"Phase 1 Terminate must mark the session terminated, not delete it")
	})
}

// TestTerminate_LegacyFormatSession_TakesPlaceholderPath is a B5 documentation
// test. It verifies that a legacy session stored in Redis with only the old
// MetadataKeyTokenHash key (no MetadataKeyIdentityBinding) causes Terminate to
// take the placeholder (mark-terminated) path rather than deleting the session.
//
// This is intentional: without the identity binding key, the Manager cannot
// tell whether the record is a real Phase-2 session or a corrupted/partial
// record. Treating it as a placeholder (soft termination) is safe — the TTL
// will eventually clean it up. The comment at session_manager.go line 485
// references this test.
func TestTerminate_LegacyFormatSession_TakesPlaceholderPath(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	sess := newMockSession(t, ctrl, "", nil)
	factory := newMockFactory(t, ctrl, sess)
	registry := newFakeRegistry()
	sm, storage := newTestSessionManager(t, factory, registry)

	sessionID := sm.Generate()
	require.NotEmpty(t, sessionID)

	// Seed storage with only the legacy key — simulates a pre-#5306 session in Redis.
	// MetadataKeyIdentityBinding is absent.
	_, err := storage.Update(context.Background(), sessionID, map[string]string{
		sessiontypes.MetadataKeyTokenHash: "",
	})
	require.NoError(t, err)

	// Terminate must take the placeholder path: mark as terminated, NOT delete.
	isNotAllowed, err := sm.Terminate(sessionID)
	require.NoError(t, err)
	assert.False(t, isNotAllowed)

	// Session must still exist in storage — marked terminated, not deleted.
	// (Storage cleanup happens via TTL or the next GET → checkSession → eviction.)
	metadata, loadErr := storage.Load(context.Background(), sessionID)
	require.NoError(t, loadErr,
		"legacy session must remain in storage after Terminate (not deleted)")
	assert.Equal(t, MetadataValTrue, metadata[MetadataKeyTerminated],
		"legacy session Terminate must set MetadataKeyTerminated rather than deleting")
}

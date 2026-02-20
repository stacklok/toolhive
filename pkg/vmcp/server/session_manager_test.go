// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/auth"
	transportsession "github.com/stacklok/toolhive/pkg/transport/session"
	"github.com/stacklok/toolhive/pkg/vmcp"
	vmcpsession "github.com/stacklok/toolhive/pkg/vmcp/session"
)

// ---------------------------------------------------------------------------
// Test helpers / fakes
// ---------------------------------------------------------------------------

// fakeMultiSession is a minimal in-process MultiSession implementation for tests.
// It does NOT open real backend connections — it stores a set of tools and
// records whether Close() has been called.
type fakeMultiSession struct {
	transportsession.Session // embedded — provides ID, Type, timestamps, metadata
	tools                    []vmcp.Tool
	closed                   bool
	callToolResult           *vmcp.ToolCallResult
	callToolErr              error
	lastCallMeta             map[string]any // captures meta passed to CallTool
}

// newFakeMultiSession wraps a transportsession.Session with fake MultiSession behaviour.
func newFakeMultiSession(sess transportsession.Session, tools []vmcp.Tool) *fakeMultiSession {
	return &fakeMultiSession{Session: sess, tools: tools}
}

// Tools returns the preconfigured tool list.
func (f *fakeMultiSession) Tools() []vmcp.Tool {
	result := make([]vmcp.Tool, len(f.tools))
	copy(result, f.tools)
	return result
}

// Resources returns an empty list (not used in these tests).
func (*fakeMultiSession) Resources() []vmcp.Resource { return nil }

// Prompts returns an empty list (not used in these tests).
func (*fakeMultiSession) Prompts() []vmcp.Prompt { return nil }

// BackendSessions returns an empty map (not used in these tests).
func (*fakeMultiSession) BackendSessions() map[string]string { return nil }

// CallTool records the meta argument and returns the preconfigured result / error.
func (f *fakeMultiSession) CallTool(
	_ context.Context, _ string, _ map[string]any, meta map[string]any,
) (*vmcp.ToolCallResult, error) {
	f.lastCallMeta = meta
	return f.callToolResult, f.callToolErr
}

// ReadResource is a no-op stub.
func (*fakeMultiSession) ReadResource(_ context.Context, _ string) (*vmcp.ResourceReadResult, error) {
	return nil, errors.New("not implemented")
}

// GetPrompt is a no-op stub.
func (*fakeMultiSession) GetPrompt(_ context.Context, _ string, _ map[string]any) (*vmcp.PromptGetResult, error) {
	return nil, errors.New("not implemented")
}

// Close records that the session was closed.
func (f *fakeMultiSession) Close() error {
	f.closed = true
	return nil
}

// fakeMultiSessionFactory is a configurable MultiSessionFactory for tests.
type fakeMultiSessionFactory struct {
	// tools returned by every created session.
	tools []vmcp.Tool
	// err returned by MakeSession / MakeSessionWithID (when non-nil).
	err error
	// createdSessions tracks the sessions created, keyed by session ID.
	createdSessions map[string]*fakeMultiSession
}

func newFakeFactory(tools []vmcp.Tool) *fakeMultiSessionFactory {
	return &fakeMultiSessionFactory{
		tools:           tools,
		createdSessions: make(map[string]*fakeMultiSession),
	}
}

// MakeSession implements MultiSessionFactory (auto-generates ID).
func (f *fakeMultiSessionFactory) MakeSession(
	_ context.Context, _ *auth.Identity, _ []*vmcp.Backend,
) (vmcpsession.MultiSession, error) {
	if f.err != nil {
		return nil, f.err
	}
	sess := newFakeMultiSession(transportsession.NewStreamableSession("auto-id"), f.tools)
	f.createdSessions["auto-id"] = sess
	return sess, nil
}

// MakeSessionWithID implements MultiSessionFactory.
func (f *fakeMultiSessionFactory) MakeSessionWithID(
	_ context.Context, id string, _ *auth.Identity, _ []*vmcp.Backend,
) (vmcpsession.MultiSession, error) {
	if f.err != nil {
		return nil, f.err
	}
	sess := newFakeMultiSession(transportsession.NewStreamableSession(id), f.tools)
	f.createdSessions[id] = sess
	return sess, nil
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

// newTestVMCPSessionManager is a convenience constructor for tests.
func newTestVMCPSessionManager(
	t *testing.T,
	factory vmcpsession.MultiSessionFactory,
	registry vmcp.BackendRegistry,
) (*vmcpSessionManager, *transportsession.Manager) {
	t.Helper()
	storage := newTestTransportManager(t)
	return newVMCPSessionManager(storage, factory, registry), storage
}

// ---------------------------------------------------------------------------
// Tests: Generate
// ---------------------------------------------------------------------------

func TestVMCPSessionManager_Generate(t *testing.T) {
	t.Parallel()

	t.Run("stores placeholder and returns valid UUID", func(t *testing.T) {
		t.Parallel()

		factory := newFakeFactory(nil)
		registry := newFakeRegistry()
		sm, storage := newTestVMCPSessionManager(t, factory, registry)

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

		factory := newFakeFactory(nil)
		sm := newVMCPSessionManager(failingMgr, factory, newFakeRegistry())

		id := sm.Generate()
		assert.Empty(t, id, "Generate() should return '' when storage is unavailable")
	})

	t.Run("returns unique IDs on each call", func(t *testing.T) {
		t.Parallel()

		factory := newFakeFactory(nil)
		registry := newFakeRegistry()
		sm, _ := newTestVMCPSessionManager(t, factory, registry)

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

func TestVMCPSessionManager_CreateSession(t *testing.T) {
	t.Parallel()

	t.Run("replaces placeholder with MultiSession", func(t *testing.T) {
		t.Parallel()

		tools := []vmcp.Tool{{Name: "my-tool", Description: "does stuff"}}
		factory := newFakeFactory(tools)
		registry := newFakeRegistry()
		sm, storage := newTestVMCPSessionManager(t, factory, registry)

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

		factory := newFakeFactory(nil)
		registry := newFakeRegistry()
		sm, _ := newTestVMCPSessionManager(t, factory, registry)

		_, err := sm.CreateSession(context.Background(), "")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "session ID must not be empty")
	})

	t.Run("propagates factory error", func(t *testing.T) {
		t.Parallel()

		factoryErr := errors.New("backend unreachable")
		factory := newFakeFactory(nil)
		factory.err = factoryErr
		registry := newFakeRegistry()
		sm, _ := newTestVMCPSessionManager(t, factory, registry)

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
		factory := newFakeFactory(tools)
		registry := newFakeRegistry()
		sm, storage := newTestVMCPSessionManager(t, factory, registry)

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
		_, called := factory.createdSessions[sessionID]
		assert.False(t, called, "factory should not be called when placeholder is absent")
	})

	t.Run("returns error without calling factory when placeholder is marked terminated", func(t *testing.T) {
		t.Parallel()

		tools := []vmcp.Tool{{Name: "tool-a"}}
		factory := newFakeFactory(tools)
		registry := newFakeRegistry()
		sm, _ := newTestVMCPSessionManager(t, factory, registry)

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
		_, called := factory.createdSessions[sessionID]
		assert.False(t, called, "factory should not be called when placeholder is terminated")
	})
}

// ---------------------------------------------------------------------------
// Tests: Validate
// ---------------------------------------------------------------------------

func TestVMCPSessionManager_Validate(t *testing.T) {
	t.Parallel()

	t.Run("returns error for empty session ID", func(t *testing.T) {
		t.Parallel()

		factory := newFakeFactory(nil)
		registry := newFakeRegistry()
		sm, _ := newTestVMCPSessionManager(t, factory, registry)

		isTerminated, err := sm.Validate("")
		require.Error(t, err)
		assert.False(t, isTerminated)
		assert.Contains(t, err.Error(), "empty session ID")
	})

	t.Run("returns error for unknown session", func(t *testing.T) {
		t.Parallel()

		factory := newFakeFactory(nil)
		registry := newFakeRegistry()
		sm, _ := newTestVMCPSessionManager(t, factory, registry)

		isTerminated, err := sm.Validate("non-existent-id")
		require.Error(t, err)
		assert.False(t, isTerminated)
		assert.Contains(t, err.Error(), "session not found")
	})

	t.Run("returns false for active session", func(t *testing.T) {
		t.Parallel()

		factory := newFakeFactory(nil)
		registry := newFakeRegistry()
		sm, _ := newTestVMCPSessionManager(t, factory, registry)

		sessionID := sm.Generate()
		require.NotEmpty(t, sessionID)

		isTerminated, err := sm.Validate(sessionID)
		require.NoError(t, err)
		assert.False(t, isTerminated)
	})

	t.Run("returns isTerminated=true for terminated placeholder session", func(t *testing.T) {
		t.Parallel()

		factory := newFakeFactory(nil)
		registry := newFakeRegistry()
		sm, _ := newTestVMCPSessionManager(t, factory, registry)

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

func TestVMCPSessionManager_Terminate(t *testing.T) {
	t.Parallel()

	t.Run("returns error for empty session ID", func(t *testing.T) {
		t.Parallel()

		factory := newFakeFactory(nil)
		registry := newFakeRegistry()
		sm, _ := newTestVMCPSessionManager(t, factory, registry)

		isNotAllowed, err := sm.Terminate("")
		require.Error(t, err)
		assert.False(t, isNotAllowed)
		assert.Contains(t, err.Error(), "empty session ID")
	})

	t.Run("on unknown session returns no error", func(t *testing.T) {
		t.Parallel()

		factory := newFakeFactory(nil)
		registry := newFakeRegistry()
		sm, _ := newTestVMCPSessionManager(t, factory, registry)

		isNotAllowed, err := sm.Terminate("ghost-session")
		require.NoError(t, err)
		assert.False(t, isNotAllowed)
	})

	t.Run("closes MultiSession backend connections", func(t *testing.T) {
		t.Parallel()

		tools := []vmcp.Tool{{Name: "t1", Description: "tool 1"}}
		factory := newFakeFactory(tools)
		registry := newFakeRegistry()
		sm, _ := newTestVMCPSessionManager(t, factory, registry)

		sessionID := sm.Generate()
		require.NotEmpty(t, sessionID)

		// Upgrade to full MultiSession.
		_, err := sm.CreateSession(context.Background(), sessionID)
		require.NoError(t, err)

		// Retrieve the concrete fake so we can inspect closed state.
		fakeSess := factory.createdSessions[sessionID]
		require.NotNil(t, fakeSess)
		assert.False(t, fakeSess.closed, "should not be closed yet")

		// Terminate should close the backend connections.
		isNotAllowed, err := sm.Terminate(sessionID)
		require.NoError(t, err)
		assert.False(t, isNotAllowed)
		assert.True(t, fakeSess.closed, "backend connections should be closed")
	})

	t.Run("removes MultiSession from storage on Terminate", func(t *testing.T) {
		t.Parallel()

		factory := newFakeFactory(nil)
		registry := newFakeRegistry()
		sm, storage := newTestVMCPSessionManager(t, factory, registry)

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

		factory := newFakeFactory(nil)
		registry := newFakeRegistry()
		sm, storage := newTestVMCPSessionManager(t, factory, registry)

		// Generate a placeholder (no CreateSession called).
		sessionID := sm.Generate()
		require.NotEmpty(t, sessionID)

		isNotAllowed, err := sm.Terminate(sessionID)
		require.NoError(t, err)
		assert.False(t, isNotAllowed)

		// Placeholder should still be in storage but marked terminated.
		sess, exists := storage.Get(sessionID)
		require.True(t, exists, "placeholder should remain in storage (TTL will clean it)")
		assert.Equal(t, metadataValTrue, sess.GetMetadata()[metadataKeyTerminated])
	})
}

// ---------------------------------------------------------------------------
// Tests: GetMultiSession
// ---------------------------------------------------------------------------

func TestVMCPSessionManager_GetMultiSession(t *testing.T) {
	t.Parallel()

	t.Run("returns nil for unknown session", func(t *testing.T) {
		t.Parallel()

		factory := newFakeFactory(nil)
		registry := newFakeRegistry()
		sm, _ := newTestVMCPSessionManager(t, factory, registry)

		multiSess, ok := sm.GetMultiSession("ghost")
		assert.False(t, ok)
		assert.Nil(t, multiSess)
	})

	t.Run("returns nil for placeholder session (not yet upgraded)", func(t *testing.T) {
		t.Parallel()

		factory := newFakeFactory(nil)
		registry := newFakeRegistry()
		sm, _ := newTestVMCPSessionManager(t, factory, registry)

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
		factory := newFakeFactory(tools)
		registry := newFakeRegistry()
		sm, _ := newTestVMCPSessionManager(t, factory, registry)

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

func TestVMCPSessionManager_GetAdaptedTools(t *testing.T) {
	t.Parallel()

	t.Run("returns error for unknown session", func(t *testing.T) {
		t.Parallel()

		factory := newFakeFactory(nil)
		registry := newFakeRegistry()
		sm, _ := newTestVMCPSessionManager(t, factory, registry)

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
		factory := newFakeFactory(tools)
		registry := newFakeRegistry()
		sm, _ := newTestVMCPSessionManager(t, factory, registry)

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
		factory := newFakeFactory(tools)
		registry := newFakeRegistry()
		sm, _ := newTestVMCPSessionManager(t, factory, registry)

		sessionID := sm.Generate()
		_, err := sm.CreateSession(context.Background(), sessionID)
		require.NoError(t, err)

		// Configure the fake session to return a known result.
		fakeSess := factory.createdSessions[sessionID]
		require.NotNil(t, fakeSess)
		fakeSess.callToolResult = &vmcp.ToolCallResult{
			Content: []vmcp.Content{{Type: "text", Text: "Hello, world!"}},
		}

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
		factory := newFakeFactory(tools)
		registry := newFakeRegistry()
		sm, _ := newTestVMCPSessionManager(t, factory, registry)

		sessionID := sm.Generate()
		_, err := sm.CreateSession(context.Background(), sessionID)
		require.NoError(t, err)

		// Configure the fake to return an error.
		fakeSess := factory.createdSessions[sessionID]
		require.NotNil(t, fakeSess)
		fakeSess.callToolErr = errors.New("backend exploded")

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
		factory := newFakeFactory(tools)
		registry := newFakeRegistry()
		sm, _ := newTestVMCPSessionManager(t, factory, registry)

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
		factory := newFakeFactory(tools)
		registry := newFakeRegistry()
		sm, _ := newTestVMCPSessionManager(t, factory, registry)

		sessionID := sm.Generate()
		_, err := sm.CreateSession(context.Background(), sessionID)
		require.NoError(t, err)

		fakeSess := factory.createdSessions[sessionID]
		require.NotNil(t, fakeSess)
		fakeSess.callToolResult = &vmcp.ToolCallResult{}

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
		require.NotNil(t, fakeSess.lastCallMeta, "meta should be forwarded to CallTool")
		assert.Equal(t, "tok-1", fakeSess.lastCallMeta["progressToken"])
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

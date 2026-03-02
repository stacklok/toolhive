// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package session

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/auth"
	transportsession "github.com/stacklok/toolhive/pkg/transport/session"
	"github.com/stacklok/toolhive/pkg/vmcp"
	internalbk "github.com/stacklok/toolhive/pkg/vmcp/session/internal/backend"
)

// ---------------------------------------------------------------------------
// Helpers / mocks
// ---------------------------------------------------------------------------

// mockConnectedBackend is an in-process internalbk.Session for testing.
type mockConnectedBackend struct {
	callToolFunc     func(ctx context.Context, toolName string, arguments, meta map[string]any) (*vmcp.ToolCallResult, error)
	readResourceFunc func(ctx context.Context, uri string) (*vmcp.ResourceReadResult, error)
	getPromptFunc    func(ctx context.Context, name string, arguments map[string]any) (*vmcp.PromptGetResult, error)
	sessID           string
	closeCalled      atomic.Bool
	closeErr         error
}

func (m *mockConnectedBackend) CallTool(ctx context.Context, toolName string, arguments, meta map[string]any) (*vmcp.ToolCallResult, error) {
	if m.callToolFunc != nil {
		return m.callToolFunc(ctx, toolName, arguments, meta)
	}
	return &vmcp.ToolCallResult{Content: []vmcp.Content{{Type: "text", Text: "ok"}}}, nil
}

func (m *mockConnectedBackend) ReadResource(ctx context.Context, uri string) (*vmcp.ResourceReadResult, error) {
	if m.readResourceFunc != nil {
		return m.readResourceFunc(ctx, uri)
	}
	return &vmcp.ResourceReadResult{Contents: []byte("data"), MimeType: "text/plain"}, nil
}

func (m *mockConnectedBackend) GetPrompt(ctx context.Context, name string, arguments map[string]any) (*vmcp.PromptGetResult, error) {
	if m.getPromptFunc != nil {
		return m.getPromptFunc(ctx, name, arguments)
	}
	return &vmcp.PromptGetResult{Messages: "hello"}, nil
}

func (m *mockConnectedBackend) SessionID() string { return m.sessID }
func (m *mockConnectedBackend) Close() error {
	m.closeCalled.Store(true)
	return m.closeErr
}

// buildTestSession creates a defaultMultiSession wired with mock backends.
//
//nolint:unparam // backendID is intentionally a parameter for readability; callers consistently use "b1"
func buildTestSession(
	t *testing.T,
	backendID string,
	conn internalbk.Session,
	tools []vmcp.Tool,
	resources []vmcp.Resource,
	prompts []vmcp.Prompt,
) *defaultMultiSession {
	t.Helper()

	target := &vmcp.BackendTarget{
		WorkloadID:   backendID,
		WorkloadName: backendID,
		BaseURL:      "http://localhost:9999",
	}

	rt := &vmcp.RoutingTable{
		Tools:     make(map[string]*vmcp.BackendTarget),
		Resources: make(map[string]*vmcp.BackendTarget),
		Prompts:   make(map[string]*vmcp.BackendTarget),
	}
	for _, tool := range tools {
		rt.Tools[tool.Name] = target
	}
	for _, res := range resources {
		rt.Resources[res.URI] = target
	}
	for _, prompt := range prompts {
		rt.Prompts[prompt.Name] = target
	}

	return &defaultMultiSession{
		Session:         transportsession.NewStreamableSession("test-session-id"),
		connections:     map[string]internalbk.Session{backendID: conn},
		routingTable:    rt,
		tools:           tools,
		resources:       resources,
		prompts:         prompts,
		backendSessions: map[string]string{backendID: "backend-session-abc"},
		queue:           newAdmissionQueue(),
	}
}

// ---------------------------------------------------------------------------
// Interface composition
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// Tools / Resources / Prompts accessors
// ---------------------------------------------------------------------------

func TestDefaultSession_Accessors(t *testing.T) {
	t.Parallel()

	tools := []vmcp.Tool{{Name: "search", BackendID: "b1"}}
	resources := []vmcp.Resource{{URI: "file://readme", BackendID: "b1"}}
	prompts := []vmcp.Prompt{{Name: "greet", BackendID: "b1"}}

	sess := buildTestSession(t, "b1", &mockConnectedBackend{}, tools, resources, prompts)

	assert.Equal(t, tools, sess.Tools())
	assert.Equal(t, resources, sess.Resources())
	assert.Equal(t, prompts, sess.Prompts())

	bs := sess.BackendSessions()
	assert.Equal(t, "backend-session-abc", bs["b1"])
	// Returned map is a copy — mutating it must not affect the session.
	bs["b1"] = "mutated"
	assert.Equal(t, "backend-session-abc", sess.BackendSessions()["b1"])
}

// ---------------------------------------------------------------------------
// CallTool
// ---------------------------------------------------------------------------

func TestDefaultSession_CallTool(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		toolName    string
		mockFn      func(ctx context.Context, toolName string, arguments, meta map[string]any) (*vmcp.ToolCallResult, error)
		wantErr     bool
		wantErrIs   error
		wantContent string
	}{
		{
			name:     "successful tool call",
			toolName: "search",
			mockFn: func(_ context.Context, _ string, _, _ map[string]any) (*vmcp.ToolCallResult, error) {
				return &vmcp.ToolCallResult{Content: []vmcp.Content{{Type: "text", Text: "result"}}}, nil
			},
			wantContent: "result",
		},
		{
			name:      "tool not in routing table",
			toolName:  "nonexistent",
			wantErr:   true,
			wantErrIs: ErrToolNotFound,
		},
		{
			name:     "backend returns error",
			toolName: "search",
			mockFn: func(_ context.Context, _ string, _, _ map[string]any) (*vmcp.ToolCallResult, error) {
				return nil, errors.New("backend boom")
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock := &mockConnectedBackend{callToolFunc: tt.mockFn}
			sess := buildTestSession(t, "b1", mock,
				[]vmcp.Tool{{Name: "search", BackendID: "b1"}},
				nil, nil,
			)

			result, err := sess.CallTool(context.Background(), tt.toolName, nil, nil)
			if tt.wantErr {
				require.Error(t, err)
				if tt.wantErrIs != nil {
					assert.ErrorIs(t, err, tt.wantErrIs)
				}
				return
			}
			require.NoError(t, err)
			require.NotNil(t, result)
			assert.Equal(t, tt.wantContent, result.Content[0].Text)
		})
	}
}

// ---------------------------------------------------------------------------
// ReadResource
// ---------------------------------------------------------------------------

func TestDefaultSession_ReadResource(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		uri       string
		mockFn    func(ctx context.Context, uri string) (*vmcp.ResourceReadResult, error)
		wantErr   bool
		wantErrIs error
		wantData  string
	}{
		{
			name: "successful read",
			uri:  "file://readme",
			mockFn: func(_ context.Context, _ string) (*vmcp.ResourceReadResult, error) {
				return &vmcp.ResourceReadResult{Contents: []byte("hello"), MimeType: "text/plain"}, nil
			},
			wantData: "hello",
		},
		{
			name:      "resource not in routing table",
			uri:       "file://missing",
			wantErr:   true,
			wantErrIs: ErrResourceNotFound,
		},
		{
			name: "backend returns error",
			uri:  "file://readme",
			mockFn: func(_ context.Context, _ string) (*vmcp.ResourceReadResult, error) {
				return nil, errors.New("backend boom")
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock := &mockConnectedBackend{readResourceFunc: tt.mockFn}
			sess := buildTestSession(t, "b1", mock,
				nil,
				[]vmcp.Resource{{URI: "file://readme", BackendID: "b1"}},
				nil,
			)

			result, err := sess.ReadResource(context.Background(), tt.uri)
			if tt.wantErr {
				require.Error(t, err)
				if tt.wantErrIs != nil {
					assert.ErrorIs(t, err, tt.wantErrIs)
				}
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantData, string(result.Contents))
		})
	}
}

// ---------------------------------------------------------------------------
// GetPrompt
// ---------------------------------------------------------------------------

func TestDefaultSession_GetPrompt(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		prompt    string
		mockFn    func(ctx context.Context, name string, arguments map[string]any) (*vmcp.PromptGetResult, error)
		wantErr   bool
		wantErrIs error
		wantMsg   string
	}{
		{
			name:   "successful get",
			prompt: "greet",
			mockFn: func(_ context.Context, _ string, _ map[string]any) (*vmcp.PromptGetResult, error) {
				return &vmcp.PromptGetResult{Messages: "hi there"}, nil
			},
			wantMsg: "hi there",
		},
		{
			name:      "prompt not in routing table",
			prompt:    "missing",
			wantErr:   true,
			wantErrIs: ErrPromptNotFound,
		},
		{
			name:   "backend error is propagated",
			prompt: "greet",
			mockFn: func(_ context.Context, _ string, _ map[string]any) (*vmcp.PromptGetResult, error) {
				return nil, errors.New("backend unavailable")
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock := &mockConnectedBackend{getPromptFunc: tt.mockFn}
			sess := buildTestSession(t, "b1", mock,
				nil, nil,
				[]vmcp.Prompt{{Name: "greet", BackendID: "b1"}},
			)

			result, err := sess.GetPrompt(context.Background(), tt.prompt, nil)
			if tt.wantErr {
				require.Error(t, err)
				if tt.wantErrIs != nil {
					assert.ErrorIs(t, err, tt.wantErrIs)
				}
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantMsg, result.Messages)
		})
	}
}

// ---------------------------------------------------------------------------
// Close
// ---------------------------------------------------------------------------

func TestDefaultSession_Close(t *testing.T) {
	t.Parallel()

	t.Run("closes all backend clients", func(t *testing.T) {
		t.Parallel()

		mock := &mockConnectedBackend{}
		sess := buildTestSession(t, "b1", mock, nil, nil, nil)

		require.NoError(t, sess.Close())
		assert.True(t, mock.closeCalled.Load())
	})

	t.Run("idempotent", func(t *testing.T) {
		t.Parallel()

		mock := &mockConnectedBackend{}
		sess := buildTestSession(t, "b1", mock, nil, nil, nil)

		require.NoError(t, sess.Close())
		require.NoError(t, sess.Close()) // second call must not panic or error
	})

	t.Run("waits for in-flight ops before closing clients", func(t *testing.T) {
		t.Parallel()

		callInProgress := make(chan struct{})
		callRelease := make(chan struct{})

		mock := &mockConnectedBackend{
			callToolFunc: func(_ context.Context, _ string, _, _ map[string]any) (*vmcp.ToolCallResult, error) {
				close(callInProgress)
				<-callRelease
				return &vmcp.ToolCallResult{}, nil
			},
		}
		sess := buildTestSession(t, "b1", mock,
			[]vmcp.Tool{{Name: "slow"}}, nil, nil,
		)

		var callDone atomic.Bool
		go func() {
			_, _ = sess.CallTool(context.Background(), "slow", nil, nil)
			callDone.Store(true)
		}()

		// Wait until the call is actually in progress.
		<-callInProgress

		closeDone := make(chan error, 1)
		go func() {
			closeDone <- sess.Close()
		}()

		// Close must not return until the call completes.
		select {
		case <-closeDone:
			t.Fatal("Close returned before in-flight call finished")
		case <-time.After(50 * time.Millisecond):
			// Expected: Close is blocking.
		}

		close(callRelease) // let the call finish
		require.NoError(t, <-closeDone)
		assert.True(t, callDone.Load())
		assert.True(t, mock.closeCalled.Load())
	})

	t.Run("returns joined error when a client fails to close", func(t *testing.T) {
		t.Parallel()

		closeErr := errors.New("close failed")
		mock := &mockConnectedBackend{closeErr: closeErr}
		sess := buildTestSession(t, "b1", mock, nil, nil, nil)

		err := sess.Close()
		require.Error(t, err)
		assert.ErrorContains(t, err, "close failed")
	})

	t.Run("operations after close return ErrSessionClosed", func(t *testing.T) {
		t.Parallel()

		mock := &mockConnectedBackend{}
		sess := buildTestSession(t, "b1", mock,
			[]vmcp.Tool{{Name: "search"}},
			[]vmcp.Resource{{URI: "file://x"}},
			[]vmcp.Prompt{{Name: "greet"}},
		)
		require.NoError(t, sess.Close())

		_, err := sess.CallTool(context.Background(), "search", nil, nil)
		assert.ErrorIs(t, err, ErrSessionClosed)

		_, err = sess.ReadResource(context.Background(), "file://x")
		assert.ErrorIs(t, err, ErrSessionClosed)

		_, err = sess.GetPrompt(context.Background(), "greet", nil)
		assert.ErrorIs(t, err, ErrSessionClosed)
	})
}

func TestDefaultSession_ErrNoBackendClient(t *testing.T) {
	t.Parallel()

	// Build a session where the routing table points to backend "b1" but the
	// connections map has no entry for it. This exercises the ErrNoBackendClient
	// path in CallTool, ReadResource, and GetPrompt.
	target := &vmcp.BackendTarget{WorkloadID: "b1"}
	sess := &defaultMultiSession{
		Session:     transportsession.NewStreamableSession("test-no-client"),
		connections: map[string]internalbk.Session{}, // deliberately empty
		routingTable: &vmcp.RoutingTable{
			Tools:     map[string]*vmcp.BackendTarget{"search": target},
			Resources: map[string]*vmcp.BackendTarget{"file://readme": target},
			Prompts:   map[string]*vmcp.BackendTarget{"greet": target},
		},
		tools:           []vmcp.Tool{{Name: "search", BackendID: "b1"}},
		resources:       []vmcp.Resource{{URI: "file://readme", BackendID: "b1"}},
		prompts:         []vmcp.Prompt{{Name: "greet", BackendID: "b1"}},
		backendSessions: map[string]string{},
		queue:           newAdmissionQueue(),
	}
	defer func() { _ = sess.Close() }()

	_, err := sess.CallTool(context.Background(), "search", nil, nil)
	require.ErrorIs(t, err, ErrNoBackendClient)

	_, err = sess.ReadResource(context.Background(), "file://readme")
	require.ErrorIs(t, err, ErrNoBackendClient)

	_, err = sess.GetPrompt(context.Background(), "greet", nil)
	require.ErrorIs(t, err, ErrNoBackendClient)
}

func TestDefaultSession_Close_AllBackendsAttemptedOnError(t *testing.T) {
	t.Parallel()

	// Both backends return a close error. Verify that both are called (the
	// error-collection loop must not short-circuit after the first failure).
	b1 := &mockConnectedBackend{closeErr: errors.New("b1 close error")}
	b2 := &mockConnectedBackend{closeErr: errors.New("b2 close error")}

	sess := &defaultMultiSession{
		Session: transportsession.NewStreamableSession("test-multi-close"),
		connections: map[string]internalbk.Session{
			"b1": b1,
			"b2": b2,
		},
		routingTable: &vmcp.RoutingTable{
			Tools:     map[string]*vmcp.BackendTarget{},
			Resources: map[string]*vmcp.BackendTarget{},
			Prompts:   map[string]*vmcp.BackendTarget{},
		},
		backendSessions: map[string]string{},
		queue:           newAdmissionQueue(),
	}

	err := sess.Close()
	require.Error(t, err)
	assert.True(t, b1.closeCalled.Load(), "b1.close must be called even though b2 also errors")
	assert.True(t, b2.closeCalled.Load(), "b2.close must be called even though b1 also errors")
	assert.ErrorContains(t, err, "b1 close error")
	assert.ErrorContains(t, err, "b2 close error")
}

// ---------------------------------------------------------------------------
// SessionFactory / MakeSession
// ---------------------------------------------------------------------------

func TestNewSessionFactory_MakeSession(t *testing.T) {
	t.Parallel()

	tool := vmcp.Tool{Name: "search", BackendID: "b1"}
	resource := vmcp.Resource{URI: "file://readme", BackendID: "b1"}
	prompt := vmcp.Prompt{Name: "greet", BackendID: "b1"}

	backend := &vmcp.Backend{
		ID:            "b1",
		Name:          "backend-1",
		BaseURL:       "http://localhost:9999",
		TransportType: "streamable-http",
	}

	//nolint:unparam // second return is always nil by design in the success-path connector
	successConnector := func(_ context.Context, _ *vmcp.BackendTarget, _ *auth.Identity) (internalbk.Session, *vmcp.CapabilityList, error) {
		return &mockConnectedBackend{sessID: "bs-1"}, &vmcp.CapabilityList{
			Tools:     []vmcp.Tool{tool},
			Resources: []vmcp.Resource{resource},
			Prompts:   []vmcp.Prompt{prompt},
		}, nil
	}

	t.Run("creates session with backend capabilities", func(t *testing.T) {
		t.Parallel()

		factory := newSessionFactoryWithConnector(successConnector)
		sess, err := factory.MakeSession(context.Background(), nil, []*vmcp.Backend{backend})
		require.NoError(t, err)
		require.NotNil(t, sess)

		assert.NotEmpty(t, sess.ID())
		assert.Equal(t, transportsession.SessionTypeStreamable, sess.Type())
		assert.Len(t, sess.Tools(), 1)
		assert.Len(t, sess.Resources(), 1)
		assert.Len(t, sess.Prompts(), 1)
		assert.Equal(t, "bs-1", sess.BackendSessions()["b1"])

		require.NoError(t, sess.Close())
	})

	t.Run("each session gets a unique ID", func(t *testing.T) {
		t.Parallel()

		factory := newSessionFactoryWithConnector(successConnector)
		s1, err := factory.MakeSession(context.Background(), nil, []*vmcp.Backend{backend})
		require.NoError(t, err)
		s2, err := factory.MakeSession(context.Background(), nil, []*vmcp.Backend{backend})
		require.NoError(t, err)

		assert.NotEqual(t, s1.ID(), s2.ID())

		require.NoError(t, s1.Close())
		require.NoError(t, s2.Close())
	})

	t.Run("no backends produces empty session", func(t *testing.T) {
		t.Parallel()

		factory := newSessionFactoryWithConnector(successConnector)
		sess, err := factory.MakeSession(context.Background(), nil, nil)
		require.NoError(t, err)
		require.NotNil(t, sess)

		assert.Empty(t, sess.Tools())
		assert.Empty(t, sess.Resources())
		assert.Empty(t, sess.Prompts())
		require.NoError(t, sess.Close())
	})

	t.Run("nil backend entries are skipped without panic", func(t *testing.T) {
		t.Parallel()

		factory := newSessionFactoryWithConnector(successConnector)
		// Mix of valid and nil entries; nil must not cause a panic.
		backends := []*vmcp.Backend{nil, backend, nil}
		sess, err := factory.MakeSession(context.Background(), nil, backends)
		require.NoError(t, err)
		require.NotNil(t, sess)

		// The one valid backend should still have been initialised.
		assert.Len(t, sess.Tools(), 1)
		require.NoError(t, sess.Close())
	})
}

func TestNewSessionFactory_PartialInitialisation(t *testing.T) {
	t.Parallel()

	backends := []*vmcp.Backend{
		{ID: "ok", Name: "ok", BaseURL: "http://ok:9999", TransportType: "streamable-http"},
		{ID: "fail", Name: "fail", BaseURL: "http://fail:9999", TransportType: "streamable-http"},
	}

	connector := func(_ context.Context, target *vmcp.BackendTarget, _ *auth.Identity) (internalbk.Session, *vmcp.CapabilityList, error) {
		if target.WorkloadID == "fail" {
			return nil, nil, errors.New("backend unavailable")
		}
		return &mockConnectedBackend{sessID: "s-ok"}, &vmcp.CapabilityList{
			Tools: []vmcp.Tool{{Name: "tool-ok", BackendID: "ok"}},
		}, nil
	}

	factory := newSessionFactoryWithConnector(connector)
	sess, err := factory.MakeSession(context.Background(), nil, backends)
	require.NoError(t, err, "partial init must not return an error")
	require.NotNil(t, sess)

	// Only the successful backend's capabilities are present.
	assert.Len(t, sess.Tools(), 1)
	assert.Equal(t, "tool-ok", sess.Tools()[0].Name)
	assert.NotContains(t, sess.BackendSessions(), "fail")

	require.NoError(t, sess.Close())
}

func TestNewSessionFactory_ConnectorReturnsNilWithoutError(t *testing.T) {
	t.Parallel()

	backend := &vmcp.Backend{ID: "b1", Name: "b1", BaseURL: "http://x:9", TransportType: "streamable-http"}

	tests := []struct {
		name          string
		connector     backendConnector
		wantConnClose bool // true when the connector returns a non-nil conn that must be closed
	}{
		{
			name: "nil conn with nil caps",
			connector: func(_ context.Context, _ *vmcp.BackendTarget, _ *auth.Identity) (internalbk.Session, *vmcp.CapabilityList, error) {
				return nil, nil, nil
			},
		},
		{
			name: "nil conn with non-nil caps",
			connector: func(_ context.Context, _ *vmcp.BackendTarget, _ *auth.Identity) (internalbk.Session, *vmcp.CapabilityList, error) {
				return nil, &vmcp.CapabilityList{}, nil
			},
		},
		{
			name:          "non-nil conn with nil caps must close conn to avoid leak",
			wantConnClose: true,
			connector: func(_ context.Context, _ *vmcp.BackendTarget, _ *auth.Identity) (internalbk.Session, *vmcp.CapabilityList, error) {
				return &mockConnectedBackend{}, nil, nil
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Replace the connector with one that captures the mock so we can
			// inspect closeCalled after MakeSession returns.
			var captured *mockConnectedBackend
			wrappedConnector := func(ctx context.Context, target *vmcp.BackendTarget, id *auth.Identity) (internalbk.Session, *vmcp.CapabilityList, error) {
				conn, caps, err := tt.connector(ctx, target, id)
				if m, ok := conn.(*mockConnectedBackend); ok {
					captured = m
				}
				return conn, caps, err
			}

			factory := newSessionFactoryWithConnector(wrappedConnector)
			sess, err := factory.MakeSession(context.Background(), nil, []*vmcp.Backend{backend})
			require.NoError(t, err)
			require.NotNil(t, sess)
			assert.Empty(t, sess.Tools())
			require.NoError(t, sess.Close())

			if tt.wantConnClose {
				require.NotNil(t, captured, "expected connector to return a mock conn")
				assert.True(t, captured.closeCalled.Load(), "leaked connection was not closed")
			}
		})
	}
}

func TestNewSessionFactory_ConnectorReturnsConnWithError(t *testing.T) {
	t.Parallel()

	// Connector returns a non-nil conn alongside an error — the conn must be
	// closed to avoid a connection leak.
	backend := &vmcp.Backend{ID: "b1", Name: "b1", BaseURL: "http://x:9", TransportType: "streamable-http"}
	leaked := &mockConnectedBackend{}

	connector := func(_ context.Context, _ *vmcp.BackendTarget, _ *auth.Identity) (internalbk.Session, *vmcp.CapabilityList, error) {
		return leaked, nil, errors.New("init failed but conn was partially opened")
	}

	factory := newSessionFactoryWithConnector(connector)
	sess, err := factory.MakeSession(context.Background(), nil, []*vmcp.Backend{backend})
	require.NoError(t, err, "partial failure must not abort the session")
	require.NotNil(t, sess)
	assert.Empty(t, sess.Tools())
	require.NoError(t, sess.Close())

	assert.True(t, leaked.closeCalled.Load(), "leaked connection was not closed")
}

func TestNewSessionFactory_CapabilityNameConflictIsResolvedDeterministically(t *testing.T) {
	t.Parallel()

	// Both backends advertise the same tool, resource, and prompt name.
	// "alpha" sorts before "zeta" alphabetically, so "alpha" must always win.
	backends := []*vmcp.Backend{
		// Intentionally listed in reverse order to prove sorting is applied.
		{ID: "zeta", Name: "zeta", BaseURL: "http://zeta:9", TransportType: "streamable-http"},
		{ID: "alpha", Name: "alpha", BaseURL: "http://alpha:9", TransportType: "streamable-http"},
	}

	connector := func(_ context.Context, target *vmcp.BackendTarget, _ *auth.Identity) (internalbk.Session, *vmcp.CapabilityList, error) {
		return &mockConnectedBackend{sessID: target.WorkloadID}, &vmcp.CapabilityList{
			Tools:     []vmcp.Tool{{Name: "fetch", BackendID: target.WorkloadID}},
			Resources: []vmcp.Resource{{URI: "file://data", BackendID: target.WorkloadID}},
			Prompts:   []vmcp.Prompt{{Name: "greet", BackendID: target.WorkloadID}},
		}, nil
	}

	factory := newSessionFactoryWithConnector(connector)
	sess, err := factory.MakeSession(context.Background(), nil, backends)
	require.NoError(t, err)
	require.NotNil(t, sess)
	defer func() { require.NoError(t, sess.Close()) }()

	// Each capability should appear exactly once (no duplicates).
	require.Len(t, sess.Tools(), 1)
	require.Len(t, sess.Resources(), 1)
	require.Len(t, sess.Prompts(), 1)

	// "alpha" must win because it sorts before "zeta".
	assert.Equal(t, "alpha", sess.Tools()[0].BackendID)
	assert.Equal(t, "alpha", sess.Resources()[0].BackendID)
	assert.Equal(t, "alpha", sess.Prompts()[0].BackendID)

	// Calling the conflicted tool must reach "alpha", not "zeta".
	result, err := sess.CallTool(context.Background(), "fetch", nil, nil)
	require.NoError(t, err)
	require.NotNil(t, result)
}

func TestNewSessionFactory_AllBackendsFail(t *testing.T) {
	t.Parallel()

	backend := &vmcp.Backend{ID: "b1", Name: "b1", BaseURL: "http://x:9", TransportType: "streamable-http"}
	connector := func(_ context.Context, _ *vmcp.BackendTarget, _ *auth.Identity) (internalbk.Session, *vmcp.CapabilityList, error) {
		return nil, nil, errors.New("down")
	}

	factory := newSessionFactoryWithConnector(connector)
	sess, err := factory.MakeSession(context.Background(), nil, []*vmcp.Backend{backend})
	require.NoError(t, err, "all-fail must still return a valid (empty) session")
	require.NotNil(t, sess)

	assert.Empty(t, sess.Tools())
	require.NoError(t, sess.Close())
}

func TestNewSessionFactory_BackendInitTimeout(t *testing.T) {
	t.Parallel()

	backend := &vmcp.Backend{ID: "slow", Name: "slow", BaseURL: "http://x:9", TransportType: "streamable-http"}

	released := make(chan struct{})
	connector := func(ctx context.Context, _ *vmcp.BackendTarget, _ *auth.Identity) (internalbk.Session, *vmcp.CapabilityList, error) {
		select {
		case <-ctx.Done():
			return nil, nil, ctx.Err()
		case <-released:
			return &mockConnectedBackend{}, &vmcp.CapabilityList{}, nil
		}
	}

	factory := newSessionFactoryWithConnector(connector, WithBackendInitTimeout(50*time.Millisecond))
	sess, err := factory.MakeSession(context.Background(), nil, []*vmcp.Backend{backend})
	require.NoError(t, err, "timeout is a partial failure, not a hard error")
	require.NotNil(t, sess)

	// Timed-out backend produces no capabilities.
	assert.Empty(t, sess.Tools())
	close(released) // allow goroutine to unblock
	require.NoError(t, sess.Close())
}

func TestNewSessionFactory_ParallelInit(t *testing.T) {
	t.Parallel()

	const numBackends = 5
	backends := make([]*vmcp.Backend, numBackends)
	for i := range backends {
		backends[i] = &vmcp.Backend{
			ID:            fmt.Sprintf("b%d", i),
			Name:          fmt.Sprintf("b%d", i),
			BaseURL:       "http://x:9",
			TransportType: "streamable-http",
		}
	}

	var initCount atomic.Int32
	var mu sync.Mutex
	var maxConcurrent, current int32

	connector := func(_ context.Context, target *vmcp.BackendTarget, _ *auth.Identity) (internalbk.Session, *vmcp.CapabilityList, error) {
		mu.Lock()
		current++
		if current > maxConcurrent {
			maxConcurrent = current
		}
		mu.Unlock()

		time.Sleep(10 * time.Millisecond) // simulate network latency
		initCount.Add(1)

		mu.Lock()
		current--
		mu.Unlock()

		return &mockConnectedBackend{sessID: target.WorkloadID}, &vmcp.CapabilityList{
			Tools: []vmcp.Tool{{Name: "t-" + target.WorkloadID, BackendID: target.WorkloadID}},
		}, nil
	}

	factory := newSessionFactoryWithConnector(connector, WithMaxBackendInitConcurrency(3))
	sess, err := factory.MakeSession(context.Background(), nil, backends)
	require.NoError(t, err)

	// All backends must have been initialised.
	assert.Equal(t, int32(numBackends), initCount.Load())
	assert.Len(t, sess.Tools(), numBackends)

	// Concurrency limit must have been respected.
	assert.LessOrEqual(t, maxConcurrent, int32(3))

	require.NoError(t, sess.Close())
}

func TestNewSessionFactory_MakeSession_Metadata(t *testing.T) {
	t.Parallel()

	backend1 := &vmcp.Backend{ID: "b1", Name: "backend-1", BaseURL: "http://localhost:9001", TransportType: "streamable-http"}
	backend2 := &vmcp.Backend{ID: "b2", Name: "backend-2", BaseURL: "http://localhost:9002", TransportType: "streamable-http"}

	//nolint:unparam // error return is always nil by design in the success-path connector
	successConnector := func(_ context.Context, _ *vmcp.BackendTarget, _ *auth.Identity) (internalbk.Session, *vmcp.CapabilityList, error) {
		return &mockConnectedBackend{}, &vmcp.CapabilityList{}, nil
	}
	failConnector := func(_ context.Context, _ *vmcp.BackendTarget, _ *auth.Identity) (internalbk.Session, *vmcp.CapabilityList, error) {
		return nil, nil, errors.New("connection refused")
	}

	tests := []struct {
		name           string
		connector      backendConnector
		identity       *auth.Identity
		backends       []*vmcp.Backend
		wantSubject    string // non-empty → assert equal; empty → assert key absent
		wantBackendIDs string // non-empty → assert equal; empty → assert key absent
	}{
		{
			name:           "sets identity subject and backend IDs",
			connector:      successConnector,
			identity:       &auth.Identity{Subject: "user-123"},
			backends:       []*vmcp.Backend{backend1},
			wantSubject:    "user-123",
			wantBackendIDs: "b1",
		},
		{
			name:           "omits subject when identity is nil",
			connector:      successConnector,
			identity:       nil,
			backends:       []*vmcp.Backend{backend1},
			wantBackendIDs: "b1",
		},
		{
			name:           "omits subject when subject is empty",
			connector:      successConnector,
			identity:       &auth.Identity{Subject: ""},
			backends:       []*vmcp.Backend{backend1},
			wantBackendIDs: "b1",
		},
		{
			name:           "backend IDs are sorted",
			connector:      successConnector,
			backends:       []*vmcp.Backend{backend2, backend1}, // intentionally reversed
			wantBackendIDs: "b1,b2",
		},
		{
			name:      "omits backend IDs when no backends connect",
			connector: failConnector,
			backends:  []*vmcp.Backend{backend1},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			factory := newSessionFactoryWithConnector(tt.connector)
			sess, err := factory.MakeSession(context.Background(), tt.identity, tt.backends)
			require.NoError(t, err)
			require.NotNil(t, sess)
			defer func() { require.NoError(t, sess.Close()) }()

			meta := sess.GetMetadata()

			if tt.wantSubject != "" {
				assert.Equal(t, tt.wantSubject, meta[MetadataKeyIdentitySubject])
			} else {
				_, ok := meta[MetadataKeyIdentitySubject]
				assert.False(t, ok, "identity subject key should be absent")
			}

			if tt.wantBackendIDs != "" {
				assert.Equal(t, tt.wantBackendIDs, meta[MetadataKeyBackendIDs])
			} else {
				_, ok := meta[MetadataKeyBackendIDs]
				assert.False(t, ok, "backend IDs key should be absent")
			}
		})
	}
}

// ---------------------------------------------------------------------------
// buildRoutingTable
// ---------------------------------------------------------------------------

func TestBuildRoutingTable(t *testing.T) {
	t.Parallel()

	target := func(id string) *vmcp.BackendTarget {
		return &vmcp.BackendTarget{WorkloadID: id, WorkloadName: id}
	}

	tests := []struct {
		name          string
		results       []initResult
		wantTools     []string // expected tool names in order
		wantResources []string // expected resource URIs in order
		wantPrompts   []string // expected prompt names in order
		// When a capability appears in multiple backends, wantWinner[capName] is
		// the expected winning WorkloadID.
		wantWinner map[string]string
	}{
		{
			name:          "empty input",
			results:       nil,
			wantTools:     nil,
			wantResources: nil,
			wantPrompts:   nil,
		},
		{
			name: "single backend all capability types",
			results: []initResult{
				{
					target: target("a"),
					caps: &vmcp.CapabilityList{
						Tools:     []vmcp.Tool{{Name: "t1"}, {Name: "t2"}},
						Resources: []vmcp.Resource{{URI: "res://1"}, {URI: "res://2"}},
						Prompts:   []vmcp.Prompt{{Name: "p1"}},
					},
				},
			},
			wantTools:     []string{"t1", "t2"},
			wantResources: []string{"res://1", "res://2"},
			wantPrompts:   []string{"p1"},
		},
		{
			name: "conflict resolution: first backend in sorted order wins",
			results: []initResult{
				// Pre-sorted: "alpha" before "zeta"
				{
					target: target("alpha"),
					caps: &vmcp.CapabilityList{
						Tools: []vmcp.Tool{{Name: "shared"}},
					},
				},
				{
					target: target("zeta"),
					caps: &vmcp.CapabilityList{
						Tools: []vmcp.Tool{{Name: "shared"}},
					},
				},
			},
			wantTools:  []string{"shared"},
			wantWinner: map[string]string{"shared": "alpha"},
		},
		{
			name: "non-conflicting capabilities from two backends are merged",
			results: []initResult{
				{
					target: target("a"),
					caps:   &vmcp.CapabilityList{Tools: []vmcp.Tool{{Name: "t-a"}}},
				},
				{
					target: target("b"),
					caps:   &vmcp.CapabilityList{Tools: []vmcp.Tool{{Name: "t-b"}}},
				},
			},
			wantTools: []string{"t-a", "t-b"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			rt, tools, resources, prompts := buildRoutingTable(tt.results)
			require.NotNil(t, rt)

			// Check list lengths and names.
			toolNames := make([]string, len(tools))
			for i, t := range tools {
				toolNames[i] = t.Name
			}
			if tt.wantTools == nil {
				assert.Empty(t, tools)
			} else {
				assert.Equal(t, tt.wantTools, toolNames)
			}

			resURIs := make([]string, len(resources))
			for i, r := range resources {
				resURIs[i] = r.URI
			}
			if tt.wantResources == nil {
				assert.Empty(t, resources)
			} else {
				assert.Equal(t, tt.wantResources, resURIs)
			}

			promptNames := make([]string, len(prompts))
			for i, p := range prompts {
				promptNames[i] = p.Name
			}
			if tt.wantPrompts == nil {
				assert.Empty(t, prompts)
			} else {
				assert.Equal(t, tt.wantPrompts, promptNames)
			}

			// Check conflict winners.
			for capName, wantBackend := range tt.wantWinner {
				if got, ok := rt.Tools[capName]; ok {
					assert.Equal(t, wantBackend, got.WorkloadID, "tool %q winner", capName)
				} else if got, ok := rt.Resources[capName]; ok {
					assert.Equal(t, wantBackend, got.WorkloadID, "resource %q winner", capName)
				} else if got, ok := rt.Prompts[capName]; ok {
					assert.Equal(t, wantBackend, got.WorkloadID, "prompt %q winner", capName)
				} else {
					t.Errorf("capability %q not found in any routing table", capName)
				}
			}
		})
	}
}

func TestWithMaxBackendInitConcurrency_IgnoresNonPositive(t *testing.T) {
	t.Parallel()

	f := &defaultMultiSessionFactory{maxConcurrency: defaultMaxBackendInitConcurrency}
	WithMaxBackendInitConcurrency(0)(f)
	assert.Equal(t, defaultMaxBackendInitConcurrency, f.maxConcurrency)

	WithMaxBackendInitConcurrency(-5)(f)
	assert.Equal(t, defaultMaxBackendInitConcurrency, f.maxConcurrency)
}

func TestWithBackendInitTimeout_IgnoresNonPositive(t *testing.T) {
	t.Parallel()

	f := &defaultMultiSessionFactory{backendInitTimeout: defaultBackendInitTimeout}
	WithBackendInitTimeout(0)(f)
	assert.Equal(t, defaultBackendInitTimeout, f.backendInitTimeout)

	WithBackendInitTimeout(-time.Second)(f)
	assert.Equal(t, defaultBackendInitTimeout, f.backendInitTimeout)
}

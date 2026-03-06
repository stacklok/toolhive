// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package sessionmanager_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/auth"
	transportsession "github.com/stacklok/toolhive/pkg/transport/session"
	"github.com/stacklok/toolhive/pkg/vmcp"
	vmcpauth "github.com/stacklok/toolhive/pkg/vmcp/auth"
	"github.com/stacklok/toolhive/pkg/vmcp/auth/strategies"
	authtypes "github.com/stacklok/toolhive/pkg/vmcp/auth/types"
	"github.com/stacklok/toolhive/pkg/vmcp/server/sessionmanager"
	vmcpsession "github.com/stacklok/toolhive/pkg/vmcp/session"
)

// createTestFactory creates a MultiSessionFactory for testing
func createTestFactory(t *testing.T) vmcpsession.MultiSessionFactory {
	t.Helper()

	authReg := vmcpauth.NewDefaultOutgoingAuthRegistry()
	require.NoError(t, authReg.RegisterStrategy(
		authtypes.StrategyTypeUnauthenticated,
		strategies.NewUnauthenticatedStrategy(),
	))

	return vmcpsession.NewSessionFactory(authReg)
}

func TestManager_ListActiveSessions_EmptyStore(t *testing.T) {
	t.Parallel()

	// Create a manager with empty session store
	storage := transportsession.NewTypedManager(30*time.Minute, transportsession.SessionTypeMCP)
	defer storage.Stop()

	factory := createTestFactory(t)
	mgr := sessionmanager.New(storage, factory, vmcp.NewImmutableRegistry([]vmcp.Backend{}))

	// List sessions from empty store
	activeCount, backendUsage := mgr.ListActiveSessions()

	assert.Equal(t, 0, activeCount, "Should have 0 active sessions")
	assert.NotNil(t, backendUsage, "Should return non-nil map")
	assert.Empty(t, backendUsage, "Should return empty map for empty store")
}

func TestManager_ListActiveSessions_WithPlaceholderSessions(t *testing.T) {
	t.Parallel()

	// Create a manager with session store
	storage := transportsession.NewTypedManager(30*time.Minute, transportsession.SessionTypeMCP)
	defer storage.Stop()

	factory := createTestFactory(t)
	mgr := sessionmanager.New(storage, factory, vmcp.NewImmutableRegistry([]vmcp.Backend{}))

	// Generate some placeholder sessions (Phase 1 only, not fully initialized)
	sessionID1 := mgr.Generate()
	sessionID2 := mgr.Generate()

	assert.NotEmpty(t, sessionID1)
	assert.NotEmpty(t, sessionID2)

	// List sessions - placeholders should be skipped
	activeCount, backendUsage := mgr.ListActiveSessions()

	// Placeholder sessions (not yet MultiSession) should not be included
	assert.Equal(t, 0, activeCount, "Placeholder sessions should not be counted")
	assert.Empty(t, backendUsage, "Placeholder sessions should not contribute to backend usage")
}

func TestManager_ListActiveSessions_ReturnsEmptyMapNotNil(t *testing.T) {
	t.Parallel()

	storage := transportsession.NewTypedManager(30*time.Minute, transportsession.SessionTypeMCP)
	defer storage.Stop()

	factory := createTestFactory(t)
	mgr := sessionmanager.New(storage, factory, vmcp.NewImmutableRegistry([]vmcp.Backend{}))

	// Even with no sessions, should return non-nil empty map
	activeCount, backendUsage := mgr.ListActiveSessions()

	assert.Equal(t, 0, activeCount, "Should have 0 active sessions")
	assert.NotNil(t, backendUsage, "Should return non-nil map")
	assert.Len(t, backendUsage, 0, "Should be empty")
}

// TestManager_ListActiveSessions_SkipsNonMultiSession verifies that
// only MultiSession instances are included in the results.
func TestManager_ListActiveSessions_SkipsNonMultiSession(t *testing.T) {
	t.Parallel()

	storage := transportsession.NewTypedManager(30*time.Minute, transportsession.SessionTypeMCP)
	defer storage.Stop()

	factory := createTestFactory(t)
	mgr := sessionmanager.New(storage, factory, vmcp.NewImmutableRegistry([]vmcp.Backend{}))

	// Generate placeholders - these are ProxySession, not MultiSession
	_ = mgr.Generate()
	_ = mgr.Generate()

	// Also manually add a non-MultiSession session to storage
	plainSession := transportsession.NewProxySession("plain-session")
	err := storage.AddSession(plainSession)
	require.NoError(t, err)

	// List should skip all non-MultiSession types
	activeCount, backendUsage := mgr.ListActiveSessions()

	assert.Equal(t, 0, activeCount, "Should skip non-MultiSession instances")
	assert.Empty(t, backendUsage, "Should have no backend usage from non-MultiSession")
}

// TestManager_ListActiveSessions_BackendUsageStructure verifies the
// structure of BackendUsage returned by ListActiveSessions.
func TestManager_ListActiveSessions_BackendUsageStructure(t *testing.T) {
	t.Parallel()

	// This test verifies that BackendUsage has the expected fields by
	// creating an instance and checking it can be constructed.
	usage := sessionmanager.BackendUsage{
		SessionCount: 5,
		HealthyCount: 4,
		FailedCount:  1,
	}

	assert.Equal(t, 5, usage.SessionCount)
	assert.Equal(t, 4, usage.HealthyCount)
	assert.Equal(t, 1, usage.FailedCount)
}

// mockMultiSession is a minimal mock that implements vmcpsession.MultiSession
// for testing purposes. It embeds a real ProxySession for base functionality
// and adds the MultiSession-specific methods.
type mockMultiSession struct {
	*transportsession.ProxySession
	backendSessions map[string]string
	tools           []vmcp.Tool
	resources       []vmcp.Resource
	prompts         []vmcp.Prompt
}

func newMockMultiSession(id string, backendSessions map[string]string) *mockMultiSession {
	return &mockMultiSession{
		ProxySession:    transportsession.NewProxySession(id),
		backendSessions: backendSessions,
		tools:           []vmcp.Tool{},
		resources:       []vmcp.Resource{},
		prompts:         []vmcp.Prompt{},
	}
}

func (m *mockMultiSession) BackendSessions() map[string]string {
	return m.backendSessions
}

func (m *mockMultiSession) Tools() []vmcp.Tool {
	return m.tools
}

func (m *mockMultiSession) Resources() []vmcp.Resource {
	return m.resources
}

func (m *mockMultiSession) Prompts() []vmcp.Prompt {
	return m.prompts
}

// Implement Caller interface methods (required but not used in these tests)
func (*mockMultiSession) CallTool(_ context.Context, _ *auth.Identity, _ string, _, _ map[string]any) (*vmcp.ToolCallResult, error) {
	return nil, nil
}

func (*mockMultiSession) ReadResource(_ context.Context, _ *auth.Identity, _ string) (*vmcp.ResourceReadResult, error) {
	return nil, nil
}

func (*mockMultiSession) GetPrompt(_ context.Context, _ *auth.Identity, _ string, _ map[string]any) (*vmcp.PromptGetResult, error) {
	return nil, nil
}

func (*mockMultiSession) Close() error {
	return nil
}

// Compile-time check that mockMultiSession implements MultiSession
var _ vmcpsession.MultiSession = (*mockMultiSession)(nil)

// TestManager_ListActiveSessions_WithMockMultiSession tests the iteration
// logic and backend aggregation by manually storing mock MultiSession instances.
func TestManager_ListActiveSessions_WithMockMultiSession(t *testing.T) {
	t.Parallel()

	storage := transportsession.NewTypedManager(30*time.Minute, transportsession.SessionTypeMCP)
	defer storage.Stop()

	factory := createTestFactory(t)
	mgr := sessionmanager.New(storage, factory, vmcp.NewImmutableRegistry([]vmcp.Backend{}))

	// Manually add mock MultiSessions to storage
	session1 := newMockMultiSession("session-1", map[string]string{
		"backend1": "backend1-session-id",
		"backend2": "backend2-session-id",
	})
	session2 := newMockMultiSession("session-2", map[string]string{
		"backend1": "backend1-session-id-2", // backend1 used by 2 sessions
		"backend3": "backend3-session-id",
	})

	err := storage.AddSession(session1)
	require.NoError(t, err)
	err = storage.AddSession(session2)
	require.NoError(t, err)

	// Also add a placeholder to verify it's skipped
	placeholder := transportsession.NewProxySession("placeholder-session")
	err = storage.AddSession(placeholder)
	require.NoError(t, err)

	// Verify storage has the sessions
	t.Logf("Storage count before listing: %d", storage.Count())

	// List sessions
	activeCount, backendUsage := mgr.ListActiveSessions()

	// Should have 2 active sessions (placeholder should be skipped)
	t.Logf("ListActiveSessions returned %d sessions, %d backends", activeCount, len(backendUsage))
	assert.Equal(t, 2, activeCount, "Should have 2 active MultiSession instances")
	assert.Len(t, backendUsage, 3, "Should have 3 unique backends")

	// Verify backend1 (used by 2 sessions)
	backend1Usage, ok := backendUsage["backend1"]
	require.True(t, ok, "Should include backend1")
	assert.Equal(t, 2, backend1Usage.SessionCount, "backend1 should be used by 2 sessions")
	assert.Equal(t, 2, backend1Usage.HealthyCount, "backend1 should have 2 healthy connections")
	assert.Equal(t, 0, backend1Usage.FailedCount, "backend1 should have 0 failed connections")

	// Verify backend2 (used by 1 session)
	backend2Usage, ok := backendUsage["backend2"]
	require.True(t, ok, "Should include backend2")
	assert.Equal(t, 1, backend2Usage.SessionCount, "backend2 should be used by 1 session")
	assert.Equal(t, 1, backend2Usage.HealthyCount, "backend2 should have 1 healthy connection")
	assert.Equal(t, 0, backend2Usage.FailedCount, "backend2 should have 0 failed connections")

	// Verify backend3 (used by 1 session)
	backend3Usage, ok := backendUsage["backend3"]
	require.True(t, ok, "Should include backend3")
	assert.Equal(t, 1, backend3Usage.SessionCount, "backend3 should be used by 1 session")
	assert.Equal(t, 1, backend3Usage.HealthyCount, "backend3 should have 1 healthy connection")
	assert.Equal(t, 0, backend3Usage.FailedCount, "backend3 should have 0 failed connections")
}

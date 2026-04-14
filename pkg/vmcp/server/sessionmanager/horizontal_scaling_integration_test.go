// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package sessionmanager

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	mcpmcp "github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/auth"
	transportsession "github.com/stacklok/toolhive/pkg/transport/session"
	"github.com/stacklok/toolhive/pkg/vmcp"
	vmcpauth "github.com/stacklok/toolhive/pkg/vmcp/auth"
	"github.com/stacklok/toolhive/pkg/vmcp/auth/strategies"
	authtypes "github.com/stacklok/toolhive/pkg/vmcp/auth/types"
	vmcpsession "github.com/stacklok/toolhive/pkg/vmcp/session"
	sessiontypes "github.com/stacklok/toolhive/pkg/vmcp/session/types"
)

// hmacSecret is a fixed 32-byte secret used across all integration tests.
var hmacSecret = []byte("test-hmac-secret-32bytes-exactly")

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// newUnauthenticatedAuthRegistry builds an OutgoingAuthRegistry with only the
// unauthenticated strategy registered — suitable for tests whose backend MCP
// servers require no auth.
func newUnauthenticatedAuthRegistry(t *testing.T) vmcpauth.OutgoingAuthRegistry {
	t.Helper()
	reg := vmcpauth.NewDefaultOutgoingAuthRegistry()
	require.NoError(t, reg.RegisterStrategy(authtypes.StrategyTypeUnauthenticated, strategies.NewUnauthenticatedStrategy()))
	return reg
}

// newSharedRedisStorage creates a RedisSessionDataStorage pointing at mr.
// The storage is closed via t.Cleanup.
func newSharedRedisStorage(t *testing.T, mr *miniredis.Miniredis) transportsession.DataStorage {
	t.Helper()
	storage, err := transportsession.NewRedisSessionDataStorage(
		context.Background(),
		transportsession.RedisConfig{
			Addr:      mr.Addr(),
			KeyPrefix: "test:vmcp:session:",
		},
		time.Hour,
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = storage.Close() })
	return storage
}

// newTestManagerWithSharedStorage creates a Manager backed by the given
// DataStorage, a real session factory with the package-level hmacSecret, and
// an ImmutableRegistry containing backends. Cleanup is registered via
// t.Cleanup.
func newTestManagerWithSharedStorage(t *testing.T, storage transportsession.DataStorage, backends []*vmcp.Backend) *Manager {
	t.Helper()
	backendList := make([]vmcp.Backend, len(backends))
	for i, b := range backends {
		backendList[i] = *b
	}
	registry := vmcp.NewImmutableRegistry(backendList)
	factory := vmcpsession.NewSessionFactory(
		newUnauthenticatedAuthRegistry(t),
		vmcpsession.WithHMACSecret(hmacSecret),
	)
	sm, cleanup, err := New(storage, &FactoryConfig{Base: factory}, registry)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, cleanup(context.Background())) })
	return sm
}

// createSession runs the two-phase Generate + CreateSession flow.
// identity may be nil for anonymous sessions.
// Returns the assigned session ID.
func createSession(t *testing.T, sm *Manager, identity *auth.Identity) string {
	t.Helper()
	sessionID := sm.Generate()
	require.NotEmpty(t, sessionID)
	ctx := context.Background()
	if identity != nil {
		ctx = auth.WithIdentity(ctx, identity)
	}
	_, err := sm.CreateSession(ctx, sessionID)
	require.NoError(t, err)
	return sessionID
}

// startMCPBackend starts an in-process streamable-HTTP MCP server that
// exposes a single tool named toolName (which echoes its "input" argument).
// The server is shut down when t completes.
// Returns a *vmcp.Backend pointing at the server.
func startMCPBackend(t *testing.T, backendID, toolName string) *vmcp.Backend {
	t.Helper()
	mcpSrv := mcpserver.NewMCPServer(backendID, "1.0.0")
	mcpSrv.AddTool(
		mcpmcp.NewTool(toolName,
			mcpmcp.WithDescription("Echoes the input argument"),
			mcpmcp.WithString("input", mcpmcp.Required()),
		),
		func(_ context.Context, req mcpmcp.CallToolRequest) (*mcpmcp.CallToolResult, error) {
			args, _ := req.Params.Arguments.(map[string]any)
			input, _ := args["input"].(string)
			return &mcpmcp.CallToolResult{
				Content: []mcpmcp.Content{mcpmcp.NewTextContent(input)},
			}, nil
		},
	)
	streamableSrv := mcpserver.NewStreamableHTTPServer(mcpSrv)
	mux := http.NewServeMux()
	mux.Handle("/mcp", streamableSrv)
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return &vmcp.Backend{
		ID:            backendID,
		Name:          backendID,
		BaseURL:       ts.URL + "/mcp",
		TransportType: "streamable-http",
	}
}

// startStoppableMCPBackend is like startMCPBackend but also returns a stop
// function so the caller can shut the backend down mid-test (e.g. to simulate
// a backend going away). The stop function is idempotent and is also
// registered with t.Cleanup so the server is always closed even if the test
// fails before the caller invokes stop.
func startStoppableMCPBackend(t *testing.T, backendID, toolName string) (*vmcp.Backend, func()) {
	t.Helper()
	mcpSrv := mcpserver.NewMCPServer(backendID, "1.0.0")
	mcpSrv.AddTool(
		mcpmcp.NewTool(toolName,
			mcpmcp.WithDescription("Echoes the input argument"),
			mcpmcp.WithString("input", mcpmcp.Required()),
		),
		func(_ context.Context, req mcpmcp.CallToolRequest) (*mcpmcp.CallToolResult, error) {
			args, _ := req.Params.Arguments.(map[string]any)
			input, _ := args["input"].(string)
			return &mcpmcp.CallToolResult{
				Content: []mcpmcp.Content{mcpmcp.NewTextContent(input)},
			}, nil
		},
	)
	streamableSrv := mcpserver.NewStreamableHTTPServer(mcpSrv)
	mux := http.NewServeMux()
	mux.Handle("/mcp", streamableSrv)
	ts := httptest.NewServer(mux)
	var once sync.Once
	stop := func() { once.Do(ts.Close) }
	t.Cleanup(stop)
	return &vmcp.Backend{
		ID:            backendID,
		Name:          backendID,
		BaseURL:       ts.URL + "/mcp",
		TransportType: "streamable-http",
	}, stop
}

// ---------------------------------------------------------------------------
// AC1: Cross-pod session reconstruction
// ---------------------------------------------------------------------------

// TestHorizontalScaling_CrossPodReconstruction verifies that a session
// created on "pod A" (Manager A) can be reconstructed on "pod B" (Manager B)
// via GetMultiSession → cache miss → RestoreSession from Redis.
func TestHorizontalScaling_CrossPodReconstruction(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)
	storage := newSharedRedisStorage(t, mr)
	backend := startMCPBackend(t, "backend-alpha", "echo")

	// Pod A: create a session; it is stored in Redis and cached locally in smA.
	smA := newTestManagerWithSharedStorage(t, storage, []*vmcp.Backend{backend})
	sessionID := createSession(t, smA, nil)

	// Pod B: fresh Manager, same Redis storage — session is NOT in local cache.
	smB := newTestManagerWithSharedStorage(t, storage, []*vmcp.Backend{backend})

	// GetMultiSession triggers cache miss → loadSession → RestoreSession from Redis.
	sess, ok := smB.GetMultiSession(sessionID)
	require.True(t, ok, "pod B must reconstruct the session from Redis on cache miss")
	require.NotNil(t, sess)

	// The restored session must have reconnected to the backend and discovered tools.
	require.NotEmpty(t, sess.Tools(), "restored session must have the backend's tools")
	assert.Equal(t, "echo", sess.Tools()[0].Name)
}

// ---------------------------------------------------------------------------
// AC2: Cross-pod hijack prevention
// ---------------------------------------------------------------------------

// TestHorizontalScaling_CrossPodHijackPrevention verifies that:
//   - A session bound to alice on pod A can be reconstructed on pod B.
//   - After restoration, a wrong-token caller is rejected (ErrUnauthorizedCaller).
//   - A nil caller is rejected (ErrNilCaller).
//   - The original caller (correct token) is not rejected at the auth layer.
func TestHorizontalScaling_CrossPodHijackPrevention(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)
	storage := newSharedRedisStorage(t, mr)
	backend := startMCPBackend(t, "backend-alpha", "echo")

	identity := &auth.Identity{
		PrincipalInfo: auth.PrincipalInfo{Subject: "alice"},
		Token:         "alice-bearer-token",
	}
	wrongCaller := &auth.Identity{
		PrincipalInfo: auth.PrincipalInfo{Subject: "eve"},
		Token:         "eve-bearer-token",
	}

	// Pod A: create session bound to alice.
	smA := newTestManagerWithSharedStorage(t, storage, []*vmcp.Backend{backend})
	sessionID := createSession(t, smA, identity)

	// Pod B: restore from Redis.
	smB := newTestManagerWithSharedStorage(t, storage, []*vmcp.Backend{backend})
	sess, ok := smB.GetMultiSession(sessionID)
	require.True(t, ok, "session must be restorable on pod B")
	require.NotNil(t, sess)

	ctx := context.Background()

	// Wrong caller must be rejected before any backend routing.
	_, err := sess.CallTool(ctx, wrongCaller, "echo", map[string]any{"input": "hi"}, nil)
	assert.ErrorIs(t, err, sessiontypes.ErrUnauthorizedCaller, "wrong token must be rejected")

	// Nil caller must be rejected.
	_, err = sess.CallTool(ctx, nil, "echo", map[string]any{"input": "hi"}, nil)
	assert.ErrorIs(t, err, sessiontypes.ErrNilCaller, "nil caller must be rejected")

	// Original caller must pass auth and successfully route to the backend.
	// The backend is still running, so the call must complete without error.
	_, err = sess.CallTool(ctx, identity, "echo", map[string]any{"input": "hi"}, nil)
	require.NoError(t, err, "correct caller must be able to invoke the tool after restore")
}

// ---------------------------------------------------------------------------
// AC3 is intentionally omitted: LRU eviction (RC-10, issue #4221) was dropped
// in favour of TTL-based Redis eviction.
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// AC4: All backends fail during RestoreSession → empty routing table
// ---------------------------------------------------------------------------

// TestHorizontalScaling_AllBackendsFailOnRestore verifies that when all
// backends are unreachable at restore time, GetMultiSession still returns a
// valid (non-nil) session with an empty routing table — consistent with the
// makeSession partial-failure behaviour documented in the spec.
func TestHorizontalScaling_AllBackendsFailOnRestore(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)
	storage := newSharedRedisStorage(t, mr)

	// Use a stoppable backend so we can shut it down mid-test.
	backend, stopBackend := startStoppableMCPBackend(t, "backend-alpha", "echo")

	smWriter := newTestManagerWithSharedStorage(t, storage, []*vmcp.Backend{backend})
	sessionID := createSession(t, smWriter, nil)

	// Stop the backend — RestoreSession will be unable to reconnect.
	stopBackend()

	// Use a fresh manager: its cache is empty, so GetMultiSession takes the
	// restore path without needing to explicitly evict the session.
	smReader := newTestManagerWithSharedStorage(t, storage, []*vmcp.Backend{backend})
	sess, ok := smReader.GetMultiSession(sessionID)
	require.True(t, ok, "GetMultiSession must return ok=true even when backends are unreachable")
	require.NotNil(t, sess)
	assert.Empty(t, sess.Tools(), "routing table must be empty when no backend reconnected")
}

// ---------------------------------------------------------------------------
// AC5: RC-16 backend expiry — NotifyBackendExpired removes metadata;
//       subsequent RestoreSession skips the expired backend.
// ---------------------------------------------------------------------------

// TestHorizontalScaling_BackendExpiry_SkipsExpiredOnRestore verifies that
// after NotifyBackendExpired removes a backend from Redis metadata, a
// subsequent RestoreSession on a different pod only connects to the remaining
// backend and does not include the expired backend's tools.
func TestHorizontalScaling_BackendExpiry_SkipsExpiredOnRestore(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)
	storage := newSharedRedisStorage(t, mr)

	// Two backends with distinct tool names so we can tell them apart.
	backendA := startMCPBackend(t, "backend-alpha", "tool-alpha")
	backendB := startMCPBackend(t, "backend-beta", "tool-beta")

	// Pod A: create session connected to both backends.
	smA := newTestManagerWithSharedStorage(t, storage, []*vmcp.Backend{backendA, backendB})
	sessionID := createSession(t, smA, nil)

	// Verify session A has tools from both backends before expiry.
	sessA, ok := smA.GetMultiSession(sessionID)
	require.True(t, ok)
	toolNames := make(map[string]bool)
	for _, tool := range sessA.Tools() {
		toolNames[tool.Name] = true
	}
	require.True(t, toolNames["tool-alpha"], "session A must have tool-alpha before expiry")
	require.True(t, toolNames["tool-beta"], "session A must have tool-beta before expiry")

	// NotifyBackendExpired updates Redis to remove backend-beta; the node-local cache
	// entry is evicted lazily on the next GetMultiSession when checkSession detects drift.
	smA.NotifyBackendExpired(sessionID, backendB.ID, sessA.GetMetadata())

	// Pod C: fresh Manager, same storage and both backends in registry.
	// (backendB is still running — we're testing that RestoreSession filters
	// it out based on the updated Redis metadata, not because it's unreachable.)
	smC := newTestManagerWithSharedStorage(t, storage, []*vmcp.Backend{backendA, backendB})
	sessC, ok := smC.GetMultiSession(sessionID)
	require.True(t, ok, "session must be restorable after NotifyBackendExpired")
	require.NotNil(t, sessC)

	// Restored session must only have tool-alpha; tool-beta was filtered out.
	restoredTools := make(map[string]bool)
	for _, tool := range sessC.Tools() {
		restoredTools[tool.Name] = true
	}
	assert.True(t, restoredTools["tool-alpha"], "restored session must have tool-alpha")
	assert.False(t, restoredTools["tool-beta"], "restored session must NOT have tool-beta after expiry")
}

// ---------------------------------------------------------------------------
// AC6: In-memory-only mode (no Redis) — no cross-pod sharing
// ---------------------------------------------------------------------------

// TestHorizontalScaling_InMemoryOnlyMode verifies that when Redis is not
// configured (LocalSessionDataStorage), sessions are not visible to a second
// Manager instance, and single-pod usage continues to work correctly.
func TestHorizontalScaling_InMemoryOnlyMode(t *testing.T) {
	t.Parallel()

	backend := startMCPBackend(t, "backend-alpha", "echo")

	newLocalStorage := func(t *testing.T) transportsession.DataStorage {
		t.Helper()
		s, err := transportsession.NewLocalSessionDataStorage(time.Hour)
		require.NoError(t, err)
		t.Cleanup(func() { _ = s.Close() })
		return s
	}

	// Pod A and pod B each have their own local storage — no sharing.
	storageA := newLocalStorage(t)
	storageB := newLocalStorage(t)

	smA := newTestManagerWithSharedStorage(t, storageA, []*vmcp.Backend{backend})
	smB := newTestManagerWithSharedStorage(t, storageB, []*vmcp.Backend{backend})

	sessionID := createSession(t, smA, nil)

	// Pod B must not be able to see pod A's session.
	_, ok := smB.GetMultiSession(sessionID)
	assert.False(t, ok, "in-memory-only: pod B must not see pod A's session")

	// Single-pod usage on pod A must still work.
	sess, ok := smA.GetMultiSession(sessionID)
	require.True(t, ok, "pod A must still serve its own session")
	require.NotNil(t, sess)
	assert.NotEmpty(t, sess.Tools(), "session on pod A must have tools")
}

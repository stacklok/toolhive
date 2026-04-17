// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package session

import (
	"context"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/google/uuid"
	mcpmcp "github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/vmcp"
	vmcpauth "github.com/stacklok/toolhive/pkg/vmcp/auth"
	"github.com/stacklok/toolhive/pkg/vmcp/auth/strategies"
	authtypes "github.com/stacklok/toolhive/pkg/vmcp/auth/types"
	"github.com/stacklok/toolhive/pkg/vmcp/session/internal/security"
	sessiontypes "github.com/stacklok/toolhive/pkg/vmcp/session/types"
)

// startInProcessMCPServer creates a real in-process MCP server over
// streamable-HTTP and returns its base URL. The server is shut down when the
// test ends via t.Cleanup.
//
// The server exposes:
//   - tool "echo": returns the "input" argument as text content
//   - resource "test://data": returns the static text "hello"
//   - prompt "greet": returns a greeting message
func startInProcessMCPServer(t *testing.T) string {
	t.Helper()

	mcpSrv := mcpserver.NewMCPServer("integration-test-backend", "1.0.0")

	mcpSrv.AddTool(
		mcpmcp.NewTool("echo",
			mcpmcp.WithDescription("Echoes the input back"),
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

	mcpSrv.AddResource(
		mcpmcp.Resource{
			URI:      "test://data",
			Name:     "Test Data",
			MIMEType: "text/plain",
		},
		func(_ context.Context, _ mcpmcp.ReadResourceRequest) ([]mcpmcp.ResourceContents, error) {
			return []mcpmcp.ResourceContents{
				mcpmcp.TextResourceContents{URI: "test://data", MIMEType: "text/plain", Text: "hello"},
			}, nil
		},
	)

	mcpSrv.AddPrompt(
		mcpmcp.NewPrompt("greet",
			mcpmcp.WithPromptDescription("Returns a greeting"),
		),
		func(_ context.Context, _ mcpmcp.GetPromptRequest) (*mcpmcp.GetPromptResult, error) {
			return &mcpmcp.GetPromptResult{
				Messages: []mcpmcp.PromptMessage{
					{Role: "user", Content: mcpmcp.NewTextContent("Hello!")},
				},
			}, nil
		},
	)

	streamableSrv := mcpserver.NewStreamableHTTPServer(mcpSrv)
	mux := http.NewServeMux()
	mux.Handle("/mcp", streamableSrv)

	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)

	return ts.URL + "/mcp"
}

// newUnauthenticatedRegistry returns a minimal OutgoingAuthRegistry that
// uses the unauthenticated (no-op) strategy — suitable for tests where the
// backend MCP server does not require auth.
func newUnauthenticatedRegistry(t *testing.T) vmcpauth.OutgoingAuthRegistry {
	t.Helper()
	reg := vmcpauth.NewDefaultOutgoingAuthRegistry()
	require.NoError(t, reg.RegisterStrategy(authtypes.StrategyTypeUnauthenticated, strategies.NewUnauthenticatedStrategy()))
	return reg
}

// ---------------------------------------------------------------------------
// Integration tests — exercise the real HTTP connector
// ---------------------------------------------------------------------------

func TestSessionFactory_Integration_CapabilityDiscovery(t *testing.T) {
	t.Parallel()

	baseURL := startInProcessMCPServer(t)
	backend := &vmcp.Backend{
		ID:            "integration-backend",
		Name:          "integration-backend",
		BaseURL:       baseURL,
		TransportType: "streamable-http",
	}

	factory := NewSessionFactory(newUnauthenticatedRegistry(t))
	sess, err := factory.MakeSessionWithID(context.Background(), uuid.New().String(), nil, true, []*vmcp.Backend{backend})
	require.NoError(t, err)
	require.NotNil(t, sess)
	t.Cleanup(func() { require.NoError(t, sess.Close()) })

	// The real MCP Initialize + ListTools/Resources/Prompts handshake must
	// have discovered all three capabilities.
	require.Len(t, sess.Tools(), 1)
	assert.Equal(t, "echo", sess.Tools()[0].Name)

	require.Len(t, sess.Resources(), 1)
	assert.Equal(t, "test://data", sess.Resources()[0].URI)

	require.Len(t, sess.Prompts(), 1)
	assert.Equal(t, "greet", sess.Prompts()[0].Name)
}

func TestSessionFactory_Integration_CallTool(t *testing.T) {
	t.Parallel()

	baseURL := startInProcessMCPServer(t)
	backend := &vmcp.Backend{
		ID:            "integration-backend",
		Name:          "integration-backend",
		BaseURL:       baseURL,
		TransportType: "streamable-http",
	}

	factory := NewSessionFactory(newUnauthenticatedRegistry(t))
	sess, err := factory.MakeSessionWithID(context.Background(), uuid.New().String(), nil, true, []*vmcp.Backend{backend})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sess.Close()) })

	result, err := sess.CallTool(context.Background(), nil, "echo", map[string]any{"input": "hello world"}, nil)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Len(t, result.Content, 1)
	assert.Equal(t, "hello world", result.Content[0].Text)
}

func TestSessionFactory_Integration_ReadResource(t *testing.T) {
	t.Parallel()

	baseURL := startInProcessMCPServer(t)
	backend := &vmcp.Backend{
		ID:            "integration-backend",
		Name:          "integration-backend",
		BaseURL:       baseURL,
		TransportType: "streamable-http",
	}

	factory := NewSessionFactory(newUnauthenticatedRegistry(t))
	sess, err := factory.MakeSessionWithID(context.Background(), uuid.New().String(), nil, true, []*vmcp.Backend{backend})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sess.Close()) })

	result, err := sess.ReadResource(context.Background(), nil, "test://data")
	require.NoError(t, err)
	require.NotNil(t, result)
	require.NotEmpty(t, result.Contents)
	assert.Equal(t, "hello", result.Contents[0].Text)
}

func TestSessionFactory_Integration_GetPrompt(t *testing.T) {
	t.Parallel()

	baseURL := startInProcessMCPServer(t)
	backend := &vmcp.Backend{
		ID:            "integration-backend",
		Name:          "integration-backend",
		BaseURL:       baseURL,
		TransportType: "streamable-http",
	}

	factory := NewSessionFactory(newUnauthenticatedRegistry(t))
	sess, err := factory.MakeSessionWithID(context.Background(), uuid.New().String(), nil, true, []*vmcp.Backend{backend})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sess.Close()) })

	result, err := sess.GetPrompt(context.Background(), nil, "greet", nil)
	require.NoError(t, err)
	require.NotNil(t, result)
	// Messages preserve individual roles and content structure
	require.Len(t, result.Messages, 1)
	assert.Equal(t, "user", result.Messages[0].Role)
	assert.Equal(t, vmcp.ContentTypeText, result.Messages[0].Content.Type)
	assert.Equal(t, "Hello!", result.Messages[0].Content.Text)
}

func TestSessionFactory_Integration_MultipleBackends(t *testing.T) {
	t.Parallel()

	// Start two independent backends — each has its own "echo" tool.
	// The factory must route each call to the correct backend after resolving
	// the capability-name conflict (alphabetically-earlier backend wins).
	url1 := startInProcessMCPServer(t)
	url2 := startInProcessMCPServer(t)

	backends := []*vmcp.Backend{
		{ID: "backend-b", Name: "backend-b", BaseURL: url2, TransportType: "streamable-http"},
		{ID: "backend-a", Name: "backend-a", BaseURL: url1, TransportType: "streamable-http"},
	}

	factory := NewSessionFactory(newUnauthenticatedRegistry(t))
	sess, err := factory.MakeSessionWithID(context.Background(), uuid.New().String(), nil, true, backends)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sess.Close()) })

	// Both backends expose "echo"; "backend-a" sorts first and must win.
	require.Len(t, sess.Tools(), 1, "conflicting tool names collapse to one")
	assert.Equal(t, "backend-a", sess.Tools()[0].BackendID)
}

// ---------------------------------------------------------------------------
// Token-binding integration tests — HMAC rejection for ReadResource / GetPrompt
// ---------------------------------------------------------------------------

// TestTokenBinding_CallerRejection verifies that the hijack-prevention decorator
// is applied to all three protected methods (CallTool, ReadResource, GetPrompt):
// each rejects a wrong token (ErrUnauthorizedCaller) and a nil caller
// (ErrNilCaller) before any backend routing occurs, so nilBackendConnector suffices.
func TestTokenBinding_CallerRejection(t *testing.T) {
	t.Parallel()

	identity := &auth.Identity{PrincipalInfo: auth.PrincipalInfo{Subject: "alice"}, Token: "alice-token"}
	wrongCaller := &auth.Identity{PrincipalInfo: auth.PrincipalInfo{Subject: "bob"}, Token: "wrong-token"}

	factory := newSessionFactoryWithConnector(nilBackendConnector(), WithHMACSecret([]byte("test-hmac-secret-exactly-32bytes")))
	sess, err := factory.MakeSessionWithID(context.Background(), uuid.New().String(), identity, false, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = sess.Close() })

	callFns := []struct {
		name string
		call func(caller *auth.Identity) error
	}{
		{"CallTool", func(caller *auth.Identity) error {
			_, err := sess.CallTool(context.Background(), caller, "echo", map[string]any{"input": "test"}, nil)
			return err
		}},
		{"ReadResource", func(caller *auth.Identity) error {
			_, err := sess.ReadResource(context.Background(), caller, "test://data")
			return err
		}},
		{"GetPrompt", func(caller *auth.Identity) error {
			_, err := sess.GetPrompt(context.Background(), caller, "greet", nil)
			return err
		}},
	}

	for _, fn := range callFns {
		t.Run(fn.name+"/wrong token", func(t *testing.T) {
			t.Parallel()
			assert.ErrorIs(t, fn.call(wrongCaller), sessiontypes.ErrUnauthorizedCaller)
		})
		t.Run(fn.name+"/nil caller", func(t *testing.T) {
			t.Parallel()
			assert.ErrorIs(t, fn.call(nil), sessiontypes.ErrNilCaller)
		})
	}
}

// TestTokenBinding_ReadResource_And_GetPrompt_WithRealBackend verifies that a
// bound session accepts ReadResource and GetPrompt calls from the correct caller
// when a real backend is connected.
func TestTokenBinding_ReadResource_And_GetPrompt_WithRealBackend(t *testing.T) {
	t.Parallel()

	baseURL := startInProcessMCPServer(t)
	backend := &vmcp.Backend{
		ID:            "integration-backend",
		Name:          "integration-backend",
		BaseURL:       baseURL,
		TransportType: "streamable-http",
	}

	const rawToken = "alice-real-token"
	identity := &auth.Identity{PrincipalInfo: auth.PrincipalInfo{Subject: "alice"}, Token: rawToken}

	factory := NewSessionFactory(newUnauthenticatedRegistry(t), WithHMACSecret([]byte("test-hmac-secret-exactly-32bytes")))
	sess, err := factory.MakeSessionWithID(context.Background(), uuid.New().String(), identity, false, []*vmcp.Backend{backend})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sess.Close()) })

	t.Run("allows ReadResource with correct token", func(t *testing.T) {
		t.Parallel()
		result, err := sess.ReadResource(context.Background(), identity, "test://data")
		require.NoError(t, err)
		require.NotNil(t, result)
		require.NotEmpty(t, result.Contents)
		assert.Equal(t, "hello", result.Contents[0].Text)
	})

	t.Run("allows GetPrompt with correct token", func(t *testing.T) {
		t.Parallel()
		result, err := sess.GetPrompt(context.Background(), identity, "greet", nil)
		require.NoError(t, err)
		require.NotNil(t, result)
		require.Len(t, result.Messages, 1)
		assert.Equal(t, "user", result.Messages[0].Role)
		assert.Equal(t, "Hello!", result.Messages[0].Content.Text)
	})
}

// TestTokenBinding_DifferentSecretsProduceDifferentHashes verifies that two
// session factories configured with different HMAC secrets store different token
// hashes for the same raw bearer token. This is the key isolation property that
// prevents sessions from one secret epoch from being validated against another.
func TestTokenBinding_DifferentSecretsProduceDifferentHashes(t *testing.T) {
	t.Parallel()

	const rawToken = "shared-token-same-for-both"
	identity := &auth.Identity{PrincipalInfo: auth.PrincipalInfo{Subject: "user"}, Token: rawToken}

	factoryA := newSessionFactoryWithConnector(nilBackendConnector(), WithHMACSecret([]byte("secret-A-exactly-32-bytes-long!!")))
	factoryB := newSessionFactoryWithConnector(nilBackendConnector(), WithHMACSecret([]byte("secret-B-exactly-32-bytes-long!!")))

	sessA, err := factoryA.MakeSessionWithID(context.Background(), uuid.New().String(), identity, false, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = sessA.Close() })

	sessB, err := factoryB.MakeSessionWithID(context.Background(), uuid.New().String(), identity, false, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = sessB.Close() })

	hashA := sessA.GetMetadata()[MetadataKeyTokenHash]
	hashB := sessB.GetMetadata()[MetadataKeyTokenHash]

	assert.NotEmpty(t, hashA)
	assert.NotEmpty(t, hashB)
	assert.NotEqual(t, hashA, hashB,
		"different HMAC secrets must produce different token hashes for the same input token")
}

// TestRestoreHijackPrevention_Integration_RoundTrip verifies the full
// store-then-restore flow across a real factory-created session:
//
//  1. Create a session via the factory (writes tokenHash + tokenSalt to metadata).
//  2. Extract the persisted values.
//  3. Wrap a fresh base session with RestoreHijackPrevention using those values.
//  4. Confirm the restored decorator accepts the original token and rejects others.
func TestRestoreHijackPrevention_Integration_RoundTrip(t *testing.T) {
	t.Parallel()

	const rawToken = "integration-token"
	hmacSecret := []byte("test-hmac-secret-exactly-32bytes")
	identity := &auth.Identity{PrincipalInfo: auth.PrincipalInfo{Subject: "alice"}, Token: rawToken}

	factory := newSessionFactoryWithConnector(nilBackendConnector(), WithHMACSecret(hmacSecret))
	sess, err := factory.MakeSessionWithID(context.Background(), uuid.New().String(), identity, false, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = sess.Close() })

	// Extract persisted values — these simulate what would be read back from Redis.
	meta := sess.GetMetadata()
	persistedHash := meta[MetadataKeyTokenHash]
	persistedSalt := meta[sessiontypes.MetadataKeyTokenSalt]
	require.NotEmpty(t, persistedHash, "factory must write tokenHash to metadata")
	require.NotEmpty(t, persistedSalt, "factory must write tokenSalt to metadata")

	// Simulate "Pod B": restore the decorator from persisted metadata.
	// We use a nil-connector session as the inner session (no real backend needed
	// to test auth path).
	innerSess, err := factory.MakeSessionWithID(context.Background(), uuid.New().String(), identity, false, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = innerSess.Close() })

	restored, err := security.RestoreHijackPrevention(innerSess, persistedHash, persistedSalt, hmacSecret)
	require.NoError(t, err)

	ctx := context.Background()

	// Original caller is accepted.
	_, err = restored.CallTool(ctx, identity, "any-tool", nil, nil)
	// ErrToolNotFound is expected (no backends), not an auth error.
	require.NotErrorIs(t, err, sessiontypes.ErrUnauthorizedCaller)
	require.NotErrorIs(t, err, sessiontypes.ErrNilCaller)

	// A different caller is rejected at the auth layer — before any backend routing.
	wrongCaller := &auth.Identity{PrincipalInfo: auth.PrincipalInfo{Subject: "eve"}, Token: "eve-token"}
	_, err = restored.CallTool(ctx, wrongCaller, "any-tool", nil, nil)
	require.ErrorIs(t, err, sessiontypes.ErrUnauthorizedCaller)

	// Nil caller is rejected at the auth layer.
	_, err = restored.CallTool(ctx, nil, "any-tool", nil, nil)
	require.ErrorIs(t, err, sessiontypes.ErrNilCaller)
}

// TestRestoreHijackPrevention_Integration_CrossReplicaSecretMismatch verifies
// that a session restored on a replica with a different HMAC secret rejects
// the original caller's token, documenting the operational requirement that
// all replicas must share the same secret.
func TestRestoreHijackPrevention_Integration_CrossReplicaSecretMismatch(t *testing.T) {
	t.Parallel()

	secretA := []byte("secret-A-exactly-32-bytes-long!!")
	secretB := []byte("secret-B-exactly-32-bytes-long!!")

	identity := &auth.Identity{PrincipalInfo: auth.PrincipalInfo{Subject: "alice"}, Token: "alice-token"}

	// Pod A creates the session with secretA, persisting the hash.
	factoryA := newSessionFactoryWithConnector(nilBackendConnector(), WithHMACSecret(secretA))
	sessA, err := factoryA.MakeSessionWithID(context.Background(), uuid.New().String(), identity, false, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = sessA.Close() })

	persistedHash := sessA.GetMetadata()[MetadataKeyTokenHash]
	persistedSalt := sessA.GetMetadata()[sessiontypes.MetadataKeyTokenSalt]

	// Pod B restores with secretB — the persisted hash was computed with secretA,
	// so validation will produce a different HMAC and reject the caller.
	factoryB := newSessionFactoryWithConnector(nilBackendConnector(), WithHMACSecret(secretB))
	innerSess, err := factoryB.MakeSessionWithID(context.Background(), uuid.New().String(), identity, false, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = innerSess.Close() })

	restored, err := security.RestoreHijackPrevention(innerSess, persistedHash, persistedSalt, secretB)
	require.NoError(t, err)

	_, err = restored.CallTool(context.Background(), identity, "any-tool", nil, nil)
	require.ErrorIs(t, err, sessiontypes.ErrUnauthorizedCaller,
		"cross-replica secret mismatch must reject the original caller")
}

// TestTokenBinding_MetadataEncoding verifies that the token hash and salt stored
// in session metadata are valid hex strings of the expected lengths:
//   - token hash: 64 hex chars (32-byte HMAC-SHA256)
//   - token salt: 32 hex chars (16-byte random salt)
func TestTokenBinding_MetadataEncoding(t *testing.T) {
	t.Parallel()

	identity := &auth.Identity{PrincipalInfo: auth.PrincipalInfo{Subject: "user"}, Token: "test-token-123"}

	factory := newSessionFactoryWithConnector(nilBackendConnector(), WithHMACSecret([]byte("test-hmac-secret-exactly-32bytes")))
	sess, err := factory.MakeSessionWithID(context.Background(), uuid.New().String(), identity, false, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = sess.Close() })

	tokenHash := sess.GetMetadata()[MetadataKeyTokenHash]
	require.NotEmpty(t, tokenHash)
	assert.Len(t, tokenHash, 64, "HMAC-SHA256 hex-encoded hash must be 64 characters")
	hashBytes, err := hex.DecodeString(tokenHash)
	require.NoError(t, err, "token hash must be valid hex")
	assert.Len(t, hashBytes, 32, "decoded token hash must be 32 bytes")

	tokenSalt := sess.GetMetadata()[sessiontypes.MetadataKeyTokenSalt]
	require.NotEmpty(t, tokenSalt)
	saltBytes, err := hex.DecodeString(tokenSalt)
	require.NoError(t, err, "token salt must be valid hex")
	assert.Len(t, saltBytes, 16, "decoded token salt must be 16 bytes")
}

// startInProcessMCPServerWithHeaderCapture starts an in-process MCP server and
// returns the base URL along with a function that returns all Mcp-Session-Id
// header values received by the server from clients.
func startInProcessMCPServerWithHeaderCapture(t *testing.T) (string, func() []string) {
	t.Helper()

	mcpSrv := mcpserver.NewMCPServer("integration-test-backend", "1.0.0")
	mcpSrv.AddTool(
		mcpmcp.NewTool("echo", mcpmcp.WithDescription("echo"), mcpmcp.WithString("input", mcpmcp.Required())),
		func(_ context.Context, req mcpmcp.CallToolRequest) (*mcpmcp.CallToolResult, error) {
			args, _ := req.Params.Arguments.(map[string]any)
			input, _ := args["input"].(string)
			return &mcpmcp.CallToolResult{Content: []mcpmcp.Content{mcpmcp.NewTextContent(input)}}, nil
		},
	)

	streamableSrv := mcpserver.NewStreamableHTTPServer(mcpSrv)

	var mu sync.Mutex
	var capturedIDs []string

	mux := http.NewServeMux()
	mux.Handle("/mcp", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if id := r.Header.Get("Mcp-Session-Id"); id != "" {
			mu.Lock()
			capturedIDs = append(capturedIDs, id)
			mu.Unlock()
		}
		streamableSrv.ServeHTTP(w, r)
	}))

	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)

	return ts.URL + "/mcp", func() []string {
		mu.Lock()
		defer mu.Unlock()
		out := make([]string, len(capturedIDs))
		copy(out, capturedIDs)
		return out
	}
}

// TestSessionFactory_Integration_RestoreSession_SendsStoredSessionHintToBackend
// verifies that RestoreSession passes the stored backend session ID as the
// Mcp-Session-Id hint in the Initialize request so the backend can resume
// rather than create a new session.
func TestSessionFactory_Integration_RestoreSession_SendsStoredSessionHintToBackend(t *testing.T) {
	t.Parallel()

	baseURL, capturedIDs := startInProcessMCPServerWithHeaderCapture(t)
	backend := &vmcp.Backend{
		ID:            "integration-backend",
		Name:          "integration-backend",
		BaseURL:       baseURL,
		TransportType: "streamable-http",
	}

	factory := NewSessionFactory(newUnauthenticatedRegistry(t))

	// Create the original session — the backend assigns a session ID over
	// streamable-HTTP and we store it in metadata.
	orig, err := factory.MakeSessionWithID(context.Background(), uuid.New().String(), nil, true, []*vmcp.Backend{backend})
	require.NoError(t, err)
	t.Cleanup(func() { _ = orig.Close() })

	storedMeta := orig.GetMetadata()
	storedBackendSessionID := storedMeta[MetadataKeyBackendSessionPrefix+"integration-backend"]
	require.NotEmpty(t, storedBackendSessionID, "streamable-HTTP backend must assign a session ID on Initialize")

	// RestoreSession: the factory must send the stored session ID as Mcp-Session-Id.
	restored, err := factory.RestoreSession(context.Background(), uuid.New().String(), storedMeta, []*vmcp.Backend{backend})
	require.NoError(t, err)
	t.Cleanup(func() { _ = restored.Close() })

	// The server must have received the stored ID as a hint in the Initialize request.
	assert.Contains(t, capturedIDs(), storedBackendSessionID,
		"RestoreSession must send the stored backend session ID as Mcp-Session-Id hint")
}

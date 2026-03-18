// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package session

import (
	"context"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
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
	assert.Equal(t, "hello", string(result.Contents))
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
	// ConvertPromptMessages formats messages as "[role] text\n"
	assert.Equal(t, "[user] Hello!\n", result.Messages)
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

	identity := &auth.Identity{Subject: "alice", Token: "alice-token"}
	wrongCaller := &auth.Identity{Subject: "bob", Token: "wrong-token"}

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
	identity := &auth.Identity{Subject: "alice", Token: rawToken}

	factory := NewSessionFactory(newUnauthenticatedRegistry(t), WithHMACSecret([]byte("test-hmac-secret-exactly-32bytes")))
	sess, err := factory.MakeSessionWithID(context.Background(), uuid.New().String(), identity, false, []*vmcp.Backend{backend})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sess.Close()) })

	t.Run("allows ReadResource with correct token", func(t *testing.T) {
		t.Parallel()
		result, err := sess.ReadResource(context.Background(), identity, "test://data")
		require.NoError(t, err)
		require.NotNil(t, result)
		assert.Equal(t, "hello", string(result.Contents))
	})

	t.Run("allows GetPrompt with correct token", func(t *testing.T) {
		t.Parallel()
		result, err := sess.GetPrompt(context.Background(), identity, "greet", nil)
		require.NoError(t, err)
		require.NotNil(t, result)
		assert.Equal(t, "[user] Hello!\n", result.Messages)
	})
}

// TestTokenBinding_DifferentSecretsProduceDifferentHashes verifies that two
// session factories configured with different HMAC secrets store different token
// hashes for the same raw bearer token. This is the key isolation property that
// prevents sessions from one secret epoch from being validated against another.
func TestTokenBinding_DifferentSecretsProduceDifferentHashes(t *testing.T) {
	t.Parallel()

	const rawToken = "shared-token-same-for-both"
	identity := &auth.Identity{Subject: "user", Token: rawToken}

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

// TestTokenBinding_MetadataEncoding verifies that the token hash and salt stored
// in session metadata are valid hex strings of the expected lengths:
//   - token hash: 64 hex chars (32-byte HMAC-SHA256)
//   - token salt: 32 hex chars (16-byte random salt)
func TestTokenBinding_MetadataEncoding(t *testing.T) {
	t.Parallel()

	identity := &auth.Identity{Subject: "user", Token: "test-token-123"}

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

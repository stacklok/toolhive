// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package session

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	mcpmcp "github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/vmcp"
	vmcpauth "github.com/stacklok/toolhive/pkg/vmcp/auth"
	"github.com/stacklok/toolhive/pkg/vmcp/auth/strategies"
	authtypes "github.com/stacklok/toolhive/pkg/vmcp/auth/types"
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
	sess, err := factory.MakeSession(context.Background(), nil, []*vmcp.Backend{backend})
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
	sess, err := factory.MakeSession(context.Background(), nil, []*vmcp.Backend{backend})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sess.Close()) })

	result, err := sess.CallTool(context.Background(), "echo", map[string]any{"input": "hello world"}, nil)
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
	sess, err := factory.MakeSession(context.Background(), nil, []*vmcp.Backend{backend})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sess.Close()) })

	result, err := sess.ReadResource(context.Background(), "test://data")
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
	sess, err := factory.MakeSession(context.Background(), nil, []*vmcp.Backend{backend})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sess.Close()) })

	result, err := sess.GetPrompt(context.Background(), "greet", nil)
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
	sess, err := factory.MakeSession(context.Background(), nil, backends)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sess.Close()) })

	// Both backends expose "echo"; "backend-a" sorts first and must win.
	require.Len(t, sess.Tools(), 1, "conflicting tool names collapse to one")
	assert.Equal(t, "backend-a", sess.Tools()[0].BackendID)
}

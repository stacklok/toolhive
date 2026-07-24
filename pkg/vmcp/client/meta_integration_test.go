// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package client_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"

	"github.com/stacklok/toolhive-core/mcpcompat/mcp"
	"github.com/stacklok/toolhive-core/mcpcompat/server"
	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/auth"
	"github.com/stacklok/toolhive/pkg/vmcp/auth/strategies"
	vmcpclient "github.com/stacklok/toolhive/pkg/vmcp/client"
)

// TestMetaPreservation_CallTool tests that _meta fields are preserved when calling tools.
func TestMetaPreservation_CallTool(t *testing.T) {
	t.Parallel()

	// Create and start a real MCP server that returns _meta
	port, _, cleanup := startTestMCPServer(t)
	defer cleanup()

	// Create vMCP backend client with unauthenticated strategy
	registry := auth.NewDefaultOutgoingAuthRegistry()
	err := registry.RegisterStrategy("unauthenticated", &strategies.UnauthenticatedStrategy{})
	require.NoError(t, err)

	backendClient, err := vmcpclient.NewHTTPBackendClient(registry)
	require.NoError(t, err)

	// Create backend target pointing to our test server
	target := &vmcp.BackendTarget{
		WorkloadID:    "test-backend",
		WorkloadName:  "Test Backend",
		BaseURL:       "http://127.0.0.1:" + port,
		TransportType: "streamable-http",
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Call tool through vMCP backend client
	result, err := backendClient.CallTool(ctx, target, "test_tool_with_meta", map[string]any{
		"input": "test-value",
	}, nil)

	// Verify call succeeded
	require.NoError(t, err)
	require.NotNil(t, result)

	// Verify _meta field is preserved
	assert.NotNil(t, result.Meta, "_meta field should be preserved")
	assert.Equal(t, "test-progress-token-123", result.Meta["progressToken"], "progressToken should be preserved")
	assert.Equal(t, "00-0123456789abcdef0123456789abcdef-fedcba9876543210-01", result.Meta["traceparent"], "traceparent should be preserved")
	assert.Equal(t, "custom-value", result.Meta["customField"], "custom metadata should be preserved")

	// Verify content is also correct
	assert.NotNil(t, result.Content)
	assert.Len(t, result.Content, 1)
	assert.Equal(t, vmcp.ContentTypeText, result.Content[0].Type)
	assert.Equal(t, "Response from test tool", result.Content[0].Text)
}

// TestMetaPreservation_CallTool_NoMeta tests that tools without _meta don't break.
func TestMetaPreservation_CallTool_NoMeta(t *testing.T) {
	t.Parallel()

	port, _, cleanup := startTestMCPServer(t)
	defer cleanup()

	registry := auth.NewDefaultOutgoingAuthRegistry()
	err := registry.RegisterStrategy("unauthenticated", &strategies.UnauthenticatedStrategy{})
	require.NoError(t, err)

	backendClient, err := vmcpclient.NewHTTPBackendClient(registry)
	require.NoError(t, err)

	target := &vmcp.BackendTarget{
		WorkloadID:    "test-backend",
		WorkloadName:  "Test Backend",
		BaseURL:       "http://127.0.0.1:" + port,
		TransportType: "streamable-http",
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Call tool that doesn't return _meta
	result, err := backendClient.CallTool(ctx, target, "test_tool_no_meta", map[string]any{
		"input": "test-value",
	}, nil)

	require.NoError(t, err)
	require.NotNil(t, result)

	// Verify _meta is nil (not present)
	assert.Nil(t, result.Meta, "_meta should be nil when backend doesn't provide it")

	// Verify content is still correct
	assert.NotNil(t, result.Content)
	assert.Len(t, result.Content, 1)
	assert.Equal(t, vmcp.ContentTypeText, result.Content[0].Type)
}

// TestMetaPreservation_CallTool_Error tests that _meta fields are preserved even when tool returns IsError=true.
// This test verifies that error results (IsError=true) preserve metadata for distributed tracing.
//
// Note: This test does not verify that _meta is included in error logs. Log output verification
// would require capturing logger output, which is typically done manually or in log aggregation systems.
// The client code logs _meta fields when present (see client.go error handling), but automated
// verification of log output is outside the scope of this integration test.
func TestMetaPreservation_CallTool_Error(t *testing.T) {
	t.Parallel()

	port, _, cleanup := startTestMCPServer(t)
	defer cleanup()

	registry := auth.NewDefaultOutgoingAuthRegistry()
	err := registry.RegisterStrategy("unauthenticated", &strategies.UnauthenticatedStrategy{})
	require.NoError(t, err)

	backendClient, err := vmcpclient.NewHTTPBackendClient(registry)
	require.NoError(t, err)

	target := &vmcp.BackendTarget{
		WorkloadID:    "test-backend",
		WorkloadName:  "Test Backend",
		BaseURL:       "http://127.0.0.1:" + port,
		TransportType: "streamable-http",
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Call tool that returns an error with _meta
	result, err := backendClient.CallTool(ctx, target, "test_tool_error", map[string]any{
		"input": "trigger-error",
	}, nil)

	// Should return result (not a Go error) when tool returns IsError=true
	require.NoError(t, err, "IsError=true is not a transport error, should return result")
	require.NotNil(t, result)

	// Verify the result has IsError flag set
	assert.True(t, result.IsError, "Result should have IsError=true")

	// Verify _meta is preserved even for error results
	assert.NotNil(t, result.Meta, "_meta should be preserved for error results")
	assert.Equal(t, "error-token-999", result.Meta["progressToken"])
	assert.Equal(t, "error-trace-abc123", result.Meta["traceId"])
	assert.Equal(t, "req-error-xyz789", result.Meta["requestId"])

	// Verify error content is present
	assert.NotEmpty(t, result.Content)
	assert.Contains(t, result.Content[0].Text, "Tool execution failed")
}

// TestMetaPreservation_GetPrompt tests that _meta fields are preserved when getting prompts.
func TestMetaPreservation_GetPrompt(t *testing.T) {
	t.Parallel()

	port, _, cleanup := startTestMCPServer(t)
	defer cleanup()

	registry := auth.NewDefaultOutgoingAuthRegistry()
	err := registry.RegisterStrategy("unauthenticated", &strategies.UnauthenticatedStrategy{})
	require.NoError(t, err)

	backendClient, err := vmcpclient.NewHTTPBackendClient(registry)
	require.NoError(t, err)

	target := &vmcp.BackendTarget{
		WorkloadID:    "test-backend",
		WorkloadName:  "Test Backend",
		BaseURL:       "http://127.0.0.1:" + port,
		TransportType: "streamable-http",
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Get prompt through vMCP backend client
	result, err := backendClient.GetPrompt(ctx, target, "test_prompt_with_meta", map[string]any{
		"name": "World",
	})

	require.NoError(t, err)
	require.NotNil(t, result)

	// Verify _meta field is preserved
	assert.NotNil(t, result.Meta, "_meta field should be preserved for prompts")
	assert.Equal(t, "prompt-token-456", result.Meta["progressToken"])
	assert.Equal(t, "prompt-trace-id", result.Meta["traceId"])

	// Verify prompt content preserves message structure
	require.Len(t, result.Messages, 1)
	assert.Equal(t, "user", result.Messages[0].Role)
	assert.Equal(t, "Hello, World!", result.Messages[0].Content.Text)
}

// TestMetaPreservation_ReadResource documents the SDK limitation for resource _meta.
//
// KNOWN LIMITATION: Due to MCP SDK constraints, resource handlers return []ResourceContents
// directly, not *ReadResourceResult with _meta. This prevents backends from including _meta
// in resource responses at all.
//
// As a result:
// - Backend MCP servers cannot include _meta in resource read responses (SDK limitation)
// - vMCP client cannot extract _meta because it's not in the response
// - vMCP handler cannot forward _meta to clients
//
// This test documents the expected behavior and ensures resource reads work correctly
// even though _meta is not supported. Once the SDK adds _meta support for resource handlers,
// this test can be updated to verify _meta preservation.
func TestMetaPreservation_ReadResource(t *testing.T) {
	t.Parallel()

	port, _, cleanup := startTestMCPServer(t)
	defer cleanup()

	registry := auth.NewDefaultOutgoingAuthRegistry()
	err := registry.RegisterStrategy("unauthenticated", &strategies.UnauthenticatedStrategy{})
	require.NoError(t, err)

	backendClient, err := vmcpclient.NewHTTPBackendClient(registry)
	require.NoError(t, err)

	target := &vmcp.BackendTarget{
		WorkloadID:    "test-backend",
		WorkloadName:  "Test Backend",
		BaseURL:       "http://127.0.0.1:" + port,
		TransportType: "streamable-http",
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Read resource through vMCP backend client
	result, err := backendClient.ReadResource(ctx, target, "test://resource")

	require.NoError(t, err)
	require.NotNil(t, result)

	// Verify _meta is NOT present due to SDK limitation
	// The SDK handler signature doesn't support returning _meta with resources
	assert.Nil(t, result.Meta, "_meta cannot be included due to SDK limitation - handler returns []ResourceContents without _meta wrapper")

	// Verify resource content works correctly
	require.NotEmpty(t, result.Contents)
	assert.Equal(t, "Test resource content", result.Contents[0].Text)
	assert.Equal(t, "text/plain", result.Contents[0].MimeType)
}

// TestOutboundMetaTraceContext verifies that the vMCP backend client injects the
// current W3C trace context (traceparent) into outbound params._meta (SEP-414)
// for CallTool, and that it is omitted entirely when there is no active span.
//
// ReadResource and GetPrompt are NOT asserted for on-the-wire traceparent
// delivery here: github.com/stacklok/toolhive-core's mcpcompat client (as of
// v0.0.32) builds the underlying go-sdk resources/read and prompts/get
// requests without forwarding request.Params.Meta at all (only CallTool's
// non-resume path converts and forwards Meta - see mcpcompat/client/client.go
// CallTool vs ReadResource/GetPrompt). This means the Meta we set on
// mcp.ReadResourceParams/mcp.GetPromptParams in client.go and mcp_session.go
// is silently dropped before it reaches the wire for these two operations
// today. The code changes are still correct and forward-compatible: they will
// start working the moment mcpcompat forwards Meta the same way it already
// does for CallTool, with no further change needed on our side. This is
// analogous to the pre-existing SDK limitation documented in
// TestMetaPreservation_ReadResource (inbound resource _meta).
//
// This test mutates the global OTEL propagator, so it must NOT run in
// parallel with the other tests in this file (see propagation_test.go for the
// same convention in pkg/telemetry).
func TestOutboundMetaTraceContext(t *testing.T) { //nolint:paralleltest // Mutates global OTEL propagator
	oldPropagator := otel.GetTextMapPropagator()
	otel.SetTextMapPropagator(propagation.TraceContext{})
	defer otel.SetTextMapPropagator(oldPropagator)

	port, capture, cleanup := startTestMCPServer(t)
	defer cleanup()

	registry := auth.NewDefaultOutgoingAuthRegistry()
	err := registry.RegisterStrategy("unauthenticated", &strategies.UnauthenticatedStrategy{})
	require.NoError(t, err)

	backendClient, err := vmcpclient.NewHTTPBackendClient(registry)
	require.NoError(t, err)

	target := &vmcp.BackendTarget{
		WorkloadID:    "test-backend",
		WorkloadName:  "Test Backend",
		BaseURL:       "http://127.0.0.1:" + port,
		TransportType: "streamable-http",
	}

	tp := sdktrace.NewTracerProvider()
	defer func() { _ = tp.Shutdown(context.Background()) }()
	tracer := tp.Tracer("test")

	//nolint:paralleltest // sequential: shares capture/backendClient state with the sibling subtests below
	t.Run("CallTool injects traceparent", func(t *testing.T) {
		spanCtx, span := tracer.Start(context.Background(), "test-span")
		ctx, cancel := context.WithTimeout(spanCtx, 5*time.Second)
		defer cancel()
		defer span.End()

		_, err := backendClient.CallTool(ctx, target, "test_tool_capture_meta", nil, nil)
		require.NoError(t, err)

		got := capture.get("tool")
		require.NotNil(t, got, "expected params._meta to be sent")
		traceparent, ok := got.AdditionalFields["traceparent"].(string)
		require.True(t, ok, "expected traceparent in captured _meta")
		// Prove span identity, not just presence: assert the W3C traceparent
		// carries the active span's TraceID rather than a stale/global one.
		assert.Contains(t, traceparent, span.SpanContext().TraceID().String(),
			"traceparent must carry the active span's TraceID")
	})

	//nolint:paralleltest // sequential: shares capture/backendClient state with the sibling subtests
	t.Run("ReadResource and GetPrompt: mcpcompat drops outbound Meta (documented SDK limitation)", func(t *testing.T) {
		spanCtx, span := tracer.Start(context.Background(), "test-span")
		ctx, cancel := context.WithTimeout(spanCtx, 5*time.Second)
		defer cancel()
		defer span.End()

		// TODO: tripwire for a toolhive-core/mcpcompat limitation (see this
		// test's doc comment). These asserts pass ONLY because mcpcompat drops
		// outbound Params.Meta on the resources/read and prompts/get paths. When
		// they start failing, mcpcompat now forwards Meta: delete the
		// forward-compat NOTE comments in client.go/mcp_session.go and replace
		// these nil checks with traceparent assertions (as done for CallTool).
		_, err := backendClient.ReadResource(ctx, target, "test://capture-resource")
		require.NoError(t, err)
		assert.Nil(t, capture.get("resource"),
			"expected nil ONLY because mcpcompat v0.0.32 drops outbound Meta for resources/read; "+
				"if this is now non-nil, mcpcompat forwards Meta — delete the forward-compat NOTE comments "+
				"in client.go/mcp_session.go and assert traceparent here instead")

		_, err = backendClient.GetPrompt(ctx, target, "test_prompt_capture_meta", nil)
		require.NoError(t, err)
		assert.Nil(t, capture.get("prompt"),
			"expected nil ONLY because mcpcompat v0.0.32 drops outbound Meta for prompts/get; "+
				"if this is now non-nil, mcpcompat forwards Meta — delete the forward-compat NOTE comments "+
				"in client.go/mcp_session.go and assert traceparent here instead")
	})

	//nolint:paralleltest // sequential: reuses the "tool" capture key populated above to prove _meta is now absent
	t.Run("CallTool omits _meta without an active span", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		_, err := backendClient.CallTool(ctx, target, "test_tool_capture_meta", nil, nil)
		require.NoError(t, err)

		got := capture.get("tool")
		assert.Nil(t, got, "expected no _meta to be sent without an active trace context")
	})
}

// metaCapture records the inbound params._meta observed by the test server's
// capture-only tool/resource/prompt handlers, keyed by capability name. It
// lets tests assert what the vMCP client actually sent on the wire (e.g. an
// injected traceparent), as opposed to what the backend echoes back.
type metaCapture struct {
	mu   sync.Mutex
	meta map[string]*mcp.Meta
}

func newMetaCapture() *metaCapture {
	return &metaCapture{meta: make(map[string]*mcp.Meta)}
}

func (c *metaCapture) record(key string, meta *mcp.Meta) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.meta[key] = meta
}

// get returns the captured _meta for key, or nil if the capability was never
// invoked or was invoked without a _meta field.
func (c *metaCapture) get(key string) *mcp.Meta {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.meta[key]
}

// startTestMCPServer creates and starts a test MCP server with tools that return _meta.
// It also registers capture-only "test_tool_capture_meta", "test_prompt_capture_meta", and
// "test://capture-resource" capabilities that record the inbound params._meta they receive
// into the returned metaCapture, so tests can assert on OUTBOUND _meta (e.g. injected W3C
// trace context) rather than only on what the backend echoes back.
// Returns the port, the meta capture, and a cleanup function.
func startTestMCPServer(t *testing.T) (string, *metaCapture, func()) {
	t.Helper()

	capture := newMetaCapture()

	// Create MCP server
	mcpServer := server.NewMCPServer("test-backend", "1.0.0")

	// Add tool that returns _meta
	mcpServer.AddTool(
		mcp.NewTool("test_tool_with_meta",
			mcp.WithDescription("Test tool that returns metadata"),
			mcp.WithString("input", mcp.Required()),
		),
		func(_ context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return &mcp.CallToolResult{
				Result: mcp.Result{
					Meta: &mcp.Meta{
						ProgressToken: "test-progress-token-123",
						AdditionalFields: map[string]any{
							"traceparent": "00-0123456789abcdef0123456789abcdef-fedcba9876543210-01",
							"customField": "custom-value",
						},
					},
				},
				Content: []mcp.Content{
					mcp.NewTextContent("Response from test tool"),
				},
			}, nil
		},
	)

	// Add tool that doesn't return _meta (backward compatibility test)
	mcpServer.AddTool(
		mcp.NewTool("test_tool_no_meta",
			mcp.WithDescription("Test tool without metadata"),
			mcp.WithString("input", mcp.Required()),
		),
		func(_ context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return &mcp.CallToolResult{
				Content: []mcp.Content{
					mcp.NewTextContent("Response without meta"),
				},
			}, nil
		},
	)

	// Add tool that returns error with _meta (for error logging test)
	mcpServer.AddTool(
		mcp.NewTool("test_tool_error",
			mcp.WithDescription("Test tool that returns error with metadata"),
			mcp.WithString("input", mcp.Required()),
		),
		func(_ context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return &mcp.CallToolResult{
				Result: mcp.Result{
					Meta: &mcp.Meta{
						ProgressToken: "error-token-999",
						AdditionalFields: map[string]any{
							"traceId":   "error-trace-abc123",
							"requestId": "req-error-xyz789",
						},
					},
				},
				IsError: true,
				Content: []mcp.Content{
					mcp.NewTextContent("Tool execution failed: invalid input"),
				},
			}, nil
		},
	)

	// Add prompt that returns _meta
	mcpServer.AddPrompt(
		mcp.NewPrompt("test_prompt_with_meta",
			mcp.WithPromptDescription("Test prompt with metadata"),
		),
		func(_ context.Context, request mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
			name := "there"
			if nameArg, ok := request.Params.Arguments["name"]; ok {
				name = nameArg
			}
			return &mcp.GetPromptResult{
				Result: mcp.Result{
					Meta: &mcp.Meta{
						ProgressToken: "prompt-token-456",
						AdditionalFields: map[string]any{
							"traceId": "prompt-trace-id",
						},
					},
				},
				Messages: []mcp.PromptMessage{
					{
						Role:    "user",
						Content: mcp.NewTextContent("Hello, " + name + "!"),
					},
				},
			}, nil
		},
	)

	// Add resource that returns _meta
	mcpServer.AddResource(
		mcp.Resource{
			URI:         "test://resource",
			Name:        "Test Resource",
			Description: "Test resource with metadata",
			MIMEType:    "text/plain",
		},
		func(_ context.Context, _ mcp.ReadResourceRequest) ([]mcp.ResourceContents, error) {
			// Note: The handler returns []ResourceContents, not *ReadResourceResult
			// This is why _meta cannot be forwarded - SDK limitation
			return []mcp.ResourceContents{
				mcp.TextResourceContents{
					URI:      "test://resource",
					MIMEType: "text/plain",
					Text:     "Test resource content",
				},
			}, nil
		},
	)

	// Add capture-only tool/resource/prompt that record inbound params._meta
	// for outbound-propagation assertions (see metaCapture).
	mcpServer.AddTool(
		mcp.NewTool("test_tool_capture_meta",
			mcp.WithDescription("Capture-only tool that records inbound _meta"),
		),
		func(_ context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			capture.record("tool", request.Params.Meta)
			return &mcp.CallToolResult{
				Content: []mcp.Content{mcp.NewTextContent("captured")},
			}, nil
		},
	)
	mcpServer.AddPrompt(
		mcp.NewPrompt("test_prompt_capture_meta",
			mcp.WithPromptDescription("Capture-only prompt that records inbound _meta"),
		),
		func(_ context.Context, request mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
			capture.record("prompt", request.Params.Meta)
			return &mcp.GetPromptResult{
				Messages: []mcp.PromptMessage{
					{Role: "user", Content: mcp.NewTextContent("captured")},
				},
			}, nil
		},
	)
	mcpServer.AddResource(
		mcp.Resource{
			URI:         "test://capture-resource",
			Name:        "Capture Resource",
			Description: "Capture-only resource that records inbound _meta",
			MIMEType:    "text/plain",
		},
		func(_ context.Context, request mcp.ReadResourceRequest) ([]mcp.ResourceContents, error) {
			capture.record("resource", request.Params.Meta)
			return []mcp.ResourceContents{
				mcp.TextResourceContents{
					URI:      "test://capture-resource",
					MIMEType: "text/plain",
					Text:     "captured",
				},
			}, nil
		},
	)

	// Create HTTP handler for the MCP server
	httpHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// MCP over HTTP uses POST requests with JSON-RPC
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		// Read the request body
		rawMessage, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "Failed to read request", http.StatusBadRequest)
			return
		}
		defer r.Body.Close()

		// Handle message through MCP server
		response := mcpServer.HandleMessage(r.Context(), rawMessage)

		// Marshal response to JSON
		responseBytes, err := json.Marshal(response)
		if err != nil {
			http.Error(w, "Failed to marshal response", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(responseBytes)
	})

	// Start HTTP server on random available port
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	port := fmt.Sprintf("%d", listener.Addr().(*net.TCPAddr).Port)

	httpServer := &http.Server{
		Handler: httpHandler,
	}

	// Start server in background
	go func() {
		_ = httpServer.Serve(listener)
	}()

	// Give server time to start
	time.Sleep(100 * time.Millisecond)

	cleanup := func() {
		_ = httpServer.Close()
		_ = listener.Close()
	}

	return port, capture, cleanup
}

// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	mcpclient "github.com/stacklok/toolhive-core/mcpcompat/client"
	mcptransport "github.com/stacklok/toolhive-core/mcpcompat/client/transport"
	"github.com/stacklok/toolhive-core/mcpcompat/mcp"
	"github.com/stacklok/toolhive/pkg/auth"
	mcpparser "github.com/stacklok/toolhive/pkg/mcp"
	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/server/sessionmanager"
)

type elicitingCore struct {
	*fakeCore
	requester vmcp.ElicitationRequester
}

func (c *elicitingCore) CallTool(
	ctx context.Context, _ *auth.Identity, _ string, _ map[string]any, _ map[string]any,
) (*vmcp.ToolCallResult, error) {
	c.callToolCalls.Add(1)
	result, err := c.requester.RequestElicitation(ctx, vmcp.ElicitationRequest{
		Message: "What is your name?",
		RequestedSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name": map[string]any{"type": "string"},
			},
		},
	})
	if err != nil {
		return nil, err
	}
	content, ok := result.Content.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("unexpected elicitation content type %T", result.Content)
	}
	name, _ := content["name"].(string)
	return &vmcp.ToolCallResult{
		Content: []vmcp.Content{{Type: vmcp.ContentTypeText, Text: "hello " + name}},
	}, nil
}

type responsePostObservation struct {
	statusCode int
	body       []byte
	readErr    error
}

type responsePostRecordingTransport struct {
	base     http.RoundTripper
	observed chan<- responsePostObservation
}

func legacyRequestAuthorization(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			next.ServeHTTP(w, r)
			return
		}
		if mcpparser.GetParsedMCPRequest(r.Context()) == nil {
			http.Error(w, "Invalid or malformed MCP request", http.StatusBadRequest)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (t *responsePostRecordingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	isResponse := false
	if req.Method == http.MethodPost && req.Body != nil {
		requestBody, err := io.ReadAll(req.Body)
		if err != nil {
			return nil, err
		}
		_ = req.Body.Close()
		req.Body = io.NopCloser(bytes.NewReader(requestBody))

		var payload map[string]json.RawMessage
		if json.Unmarshal(requestBody, &payload) == nil {
			_, hasID := payload["id"]
			_, hasMethod := payload["method"]
			_, hasResult := payload["result"]
			_, hasError := payload["error"]
			isResponse = hasID && !hasMethod && (hasResult || hasError)
		}
	}

	base := t.base
	if base == nil {
		base = http.DefaultTransport
	}
	resp, err := base.RoundTrip(req)
	if err != nil || !isResponse {
		return resp, err
	}

	responseBody, readErr := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	resp.Body = io.NopCloser(bytes.NewReader(responseBody))
	observation := responsePostObservation{
		statusCode: resp.StatusCode,
		body:       responseBody,
		readErr:    readErr,
	}
	select {
	case t.observed <- observation:
	default:
	}
	return resp, nil
}

func cleanupWithin(t *testing.T, name string, cleanup func() error) {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- cleanup() }()
	select {
	case err := <-done:
		assert.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Errorf("timed out cleaning up %s", name)
	}
}

func TestHandler_AcceptsClientResponsePostWithLegacyAuthzConfigured(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	t.Cleanup(cancel)

	ctrl := gomock.NewController(t)
	testTool := vmcp.Tool{
		Name:        "elicit",
		Description: "elicits a name before responding",
		InputSchema: map[string]any{"type": "object"},
	}
	factory, _ := newToolSessionFactory(t, ctrl, []vmcp.Tool{testTool})
	core := &elicitingCore{fakeCore: &fakeCore{tools: []vmcp.Tool{testTool}}}

	srv, err := Serve(ctx, core, &ServerConfig{
		SessionTTL:           time.Minute,
		SessionManagerConfig: &sessionmanager.FactoryConfig{Base: factory},
		BackendRegistry:      vmcp.NewImmutableRegistry([]vmcp.Backend{}),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		cleanupWithin(t, "vMCP server", func() error { return srv.Stop(context.Background()) })
	})
	core.requester = NewSDKElicitationAdapter(srv.MCPServer())

	// Before server.New moved onto the core admission path, production configured
	// these two layers together. The request-only authz layer treated a JSON-RPC
	// response as malformed because the parser intentionally returned no request.
	srv.config.AuthMiddleware = mcpparser.ParsingMiddleware
	srv.config.AuthzMiddleware = legacyRequestAuthorization

	handler, err := srv.Handler(ctx)
	require.NoError(t, err)
	httpServer := httptest.NewServer(handler)
	t.Cleanup(func() {
		cleanupWithin(t, "HTTP server", func() error {
			httpServer.CloseClientConnections()
			httpServer.Close()
			return nil
		})
	})

	observed := make(chan responsePostObservation, 1)
	httpClient := &http.Client{Transport: &responsePostRecordingTransport{
		base:     http.DefaultTransport,
		observed: observed,
	}}
	client, err := mcpclient.NewStreamableHttpClientWithOpts(
		httpServer.URL+"/mcp",
		[]mcptransport.StreamableHTTPCOption{
			mcptransport.WithContinuousListening(),
			mcptransport.WithHTTPBasicClient(httpClient),
		},
		[]mcpclient.ClientOption{mcpclient.WithElicitationHandler(mcpclient.ElicitationHandlerFunc(
			func(_ context.Context, _ mcp.ElicitationRequest) (*mcp.ElicitationResult, error) {
				return &mcp.ElicitationResult{
					ElicitationResponse: mcp.ElicitationResponse{
						Action:  mcp.ElicitationResponseActionAccept,
						Content: map[string]any{"name": "grace"},
					},
				}, nil
			},
		))},
	)
	require.NoError(t, err)
	require.NoError(t, client.Start(ctx))
	t.Cleanup(func() {
		cancel()
		cleanupWithin(t, "MCP client", client.Close)
	})

	_, err = client.Initialize(ctx, mcp.InitializeRequest{
		Params: mcp.InitializeParams{
			ProtocolVersion: mcp.LATEST_PROTOCOL_VERSION,
			ClientInfo:      mcp.Implementation{Name: "response-test", Version: "1.0.0"},
		},
	})
	require.NoError(t, err)

	type callOutcome struct {
		result *mcp.CallToolResult
		err    error
	}
	callDone := make(chan callOutcome, 1)
	go func() {
		result, err := client.CallTool(ctx, mcp.CallToolRequest{
			Params: mcp.CallToolParams{Name: testTool.Name},
		})
		callDone <- callOutcome{result: result, err: err}
	}()

	var responsePost responsePostObservation
	select {
	case responsePost = <-observed:
	case <-ctx.Done():
		t.Fatalf("timed out waiting for client response POST: %v", ctx.Err())
	}
	require.NoError(t, responsePost.readErr)
	if responsePost.statusCode != http.StatusAccepted {
		cancel()
	}
	assert.Equal(t, http.StatusAccepted, responsePost.statusCode)
	assert.Empty(t, responsePost.body)

	var outcome callOutcome
	select {
	case outcome = <-callDone:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for tool call after client response POST")
	}
	require.NoError(t, outcome.err)
	require.NotNil(t, outcome.result)
	require.False(t, outcome.result.IsError)
	require.Len(t, outcome.result.Content, 1)
	text, ok := mcp.AsTextContent(outcome.result.Content[0])
	require.True(t, ok)
	assert.Equal(t, "hello grace", text.Text)
}

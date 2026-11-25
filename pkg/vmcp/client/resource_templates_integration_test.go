// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package client_test

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive-core/mcpcompat/mcp"
	"github.com/stacklok/toolhive-core/mcpcompat/server"
	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/auth"
	"github.com/stacklok/toolhive/pkg/vmcp/auth/strategies"
	vmcpclient "github.com/stacklok/toolhive/pkg/vmcp/client"
)

// TestListCapabilities_IncludesResourceTemplates verifies that a backend
// advertising a resource template surfaces it in the aggregated CapabilityList,
// as a pass-through (no URI-template rewriting) tagged with the backend ID.
func TestListCapabilities_IncludesResourceTemplates(t *testing.T) {
	t.Parallel()

	port, cleanup := startResourceTemplateMCPServer(t)
	defer cleanup()

	registry := auth.NewDefaultOutgoingAuthRegistry()
	require.NoError(t, registry.RegisterStrategy("unauthenticated", &strategies.UnauthenticatedStrategy{}))

	backendClient, err := vmcpclient.NewHTTPBackendClient(registry)
	require.NoError(t, err)

	target := &vmcp.BackendTarget{
		WorkloadID:    "docs-backend",
		WorkloadName:  "Docs Backend",
		BaseURL:       "http://127.0.0.1:" + port,
		TransportType: "streamable-http",
	}

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	caps, err := backendClient.ListCapabilities(ctx, target)
	require.NoError(t, err)
	require.NotNil(t, caps)

	require.Len(t, caps.ResourceTemplates, 1, "the advertised resource template must be surfaced")
	tmpl := caps.ResourceTemplates[0]
	assert.Equal(t, "file:///logs/{date}.txt", tmpl.URITemplate)
	assert.Equal(t, "Daily log", tmpl.Name)
	assert.Equal(t, "text/plain", tmpl.MimeType)
	assert.Equal(t, "docs-backend", tmpl.BackendID, "the template must be tagged with the backend ID")
}

// TestComplete_NoCompletionsCapability verifies the spec-lenient completion path:
// a backend that does NOT advertise the completions capability (this fixture only
// registers a resource template, no completion handler) yields a non-nil, EMPTY
// completion result and no error. The client must not surface a hard failure for a
// backend that simply cannot complete.
func TestComplete_NoCompletionsCapability(t *testing.T) {
	t.Parallel()

	port, cleanup := startResourceTemplateMCPServer(t)
	defer cleanup()

	registry := auth.NewDefaultOutgoingAuthRegistry()
	require.NoError(t, registry.RegisterStrategy("unauthenticated", &strategies.UnauthenticatedStrategy{}))

	backendClient, err := vmcpclient.NewHTTPBackendClient(registry)
	require.NoError(t, err)

	target := &vmcp.BackendTarget{
		WorkloadID:    "docs-backend",
		WorkloadName:  "Docs Backend",
		BaseURL:       "http://127.0.0.1:" + port,
		TransportType: "streamable-http",
	}

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	res, err := backendClient.Complete(ctx, target,
		vmcp.CompletionRef{Type: vmcp.CompletionRefTypePrompt, Name: "greeting"},
		"name", "wor", nil)
	require.NoError(t, err, "a backend without completions must not error (spec-lenient)")
	require.NotNil(t, res, "the completion result must be non-nil")
	assert.Empty(t, res.Values, "the completion result must carry an empty value set")
}

// startResourceTemplateMCPServer starts a test MCP server advertising a single
// resource template and serving its expanded URIs via the template handler.
func startResourceTemplateMCPServer(t *testing.T) (string, func()) {
	t.Helper()

	mcpServer := server.NewMCPServer("docs-backend", "1.0.0")

	mcpServer.AddResourceTemplate(
		mcp.ResourceTemplate{
			URITemplate: "file:///logs/{date}.txt",
			Name:        "Daily log",
			Description: "A day's log file",
			MIMEType:    "text/plain",
		},
		func(_ context.Context, req mcp.ReadResourceRequest) ([]mcp.ResourceContents, error) {
			return []mcp.ResourceContents{
				mcp.TextResourceContents{
					URI:      req.Params.URI,
					MIMEType: "text/plain",
					Text:     "log contents for " + req.Params.URI,
				},
			}, nil
		},
	)

	httpHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		rawMessage, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "Failed to read request", http.StatusBadRequest)
			return
		}
		defer r.Body.Close()
		response := mcpServer.HandleMessage(r.Context(), rawMessage)
		responseBytes, err := json.Marshal(response)
		if err != nil {
			http.Error(w, "Failed to marshal response", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(responseBytes)
	})

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	port := listener.Addr().(*net.TCPAddr).Port
	httpServer := &http.Server{Handler: httpHandler}
	go func() { _ = httpServer.Serve(listener) }()

	cleanup := func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = httpServer.Shutdown(ctx)
	}
	return strconv.Itoa(port), cleanup
}

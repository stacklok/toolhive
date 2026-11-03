// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive-core/mcpcompat/server"
	"github.com/stacklok/toolhive/pkg/auth"
	mcpparser "github.com/stacklok/toolhive/pkg/mcp"
	"github.com/stacklok/toolhive/pkg/vmcp"
	vmcpconfig "github.com/stacklok/toolhive/pkg/vmcp/config"
	"github.com/stacklok/toolhive/pkg/vmcp/core"
	"github.com/stacklok/toolhive/pkg/vmcp/server/sessionmanager"
)

func TestMergeInstructions(t *testing.T) {
	t.Parallel()

	byBackend := map[string]string{
		"jira":   "Search issues before creating one.\n",
		"github": "  Use the repo full name. ",
		"empty":  "   ",
	}
	got := mergeInstructions(t.Context(), byBackend, vmcpconfig.DefaultInstructionsMaxLength)
	assert.Equal(t, "[github]\nUse the repo full name.\n\n[jira]\nSearch issues before creating one.", got,
		"one trimmed block per backend, labelled, sorted by backend ID, blank-line separated")

	assert.Empty(t, mergeInstructions(t.Context(), nil, 100))
	assert.Empty(t, mergeInstructions(t.Context(), map[string]string{"a": ""}, 100))
}

func TestMergeInstructions_Cap(t *testing.T) {
	t.Parallel()

	byBackend := map[string]string{
		"a": strings.Repeat("x", 50),   // block = 4 + 50 bytes
		"b": strings.Repeat("y", 1000), // never fits: skipped
		"c": strings.Repeat("z", 10),   // 2 + 4 + 10 bytes: still fits after the skip
	}
	got := mergeInstructions(t.Context(), byBackend, 100)
	assert.Equal(t, "[a]\n"+strings.Repeat("x", 50)+"\n\n[c]\n"+strings.Repeat("z", 10), got)
	assert.LessOrEqual(t, len(got), 100)

	assert.Empty(t, mergeInstructions(t.Context(), map[string]string{"a": strings.Repeat("x", 50)}, 20),
		"a block is skipped whole, never cut")
}

func TestRenderInstructions_Config(t *testing.T) {
	t.Parallel()

	text := map[string]string{"a": "alpha"}
	for name, cfg := range map[string]*vmcpconfig.InstructionsConfig{
		"unset":         nil,
		"forward false": {Forward: false},
	} {
		s := &Server{config: &Config{Instructions: cfg}}
		assert.Empty(t, s.renderInstructions(t.Context(), text), name)
	}

	on := &Server{config: &Config{Instructions: &vmcpconfig.InstructionsConfig{Forward: true}}}
	assert.Equal(t, "[a]\nalpha", on.renderInstructions(t.Context(), text))

	capped := &Server{config: &Config{Instructions: &vmcpconfig.InstructionsConfig{Forward: true, MaxLength: 5}}}
	assert.Empty(t, capped.renderInstructions(t.Context(), text))
}

func TestDispatchModernDiscover_Instructions(t *testing.T) {
	t.Parallel()

	parsed := &mcpparser.ParsedMCPRequest{Method: "server/discover", IsRequest: true, ID: "1"}
	run := func(t *testing.T, cfg *vmcpconfig.InstructionsConfig) map[string]any {
		t.Helper()
		fc := &modernFakeCore{discoverCaps: core.DiscoverCapabilities{
			HasTools: true, BackendInstructions: map[string]string{"echo": "Call echo once."},
		}}
		s := &Server{config: &Config{Name: testServerName, Version: testServerVersion, Instructions: cfg}, core: fc}
		req := httptest.NewRequest(http.MethodPost, "/mcp", nil).WithContext(t.Context())
		rec := httptest.NewRecorder()
		s.dispatchModern(rec, req, parsed)
		require.Equal(t, http.StatusOK, rec.Code)
		var body map[string]any
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
		result, ok := body["result"].(map[string]any)
		require.True(t, ok, "got %v", body)
		return result
	}

	t.Run("forwarding off: no instructions key", func(t *testing.T) {
		t.Parallel()
		result := run(t, nil)
		_, present := result["instructions"]
		assert.False(t, present)
	})
	t.Run("forwarding on: merged text", func(t *testing.T) {
		t.Parallel()
		result := run(t, &vmcpconfig.InstructionsConfig{Forward: true})
		assert.Equal(t, "[echo]\nCall echo once.", result["instructions"])
	})
}

// serveInstructionsHarness starts Serve over fc with the given instructions
// config behind a Streamable HTTP server. Like the production Handler, an
// identity middleware runs in front of the shim: a request with an X-Subject
// header is bound to that subject; one without stays anonymous (nil identity).
// It returns the base URL.
func serveInstructionsHarness(t *testing.T, fc *fakeCore, cfg *vmcpconfig.InstructionsConfig) string {
	t.Helper()
	ctrl := gomock.NewController(t)
	factory, _ := newToolSessionFactory(t, ctrl, fc.tools)

	srv, err := Serve(context.Background(), fc, &ServerConfig{
		SessionTTL:           time.Minute,
		SessionManagerConfig: &sessionmanager.FactoryConfig{Base: factory},
		BackendRegistry:      vmcp.NewImmutableRegistry([]vmcp.Backend{}),
		Instructions:         cfg,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = srv.Stop(context.Background()) })

	streamable := server.NewStreamableHTTPServer(
		srv.mcpServer, server.WithEndpointPath("/mcp"), server.WithSessionIdManager(srv.vmcpSessionMgr))
	identityMiddleware := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if subject := r.Header.Get("X-Subject"); subject != "" {
			r = r.WithContext(auth.WithIdentity(r.Context(), &auth.Identity{Subject: subject}))
		}
		streamable.ServeHTTP(w, r)
	})
	ts := httptest.NewServer(identityMiddleware)
	t.Cleanup(ts.Close)
	return ts.URL
}

// instructionsHTTPClient is the transport the Serve-based tests below post
// with. httptest.Server.Close closes http.DefaultTransport's idle connections,
// so a parallel test's in-flight request on http.DefaultClient can fail with
// "CloseIdleConnections called". Keep-alives off and a transport of our own
// keep these tests out of that race.
var instructionsHTTPClient = &http.Client{Transport: &http.Transport{DisableKeepAlives: true}}

// initializeAs sends one Legacy initialize to baseURL as subject (empty =
// anonymous) and returns the result object plus the raw envelope (for
// absence checks).
func initializeAs(t *testing.T, baseURL, subject string) (map[string]any, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	body := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18",` +
		`"capabilities":{},"clientInfo":{"name":"test","version":"1.0"}}}`
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/mcp", strings.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if subject != "" {
		req.Header.Set("X-Subject", subject)
	}
	resp, err := instructionsHTTPClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	env, raw := readServeJSONRPC(t, resp)
	result, ok := env["result"].(map[string]any)
	require.True(t, ok, "initialize must return a result: %s", raw)
	return result, string(raw)
}

// serveInitialize is serveInstructionsHarness plus one anonymous initialize.
func serveInitialize(t *testing.T, fc *fakeCore, cfg *vmcpconfig.InstructionsConfig) (map[string]any, string) {
	t.Helper()
	return initializeAs(t, serveInstructionsHarness(t, fc, cfg), "")
}

// TestServe_InitializeForwardsBackendInstructions is the Legacy wire test:
// the per-backend text the core returns reaches the initialize result through
// the SDK shim's WithInstructionsFunc, only when forwarding is enabled.
func TestServe_InitializeForwardsBackendInstructions(t *testing.T) {
	t.Parallel()

	newCore := func() *fakeCore {
		return &fakeCore{
			tools:               []vmcp.Tool{{Name: "echo", Description: "echoes"}},
			backendInstructions: map[string]string{"echo": "Call echo once.", "jira": "Search first."},
		}
	}

	t.Run("off by default", func(t *testing.T) {
		t.Parallel()
		_, raw := serveInitialize(t, newCore(), nil)
		assert.NotContains(t, raw, `"instructions"`)
	})
	t.Run("forwarded, one block per backend", func(t *testing.T) {
		t.Parallel()
		result, _ := serveInitialize(t, newCore(), &vmcpconfig.InstructionsConfig{Forward: true})
		assert.Equal(t, "[echo]\nCall echo once.\n\n[jira]\nSearch first.", result["instructions"])
	})
	t.Run("forwarded but nothing to say", func(t *testing.T) {
		t.Parallel()
		fc := newCore()
		fc.backendInstructions = nil
		_, raw := serveInitialize(t, fc, &vmcpconfig.InstructionsConfig{Forward: true})
		assert.NotContains(t, raw, `"instructions"`)
	})
}

// TestServe_InitializeInstructionsFollowCaller proves the per-caller claim on
// the Legacy path end to end: the identity the HTTP layer bound to the
// initialize request reaches core.Discover through the shim's request-context
// bridge, so two callers on one server get their own text, and an anonymous
// caller gets what the core returns for a nil identity.
func TestServe_InitializeInstructionsFollowCaller(t *testing.T) {
	t.Parallel()

	fc := &fakeCore{
		tools: []vmcp.Tool{{Name: "echo", Description: "echoes"}},
		instructionsFor: func(identity *auth.Identity) map[string]string {
			if identity == nil {
				return nil
			}
			return map[string]string{identity.Subject: "guidance for " + identity.Subject}
		},
	}
	baseURL := serveInstructionsHarness(t, fc, &vmcpconfig.InstructionsConfig{Forward: true})

	alice, _ := initializeAs(t, baseURL, "alice")
	assert.Equal(t, "[alice]\nguidance for alice", alice["instructions"])

	bob, _ := initializeAs(t, baseURL, "bob")
	assert.Equal(t, "[bob]\nguidance for bob", bob["instructions"])

	_, raw := initializeAs(t, baseURL, "")
	assert.NotContains(t, raw, `"instructions"`, "an anonymous caller gets no text from this core")
}

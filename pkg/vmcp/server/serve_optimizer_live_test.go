// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"cmp"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive-core/mcpcompat/server"
	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/optimizer"
	"github.com/stacklok/toolhive/pkg/vmcp/server/sessionmanager"
	"github.com/stacklok/toolhive/pkg/vmcp/session/optimizerdec"
)

// Environment variables gating the live Serve-path optimizer test.
const (
	liveEmbeddingURLEnv   = "VMCP_LIVE_EMBEDDING_URL"
	liveEmbeddingModelEnv = "VMCP_LIVE_EMBEDDING_MODEL"
	liveToolCountEnv      = "VMCP_LIVE_TOOL_COUNT"
)

// TestServeOptimizerLive_SecondSessionServedWarm measures what a client actually
// experiences on the Serve path — the wait from `initialize` until find_tool is
// advertised — across consecutive sessions, against a real embedding backend.
//
// This is the end-to-end counterpart to the store-level embedding-reuse tests:
// it exercises the real optimizer factory, the real SQLite store, and a real
// embedding round-trip through the session-registration path that
// stacklok/toolhive#5847 describes, rather than asserting on a mock.
//
// Skipped unless VMCP_LIVE_EMBEDDING_URL points at an OpenAI-compatible
// /embeddings endpoint, so the default `task test` run stays green. Any such
// endpoint works; a local Ollama needs no API key:
//
//	VMCP_LIVE_EMBEDDING_URL=http://127.0.0.1:11434/v1 \
//	VMCP_LIVE_EMBEDDING_MODEL=bge-m3 \
//	VMCP_LIVE_TOOL_COUNT=140 \
//	go test ./pkg/vmcp/server/ -run TestServeOptimizerLive -v
func TestServeOptimizerLive_SecondSessionServedWarm(t *testing.T) {
	// Safe to run alongside other tests: every assertion is relative to this
	// run's own cold measurement, so a loaded machine moves both numbers.
	t.Parallel()

	endpoint := os.Getenv(liveEmbeddingURLEnv)
	if endpoint == "" {
		t.Skipf("%s not set; skipping live Serve-path optimizer test", liveEmbeddingURLEnv)
	}
	model := cmp.Or(os.Getenv(liveEmbeddingModelEnv), "bge-m3")

	toolCount := 140
	if raw := os.Getenv(liveToolCountEnv); raw != "" {
		parsed, err := strconv.Atoi(raw)
		require.NoErrorf(t, err, "%s must be an integer", liveToolCountEnv)
		require.Positive(t, parsed, "%s must be positive", liveToolCountEnv)
		toolCount = parsed
	}

	optFactory, cleanup, err := optimizer.NewOptimizerFactory(&optimizer.Config{
		EmbeddingService:        endpoint,
		EmbeddingProvider:       "openai",
		EmbeddingModel:          model,
		EmbeddingServiceTimeout: 2 * time.Minute,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = cleanup(context.Background()) })

	fc := &fakeCore{tools: liveTools(toolCount)}
	baseURL := serveWithOptimizerFactory(t, fc, optFactory)

	const sessions = 3
	elapsed := make([]time.Duration, sessions)
	for i := range sessions {
		elapsed[i] = timeToolsAvailable(t, baseURL)
		t.Logf("session %d: find_tool advertised after %s (%d tools)", i+1, elapsed[i].Round(time.Millisecond), toolCount)
	}

	// The first session pays for embedding the catalog; every later session must
	// be served from the stored vectors. The bound is deliberately loose — this
	// asserts the cold build is gone, not a particular backend's speed.
	warmBudget := max(elapsed[0]/4, 500*time.Millisecond)
	for i := 1; i < sessions; i++ {
		assert.Lessf(t, elapsed[i], warmBudget,
			"session %d must be served from stored embeddings, not rebuilt (cold was %s)", i+1, elapsed[0])
	}
}

// liveTools builds a synthetic catalog roughly the shape of an aggregated vMCP:
// several backends, each contributing tools with prose descriptions long enough
// to be representative of real embedding input.
func liveTools(n int) []vmcp.Tool {
	backends := []string{"grafana", "datadog", "argocd", "k8s", "gitnexus", "dbhub", "firecrawl", "context7"}
	tools := make([]vmcp.Tool, n)
	for i := range n {
		backend := backends[i%len(backends)]
		tools[i] = vmcp.Tool{
			Name: fmt.Sprintf("%s_operation_%03d", backend, i),
			Description: fmt.Sprintf(
				"Perform operation %d against the %s backend. This tool queries the %s subsystem, "+
					"applies the requested filters, and returns the matching records together with their "+
					"metadata so the caller can decide how to proceed.", i, backend, backend),
		}
	}
	return tools
}

// serveWithOptimizerFactory starts a Serve-path test server wired to the given
// optimizer factory and returns its base URL. It mirrors
// registerServeOptimizerSession but takes a real factory and registers no
// session, so each session can be timed individually.
func serveWithOptimizerFactory(
	t *testing.T, vmcpCore *fakeCore, optFactory func(context.Context, []server.ServerTool) (optimizer.Optimizer, error),
) string {
	t.Helper()
	ctrl := gomock.NewController(t)
	factory, _ := newToolSessionFactory(t, ctrl, vmcpCore.tools)

	srv, err := Serve(context.Background(), vmcpCore, &ServerConfig{
		SessionTTL: time.Minute,
		SessionManagerConfig: &sessionmanager.FactoryConfig{
			Base:              factory,
			OptimizerFactory:  optFactory,
			AdvertiseFromCore: true,
		},
		BackendRegistry: vmcp.NewImmutableRegistry([]vmcp.Backend{}),
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = srv.Stop(context.Background()) })

	streamable := server.NewStreamableHTTPServer(
		srv.mcpServer,
		server.WithEndpointPath("/mcp"),
		server.WithSessionIdManager(srv.vmcpSessionMgr),
	)
	ts := httptest.NewServer(streamable)
	t.Cleanup(ts.Close)
	return ts.URL
}

// timeToolsAvailable opens a fresh session and returns how long it takes before
// find_tool is advertised — the moment a client can actually use the server.
func timeToolsAvailable(t *testing.T, baseURL string) time.Duration {
	t.Helper()
	start := time.Now()

	initResp := postServeMCP(t, baseURL, initBody, "")
	defer initResp.Body.Close()
	require.Equal(t, http.StatusOK, initResp.StatusCode)
	sessionID := initResp.Header.Get("Mcp-Session-Id")
	require.NotEmpty(t, sessionID)

	require.Eventually(t, func() bool {
		for _, name := range serveToolNames(t, baseURL, sessionID) {
			if name == optimizerdec.FindToolName {
				return true
			}
		}
		return false
	}, 3*time.Minute, 20*time.Millisecond, "find_tool should eventually be advertised")

	return time.Since(start)
}

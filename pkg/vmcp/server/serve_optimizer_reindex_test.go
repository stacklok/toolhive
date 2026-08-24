// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive-core/mcpcompat/mcp"
	"github.com/stacklok/toolhive-core/mcpcompat/server"
	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/optimizer"
)

// These tests cover the optimizer-mode half of #5786 (PR2): the session's
// optimizer index is rebuilt behind a stable handle when the health-filtered
// core tool set changes, so find_tool stops surfacing a failed backend's tools
// and starts surfacing a recovered one's — without rewriting the session's
// advertised meta-tools (which would emit a downstream
// notifications/tools/list_changed carrying no news).

// optimizerToolNames extracts the tool names find_tool would surface.
func optimizerToolNames(t *testing.T, opt optimizer.Optimizer) []string {
	t.Helper()
	out, err := opt.FindTool(context.Background(), optimizer.FindToolInput{ToolDescription: "anything"})
	require.NoError(t, err)
	names := make([]string, 0, len(out.Tools))
	for _, tool := range out.Tools {
		names = append(names, tool.Name)
	}
	return names
}

// TestSessionOptimizer_SwapRedirectsBothMetaTools verifies the handle delegates
// to whichever instance is current: both find_tool's scope and call_tool's
// dispatch follow a swap, which is what lets a re-index take effect without
// touching handlers the session already advertises.
func TestSessionOptimizer_SwapRedirectsBothMetaTools(t *testing.T) {
	t.Parallel()

	before := &dispatchOptimizer{
		tools: map[string]server.ServerTool{"old": {Tool: mcp.Tool{Name: "old"}}},
		defs:  []mcp.Tool{{Name: "old"}},
	}
	handle := newSessionOptimizer(before)
	assert.Equal(t, []string{"old"}, optimizerToolNames(t, handle))

	after := &dispatchOptimizer{
		tools: map[string]server.ServerTool{"new": {Tool: mcp.Tool{Name: "new"}}},
		defs:  []mcp.Tool{{Name: "new"}},
	}
	handle.swap(after)

	assert.Equal(t, []string{"new"}, optimizerToolNames(t, handle),
		"find_tool must resolve against the swapped-in index")

	res, err := handle.CallTool(context.Background(), optimizer.CallToolInput{ToolName: "old"})
	require.NoError(t, err)
	require.NotNil(t, res)
	assert.True(t, res.IsError, "a tool dropped by the swap must no longer be callable")
}

// TestReindexSessionOptimizer_DropsFailedAndGainsRecovered is the behavior a
// client sees on a health flip in optimizer mode: the meta-tools are untouched,
// but find_tool's scope now matches the health-filtered core set.
func TestReindexSessionOptimizer_DropsFailedAndGainsRecovered(t *testing.T) {
	t.Parallel()

	// Registration-time view: the failed backend's tool is still advertised.
	fc := &fakeCore{tools: []vmcp.Tool{{Name: "kept"}, {Name: "failed"}}}
	factory := &recordingOptimizerFactory{}
	srv := &Server{
		core:             &healthEnabledCore{fakeCore: fc, reporter: newTestHealthReporter(t)},
		vmcpSessionMgr:   &stubSessionManager{alive: true},
		resyncBaseCtx:    context.Background(),
		optimizerFactory: factory.build,
	}

	metaTools, err := srv.serveSessionTools(context.Background(), "sess-1", nil)
	require.NoError(t, err)
	require.Len(t, metaTools, 2, "optimizer mode advertises exactly find_tool and call_tool")

	handle := srv.healthResync.optimizerFor("sess-1")
	require.NotNil(t, handle, "an optimizer-mode session must get a registered handle")
	assert.ElementsMatch(t, []string{"kept", "failed"}, optimizerToolNames(t, handle))

	// The health monitor drops the failed backend and admits a recovered one.
	fc.tools = []vmcp.Tool{{Name: "kept"}, {Name: "recovered"}}

	handled, err := srv.reindexSessionOptimizer(context.Background(), "sess-1", nil)
	require.NoError(t, err)
	assert.True(t, handled)

	assert.ElementsMatch(t, []string{"kept", "recovered"}, optimizerToolNames(t, handle),
		"find_tool must gain the recovered backend's tool and drop the failed one")

	// The advertised meta-tool set is identical, so nothing about the session's
	// tool store needed rewriting.
	names := make([]string, 0, len(metaTools))
	for _, mt := range metaTools {
		names = append(names, mt.Tool.Name)
	}
	assert.ElementsMatch(t, []string{"find_tool", "call_tool"}, names)
}

// TestReindexSessionOptimizer_UnregisteredSessionNotHandled verifies the
// fallback contract: with no registered handle (health monitoring disabled, or a
// session this pod never registered) the re-index reports handled=false so the
// caller drops back to rebuild-and-replace rather than silently skipping the
// re-derivation.
func TestReindexSessionOptimizer_UnregisteredSessionNotHandled(t *testing.T) {
	t.Parallel()

	factory := &recordingOptimizerFactory{}
	srv := &Server{
		core:             &fakeCore{tools: []vmcp.Tool{{Name: "t"}}}, // BackendHealth() nil: monitoring disabled
		vmcpSessionMgr:   &stubSessionManager{alive: true},
		resyncBaseCtx:    context.Background(),
		optimizerFactory: factory.build,
	}

	// Even after building the session's optimizer, nothing is retained.
	_, err := srv.serveSessionTools(context.Background(), "sess-1", nil)
	require.NoError(t, err)
	assert.Nil(t, srv.healthResync.optimizerFor("sess-1"),
		"with monitoring disabled no per-session optimizer state may be retained")

	handled, err := srv.reindexSessionOptimizer(context.Background(), "sess-1", nil)
	require.NoError(t, err)
	assert.False(t, handled, "an unregistered session must fall back to rebuild-and-replace")
}

// TestInstallSessionOptimizer_ReusesHandleAcrossRebuilds verifies that
// re-deriving an existing session's tools (cross-pod re-injection, or a resync
// falling back to rebuild-and-replace) swaps into the SAME handle. Otherwise
// handlers installed earlier would pin the instance they were built with and
// later re-indexes would be invisible to them.
func TestInstallSessionOptimizer_ReusesHandleAcrossRebuilds(t *testing.T) {
	t.Parallel()

	fc := &fakeCore{tools: []vmcp.Tool{{Name: "first"}}}
	factory := &recordingOptimizerFactory{}
	srv := &Server{
		core:             &healthEnabledCore{fakeCore: fc, reporter: newTestHealthReporter(t)},
		vmcpSessionMgr:   &stubSessionManager{alive: true},
		resyncBaseCtx:    context.Background(),
		optimizerFactory: factory.build,
	}

	_, err := srv.serveSessionTools(context.Background(), "sess-1", nil)
	require.NoError(t, err)
	first := srv.healthResync.optimizerFor("sess-1")
	require.NotNil(t, first)

	fc.tools = []vmcp.Tool{{Name: "second"}}
	_, err = srv.serveSessionTools(context.Background(), "sess-1", nil)
	require.NoError(t, err)

	assert.Same(t, first, srv.healthResync.optimizerFor("sess-1"),
		"a rebuild must swap into the existing handle, not replace it")
	assert.Equal(t, []string{"second"}, optimizerToolNames(t, first),
		"handlers bound to the original handle must observe the rebuilt index")
}

// TestHealthResyncRegistry_RemoveDropsOptimizerHandle verifies the optimizer
// handle shares the worker's lifecycle, so optimizer-mode re-indexing adds no
// second set of prune sites (and cannot leak a handle after termination).
func TestHealthResyncRegistry_RemoveDropsOptimizerHandle(t *testing.T) {
	t.Parallel()

	var r healthResyncRegistry
	r.add("sess-1", &listChangedResyncWorker{})
	r.installOptimizer("sess-1", &dispatchOptimizer{})
	require.NotNil(t, r.optimizerFor("sess-1"))

	r.remove("sess-1")

	assert.Empty(t, r.snapshot())
	assert.Nil(t, r.optimizerFor("sess-1"), "remove must drop the optimizer handle too")
}

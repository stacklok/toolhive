// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive-core/mcpcompat/mcp"
	"github.com/stacklok/toolhive-core/mcpcompat/server"
	"github.com/stacklok/toolhive/pkg/vmcp"
)

// fakeToolsSession is a minimal server.ClientSession + server.SessionWithTools
// fake used to test resyncSessionTools/buildListChangedSink without a live SDK
// session. It records every SetSessionTools call so tests can assert
// replacement semantics.
type fakeToolsSession struct {
	id string

	mu                   sync.Mutex
	tools                map[string]server.ServerTool
	setSessionToolsCalls int
}

func (*fakeToolsSession) Initialize()                                         {}
func (*fakeToolsSession) Initialized() bool                                   { return true }
func (*fakeToolsSession) NotificationChannel() chan<- mcp.JSONRPCNotification { return nil }
func (f *fakeToolsSession) SessionID() string                                 { return f.id }

func (f *fakeToolsSession) GetSessionTools() map[string]server.ServerTool {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make(map[string]server.ServerTool, len(f.tools))
	for k, v := range f.tools {
		out[k] = v
	}
	return out
}

func (f *fakeToolsSession) SetSessionTools(tools map[string]server.ServerTool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tools = tools
	f.setSessionToolsCalls++
}

var _ server.ClientSession = (*fakeToolsSession)(nil)
var _ server.SessionWithTools = (*fakeToolsSession)(nil)

// TestResyncSessionTools_ReplacesRatherThanMerges verifies (#5748) that
// resyncSessionTools REPLACES the session's tool store with the freshly
// core-derived set — unlike setSessionToolsDirect's registration-time MERGE —
// so a tool the backend removed (and the core therefore no longer advertises)
// disappears rather than lingering.
func TestResyncSessionTools_ReplacesRatherThanMerges(t *testing.T) {
	t.Parallel()

	fc := &fakeCore{tools: []vmcp.Tool{{Name: "kept"}, {Name: "added"}}}
	srv := &Server{core: fc}

	sess := &fakeToolsSession{id: "sess-1", tools: map[string]server.ServerTool{
		"kept":    {Tool: mcp.Tool{Name: "kept"}},
		"removed": {Tool: mcp.Tool{Name: "removed"}}, // must disappear after resync
	}}

	err := srv.resyncSessionTools(context.Background(), sess, "sess-1", nil)
	require.NoError(t, err)

	got := sess.GetSessionTools()
	assert.Len(t, got, 2)
	assert.Contains(t, got, "kept")
	assert.Contains(t, got, "added")
	assert.NotContains(t, got, "removed", "resync must REPLACE the tool store, dropping a no-longer-advertised tool")
	assert.Equal(t, 1, sess.setSessionToolsCalls)
}

// TestResyncSessionTools_SessionWithoutToolSupport verifies resyncSessionTools
// fails loudly (rather than silently no-opping) when the session does not
// implement server.SessionWithTools.
func TestResyncSessionTools_SessionWithoutToolSupport(t *testing.T) {
	t.Parallel()

	fc := &fakeCore{tools: []vmcp.Tool{{Name: "t"}}}
	srv := &Server{core: fc}

	// plainSession implements only server.ClientSession, not SessionWithTools.
	type plainSession struct{ server.ClientSession }
	sess := plainSession{&fakeToolsSession{id: "sess-1"}}

	err := srv.resyncSessionTools(context.Background(), sess, "sess-1", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "does not support per-session tools")
}

// TestBuildListChangedSink_TableDriven exercises buildListChangedSink's
// behavior (#5748): it is a no-op for anything other than kind=="tools", and
// for kind=="tools" it invalidates the shared capability cache BEFORE
// re-deriving and replacing the session's tool set.
func TestBuildListChangedSink_TableDriven(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                 string
		kind                 string
		wantInvalidateCalls  int32
		wantSetSessionToolsN int
		wantFinalToolCount   int
	}{
		{
			name:                 "kind=resources is ignored (out of scope)",
			kind:                 "resources",
			wantInvalidateCalls:  0,
			wantSetSessionToolsN: 0,
			wantFinalToolCount:   1, // unchanged: only "stale" remains
		},
		{
			name:                 "kind=tools invalidates cache and resyncs",
			kind:                 "tools",
			wantInvalidateCalls:  1,
			wantSetSessionToolsN: 1,
			wantFinalToolCount:   1, // replaced: only "fresh" remains
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			fc := &fakeCore{tools: []vmcp.Tool{{Name: "fresh"}}}
			srv := &Server{core: fc}
			sess := &fakeToolsSession{id: "sess-1", tools: map[string]server.ServerTool{
				"stale": {Tool: mcp.Tool{Name: "stale"}},
			}}

			sink := srv.buildListChangedSink("sess-1", sess, nil)
			sink(context.Background(), "backend-1", tc.kind)

			assert.Equal(t, tc.wantInvalidateCalls, fc.invalidateCacheCalls.Load())
			assert.Equal(t, tc.wantSetSessionToolsN, sess.setSessionToolsCalls)
			assert.Len(t, sess.GetSessionTools(), tc.wantFinalToolCount)
		})
	}
}

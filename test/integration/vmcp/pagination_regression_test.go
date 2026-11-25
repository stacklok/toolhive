// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package vmcp_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/test/integration/vmcp/helpers"
)

// TestRegression_Over1000Tools_CompleteSetReceived is a regression anchor for
// the vMCP pagination gap: a backend exposing more than the MCP page size
// (mcpcompat paginates at DefaultPageSize=1000) must surface its complete tool
// set across pagination cursors.
//
// vMCP follows list-pagination cursors in two independent code paths, and this
// test gates both:
//   - the session-projection path (pkg/vmcp/session/.../mcp_session.go), which
//     the aggregated ListTools count exercises; and
//   - the aggregator/routing-table path (pkg/vmcp/client), which CallTool on a
//     tool living beyond the first page exercises.
//
// Before the fix, each path issued a single ListTools with no cursor loop, so a
// paginating backend returned only its first page and every tool beyond it was
// silently dropped.
func TestRegression_Over1000Tools_CompleteSetReceived(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	// Generate 1000+ tools on a single backend to exceed the MCP page size.
	// mcpcompat's default page size is 1000, so 1100 tools force pagination
	// into a first full page plus a short tail page.
	const toolCount = 1100
	tools := make([]helpers.BackendTool, 0, toolCount)
	for i := 0; i < toolCount; i++ {
		name := fmt.Sprintf("tool_%04d", i)
		tools = append(tools, helpers.NewBackendTool(
			name,
			fmt.Sprintf("backend tool number %d", i),
			func(_ context.Context, _ map[string]any) string {
				return `{"ok": true}`
			},
		))
	}

	backend := helpers.CreateBackendServer(t, tools, helpers.WithBackendName("many-tools"))
	defer backend.Close()

	backends := []vmcp.Backend{
		helpers.NewBackend("many-tools",
			helpers.WithURL(backend.URL+"/mcp"),
			helpers.WithMetadata("group", "pagination-test"),
		),
	}

	vmcpServer := helpers.NewVMCPServer(ctx, t, backends,
		helpers.WithPrefixConflictResolution("{workload}_"),
	)

	vmcpURL := "http://" + vmcpServer.Address() + "/mcp"
	client := helpers.NewMCPClient(ctx, t, vmcpURL)
	defer client.Close()

	// The first vMCP page must be bounded by the server page size and carry a
	// non-empty NextCursor. This proves pagination actually occurred rather
	// than the backend returning everything in one page (which would make the
	// count assertions below pass even without cursor following).
	firstPage, firstCursor := client.ListToolsPage(ctx, "")
	assert.LessOrEqual(t, len(firstPage), 1000,
		"the first vMCP page must not exceed the server page size (1000)")
	assert.NotEmpty(t, firstCursor,
		"a non-empty NextCursor must be returned when tools remain beyond the first page")

	// The complete tool set must be received across pages. Assert an exact
	// count AND that many DISTINCT names: a plain count could be satisfied by
	// page overlap/duplication, which distinct-name counting catches.
	result := client.ListTools(ctx)
	require.Len(t, result.Tools, toolCount,
		"vMCP must surface the complete backend tool set across pagination cursors")

	names := helpers.GetToolNames(result)
	distinct := make(map[string]struct{}, len(names))
	for _, n := range names {
		distinct[n] = struct{}{}
	}
	assert.Len(t, distinct, toolCount,
		"every tool name must be distinct; duplicates indicate page overlap")

	// Calling the highest-index tool (which lives on the tail page, beyond page
	// 1) must succeed. This exercises the aggregator/routing-table path in
	// pkg/vmcp/client, which the session-projection ListTools count alone does
	// not cover: if that path dropped the tail, the tool would be unroutable.
	lastTool := fmt.Sprintf("many-tools_tool_%04d", toolCount-1)
	callResult := client.CallTool(ctx, lastTool, map[string]any{})
	helpers.AssertToolCallSuccess(t, callResult)
}

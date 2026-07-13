// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package vmcp_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/test/integration/vmcp/helpers"
)

// TestRegression_Over1000Tools_CompleteSetReceived is a regression anchor for
// the vMCP pagination gap: a backend exposing more than the MCP page size
// (1000 tools) must surface its complete tool set across pagination cursors.
//
// Today the Serve-path tool query path issues a single ListTools with no
// cursor loop, so backends that paginate return only the first page. The test
// is intentionally skipped until the gap is closed (see gap-analysis V1 and the
// follow-up issue); the skeleton remains so the regression cannot regress
// silently — flipping the skip off re-runs the full assertion.
func TestRegression_Over1000Tools_CompleteSetReceived(t *testing.T) {
	t.Parallel()
	t.Skip("vMCP does not follow pagination cursors; see gap-analysis V1 / follow-up issue")

	ctx := context.Background()

	// Generate 1000+ tools on a single backend to exceed the MCP page size.
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

	result := client.ListTools(ctx)

	// The complete tool set must be received; a single-page query returns at
	// most the page size (1000), so fewer than toolCount indicates a dropped
	// tail — the regression this test pins.
	assert.GreaterOrEqual(t, len(result.Tools), toolCount,
		"vMCP must surface the complete backend tool set across pagination cursors; "+
			"received %d of %d", len(result.Tools), toolCount)
}

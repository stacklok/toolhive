// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package script

import (
	"context"

	"github.com/mark3labs/mcp-go/mcp"
)

// executor is the unexported implementation of Executor.
type executor struct {
	tools  []Tool
	config Config
}

// Execute runs a Starlark script against the bound tools.
// TODO: Wire to internal packages in commit 5.
func (e *executor) Execute(_ context.Context, _ string, _ map[string]interface{}) (*mcp.CallToolResult, error) {
	panic("not yet implemented — wired in commit 5")
}

// ToolDescription returns the dynamic description for the virtual tool.
func (e *executor) ToolDescription() string {
	return GenerateToolDescription(e.tools)
}

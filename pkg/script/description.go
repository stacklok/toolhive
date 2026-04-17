// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package script

import (
	"fmt"
	"strings"
)

// ExecuteToolScriptName is the name of the virtual tool exposed by the
// script middleware.
const ExecuteToolScriptName = "execute_tool_script"

// GenerateToolDescription creates a dynamic description for the
// execute_tool_script tool, listing all available tools and their
// calling conventions.
func GenerateToolDescription(tools []Tool) string {
	var b strings.Builder
	b.WriteString("Execute a Starlark script that orchestrates multiple tool calls ")
	b.WriteString("and returns an aggregated result. Use 'return' to produce output.\n\n")
	b.WriteString("Available tools (callable as functions with keyword or positional arguments):\n")
	for _, t := range tools {
		desc := t.Description
		runes := []rune(desc)
		if len(runes) > 80 {
			desc = string(runes[:77]) + "..."
		}
		fmt.Fprintf(&b, "  - %s: %s\n", t.Name, desc)
	}
	b.WriteString("\nUse call_tool(\"name\", ...) to call any tool by its original name.\n\n")
	b.WriteString("Built-in: parallel([fn1, fn2, ...]) executes zero-arg callables concurrently ")
	b.WriteString("and returns results in order. Use with lambda to fan out tool calls.\n\n")
	b.WriteString("Named data arguments passed in the 'data' parameter are available as top-level variables.")
	return b.String()
}

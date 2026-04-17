// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package script provides a Starlark-based script execution engine for
// orchestrating MCP tool calls. It allows agents to send scripts that call
// multiple tools and return aggregated results in a single round-trip.
package script

import (
	"context"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
)

// DefaultStepLimit is the default maximum number of Starlark execution steps.
const DefaultStepLimit uint64 = 100_000

// Executor runs Starlark scripts and describes the virtual tool.
//
// Scripts can call any tool bound at construction time, use loops and
// conditionals, fan out with parallel(), and return aggregated results.
// The returned CallToolResult is ready for direct serialization into a
// JSON-RPC response by the middleware layer.
type Executor interface {
	// Execute runs a Starlark script with optional named data arguments
	// injected as top-level variables. Tools are already bound from
	// construction and available as callable functions within the script.
	Execute(ctx context.Context, script string, data map[string]interface{}) (*mcp.CallToolResult, error)

	// ToolDescription returns the dynamic description for the
	// execute_tool_script virtual tool definition, listing all available
	// tools and their calling conventions.
	ToolDescription() string
}

// Tool bundles an MCP tool's metadata with a callback for invoking it.
//
// The middleware layer constructs these with Call closures that route
// invocations through the middleware chain, ensuring authz and other
// policies are enforced on inner tool calls.
type Tool struct {
	// Name is the MCP tool name (e.g., "github-fetch-prs").
	Name string

	// Description is the human-readable tool description.
	Description string

	// Call invokes the tool with the given arguments and returns the MCP result.
	// Arguments are always a string-keyed map. When scripts use positional
	// arguments, they are converted to "arg0", "arg1", etc.
	//
	// The caller is responsible for enforcing per-call timeouts (e.g., by
	// wrapping ctx with context.WithTimeout in the closure). The engine
	// passes the context through but does not apply additional deadlines.
	Call func(ctx context.Context, arguments map[string]interface{}) (*mcp.CallToolResult, error)
}

// DefaultScriptTimeout is the default maximum wall-clock duration for a
// single script execution, including all tool calls.
const DefaultScriptTimeout = 30 * time.Second

// Config holds script execution parameters. A nil Config passed to New
// uses sensible defaults for all fields.
type Config struct {
	// StepLimit is the maximum number of Starlark execution steps per script.
	// Prevents infinite loops and runaway computation.
	// Zero uses DefaultStepLimit (100,000).
	StepLimit uint64

	// ParallelMax is the maximum number of concurrent goroutines that
	// parallel() can spawn. Zero means unlimited.
	ParallelMax int

	// ScriptTimeout is the maximum wall-clock duration for a single script
	// execution, including all tool calls. Bounds total execution time so
	// a script with many slow tool calls cannot block indefinitely.
	// Zero uses DefaultScriptTimeout (30s).
	ScriptTimeout time.Duration
}

// New creates an Executor bound to the given tools and configuration.
// A nil cfg uses defaults (DefaultStepLimit, unlimited parallelism, no timeout).
func New(tools []Tool, cfg *Config) Executor {
	c := resolveConfig(cfg)
	return &executor{
		tools:  tools,
		config: c,
	}
}

func resolveConfig(cfg *Config) Config {
	if cfg == nil {
		return Config{
			StepLimit:     DefaultStepLimit,
			ScriptTimeout: DefaultScriptTimeout,
		}
	}
	c := *cfg
	if c.StepLimit == 0 {
		c.StepLimit = DefaultStepLimit
	}
	if c.ParallelMax < 0 {
		c.ParallelMax = 0
	}
	if c.ScriptTimeout == 0 {
		c.ScriptTimeout = DefaultScriptTimeout
	}
	return c
}

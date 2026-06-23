// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package codemode adapts the vMCP-agnostic Starlark engine in pkg/script into a
// [core.VMCP] decorator. The decorator advertises the execute_tool_script virtual
// tool alongside the backend tools and, when that tool is called, runs the submitted
// Starlark script. Inner tool calls made by the script route back through the inner
// core's CallTool, so the core admission seam authorizes each one by its real name —
// the script can only reach tools the identity is already admitted to.
//
// The decorator is the seam where vMCP domain types ([vmcp.Tool], [vmcp.ToolCallResult])
// meet the engine's mcp-go types. That conversion is contained here so it does not leak
// across the core boundary (vmcp anti-pattern #5).
package codemode

import (
	"time"

	"github.com/stacklok/toolhive/pkg/script"
	"github.com/stacklok/toolhive/pkg/vmcp/config"
)

// Default tuning values for enabled code mode, applied by resolve to any field left
// unset or non-positive. They mirror the kubebuilder defaults on [config.CodeModeConfig]
// so every entry point — the operator/CRD path, the standalone YAML path, and a direct
// NewDecorator call — converges on the same bounded behavior. Both zero-value traps are
// intentional to guard against: a zero ParallelMax means "unlimited" at the engine, and a
// zero ToolCallTimeout means "no deadline".
const (
	defaultParallelMax     = 10
	defaultToolCallTimeout = 30 * time.Second
)

// FromConfig translates the serialized vMCP code mode config into the runtime decorator
// [Config]. It returns nil when c is nil or disabled, so the caller can hand the result
// straight to the server config (a nil value leaves the core undecorated). It performs no
// defaulting: unset fields stay zero and are resolved to defaults by [NewDecorator] (see
// resolve), so the operator/CRD, YAML, and direct-construction paths share one source of
// defaults.
func FromConfig(c *config.CodeModeConfig) *Config {
	if c == nil || !c.Enabled {
		return nil
	}
	out := &Config{
		ParallelMax:     c.ParallelMaxConcurrency,
		ToolCallTimeout: time.Duration(c.ToolCallTimeout),
	}
	if c.StepLimit > 0 {
		out.StepLimit = uint64(c.StepLimit)
	}
	return out
}

// Config holds the code mode tuning parameters. Presence of a non-nil *Config at the
// composition root is itself the opt-in toggle; a nil *Config (the default) leaves the
// core undecorated and execute_tool_script absent from tools/list.
//
// A nil *Config passed to NewDecorator, or any field left zero, resolves to the safe
// defaults in resolve — never to the engine's unbounded zero-value behavior.
type Config struct {
	// StepLimit is the maximum number of Starlark execution steps per script.
	// Zero resolves to [script.DefaultStepLimit].
	StepLimit uint64

	// ParallelMax is the maximum number of concurrent goroutines that parallel()
	// may spawn within a script. Zero (or negative) resolves to defaultParallelMax.
	ParallelMax int

	// ToolCallTimeout bounds each individual inner tool call made from a script.
	// Zero (or negative) resolves to defaultToolCallTimeout.
	ToolCallTimeout time.Duration
}

// scriptConfig projects the (already resolved) decorator config onto the engine's
// [script.Config]. Defaulting happens in resolve, so the values here are concrete.
func (c *Config) scriptConfig() *script.Config {
	return &script.Config{
		StepLimit:   c.StepLimit,
		ParallelMax: c.ParallelMax,
	}
}

// resolve returns a non-nil, fully-defaulted copy of cfg. It is the single source of code
// mode defaults: a nil cfg or any zero/negative field resolves to a safe bound, so callers
// that construct a Config directly get the same caps as the FromConfig path. This is what
// makes a zero ParallelMax (engine: unlimited) and a zero ToolCallTimeout (engine: no
// deadline) safe at this layer.
func resolve(cfg *Config) Config {
	c := Config{}
	if cfg != nil {
		c = *cfg
	}
	if c.StepLimit == 0 {
		c.StepLimit = script.DefaultStepLimit
	}
	if c.ParallelMax <= 0 {
		c.ParallelMax = defaultParallelMax
	}
	if c.ToolCallTimeout <= 0 {
		c.ToolCallTimeout = defaultToolCallTimeout
	}
	return c
}

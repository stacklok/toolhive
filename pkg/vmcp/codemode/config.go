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

// Defaults applied to enabled code mode when the serialized config leaves a field unset.
// They mirror the kubebuilder defaults on [config.CodeModeConfig] so the operator/CRD path
// and the standalone YAML path converge on the same behavior.
const (
	// defaultParallelMax bounds parallel() fan-out. A zero ParallelMax means "unlimited"
	// at the engine, so an unset value must resolve to this cap rather than to zero.
	defaultParallelMax = 10

	// defaultToolCallTimeout bounds each inner tool call. Zero imposes no deadline at the
	// engine, so an unset value must resolve to this timeout rather than to zero.
	defaultToolCallTimeout = 30 * time.Second
)

// FromConfig translates the serialized vMCP code mode config into the runtime decorator
// [Config]. It returns nil when c is nil or disabled, so the caller can hand the result
// straight to the server config (a nil value leaves the core undecorated). Unset fields
// fall back to the defaults above; a zero StepLimit is left for [script.New] to resolve to
// [script.DefaultStepLimit].
func FromConfig(c *config.CodeModeConfig) *Config {
	if c == nil || !c.Enabled {
		return nil
	}
	out := &Config{ParallelMax: defaultParallelMax, ToolCallTimeout: defaultToolCallTimeout}
	if c.StepLimit > 0 {
		out.StepLimit = uint64(c.StepLimit)
	}
	if c.ParallelMaxConcurrency > 0 {
		out.ParallelMax = c.ParallelMaxConcurrency
	}
	if c.ToolCallTimeout > 0 {
		out.ToolCallTimeout = time.Duration(c.ToolCallTimeout)
	}
	return out
}

// Config holds the code mode tuning parameters. Presence of a non-nil *Config at the
// composition root is itself the opt-in toggle; a nil *Config (the default) leaves the
// core undecorated and execute_tool_script absent from tools/list.
//
// A nil *Config passed to NewDecorator uses defaults for every field.
type Config struct {
	// StepLimit is the maximum number of Starlark execution steps per script.
	// Zero uses [script.DefaultStepLimit].
	StepLimit uint64

	// ParallelMax is the maximum number of concurrent goroutines that parallel()
	// may spawn within a script. Zero means unlimited; negative is treated as zero.
	ParallelMax int

	// ToolCallTimeout bounds each individual inner tool call made from a script.
	// Zero (or negative) means no per-call deadline is imposed by the decorator.
	ToolCallTimeout time.Duration
}

// scriptConfig projects the decorator config onto the engine's [script.Config].
// Step-limit and parallelism defaulting is delegated to script.New, which resolves
// a zero StepLimit to script.DefaultStepLimit and a negative ParallelMax to zero.
func (c *Config) scriptConfig() *script.Config {
	return &script.Config{
		StepLimit:   c.StepLimit,
		ParallelMax: c.ParallelMax,
	}
}

// resolve returns a non-nil, normalized copy of cfg. A nil cfg yields zero-valued
// defaults (which script.New in turn resolves to its own defaults).
func resolve(cfg *Config) Config {
	if cfg == nil {
		return Config{}
	}
	c := *cfg
	if c.ToolCallTimeout < 0 {
		c.ToolCallTimeout = 0
	}
	return c
}

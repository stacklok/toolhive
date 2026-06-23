// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package codemode

import (
	"context"
	"fmt"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/script"
	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/conversion"
	"github.com/stacklok/toolhive/pkg/vmcp/core"
)

// exampleScript is a worked example shown in the execute_tool_script input schema so the
// calling model can see the calling conventions (tool-as-function calls, parallel() fan-out,
// data variables, and return) rather than inferring them from prose. Tool names here are
// illustrative; the real callable names are listed in the tool's dynamic description.
const exampleScript = `# 'deps' is supplied via the data argument below. Bind the loop variable with a
# default arg (d=d) so each lambda captures its own value rather than the last one.
results = parallel([
    lambda d=d: osv_query_vulnerability(package=d["name"], version=d["version"])
    for d in deps
])
vulnerable = [r for r in results if r.get("vulns")]
return {"checked": len(results), "vulnerable": vulnerable}`

// virtualToolInputSchema is the JSON Schema advertised for execute_tool_script.
// It is a fixed shape (a script string plus an optional data object), so it is shared
// across every ListTools/LookupTool call rather than rebuilt per request. Callers must
// not mutate the returned map; build a fresh one per advertised tool to be safe.
//
// A top-level "examples" entry shows a complete, valid invocation so the model can see how
// to combine a script with its data variables; "script" carries the example inline too,
// since some clients surface per-property examples but not object-level ones.
func virtualToolInputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"script": map[string]any{
				"type": "string",
				"description": "A Starlark script. Call any listed tool as a function, use " +
					"loops/conditionals, fan out with parallel(), and use 'return' to produce output.",
				"examples": []any{exampleScript},
			},
			"data": map[string]any{
				"type":        "object",
				"description": "Optional named values injected as top-level variables in the script.",
			},
		},
		"required": []any{"script"},
		"examples": []any{
			map[string]any{
				"script": exampleScript,
				"data": map[string]any{
					"deps": []any{
						map[string]any{"name": "lodash", "version": "4.17.20"},
						map[string]any{"name": "express", "version": "4.17.1"},
					},
				},
			},
		},
	}
}

// decorator wraps a [core.VMCP] to add the execute_tool_script virtual tool. Every
// method other than ListTools, LookupTool, and CallTool is promoted from the embedded
// inner core unchanged, so the decorator only ever ADDS the virtual tool and otherwise
// defers to inner — it never widens backend reachability, since a script's inner calls
// flow back through inner.CallTool and are admission-checked there.
//
// The decorator is stateless and safe for concurrent use: a fresh [script.Executor] is
// built per execution from the inner core's identity-filtered tool set, so two callers
// never share an engine or a tool binding.
type decorator struct {
	core.VMCP
	cfg Config
}

// NewDecorator wraps inner with vMCP code mode. The returned VMCP advertises
// execute_tool_script in addition to inner's tools and executes submitted Starlark
// scripts when that tool is called. A nil cfg uses defaults.
//
// inner must be non-nil; a nil inner is a composition-root wiring bug and panics rather
// than deferring the failure to the first promoted method call.
func NewDecorator(inner core.VMCP, cfg *Config) core.VMCP {
	if inner == nil {
		panic("codemode: NewDecorator requires a non-nil inner VMCP")
	}
	return &decorator{
		VMCP: inner,
		cfg:  resolve(cfg),
	}
}

// ListTools returns inner's tools plus the execute_tool_script virtual tool. The
// virtual tool's description is generated from inner's (identity-filtered) tools, so
// it lists exactly what the script may call for this identity.
func (d *decorator) ListTools(ctx context.Context, identity *auth.Identity) ([]vmcp.Tool, error) {
	tools, err := d.VMCP.ListTools(ctx, identity)
	if err != nil {
		return nil, err
	}
	// Append onto a fresh slice so we never mutate inner's backing array.
	out := make([]vmcp.Tool, 0, len(tools)+1)
	out = append(out, tools...)
	out = append(out, virtualTool(tools))
	return out, nil
}

// LookupTool resolves execute_tool_script to its virtual definition and defers every
// other name to inner. ListTools advertises the virtual tool, so LookupTool must
// resolve it too — otherwise the advertised set and the validation seam would disagree.
func (d *decorator) LookupTool(ctx context.Context, identity *auth.Identity, name string) (*vmcp.Tool, error) {
	if name != script.ExecuteToolScriptName {
		return d.VMCP.LookupTool(ctx, identity, name)
	}
	tools, err := d.VMCP.ListTools(ctx, identity)
	if err != nil {
		return nil, err
	}
	t := virtualTool(tools)
	return &t, nil
}

// CallTool intercepts execute_tool_script and runs the submitted script; every other
// name is forwarded to inner unchanged. Script-level failures (missing/invalid args,
// step-limit, tool timeout, runtime errors) are returned as an IsError result rather
// than a transport error, so the calling agent sees the message and can correct the
// script.
func (d *decorator) CallTool(
	ctx context.Context, identity *auth.Identity, name string,
	args map[string]any, meta map[string]any,
) (*vmcp.ToolCallResult, error) {
	if name != script.ExecuteToolScriptName {
		return d.VMCP.CallTool(ctx, identity, name, args, meta)
	}

	src, ok := args["script"].(string)
	if !ok || src == "" {
		return errorResult(fmt.Sprintf("%s requires a non-empty 'script' string argument",
			script.ExecuteToolScriptName)), nil
	}
	data, err := extractData(args)
	if err != nil {
		return errorResult(err.Error()), nil
	}

	// Bind the script to the identity's admission-filtered tool set. Each inner call
	// re-enters inner.CallTool, so admission is enforced per call by the real tool name.
	tools, err := d.VMCP.ListTools(ctx, identity)
	if err != nil {
		return nil, fmt.Errorf("codemode: list tools for script execution: %w", err)
	}

	exec := script.New(d.bindTools(identity, tools), d.cfg.scriptConfig())
	result, err := exec.Execute(ctx, src, data)
	if err != nil {
		// The engine already produces descriptive messages (runtime errors, step-limit
		// and tool-timeout failures); surface them verbatim as an IsError result so the
		// calling agent can correct the script rather than seeing a transport error.
		return errorResult(err.Error()), nil
	}
	return fromMCPResult(result), nil
}

// virtualTool builds the execute_tool_script definition whose description enumerates the
// callable tools (everything in innerTools except the virtual tool itself).
func virtualTool(innerTools []vmcp.Tool) vmcp.Tool {
	meta := make([]script.Tool, 0, len(innerTools))
	for _, t := range innerTools {
		if t.Name == script.ExecuteToolScriptName {
			continue
		}
		meta = append(meta, script.Tool{Name: t.Name, Description: t.Description})
	}
	return vmcp.Tool{
		Name:        script.ExecuteToolScriptName,
		Description: script.GenerateToolDescription(meta),
		InputSchema: virtualToolInputSchema(),
	}
}

// bindTools converts the identity's advertised tools into engine-callable [script.Tool]
// values. Each Call closure routes through inner.CallTool with the captured identity, so
// the inner target is admission-checked, and applies the configured per-call timeout (the
// engine itself imposes none — see the [script.Tool].Call contract). The virtual tool is
// never bound, so a script cannot recurse into execute_tool_script.
func (d *decorator) bindTools(identity *auth.Identity, tools []vmcp.Tool) []script.Tool {
	out := make([]script.Tool, 0, len(tools))
	for _, t := range tools {
		if t.Name == script.ExecuteToolScriptName {
			continue
		}
		name := t.Name // capture per iteration for the closure
		out = append(out, script.Tool{
			Name:        name,
			Description: t.Description,
			Call: func(callCtx context.Context, arguments map[string]any) (*mcp.CallToolResult, error) {
				if d.cfg.ToolCallTimeout > 0 {
					var cancel context.CancelFunc
					callCtx, cancel = context.WithTimeout(callCtx, d.cfg.ToolCallTimeout)
					defer cancel()
				}
				// Inner calls carry no client _meta: the script originates them, so there is
				// no protocol metadata to forward.
				res, err := d.VMCP.CallTool(callCtx, identity, name, arguments, nil)
				if err != nil {
					return nil, err
				}
				return toMCPResult(res), nil
			},
		})
	}
	return out
}

// extractData reads the optional "data" argument as a string-keyed map. A nil/absent
// value yields a nil map (no injected variables); any other type is a usage error.
func extractData(args map[string]any) (map[string]any, error) {
	raw, ok := args["data"]
	if !ok || raw == nil {
		return nil, nil
	}
	data, ok := raw.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("'data' argument must be an object, got %T", raw)
	}
	return data, nil
}

// errorResult builds an IsError tool result carrying msg as text content.
func errorResult(msg string) *vmcp.ToolCallResult {
	return &vmcp.ToolCallResult{
		Content: []vmcp.Content{{Type: vmcp.ContentTypeText, Text: msg}},
		IsError: true,
	}
}

// toMCPResult converts a core [vmcp.ToolCallResult] into the mcp-go result the script
// engine consumes for an inner tool call.
//
// StructuredContent is assigned only when non-nil: a nil map[string]any placed in the
// mcp-go `any` field would be a non-nil interface wrapping a typed nil, which the
// engine's result parser treats as present and prefers over the text content — silently
// dropping it. The Serve-path adapter in pkg/vmcp/server/serve_handlers.go can assign it
// unconditionally because its consumer (the SDK JSON encoder, `omitempty`) is not
// sensitive to the typed-nil distinction; this consumer is.
func toMCPResult(r *vmcp.ToolCallResult) *mcp.CallToolResult {
	if r == nil {
		return nil
	}
	out := &mcp.CallToolResult{
		Result:  mcp.Result{Meta: conversion.ToMCPMeta(r.Meta)},
		Content: conversion.ToMCPContents(r.Content),
		IsError: r.IsError,
	}
	if r.StructuredContent != nil {
		out.StructuredContent = r.StructuredContent
	}
	return out
}

// fromMCPResult converts the engine's mcp-go script result back into a core
// [vmcp.ToolCallResult] so it can cross the core.VMCP boundary (no mcp-go types leak out).
func fromMCPResult(r *mcp.CallToolResult) *vmcp.ToolCallResult {
	if r == nil {
		return nil
	}
	out := &vmcp.ToolCallResult{
		Content: conversion.ConvertMCPContents(r.Content),
		IsError: r.IsError,
		Meta:    conversion.FromMCPMeta(r.Meta),
	}
	// Per the MCP spec, structuredContent is "returned as a JSON object", so it maps to
	// map[string]any. A non-object value is a protocol violation; the type assertion
	// intentionally drops it (leaving the text content as the result) rather than
	// propagating malformed structured output. vmcp.ToolCallResult.StructuredContent is
	// itself typed map[string]any, so a non-object could not round-trip regardless.
	if sc, ok := r.StructuredContent.(map[string]any); ok {
		out.StructuredContent = sc
	}
	return out
}

// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package codemode

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/stacklok/toolhive-core/mcpcompat/mcp"
	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/script"
	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/conversion"
	"github.com/stacklok/toolhive/pkg/vmcp/core"
)

// ErrReservedToolName is returned when a backend advertises a tool whose name collides
// with the reserved execute_tool_script virtual tool. It is fail-loud on purpose: a
// silent shadow would let the backend tool skip its own Cedar admission (LookupTool /
// ListTools would resolve the virtual definition instead), so the decorator refuses to
// serve rather than mask the backend tool (#5845).
var ErrReservedToolName = errors.New("codemode: backend tool collides with reserved virtual tool name")

// defaultScriptTimeout bounds the total wall-clock time of a single script execution.
// The step limit caps CPU-equivalent work and Config.ToolCallTimeout caps each inner tool
// call, but neither bounds a script that makes many sequential calls — without this an
// execution could hold a goroutine for (number of calls × ToolCallTimeout). It is a fixed
// safety bound rather than a tunable so an enabled-but-unconfigured deployment is always
// protected; a configurable version can follow if a real workload needs longer.
const defaultScriptTimeout = 1 * time.Minute

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
// Authorization: a script can only reach tools the caller is already permitted to use.
// Tool bindings are built from inner.ListTools (admission-filtered per identity) and every
// inner call is re-authorized by its real name through inner.CallTool, so Cedar policies
// are fully enforced on what a script does. The execute_tool_script meta-tool itself is
// NOT represented in the core admission seam — a Cedar policy cannot allow/deny code mode
// per-principal — so unlike the optimizer's meta-tools (which server.New makes mutually
// exclusive with Authz), code mode is intentionally allowed to coexist with Authz: the
// per-VirtualMCPServer config flag is the grant for the feature, while Cedar remains the
// grant for every tool the feature can call. This is safe precisely because code mode adds
// no reachability beyond the caller's already-authorized tool set.
//
// Because the decorator resolves execute_tool_script to its own virtual definition, a
// backend that advertised a tool of the same name would be silently shadowed and skip
// its Cedar admission. The decorator refuses that collision fail-loud via innerTools:
// ListTools, LookupTool, and the CallTool script-binding path all error with
// ErrReservedToolName rather than serve the shadowed set (#5845).
//
// The decorator is stateless and safe for concurrent use: a fresh [script.Executor] is
// built per execution from the inner core's identity-filtered tool set, so two callers
// never share an engine or a tool binding.
type decorator struct {
	// core.VMCP is embedded, so any method the decorator does not override is promoted
	// straight to the inner core. The reserved-name collision guard (#5845) lives only in
	// the overrides that read the inner tool set via innerTools (ListTools, LookupTool, and
	// CallTool's script path). If core.VMCP ever grows another tool-set-reading method, it
	// will auto-promote and bypass that guard — reintroducing the silent execute_tool_script
	// shadow. Any such new method MUST be overridden here to funnel through innerTools.
	core.VMCP
	cfg Config
}

var _ core.VMCP = (*decorator)(nil)

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

// innerTools lists inner's admission-filtered tools and fails loud if any of them
// collides with the reserved execute_tool_script name. Every decorator path that reads
// inner's tool set goes through here so a shadowing backend tool is rejected once,
// consistently: were it served, ListTools/LookupTool would advertise the virtual
// definition in its place and the backend tool would never reach its own Cedar
// admission (#5845). The defensive skips in virtualTool/bindTools remain as
// belt-and-braces for the same name.
func (d *decorator) innerTools(ctx context.Context, identity *auth.Identity) ([]vmcp.Tool, error) {
	tools, err := d.VMCP.ListTools(ctx, identity)
	if err != nil {
		return nil, err
	}
	for _, t := range tools {
		if t.Name == script.ExecuteToolScriptName {
			return nil, fmt.Errorf("%w: %q advertised by backend %q",
				ErrReservedToolName, script.ExecuteToolScriptName, t.BackendID)
		}
	}
	return tools, nil
}

// ListTools returns inner's tools plus the execute_tool_script virtual tool. The
// virtual tool's description is generated from inner's (identity-filtered) tools, so
// it lists exactly what the script may call for this identity.
func (d *decorator) ListTools(ctx context.Context, identity *auth.Identity) ([]vmcp.Tool, error) {
	tools, err := d.innerTools(ctx, identity)
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
	tools, err := d.innerTools(ctx, identity)
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

	// Record that a script ran, without logging its body (which may carry sensitive
	// arguments). Length + principal give post-incident investigation a foothold; richer
	// auditing is tracked separately under the code mode observability story.
	slog.DebugContext(ctx, "codemode: executing script",
		"principal", principalOf(identity), "script_len", len(src))

	data, err := extractData(args)
	if err != nil {
		return errorResult(err.Error()), nil
	}

	// Bind the script to the identity's admission-filtered tool set. Each inner call
	// re-enters inner.CallTool, so admission is enforced per call by the real tool name.
	// innerTools fails loud on a reserved-name collision, so a shadowing backend tool
	// surfaces as a transport error here rather than silently binding under the wrong
	// admission (#5845).
	tools, err := d.innerTools(ctx, identity)
	if err != nil {
		return nil, fmt.Errorf("codemode: list tools for script execution: %w", err)
	}

	// Bound the total wall-clock time of the whole script. This deadline is inherited by
	// the inner tool calls (they derive their context from execCtx), so it caps both a
	// single slow call and a long sequence of fast ones.
	execCtx, cancel := context.WithTimeout(ctx, defaultScriptTimeout)
	defer cancel()

	exec := script.New(d.bindTools(identity, tools), d.cfg.scriptConfig())
	result, err := exec.Execute(execCtx, src, data)
	if err != nil {
		// The engine already produces descriptive messages (runtime errors, step-limit
		// and tool-timeout failures); surface them verbatim as an IsError result so the
		// calling agent can correct the script rather than seeing a transport error.
		return errorResult(err.Error()), nil
	}
	return fromMCPResult(result), nil
}

// CheckToolCall admits execute_tool_script without consulting inner admission and
// delegates every other name to inner. This mirrors the CallTool override's guard:
// execute_tool_script is NOT represented in the core admission seam (a Cedar policy
// cannot allow/deny code mode per-principal — see the decorator's authorization
// contract), so a promoted CheckToolCall would hit inner, find the name absent from
// the advertised set, and fail the stub-tool admission under any permit-list policy —
// causing a pre-dispatch gate to 403 every code-mode call. Admitting it here is safe
// precisely because the feature flag is the grant and each inner tool call the script
// makes is re-authorized by its real name through inner.CallTool.
func (d *decorator) CheckToolCall(
	ctx context.Context, identity *auth.Identity, name string, args map[string]any,
) error {
	if name == script.ExecuteToolScriptName {
		return nil
	}
	return d.VMCP.CheckToolCall(ctx, identity, name, args)
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
					// Don't echo the denied tool name back into the script result: a generic
					// message keeps code mode from becoming a probe for which tools exist vs.
					// which are denied. (The admission filter already excludes denied tools
					// from the bound set; this guards the narrower allow-list/deny-call gap.)
					if errors.Is(err, vmcp.ErrAuthorizationFailed) {
						return nil, errors.New(vmcp.DenyMessageToolCall)
					}
					return nil, err
				}
				return toMCPResult(res), nil
			},
		})
	}
	return out
}

// principalOf returns a log-safe principal identifier for identity, or "anonymous" when
// no identity is bound. It returns only the subject — never the token or other claims.
func principalOf(identity *auth.Identity) string {
	if identity == nil || identity.Subject == "" {
		return "anonymous"
	}
	return identity.Subject
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

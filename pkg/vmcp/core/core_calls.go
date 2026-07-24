// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"

	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/composer"
	"github.com/stacklok/toolhive/pkg/vmcp/router"
)

// CallTool invokes the named tool. Composite tools (those defined as workflows)
// execute through a per-call composer bound to the freshly aggregated routing
// table; all other names route to a single backend via a session router built
// from the same table. Returns vmcp.ErrNotFound for an unadvertised name and
// vmcp.ErrAuthorizationFailed when admission denies identity the call.
//
// args and meta are treated as read-only and copied before being forwarded
// (go-style: copy before mutating caller input). The admission decision enforces
// the same policy ListTools filters on. identity is never logged. See ListTools
// for the nil/anonymous semantics.
func (c *coreVMCP) CallTool(
	ctx context.Context,
	identity *auth.Identity,
	name string,
	args map[string]any,
	meta map[string]any,
) (*vmcp.ToolCallResult, error) {
	argsCopy := maps.Clone(args)
	metaCopy := maps.Clone(meta)

	agg, err := c.aggregatedView(ctx)
	if err != nil {
		return nil, err
	}

	if err := c.authorizeToolCall(ctx, identity, name, argsCopy, agg); err != nil {
		return nil, err
	}

	// Composite tool: execute only when the workflow is actually advertised in the
	// current view — accessible AND not shadowed by a conflicting backend tool. This
	// uses the same gate as ListTools (accessibleComposites), so advertised equals
	// executed. A name that collides with a backend tool is NOT in the set and falls
	// through to backend routing, matching the legacy decorator.
	if def, ok := c.accessibleComposites(agg)[name]; ok {
		engine := c.composerFactory(agg.RoutingTable, agg.Tools)
		return executeComposite(ctx, engine, def, argsCopy)
	}

	// Backend tool: route through a session router bound to the fresh table. The
	// backend client translates the advertised name to the backend's capability
	// name internally (client.go:772), mirroring the legacy tool handler.
	target, err := router.NewSessionRouter(agg.RoutingTable).RouteTool(ctx, name)
	if err != nil {
		if errors.Is(err, router.ErrToolNotFound) {
			return nil, fmt.Errorf("%w: tool %q", vmcp.ErrNotFound, name)
		}
		return nil, fmt.Errorf("routing tool %q: %w", name, err)
	}
	result, err := c.backendClient.CallTool(ctx, target, name, argsCopy, metaCopy)
	if err != nil {
		return nil, err
	}
	result.BackendID = target.WorkloadID
	return result, nil
}

// ReadResource reads the resource at uri from its backend. Returns
// vmcp.ErrNotFound for an unadvertised URI and vmcp.ErrAuthorizationFailed when
// admission denies identity the read. See ListTools for identity semantics.
func (c *coreVMCP) ReadResource(
	ctx context.Context,
	identity *auth.Identity,
	uri string,
) (*vmcp.ResourceReadResult, error) {
	agg, err := c.aggregatedView(ctx)
	if err != nil {
		return nil, err
	}

	if err := c.authorizeResourceRead(ctx, identity, uri); err != nil {
		return nil, err
	}

	target, err := router.NewSessionRouter(agg.RoutingTable).RouteResource(ctx, uri)
	if err != nil {
		if errors.Is(err, router.ErrResourceNotFound) {
			return nil, fmt.Errorf("%w: resource %q", vmcp.ErrNotFound, uri)
		}
		return nil, fmt.Errorf("routing resource %q: %w", uri, err)
	}
	// Pass the advertised URI; the backend client owns the single translation to
	// the backend's capability name (client.go:874), matching CallTool.
	result, err := c.backendClient.ReadResource(ctx, target, uri)
	if err != nil {
		return nil, err
	}
	result.BackendID = target.WorkloadID
	return result, nil
}

// GetPrompt retrieves the named prompt from its backend. args is treated as
// read-only and copied before being forwarded. Returns vmcp.ErrNotFound for an
// unadvertised name and vmcp.ErrAuthorizationFailed when admission denies identity
// the get. See ListTools for identity semantics.
func (c *coreVMCP) GetPrompt(
	ctx context.Context,
	identity *auth.Identity,
	name string,
	args map[string]any,
) (*vmcp.PromptGetResult, error) {
	agg, err := c.aggregatedView(ctx)
	if err != nil {
		return nil, err
	}

	if err := c.authorizePromptGet(ctx, identity, name); err != nil {
		return nil, err
	}

	target, err := router.NewSessionRouter(agg.RoutingTable).RoutePrompt(ctx, name)
	if err != nil {
		if errors.Is(err, router.ErrPromptNotFound) {
			return nil, fmt.Errorf("%w: prompt %q", vmcp.ErrNotFound, name)
		}
		return nil, fmt.Errorf("routing prompt %q: %w", name, err)
	}
	// Pass the advertised name; the backend client owns the single translation to
	// the backend's capability name (client.go:927), matching CallTool.
	result, err := c.backendClient.GetPrompt(ctx, target, name, maps.Clone(args))
	if err != nil {
		return nil, err
	}
	result.BackendID = target.WorkloadID
	return result, nil
}

// Complete resolves argument-completion candidates for the referenced prompt or
// resource template. It resolves the backend from the freshly aggregated routing
// table (prompts table for a prompt ref, resource-templates table with a concrete
// fallback for a resource ref), admission-checks the referenced capability (the same
// get/read decision GetPrompt/ReadResource enforce), and forwards to the backend.
//
// An unroutable ref returns an empty (non-nil) result rather than an error, matching
// the MCP spec's lenient completion semantics (a client asking for completions on an
// unknown ref should get no candidates, not a protocol error). Admission denial
// returns an error wrapping vmcp.ErrAuthorizationFailed. See ListTools for identity
// semantics; identity is never logged.
func (c *coreVMCP) Complete(
	ctx context.Context,
	identity *auth.Identity,
	ref vmcp.CompletionRef,
	argName, argValue string,
	contextArgs map[string]string,
) (*vmcp.CompletionResult, error) {
	agg, err := c.aggregatedView(ctx)
	if err != nil {
		return nil, err
	}

	sessionRouter := router.NewSessionRouter(agg.RoutingTable)

	switch ref.Type {
	case vmcp.CompletionRefTypePrompt:
		if err := c.authorizePromptGet(ctx, identity, ref.Name); err != nil {
			return nil, err
		}
		target, err := sessionRouter.RoutePrompt(ctx, ref.Name)
		if err != nil {
			if errors.Is(err, router.ErrPromptNotFound) {
				return emptyCompletion(), nil
			}
			return nil, fmt.Errorf("routing prompt %q for completion: %w", ref.Name, err)
		}
		return c.backendClient.Complete(ctx, target, ref, argName, argValue, contextArgs)

	case vmcp.CompletionRefTypeResource:
		if err := c.authorizeResourceRead(ctx, identity, ref.URI); err != nil {
			return nil, err
		}
		// RouteResource matches the URI against concrete resources first, then the
		// resource-template table (the same fallback ReadResource uses).
		target, err := sessionRouter.RouteResource(ctx, ref.URI)
		if err != nil {
			if errors.Is(err, router.ErrResourceNotFound) {
				return emptyCompletion(), nil
			}
			return nil, fmt.Errorf("routing resource %q for completion: %w", ref.URI, err)
		}
		return c.backendClient.Complete(ctx, target, ref, argName, argValue, contextArgs)

	default:
		// Unknown ref type: no candidates, not a hard error (lenient completion).
		slog.Debug("unknown completion ref type, returning empty completion", "ref_type", ref.Type)
		return emptyCompletion(), nil
	}
}

// emptyCompletion returns a non-nil, empty completion result. It is the lenient
// answer for an unroutable or unknown completion ref.
func emptyCompletion() *vmcp.CompletionResult {
	return &vmcp.CompletionResult{Values: []string{}}
}

// executeComposite runs a composite-tool workflow and converts the result to a
// ToolCallResult. Workflow failures are returned as an IsError result (not a
// transport error), mirroring the legacy compositeToolsDecorator
// (internal/compositetools/decorator.go:76-114).
func executeComposite(
	ctx context.Context,
	engine composer.Composer,
	def *composer.WorkflowDefinition,
	params map[string]any,
) (*vmcp.ToolCallResult, error) {
	result, err := engine.ExecuteWorkflow(ctx, def, params)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			slog.Warn("workflow execution timeout", "tool", def.Name, "error", err)
			return compositeErrorResult("Workflow execution timeout exceeded"), nil
		}
		slog.Error("workflow execution failed", "tool", def.Name, "error", err)
		return compositeErrorResult(fmt.Sprintf("Workflow execution failed: %v", err)), nil
	}
	if result == nil {
		slog.Error("workflow executor returned nil result", "tool", def.Name)
		return compositeErrorResult("Workflow executor returned nil result"), nil
	}
	if result.Error != nil {
		slog.Error("workflow completed with error", "tool", def.Name, "error", result.Error)
		return compositeErrorResult(fmt.Sprintf("Workflow error: %v", result.Error)), nil
	}

	jsonBytes, err := json.Marshal(result.Output)
	if err != nil {
		return compositeErrorResult(fmt.Sprintf("failed to marshal output: %v", err)), nil
	}
	return &vmcp.ToolCallResult{
		Content:           []vmcp.Content{{Type: vmcp.ContentTypeText, Text: string(jsonBytes)}},
		StructuredContent: result.Output,
	}, nil
}

// compositeErrorResult builds a tool-level error result for a failed workflow.
func compositeErrorResult(msg string) *vmcp.ToolCallResult {
	return &vmcp.ToolCallResult{
		Content: []vmcp.Content{{Type: vmcp.ContentTypeText, Text: msg}},
		IsError: true,
	}
}

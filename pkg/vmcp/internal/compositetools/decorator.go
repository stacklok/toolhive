// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package compositetools provides a MultiSession decorator that adds composite
// tool (workflow) capabilities to a session.
package compositetools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/vmcp"
	sessiontypes "github.com/stacklok/toolhive/pkg/vmcp/session/types"
)

// WorkflowExecutor executes a named composite tool workflow.
type WorkflowExecutor interface {
	ExecuteWorkflow(ctx context.Context, params map[string]any) (*WorkflowResult, error)
}

// WorkflowResult holds the output of a workflow execution.
type WorkflowResult struct {
	Output map[string]any
	Error  error
}

// compositeToolsDecorator wraps a MultiSession to add composite tool routing.
// It overrides Tools() to append composite tool metadata and CallTool() to
// intercept composite tool names and dispatch them to workflow executors.
// All other MultiSession methods delegate to the embedded session.
type compositeToolsDecorator struct {
	sessiontypes.MultiSession
	compositeTools []vmcp.Tool
	executors      map[string]WorkflowExecutor
}

func errorResult(msg string) *vmcp.ToolCallResult {
	return &vmcp.ToolCallResult{
		Content: []vmcp.Content{{Type: "text", Text: msg}},
		IsError: true,
	}
}

// NewDecorator wraps sess with composite tool support. compositeTools is the
// metadata list appended to session.Tools(). executors maps each composite tool
// name to its workflow executor. Both may be nil/empty.
func NewDecorator(
	sess sessiontypes.MultiSession,
	compositeTools []vmcp.Tool,
	executors map[string]WorkflowExecutor,
) sessiontypes.MultiSession {
	return &compositeToolsDecorator{
		MultiSession:   sess,
		compositeTools: compositeTools,
		executors:      executors,
	}
}

// Tools returns backend tools followed by composite tools.
func (d *compositeToolsDecorator) Tools() []vmcp.Tool {
	backend := d.MultiSession.Tools()
	if len(d.compositeTools) == 0 {
		return backend
	}
	out := make([]vmcp.Tool, len(backend), len(backend)+len(d.compositeTools))
	copy(out, backend)
	return append(out, d.compositeTools...)
}

// CallTool dispatches composite tool names to their workflow executors.
// Unknown names are delegated to the embedded session.
func (d *compositeToolsDecorator) CallTool(
	ctx context.Context,
	caller *auth.Identity,
	toolName string,
	arguments map[string]any,
	meta map[string]any,
) (*vmcp.ToolCallResult, error) {
	exec, ok := d.executors[toolName]
	if !ok {
		return d.MultiSession.CallTool(ctx, caller, toolName, arguments, meta)
	}
	slog.Debug("handling composite tool call", "tool", toolName)
	res, err := exec.ExecuteWorkflow(ctx, arguments)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			slog.Warn("workflow execution timeout", "tool", toolName, "error", err)
			return errorResult("Workflow execution timeout exceeded"), nil
		}
		slog.Error("workflow execution failed", "tool", toolName, "error", err)
		return errorResult(fmt.Sprintf("Workflow execution failed: %v", err)), nil
	}
	if res == nil {
		slog.Error("workflow executor returned nil result", "tool", toolName)
		return errorResult("Workflow executor returned nil result"), nil
	}
	if res.Error != nil {
		slog.Error("workflow completed with error", "tool", toolName, "error", res.Error)
		return errorResult(fmt.Sprintf("Workflow error: %v", res.Error)), nil
	}
	slog.Debug("composite tool completed successfully", "tool", toolName)
	jsonBytes, err := json.Marshal(res.Output)
	if err != nil {
		return errorResult(fmt.Sprintf("failed to marshal output: %v", err)), nil
	}
	return &vmcp.ToolCallResult{
		Content:           []vmcp.Content{{Type: "text", Text: string(jsonBytes)}},
		StructuredContent: res.Output,
	}, nil
}

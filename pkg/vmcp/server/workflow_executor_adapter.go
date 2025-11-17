// Package server implements the Virtual MCP Server that aggregates
// multiple backend MCP servers into a unified interface.
package server

import (
	"context"

	"github.com/stacklok/toolhive/pkg/vmcp/composer"
	"github.com/stacklok/toolhive/pkg/vmcp/server/adapter"
)

// composerWorkflowExecutor adapts a composer.Composer + WorkflowDefinition
// to the adapter.WorkflowExecutor interface.
//
// This adapter enables the handler factory to execute workflows without
// depending on the full composer package. It's a thin wrapper that:
//  1. Holds a reference to the workflow definition
//  2. Delegates execution to the composer
//  3. Transforms composer results to adapter results
//
// Thread-safety: Safe for concurrent use if the underlying composer is thread-safe.
type composerWorkflowExecutor struct {
	composer composer.Composer
	def      *composer.WorkflowDefinition
}

// newComposerWorkflowExecutor creates a workflow executor adapter.
func newComposerWorkflowExecutor(
	c composer.Composer,
	def *composer.WorkflowDefinition,
) adapter.WorkflowExecutor {
	return &composerWorkflowExecutor{
		composer: c,
		def:      def,
	}
}

// ExecuteWorkflow executes the workflow using the composer.
func (e *composerWorkflowExecutor) ExecuteWorkflow(
	ctx context.Context,
	params map[string]any,
) (*adapter.WorkflowResult, error) {
	// Delegate to composer
	result, err := e.composer.ExecuteWorkflow(ctx, e.def, params)
	if err != nil {
		return nil, err
	}

	// Transform composer.WorkflowResult to adapter.WorkflowResult
	return &adapter.WorkflowResult{
		Output: result.Output,
		Error:  result.Error,
	}, nil
}

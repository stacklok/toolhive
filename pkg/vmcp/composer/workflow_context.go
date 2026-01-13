// Package composer provides composite tool workflow execution for Virtual MCP Server.
package composer

import (
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
)

// workflowContextManager manages workflow execution contexts.
type workflowContextManager struct {
	mu       sync.RWMutex
	contexts map[string]*WorkflowContext
}

// newWorkflowContextManager creates a new context manager.
func newWorkflowContextManager() *workflowContextManager {
	return &workflowContextManager{
		contexts: make(map[string]*WorkflowContext),
	}
}

// CreateContext creates a new workflow context with a unique ID.
func (m *workflowContextManager) CreateContext(params map[string]any) *WorkflowContext {
	m.mu.Lock()
	defer m.mu.Unlock()

	workflowID := uuid.New().String()
	startTime := time.Now().UTC()

	ctx := &WorkflowContext{
		WorkflowID: workflowID,
		Params:     params,
		Steps:      make(map[string]*StepResult),
		Variables:  make(map[string]any),
		Workflow: &WorkflowMetadata{
			ID:         workflowID,
			StartTime:  startTime,
			StepCount:  0,
			Status:     WorkflowStatusPending,
			DurationMs: 0,
		},
	}

	m.contexts[ctx.WorkflowID] = ctx
	return ctx
}

// GetContext retrieves a workflow context by ID.
func (m *workflowContextManager) GetContext(workflowID string) (*WorkflowContext, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	ctx, exists := m.contexts[workflowID]
	if !exists {
		return nil, fmt.Errorf("workflow context not found: %s", workflowID)
	}

	return ctx, nil
}

// DeleteContext removes a workflow context.
func (m *workflowContextManager) DeleteContext(workflowID string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	delete(m.contexts, workflowID)
}

// RecordStepStart records that a step has started execution.
// Thread-safe for concurrent step execution.
func (ctx *WorkflowContext) RecordStepStart(stepID string) {
	ctx.mu.Lock()
	defer ctx.mu.Unlock()

	ctx.Steps[stepID] = &StepResult{
		StepID:    stepID,
		Status:    StepStatusRunning,
		StartTime: time.Now(),
	}
}

// RecordStepSuccess records a successful step completion.
// Thread-safe for concurrent step execution.
func (ctx *WorkflowContext) RecordStepSuccess(stepID string, output map[string]any) {
	ctx.mu.Lock()
	defer ctx.mu.Unlock()

	if result, exists := ctx.Steps[stepID]; exists {
		result.Status = StepStatusCompleted
		result.Output = output
		result.EndTime = time.Now()
		result.Duration = result.EndTime.Sub(result.StartTime)
	}
}

// RecordStepFailure records a step failure.
// Thread-safe for concurrent step execution.
func (ctx *WorkflowContext) RecordStepFailure(stepID string, err error) {
	ctx.mu.Lock()
	defer ctx.mu.Unlock()

	if result, exists := ctx.Steps[stepID]; exists {
		result.Status = StepStatusFailed
		result.Error = err
		result.EndTime = time.Now()
		result.Duration = result.EndTime.Sub(result.StartTime)
	}
}

// RecordStepSkipped records that a step was skipped (condition was false).
// If defaultResults is provided, it will be used as the step's output for downstream templates.
// Thread-safe for concurrent step execution.
func (ctx *WorkflowContext) RecordStepSkipped(stepID string, defaultResults map[string]any) {
	ctx.mu.Lock()
	defer ctx.mu.Unlock()

	ctx.Steps[stepID] = &StepResult{
		StepID:    stepID,
		Status:    StepStatusSkipped,
		Output:    defaultResults,
		StartTime: time.Now(),
		EndTime:   time.Now(),
	}
}

// GetStepResult retrieves a step result by ID.
// Thread-safe for concurrent step execution.
func (ctx *WorkflowContext) GetStepResult(stepID string) (*StepResult, bool) {
	ctx.mu.RLock()
	defer ctx.mu.RUnlock()

	result, exists := ctx.Steps[stepID]
	return result, exists
}

// HasStepCompleted checks if a step has completed successfully.
// Thread-safe for concurrent step execution.
func (ctx *WorkflowContext) HasStepCompleted(stepID string) bool {
	ctx.mu.RLock()
	defer ctx.mu.RUnlock()

	result, exists := ctx.Steps[stepID]
	return exists && result.Status == StepStatusCompleted
}

// HasStepFailed checks if a step has failed.
// Thread-safe for concurrent step execution.
func (ctx *WorkflowContext) HasStepFailed(stepID string) bool {
	ctx.mu.RLock()
	defer ctx.mu.RUnlock()

	result, exists := ctx.Steps[stepID]
	return exists && result.Status == StepStatusFailed
}

// GetLastStepOutput retrieves the output of the most recently completed step.
// This is useful for getting the final workflow output.
// Thread-safe for concurrent step execution.
func (ctx *WorkflowContext) GetLastStepOutput() map[string]any {
	ctx.mu.RLock()
	defer ctx.mu.RUnlock()

	var lastTime time.Time
	var lastOutput map[string]any

	for _, result := range ctx.Steps {
		if result.Status == StepStatusCompleted && result.EndTime.After(lastTime) {
			lastTime = result.EndTime
			lastOutput = result.Output
		}
	}

	return lastOutput
}

// Clone creates a shallow copy of the workflow context.
// Maps and step results are cloned, but nested values within maps are shared.
// This is useful for testing and validation.
// Thread-safe: Acquires read lock to safely access Steps during cloning.
func (ctx *WorkflowContext) Clone() *WorkflowContext {
	ctx.mu.RLock()
	defer ctx.mu.RUnlock()

	clone := &WorkflowContext{
		WorkflowID: ctx.WorkflowID,
		Params:     cloneMap(ctx.Params),
		Steps:      make(map[string]*StepResult, len(ctx.Steps)),
		Variables:  cloneMap(ctx.Variables),
	}

	// Clone workflow metadata
	if ctx.Workflow != nil {
		clone.Workflow = &WorkflowMetadata{
			ID:         ctx.Workflow.ID,
			StartTime:  ctx.Workflow.StartTime,
			StepCount:  ctx.Workflow.StepCount,
			Status:     ctx.Workflow.Status,
			DurationMs: ctx.Workflow.DurationMs,
		}
	}

	// Clone step results
	for stepID, result := range ctx.Steps {
		clone.Steps[stepID] = &StepResult{
			StepID:     result.StepID,
			Status:     result.Status,
			Output:     cloneMap(result.Output),
			Error:      result.Error,
			StartTime:  result.StartTime,
			EndTime:    result.EndTime,
			Duration:   result.Duration,
			RetryCount: result.RetryCount,
		}
	}

	return clone
}

// cloneMap creates a shallow copy of a map.
func cloneMap(m map[string]any) map[string]any {
	if m == nil {
		return nil
	}

	clone := make(map[string]any, len(m))
	for k, v := range m {
		clone[k] = v
	}
	return clone
}

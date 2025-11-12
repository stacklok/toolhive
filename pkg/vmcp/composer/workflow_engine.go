// Package composer provides composite tool workflow execution for Virtual MCP Server.
package composer

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/cenkalti/backoff/v5"

	"github.com/stacklok/toolhive/pkg/logger"
	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/router"
)

const (
	// defaultWorkflowTimeout is the default maximum execution time for workflows.
	defaultWorkflowTimeout = 30 * time.Minute

	// defaultStepTimeout is the default maximum execution time for individual steps.
	defaultStepTimeout = 5 * time.Minute

	// maxWorkflowSteps is the maximum number of steps allowed in a workflow.
	// This prevents resource exhaustion from maliciously large workflows.
	maxWorkflowSteps = 100

	// maxRetryCount is the maximum number of retries allowed per step.
	// This prevents infinite retry loops from malicious configurations.
	maxRetryCount = 10
)

// workflowEngine implements Composer interface.
type workflowEngine struct {
	// router routes tool calls to backend servers.
	router router.Router

	// backendClient makes calls to backend MCP servers.
	backendClient vmcp.BackendClient

	// templateExpander handles template expansion.
	templateExpander TemplateExpander

	// contextManager manages workflow execution contexts.
	contextManager *workflowContextManager

	// elicitationHandler handles MCP elicitation protocol for user interaction.
	elicitationHandler ElicitationProtocolHandler
}

// NewWorkflowEngine creates a new workflow execution engine.
//
// The elicitationHandler parameter is optional. If nil, elicitation steps will fail.
// This allows the engine to be used without elicitation support for simple workflows.
func NewWorkflowEngine(
	rtr router.Router,
	backendClient vmcp.BackendClient,
	elicitationHandler ElicitationProtocolHandler,
) Composer {
	return &workflowEngine{
		router:             rtr,
		backendClient:      backendClient,
		templateExpander:   NewTemplateExpander(),
		contextManager:     newWorkflowContextManager(),
		elicitationHandler: elicitationHandler,
	}
}

// ExecuteWorkflow executes a composite tool workflow.
func (e *workflowEngine) ExecuteWorkflow(
	ctx context.Context,
	def *WorkflowDefinition,
	params map[string]any,
) (*WorkflowResult, error) {
	logger.Infof("Starting workflow execution: %s", def.Name)

	// Create workflow context
	workflowCtx := e.contextManager.CreateContext(params)
	defer e.contextManager.DeleteContext(workflowCtx.WorkflowID)

	// Apply workflow timeout
	timeout := def.Timeout
	if timeout == 0 {
		timeout = defaultWorkflowTimeout
	}
	execCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// Create result
	result := &WorkflowResult{
		WorkflowID: workflowCtx.WorkflowID,
		Status:     WorkflowStatusRunning,
		Steps:      make(map[string]*StepResult),
		StartTime:  time.Now(),
		Metadata:   make(map[string]string),
	}

	// Execute workflow steps sequentially
	for _, step := range def.Steps {
		// Check if context was cancelled or timed out
		select {
		case <-execCtx.Done():
			result.Status = WorkflowStatusTimedOut
			result.Error = ErrWorkflowTimeout
			result.EndTime = time.Now()
			result.Duration = result.EndTime.Sub(result.StartTime)
			logger.Warnf("Workflow %s timed out after %v", def.Name, result.Duration)
			return result, ErrWorkflowTimeout
		default:
		}

		// Execute step
		stepErr := e.executeStep(execCtx, &step, workflowCtx, def.FailureMode)

		// Copy step result to workflow result
		if stepResult, exists := workflowCtx.GetStepResult(step.ID); exists {
			result.Steps[step.ID] = stepResult
		}

		// Handle step failure
		if stepErr != nil {
			logger.Errorf("Step %s failed in workflow %s: %v", step.ID, def.Name, stepErr)

			// Check failure mode
			if def.FailureMode == "" || def.FailureMode == "abort" {
				result.Status = WorkflowStatusFailed
				result.Error = NewWorkflowError(workflowCtx.WorkflowID, step.ID, "step failed", stepErr)
				result.EndTime = time.Now()
				result.Duration = result.EndTime.Sub(result.StartTime)
				return result, result.Error
			}

			// For "continue" or "best_effort" modes, log and continue
			logger.Warnf("Continuing workflow %s despite step %s failure (mode: %s)",
				def.Name, step.ID, def.FailureMode)
		}
	}

	// Workflow completed successfully
	result.Status = WorkflowStatusCompleted
	result.Output = workflowCtx.GetLastStepOutput()
	result.EndTime = time.Now()
	result.Duration = result.EndTime.Sub(result.StartTime)

	logger.Infof("Workflow %s completed successfully in %v", def.Name, result.Duration)
	return result, nil
}

// executeStep executes a single workflow step.
func (e *workflowEngine) executeStep(
	ctx context.Context,
	step *WorkflowStep,
	workflowCtx *WorkflowContext,
	_ string, // failureMode is handled at workflow level
) error {
	logger.Debugf("Executing step: %s (type: %s)", step.ID, step.Type)

	// Record step start
	workflowCtx.RecordStepStart(step.ID)

	// Apply step timeout
	timeout := step.Timeout
	if timeout == 0 {
		timeout = defaultStepTimeout
	}
	stepCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// Check dependencies
	for _, depID := range step.DependsOn {
		if !workflowCtx.HasStepCompleted(depID) {
			err := fmt.Errorf("%w: step %s depends on %s which hasn't completed",
				ErrDependencyNotMet, step.ID, depID)
			workflowCtx.RecordStepFailure(step.ID, err)
			return err
		}
	}

	// Evaluate condition
	if step.Condition != "" {
		shouldExecute, err := e.templateExpander.EvaluateCondition(ctx, step.Condition, workflowCtx)
		if err != nil {
			condErr := fmt.Errorf("%w: failed to evaluate condition for step %s: %v",
				ErrTemplateExpansion, step.ID, err)
			workflowCtx.RecordStepFailure(step.ID, condErr)
			return condErr
		}
		if !shouldExecute {
			logger.Debugf("Step %s skipped due to condition", step.ID)
			workflowCtx.RecordStepSkipped(step.ID)
			return nil
		}
	}

	// Execute based on step type
	switch step.Type {
	case StepTypeTool:
		return e.executeToolStep(stepCtx, step, workflowCtx)
	case StepTypeElicitation:
		return e.executeElicitationStep(stepCtx, step, workflowCtx)
	case StepTypeConditional:
		// Conditional steps are not implemented in Phase 2
		err := fmt.Errorf("conditional steps are not yet supported")
		workflowCtx.RecordStepFailure(step.ID, err)
		return err
	default:
		err := fmt.Errorf("unsupported step type: %s", step.Type)
		workflowCtx.RecordStepFailure(step.ID, err)
		return err
	}
}

// executeToolStep executes a tool step.
func (e *workflowEngine) executeToolStep(
	ctx context.Context,
	step *WorkflowStep,
	workflowCtx *WorkflowContext,
) error {
	logger.Debugf("Executing tool step: %s, tool: %s", step.ID, step.Tool)

	// Expand template arguments
	expandedArgs, err := e.templateExpander.Expand(ctx, step.Arguments, workflowCtx)
	if err != nil {
		expandErr := fmt.Errorf("%w: failed to expand arguments for step %s: %v",
			ErrTemplateExpansion, step.ID, err)
		workflowCtx.RecordStepFailure(step.ID, expandErr)
		return expandErr
	}

	// Route tool to backend
	target, err := e.router.RouteTool(ctx, step.Tool)
	if err != nil {
		routeErr := fmt.Errorf("failed to route tool %s in step %s: %w",
			step.Tool, step.ID, err)
		workflowCtx.RecordStepFailure(step.ID, routeErr)
		return routeErr
	}

	// Call tool with retry logic
	output, retryCount, err := e.callToolWithRetry(ctx, target, step, expandedArgs, workflowCtx)

	// Handle result
	if err != nil {
		return e.handleToolStepFailure(step, workflowCtx, retryCount, err)
	}

	return e.handleToolStepSuccess(step, workflowCtx, output, retryCount)
}

// callToolWithRetry calls a tool with retry logic using exponential backoff.
func (e *workflowEngine) callToolWithRetry(
	ctx context.Context,
	target *vmcp.BackendTarget,
	step *WorkflowStep,
	args map[string]any,
	_ *WorkflowContext,
) (map[string]any, int, error) {
	maxRetries, initialDelay := e.getRetryConfig(step)

	// Configure exponential backoff
	expBackoff := backoff.NewExponentialBackOff()
	expBackoff.InitialInterval = initialDelay
	expBackoff.MaxInterval = 60 * initialDelay // Cap at 60x the initial delay
	expBackoff.Reset()

	attemptCount := 0
	operation := func() (map[string]any, error) {
		attemptCount++
		output, err := e.backendClient.CallTool(ctx, target, step.Tool, args)
		if err != nil {
			logger.Warnf("Tool call failed for step %s (attempt %d/%d): %v",
				step.ID, attemptCount, maxRetries+1, err)
			return nil, err
		}
		return output, nil
	}

	// Execute with retry
	// Safe conversion: maxRetries is capped by maxRetryCount constant (10)
	output, err := backoff.Retry(ctx, operation,
		backoff.WithBackOff(expBackoff),
		backoff.WithMaxTries(uint(maxRetries+1)), // #nosec G115 -- +1 because it includes the initial attempt
		backoff.WithNotify(func(_ error, duration time.Duration) {
			logger.Debugf("Retrying step %s after %v", step.ID, duration)
		}),
	)

	return output, attemptCount - 1, err // Return retry count (attempts - 1)
}

// getRetryConfig extracts retry configuration from step.
func (*workflowEngine) getRetryConfig(step *WorkflowStep) (int, time.Duration) {
	retries := 0
	retryDelay := time.Second

	if step.OnError != nil && step.OnError.Action == "retry" {
		retries = step.OnError.RetryCount

		// Cap retry count to prevent infinite retry loops
		if retries > maxRetryCount {
			logger.Warnf("Step %s retry count %d exceeds maximum %d, capping to %d",
				step.ID, retries, maxRetryCount, maxRetryCount)
			retries = maxRetryCount
		}

		if step.OnError.RetryDelay > 0 {
			retryDelay = step.OnError.RetryDelay
		}
	}

	return retries, retryDelay
}

// handleToolStepFailure handles a failed tool step.
func (*workflowEngine) handleToolStepFailure(
	step *WorkflowStep,
	workflowCtx *WorkflowContext,
	retryCount int,
	err error,
) error {
	finalErr := fmt.Errorf("%w: tool %s in step %s: %v",
		ErrToolCallFailed, step.Tool, step.ID, err)
	workflowCtx.RecordStepFailure(step.ID, finalErr)

	// Update retry count
	if result, exists := workflowCtx.GetStepResult(step.ID); exists {
		result.RetryCount = retryCount
	}

	// Check if we should continue on error
	if step.OnError != nil && step.OnError.ContinueOnError {
		logger.Warnf("Continuing workflow despite step %s failure (continue_on_error=true)", step.ID)
		return nil
	}

	return finalErr
}

// handleToolStepSuccess handles a successful tool step.
func (*workflowEngine) handleToolStepSuccess(
	step *WorkflowStep,
	workflowCtx *WorkflowContext,
	output map[string]any,
	retryCount int,
) error {
	workflowCtx.RecordStepSuccess(step.ID, output)

	// Update retry count
	if result, exists := workflowCtx.GetStepResult(step.ID); exists {
		result.RetryCount = retryCount
	}

	logger.Debugf("Step %s completed successfully", step.ID)
	return nil
}

// executeElicitationStep executes an elicitation step.
// Per MCP 2025-06-18: SDK handles JSON-RPC ID correlation, we provide validation and error handling.
func (e *workflowEngine) executeElicitationStep(
	ctx context.Context,
	step *WorkflowStep,
	workflowCtx *WorkflowContext,
) error {
	logger.Debugf("Executing elicitation step: %s", step.ID)

	// Check if elicitation handler is configured
	if e.elicitationHandler == nil {
		err := fmt.Errorf("elicitation handler not configured for step %s", step.ID)
		workflowCtx.RecordStepFailure(step.ID, err)
		return err
	}

	// Validate elicitation config
	if step.Elicitation == nil {
		err := fmt.Errorf("elicitation config is missing for step %s", step.ID)
		workflowCtx.RecordStepFailure(step.ID, err)
		return err
	}

	// Request elicitation (synchronous - blocks until response or timeout)
	// Per MCP 2025-06-18: SDK handles JSON-RPC ID correlation internally
	response, err := e.elicitationHandler.RequestElicitation(ctx, workflowCtx.WorkflowID, step.ID, step.Elicitation)
	if err != nil {
		// Handle timeout
		if errors.Is(err, ErrElicitationTimeout) {
			return e.handleElicitationTimeout(step, workflowCtx)
		}

		// Handle other errors
		requestErr := fmt.Errorf("elicitation request failed for step %s: %w", step.ID, err)
		workflowCtx.RecordStepFailure(step.ID, requestErr)
		return requestErr
	}

	// Handle response based on action
	switch response.Action {
	case elicitationActionAccept:
		return e.handleElicitationAccept(step, workflowCtx, response)
	case elicitationActionDecline:
		return e.handleElicitationDecline(step, workflowCtx)
	case elicitationActionCancel:
		return e.handleElicitationCancel(step, workflowCtx)
	default:
		err := fmt.Errorf("invalid elicitation response action %q for step %s", response.Action, step.ID)
		workflowCtx.RecordStepFailure(step.ID, err)
		return err
	}
}

// handleElicitationAccept handles when the user accepts and provides data.
func (*workflowEngine) handleElicitationAccept(
	step *WorkflowStep,
	workflowCtx *WorkflowContext,
	response *ElicitationResponse,
) error {
	logger.Debugf("User accepted elicitation for step %s", step.ID)

	// Store both the content and action in step output
	// This allows templates to access:
	//   - {{.steps.stepid.output.content}} for the data
	//   - {{.steps.stepid.output.action}} for the action
	output := map[string]any{
		"action":  response.Action,
		"content": response.Content,
	}

	workflowCtx.RecordStepSuccess(step.ID, output)
	logger.Debugf("Step %s completed with user-provided data", step.ID)
	return nil
}

// handleElicitationDecline handles when the user explicitly declines.
func (e *workflowEngine) handleElicitationDecline(
	step *WorkflowStep,
	workflowCtx *WorkflowContext,
) error {
	logger.Debugf("User declined elicitation for step %s", step.ID)

	// Check if we have an OnDecline handler
	if step.Elicitation != nil && step.Elicitation.OnDecline != nil {
		return e.handleElicitationAction(step, workflowCtx, step.Elicitation.OnDecline.Action, "decline")
	}

	// Default: treat as error
	err := fmt.Errorf("%w: step %s", ErrElicitationDeclined, step.ID)
	workflowCtx.RecordStepFailure(step.ID, err)
	return err
}

// handleElicitationCancel handles when the user cancels/dismisses.
func (e *workflowEngine) handleElicitationCancel(
	step *WorkflowStep,
	workflowCtx *WorkflowContext,
) error {
	logger.Debugf("User cancelled elicitation for step %s", step.ID)

	// Check if we have an OnCancel handler
	if step.Elicitation != nil && step.Elicitation.OnCancel != nil {
		return e.handleElicitationAction(step, workflowCtx, step.Elicitation.OnCancel.Action, "cancel")
	}

	// Default: treat as error
	err := fmt.Errorf("%w: step %s", ErrElicitationCancelled, step.ID)
	workflowCtx.RecordStepFailure(step.ID, err)
	return err
}

// handleElicitationTimeout handles when the elicitation times out.
func (e *workflowEngine) handleElicitationTimeout(
	step *WorkflowStep,
	workflowCtx *WorkflowContext,
) error {
	logger.Warnf("Elicitation timed out for step %s", step.ID)

	// Timeout is treated as cancel by default
	if step.Elicitation != nil && step.Elicitation.OnCancel != nil {
		return e.handleElicitationAction(step, workflowCtx, step.Elicitation.OnCancel.Action, "timeout")
	}

	// Default: treat as error
	err := fmt.Errorf("%w: step %s", ErrElicitationTimeout, step.ID)
	workflowCtx.RecordStepFailure(step.ID, err)
	return err
}

// handleElicitationAction handles elicitation response actions (decline/cancel).
func (*workflowEngine) handleElicitationAction(
	step *WorkflowStep,
	workflowCtx *WorkflowContext,
	action string,
	reason string,
) error {
	switch action {
	case "skip_remaining":
		// Mark this step as skipped and signal to skip remaining steps
		logger.Debugf("Skipping remaining steps after %s for step %s", reason, step.ID)
		output := map[string]any{
			"action":  reason,
			"skipped": true,
		}
		workflowCtx.RecordStepSuccess(step.ID, output)
		// Return a special error that the workflow engine can detect
		// For now, we'll just complete the step successfully
		return nil

	case "abort":
		// Abort the workflow
		logger.Debugf("Aborting workflow after %s for step %s", reason, step.ID)
		if reason == "decline" {
			err := fmt.Errorf("%w: step %s", ErrElicitationDeclined, step.ID)
			workflowCtx.RecordStepFailure(step.ID, err)
			return err
		}
		err := fmt.Errorf("%w: step %s", ErrElicitationCancelled, step.ID)
		workflowCtx.RecordStepFailure(step.ID, err)
		return err

	case "continue":
		// Continue to next step
		logger.Debugf("Continuing workflow after %s for step %s", reason, step.ID)
		output := map[string]any{
			"action": reason,
		}
		workflowCtx.RecordStepSuccess(step.ID, output)
		return nil

	default:
		err := fmt.Errorf("invalid elicitation action: %s", action)
		workflowCtx.RecordStepFailure(step.ID, err)
		return err
	}
}

// ValidateWorkflow checks if a workflow definition is valid.
func (e *workflowEngine) ValidateWorkflow(_ context.Context, def *WorkflowDefinition) error {
	if def == nil {
		return NewValidationError("workflow", "workflow definition is nil", nil)
	}

	// Validate name
	if def.Name == "" {
		return NewValidationError("name", "workflow name is required", nil)
	}

	// Validate steps
	if len(def.Steps) == 0 {
		return NewValidationError("steps", "workflow must have at least one step", nil)
	}

	// Enforce maximum steps limit to prevent resource exhaustion
	if len(def.Steps) > maxWorkflowSteps {
		return NewValidationError("steps",
			fmt.Sprintf("too many steps: %d (max %d)", len(def.Steps), maxWorkflowSteps),
			nil)
	}

	// Check for duplicate step IDs
	stepIDs := make(map[string]bool)
	for _, step := range def.Steps {
		if step.ID == "" {
			return NewValidationError("step.id", "step ID is required", nil)
		}
		if stepIDs[step.ID] {
			return NewValidationError("step.id",
				fmt.Sprintf("duplicate step ID: %s", step.ID), nil)
		}
		stepIDs[step.ID] = true
	}

	// Validate dependencies and detect cycles
	if err := e.validateDependencies(def.Steps); err != nil {
		return err
	}

	// Validate step types and configurations
	for _, step := range def.Steps {
		if err := e.validateStep(&step, stepIDs); err != nil {
			return err
		}
	}

	return nil
}

// validateDependencies checks for circular dependencies using DFS.
func (*workflowEngine) validateDependencies(steps []WorkflowStep) error {
	// Build adjacency list
	graph := make(map[string][]string)
	for i := range steps {
		graph[steps[i].ID] = steps[i].DependsOn
	}

	// Track visited and recursion stack
	visited := make(map[string]bool)
	recStack := make(map[string]bool)

	// DFS to detect cycles
	var hasCycle func(string) bool
	hasCycle = func(nodeID string) bool {
		visited[nodeID] = true
		recStack[nodeID] = true

		for _, depID := range graph[nodeID] {
			if !visited[depID] {
				if hasCycle(depID) {
					return true
				}
			} else if recStack[depID] {
				return true
			}
		}

		recStack[nodeID] = false
		return false
	}

	// Check each step
	for i := range steps {
		if !visited[steps[i].ID] {
			if hasCycle(steps[i].ID) {
				return NewValidationError("dependencies",
					fmt.Sprintf("circular dependency detected involving step %s", steps[i].ID),
					ErrCircularDependency)
			}
		}
	}

	// Validate dependency references
	for i := range steps {
		for _, depID := range steps[i].DependsOn {
			if !visited[depID] {
				return NewValidationError("dependencies",
					fmt.Sprintf("step %s depends on non-existent step %s", steps[i].ID, depID),
					nil)
			}
		}
	}

	return nil
}

// validateStep validates a single step configuration.
func (*workflowEngine) validateStep(step *WorkflowStep, validStepIDs map[string]bool) error {
	// Validate step type
	switch step.Type {
	case StepTypeTool:
		if step.Tool == "" {
			return NewValidationError("step.tool",
				fmt.Sprintf("tool name is required for tool step %s", step.ID),
				nil)
		}
	case StepTypeElicitation:
		if step.Elicitation == nil {
			return NewValidationError("step.elicitation",
				fmt.Sprintf("elicitation config is required for elicitation step %s", step.ID),
				nil)
		}
		if step.Elicitation.Message == "" {
			return NewValidationError("step.elicitation.message",
				fmt.Sprintf("elicitation message is required for step %s", step.ID),
				nil)
		}
	case StepTypeConditional:
		// Future: validate conditional step
		return NewValidationError("step.type",
			fmt.Sprintf("conditional steps are not yet supported (step %s)", step.ID),
			nil)
	default:
		return NewValidationError("step.type",
			fmt.Sprintf("invalid step type %q for step %s", step.Type, step.ID),
			nil)
	}

	// Validate dependencies exist
	for _, depID := range step.DependsOn {
		if !validStepIDs[depID] {
			return NewValidationError("step.depends_on",
				fmt.Sprintf("step %s depends on non-existent step %s", step.ID, depID),
				nil)
		}
	}

	return nil
}

// GetWorkflowStatus returns the current status of a running workflow.
// For Phase 2 (basic workflow engine), this is a placeholder.
func (*workflowEngine) GetWorkflowStatus(_ context.Context, _ string) (*WorkflowStatus, error) {
	// In Phase 2, we don't track long-running workflows
	// This will be implemented in Phase 3 with persistent state
	return nil, fmt.Errorf("workflow status tracking not yet implemented")
}

// CancelWorkflow cancels a running workflow.
// For Phase 2 (basic workflow engine), this is a placeholder.
func (*workflowEngine) CancelWorkflow(_ context.Context, _ string) error {
	// In Phase 2, workflows run synchronously and blocking
	// Cancellation will be implemented in Phase 3
	return fmt.Errorf("workflow cancellation not yet implemented")
}

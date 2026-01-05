// Package composer provides composite tool workflow execution for Virtual MCP Server.
package composer

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"time"

	"github.com/cenkalti/backoff/v5"

	"github.com/stacklok/toolhive/pkg/audit"
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

	// dagExecutor handles DAG-based parallel execution.
	dagExecutor *dagExecutor

	// stateStore manages workflow state persistence.
	stateStore WorkflowStateStore

	// auditor provides audit logging for workflow execution (optional).
	auditor *audit.WorkflowAuditor
}

// NewWorkflowEngine creates a new workflow execution engine.
//
// The elicitationHandler parameter is optional. If nil, elicitation steps will fail.
// This allows the engine to be used without elicitation support for simple workflows.
//
// The stateStore parameter is optional. If nil, workflow status tracking and cancellation
// will not be available. Use NewInMemoryStateStore() for basic state tracking.
//
// The auditor parameter is optional. If nil, workflow execution will not be audited.
func NewWorkflowEngine(
	rtr router.Router,
	backendClient vmcp.BackendClient,
	elicitationHandler ElicitationProtocolHandler,
	stateStore WorkflowStateStore,
	auditor *audit.WorkflowAuditor,
) Composer {
	return &workflowEngine{
		router:             rtr,
		backendClient:      backendClient,
		templateExpander:   NewTemplateExpander(),
		contextManager:     newWorkflowContextManager(),
		elicitationHandler: elicitationHandler,
		dagExecutor:        newDAGExecutor(defaultMaxParallelSteps),
		stateStore:         stateStore,
		auditor:            auditor,
	}
}

// ExecuteWorkflow executes a composite tool workflow.
//
// TODO(rate-limiting): Add rate limiting per user/session to prevent workflow execution DoS.
// Consider implementing:
//   - Max concurrent workflows per user (e.g., 10)
//   - Max workflow executions per time window (e.g., 100/minute)
//   - Exponential backoff for repeated failures
//
// See security review: VMCP_COMPOSITE_WORKFLOW_SECURITY_REVIEW.md (M-4)
func (e *workflowEngine) ExecuteWorkflow(
	ctx context.Context,
	def *WorkflowDefinition,
	params map[string]any,
) (*WorkflowResult, error) {
	logger.Infof("Starting workflow execution: %s", def.Name)

	// Apply parameter defaults from JSON Schema before execution
	paramsWithDefaults := applyParameterDefaults(def.Parameters, params)

	// Create workflow context
	workflowCtx := e.contextManager.CreateContext(paramsWithDefaults)
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

	// Audit workflow start
	e.auditWorkflowStart(ctx, workflowCtx.WorkflowID, def.Name, paramsWithDefaults, timeout)

	// Save initial workflow state
	if e.stateStore != nil {
		initialState := &WorkflowStatus{
			WorkflowID:          workflowCtx.WorkflowID,
			Status:              WorkflowStatusRunning,
			CurrentStep:         "",
			CompletedSteps:      []string{},
			PendingElicitations: []*PendingElicitation{},
			StartTime:           result.StartTime,
			LastUpdateTime:      result.StartTime,
		}
		if err := e.stateStore.SaveState(execCtx, workflowCtx.WorkflowID, initialState); err != nil {
			logger.Warnf("Failed to save initial workflow state: %v", err)
		}
	}

	// Execute workflow steps using DAG-based parallel execution
	// The DAG executor will:
	//  1. Build execution levels based on dependencies
	//  2. Execute independent steps in parallel
	//  3. Wait for dependencies before executing dependent steps
	stepExecutor := func(ctx context.Context, step *WorkflowStep) error {
		// Check if context was cancelled or timed out
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		// Execute step
		return e.executeStep(ctx, step, workflowCtx, def.FailureMode)
	}

	// Execute DAG
	dagErr := e.dagExecutor.executeDAG(execCtx, def.Steps, stepExecutor, def.FailureMode)

	// Copy step results to workflow result
	// Acquire read lock to safely copy Steps map
	workflowCtx.mu.RLock()
	maps.Copy(result.Steps, workflowCtx.Steps)
	workflowCtx.mu.RUnlock()

	// Handle execution failure
	if dagErr != nil {
		logger.Errorf("Workflow %s failed: %v", def.Name, dagErr)

		// Check if it was a timeout
		if errors.Is(execCtx.Err(), context.DeadlineExceeded) {
			result.Status = WorkflowStatusTimedOut
			result.Error = ErrWorkflowTimeout
			result.EndTime = time.Now()
			result.Duration = result.EndTime.Sub(result.StartTime)

			// Audit workflow timeout
			e.auditWorkflowTimeout(ctx, workflowCtx.WorkflowID, def.Name, result.Duration, len(result.Steps))

			// Save timeout state
			if e.stateStore != nil {
				finalState := e.buildWorkflowStatus(workflowCtx, WorkflowStatusTimedOut)
				finalState.StartTime = result.StartTime
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				_ = e.stateStore.SaveState(ctx, workflowCtx.WorkflowID, finalState)
			}

			logger.Warnf("Workflow %s timed out after %v", def.Name, result.Duration)
			return result, ErrWorkflowTimeout
		}

		// Otherwise it's a failure
		result.Status = WorkflowStatusFailed
		result.Error = dagErr
		result.EndTime = time.Now()
		result.Duration = result.EndTime.Sub(result.StartTime)

		// Audit workflow failure
		e.auditWorkflowFailure(ctx, workflowCtx.WorkflowID, def.Name, result.Duration, len(result.Steps), dagErr)

		// Save failure state
		if e.stateStore != nil {
			finalState := e.buildWorkflowStatus(workflowCtx, WorkflowStatusFailed)
			finalState.StartTime = result.StartTime
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = e.stateStore.SaveState(ctx, workflowCtx.WorkflowID, finalState)
		}

		return result, result.Error
	}

	// Workflow completed successfully
	result.Status = WorkflowStatusCompleted

	// Update workflow metadata before output construction
	// This ensures {{.workflow.*}} template variables are available with accurate values
	e.updateWorkflowMetadata(workflowCtx, result.StartTime, WorkflowStatusCompleted)

	// Construct output based on configuration
	if def.Output == nil {
		// Backward compatible: return last step output
		result.Output = workflowCtx.GetLastStepOutput()
	} else {
		// Construct output from schema
		constructedOutput, err := e.constructOutputFromConfig(ctx, def.Output, workflowCtx)
		if err != nil {
			result.Status = WorkflowStatusFailed
			result.Error = fmt.Errorf("output construction failed: %w", err)
			result.EndTime = time.Now()
			result.Duration = result.EndTime.Sub(result.StartTime)

			// Audit workflow failure
			e.auditWorkflowFailure(ctx, workflowCtx.WorkflowID, def.Name, result.Duration, len(result.Steps), result.Error)

			// Save failure state
			if e.stateStore != nil {
				finalState := e.buildWorkflowStatus(workflowCtx, WorkflowStatusFailed)
				finalState.StartTime = result.StartTime
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				_ = e.stateStore.SaveState(ctx, workflowCtx.WorkflowID, finalState)
			}

			logger.Errorf("Workflow %s failed during output construction: %v", def.Name, err)
			return result, result.Error
		}
		result.Output = constructedOutput
	}

	result.EndTime = time.Now()
	result.Duration = result.EndTime.Sub(result.StartTime)

	// Audit workflow completion
	e.auditWorkflowCompletion(ctx, workflowCtx.WorkflowID, def.Name, result.Duration, len(result.Steps), result.Output)

	// Save final workflow state
	if e.stateStore != nil {
		finalState := e.buildWorkflowStatus(workflowCtx, WorkflowStatusCompleted)
		finalState.StartTime = result.StartTime
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := e.stateStore.SaveState(ctx, workflowCtx.WorkflowID, finalState); err != nil {
			logger.Warnf("Failed to save final workflow state: %v", err)
		}
	}

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

	// Record step start time for audit logging
	stepStartTime := time.Now()

	// Record step start
	workflowCtx.RecordStepStart(step.ID)

	// Audit step start
	toolName := ""
	if step.Type == StepTypeTool {
		toolName = step.Tool
	}
	e.auditStepStart(ctx, workflowCtx.WorkflowID, step.ID, string(step.Type), toolName)

	// Apply step timeout
	timeout := step.Timeout
	if timeout == 0 {
		timeout = defaultStepTimeout
	}
	stepCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// Note: Dependency checking is handled by the DAG executor.
	// By the time we reach here, all dependencies are guaranteed to have completed.

	// Evaluate condition
	if step.Condition != "" {
		shouldExecute, err := e.templateExpander.EvaluateCondition(ctx, step.Condition, workflowCtx)
		if err != nil {
			condErr := fmt.Errorf("%w: failed to evaluate condition for step %s: %v",
				ErrTemplateExpansion, step.ID, err)
			workflowCtx.RecordStepFailure(step.ID, condErr)

			// Audit step failure
			e.auditStepFailure(ctx, workflowCtx.WorkflowID, step.ID, time.Since(stepStartTime), 0, condErr)

			return condErr
		}
		if !shouldExecute {
			logger.Debugf("Step %s skipped due to condition", step.ID)
			workflowCtx.RecordStepSkipped(step.ID, step.DefaultResults)

			// Audit step skipped
			e.auditStepSkipped(ctx, workflowCtx.WorkflowID, step.ID, step.Condition)

			return nil
		}
	}

	// Execute based on step type
	var err error
	switch step.Type {
	case StepTypeTool:
		err = e.executeToolStep(stepCtx, step, workflowCtx)
	case StepTypeElicitation:
		err = e.executeElicitationStep(stepCtx, step, workflowCtx)
	default:
		err = fmt.Errorf("unsupported step type: %s", step.Type)
		workflowCtx.RecordStepFailure(step.ID, err)

		// Audit step failure
		e.auditStepFailure(ctx, workflowCtx.WorkflowID, step.ID, time.Since(stepStartTime), 0, err)

		return err
	}

	// Audit step completion or failure
	duration := time.Since(stepStartTime)
	retryCount := 0
	if result, exists := workflowCtx.GetStepResult(step.ID); exists {
		retryCount = result.RetryCount
	}

	if err != nil {
		e.auditStepFailure(ctx, workflowCtx.WorkflowID, step.ID, duration, retryCount, err)
	} else {
		e.auditStepCompletion(ctx, workflowCtx.WorkflowID, step.ID, duration, retryCount)
	}

	return err
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
		if result, exists := workflowCtx.GetStepResult(step.ID); exists && step.DefaultResults != nil {
			result.Output = step.DefaultResults
		}
		return nil
	}

	return finalErr
}

// handleToolStepSuccess handles a successful tool step.
func (e *workflowEngine) handleToolStepSuccess(
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

	// Checkpoint workflow state
	e.checkpointWorkflowState(workflowCtx)

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

// buildWorkflowStatus creates a WorkflowStatus from the current workflow context.
func (*workflowEngine) buildWorkflowStatus(workflowCtx *WorkflowContext, status WorkflowStatusType) *WorkflowStatus {
	workflowCtx.mu.RLock()
	defer workflowCtx.mu.RUnlock()

	// Build list of completed steps
	completedSteps := make([]string, 0, len(workflowCtx.Steps))
	for stepID, result := range workflowCtx.Steps {
		if result.Status == StepStatusCompleted {
			completedSteps = append(completedSteps, stepID)
		}
	}

	return &WorkflowStatus{
		WorkflowID:          workflowCtx.WorkflowID,
		Status:              status,
		CurrentStep:         "",
		CompletedSteps:      completedSteps,
		PendingElicitations: []*PendingElicitation{},
		StartTime:           time.Now(),
		LastUpdateTime:      time.Now(),
	}
}

// checkpointWorkflowState saves the current workflow state to the state store.
func (e *workflowEngine) checkpointWorkflowState(workflowCtx *WorkflowContext) {
	if e.stateStore == nil {
		return
	}

	// Build workflow status
	state := e.buildWorkflowStatus(workflowCtx, WorkflowStatusRunning)

	// Save state (use background context to avoid cancellation issues)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := e.stateStore.SaveState(ctx, workflowCtx.WorkflowID, state); err != nil {
		logger.Warnf("Failed to checkpoint workflow state for %s: %v", workflowCtx.WorkflowID, err)
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

	// Validate output configuration if present
	if def.Output != nil {
		if err := ValidateOutputConfig(def.Output); err != nil {
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
func (e *workflowEngine) GetWorkflowStatus(ctx context.Context, workflowID string) (*WorkflowStatus, error) {
	if e.stateStore == nil {
		return nil, fmt.Errorf("workflow status tracking not available: state store not configured")
	}

	if workflowID == "" {
		return nil, fmt.Errorf("workflow ID is required")
	}

	status, err := e.stateStore.LoadState(ctx, workflowID)
	if err != nil {
		return nil, fmt.Errorf("failed to load workflow status: %w", err)
	}

	return status, nil
}

// CancelWorkflow cancels a running workflow.
// Note: This method marks the workflow as cancelled in the state store.
// For synchronous ExecuteWorkflow calls, cancellation must be done via context cancellation.
// This method is primarily for future async workflow support.
func (e *workflowEngine) CancelWorkflow(ctx context.Context, workflowID string) error {
	if e.stateStore == nil {
		return fmt.Errorf("workflow cancellation not available: state store not configured")
	}

	if workflowID == "" {
		return fmt.Errorf("workflow ID is required")
	}

	// Load current state
	status, err := e.stateStore.LoadState(ctx, workflowID)
	if err != nil {
		return fmt.Errorf("failed to load workflow status: %w", err)
	}

	// Check if workflow is in a cancellable state
	if status.Status == WorkflowStatusCompleted ||
		status.Status == WorkflowStatusFailed ||
		status.Status == WorkflowStatusCancelled ||
		status.Status == WorkflowStatusTimedOut {
		return fmt.Errorf("workflow %s is already in terminal state: %s", workflowID, status.Status)
	}

	// Mark as cancelled
	status.Status = WorkflowStatusCancelled
	status.LastUpdateTime = time.Now()

	if err := e.stateStore.SaveState(ctx, workflowID, status); err != nil {
		return fmt.Errorf("failed to save cancelled state: %w", err)
	}

	logger.Infof("Workflow %s marked as cancelled", workflowID)
	return nil
}

// updateWorkflowMetadata updates the workflow metadata with current execution state.
// This should be called before output construction to ensure template variables
// like {{.workflow.duration_ms}} and {{.workflow.step_count}} have accurate values.
func (*workflowEngine) updateWorkflowMetadata(
	workflowCtx *WorkflowContext,
	startTime time.Time,
	status WorkflowStatusType,
) {
	workflowCtx.mu.Lock()
	defer workflowCtx.mu.Unlock()

	if workflowCtx.Workflow == nil {
		return
	}

	// Count completed steps
	completedSteps := 0
	for _, step := range workflowCtx.Steps {
		if step.Status == StepStatusCompleted {
			completedSteps++
		}
	}

	workflowCtx.Workflow.StepCount = completedSteps
	workflowCtx.Workflow.Status = status
	workflowCtx.Workflow.DurationMs = time.Since(startTime).Milliseconds()
}

// applyParameterDefaults applies default values from JSON Schema to workflow parameters.
// This ensures that parameters with defaults are set even if not provided by the client.
//
// JSON Schema format:
//
//	{
//	  "type": "object",
//	  "properties": {
//	    "param_name": {"type": "string", "default": "default_value"}
//	  }
//	}
//
// If a parameter is missing from params but has a default in the schema, the default is applied.
// Parameters explicitly provided by the client are never overwritten.
func applyParameterDefaults(schema map[string]any, params map[string]any) map[string]any {
	if params == nil {
		params = make(map[string]any)
	}
	if schema == nil {
		return params
	}

	// Extract properties from JSON Schema
	properties, ok := schema["properties"].(map[string]any)
	if !ok || properties == nil {
		return params
	}

	// Create result map with provided params
	result := make(map[string]any, len(params))
	for k, v := range params {
		result[k] = v
	}

	// Apply defaults for missing parameters
	for paramName, propSchema := range properties {
		// Skip if parameter was explicitly provided
		if _, exists := result[paramName]; exists {
			continue
		}

		// Extract default value from property schema
		if propMap, ok := propSchema.(map[string]any); ok {
			if defaultValue, hasDefault := propMap["default"]; hasDefault {
				result[paramName] = defaultValue
				logger.Debugf("Applied default value for parameter %s: %v", paramName, defaultValue)
			}
		}
	}

	return result
}

// auditWorkflowStart logs workflow start if auditor is configured.
func (e *workflowEngine) auditWorkflowStart(
	ctx context.Context,
	workflowID string,
	workflowName string,
	parameters map[string]any,
	timeout time.Duration,
) {
	if e.auditor != nil {
		e.auditor.LogWorkflowStarted(ctx, workflowID, workflowName, parameters, timeout)
	}
}

// auditWorkflowCompletion logs successful workflow completion if auditor is configured.
func (e *workflowEngine) auditWorkflowCompletion(
	ctx context.Context,
	workflowID string,
	workflowName string,
	duration time.Duration,
	stepCount int,
	output map[string]any,
) {
	if e.auditor != nil {
		e.auditor.LogWorkflowCompleted(ctx, workflowID, workflowName, duration, stepCount, output)
	}
}

// auditWorkflowFailure logs workflow failure if auditor is configured.
func (e *workflowEngine) auditWorkflowFailure(
	ctx context.Context,
	workflowID string,
	workflowName string,
	duration time.Duration,
	stepCount int,
	err error,
) {
	if e.auditor != nil {
		e.auditor.LogWorkflowFailed(ctx, workflowID, workflowName, duration, stepCount, err)
	}
}

// auditWorkflowTimeout logs workflow timeout if auditor is configured.
func (e *workflowEngine) auditWorkflowTimeout(
	ctx context.Context,
	workflowID string,
	workflowName string,
	duration time.Duration,
	stepCount int,
) {
	if e.auditor != nil {
		e.auditor.LogWorkflowTimedOut(ctx, workflowID, workflowName, duration, stepCount)
	}
}

// auditStepStart logs step start if auditor is configured.
func (e *workflowEngine) auditStepStart(
	ctx context.Context,
	workflowID string,
	stepID string,
	stepType string,
	toolName string,
) {
	if e.auditor != nil {
		e.auditor.LogStepStarted(ctx, workflowID, stepID, stepType, toolName)
	}
}

// auditStepCompletion logs step completion if auditor is configured.
func (e *workflowEngine) auditStepCompletion(
	ctx context.Context,
	workflowID string,
	stepID string,
	duration time.Duration,
	retryCount int,
) {
	if e.auditor != nil {
		e.auditor.LogStepCompleted(ctx, workflowID, stepID, duration, retryCount)
	}
}

// auditStepFailure logs step failure if auditor is configured.
func (e *workflowEngine) auditStepFailure(
	ctx context.Context,
	workflowID string,
	stepID string,
	duration time.Duration,
	retryCount int,
	err error,
) {
	if e.auditor != nil {
		e.auditor.LogStepFailed(ctx, workflowID, stepID, duration, retryCount, err)
	}
}

// auditStepSkipped logs step skip if auditor is configured.
func (e *workflowEngine) auditStepSkipped(
	ctx context.Context,
	workflowID string,
	stepID string,
	condition string,
) {
	if e.auditor != nil {
		e.auditor.LogStepSkipped(ctx, workflowID, stepID, condition)
	}
}

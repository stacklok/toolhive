// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package composer provides composite tool workflow execution for Virtual MCP Server.
//
// Composite tools orchestrate multi-step workflows across multiple backend MCP servers.
// The package supports sequential and parallel execution, user elicitation,
// conditional logic, and error handling.
package composer

import (
	"context"
	"sync"
	"time"

	"github.com/stacklok/toolhive/pkg/vmcp/config"
)

// Composer executes composite tool workflows that orchestrate multi-step
// operations across multiple backend MCP servers.
//
// Workflows can include:
//   - Sequential tool calls
//   - Parallel execution (DAG-based)
//   - User elicitation (interactive prompts)
//   - Conditional execution
//   - Error handling and retries
type Composer interface {
	// ExecuteWorkflow executes a composite tool workflow.
	// Returns the workflow result or an error if execution fails.
	ExecuteWorkflow(ctx context.Context, def *WorkflowDefinition, params map[string]any) (*WorkflowResult, error)

	// ValidateWorkflow checks if a workflow definition is valid.
	// This includes checking for cycles, invalid tool references, etc.
	ValidateWorkflow(ctx context.Context, def *WorkflowDefinition) error

	// GetWorkflowStatus returns the current status of a running workflow.
	// Used for long-running workflows with elicitation.
	GetWorkflowStatus(ctx context.Context, workflowID string) (*WorkflowStatus, error)

	// CancelWorkflow cancels a running workflow.
	CancelWorkflow(ctx context.Context, workflowID string) error
}

// WorkflowDefinition defines a composite tool workflow.
type WorkflowDefinition struct {
	// Name is the workflow name (must be unique).
	Name string

	// Description describes what the workflow does.
	Description string

	// Parameters defines the input parameter schema (JSON Schema).
	Parameters map[string]any

	// Steps are the workflow steps to execute.
	Steps []WorkflowStep

	// Timeout is the maximum execution time for the workflow.
	// Default: 30 minutes.
	Timeout time.Duration

	// FailureMode defines how to handle step failures.
	// Options: "abort" (default), "continue", "best_effort"
	FailureMode string

	// Output defines the structured output schema for this workflow.
	// If nil, the workflow returns the last step's output (backward compatible).
	Output *config.OutputConfig

	// Metadata stores additional workflow information.
	Metadata map[string]string
}

// WorkflowStep represents a single step in a workflow.
type WorkflowStep struct {
	// ID uniquely identifies this step within the workflow.
	ID string

	// Type is the step type: "tool", "elicitation"
	Type StepType

	// Tool is the tool to call (for tool steps).
	// Format: "toolname" or "backend.toolname"
	Tool string

	// Arguments are the tool arguments with template expansion support.
	// Templates use Go text/template syntax with access to:
	//   - {{.params.name}}: Input parameters
	//   - {{.steps.stepid.output}}: Previous step outputs
	//   - {{.steps.stepid.content}}: Elicitation response data
	//   - {{.steps.stepid.action}}: Elicitation action (accept/decline/cancel)
	Arguments map[string]any

	// Condition is an optional condition for conditional execution.
	// If specified and evaluates to false, the step is skipped.
	// Uses template syntax, must evaluate to boolean.
	Condition string

	// DependsOn lists step IDs that must complete before this step.
	// Enables DAG-based parallel execution.
	DependsOn []string

	// OnError defines error handling for this step.
	OnError *ErrorHandler

	// Elicitation defines elicitation parameters (for elicitation steps).
	Elicitation *ElicitationConfig

	// Timeout is the maximum execution time for this step.
	Timeout time.Duration

	// Metadata stores additional step information.
	Metadata map[string]string

	// DefaultResults provides fallback output values when this step is skipped
	// (due to condition evaluating to false) or fails (when onError.action is "continue").
	DefaultResults map[string]any
}

// StepType defines the type of workflow step.
type StepType string

const (
	// StepTypeTool executes a backend tool.
	StepTypeTool StepType = "tool"

	// StepTypeElicitation requests user input via MCP elicitation protocol.
	StepTypeElicitation StepType = "elicitation"
)

// ErrorHandler defines how to handle step failures.
type ErrorHandler struct {
	// Action defines what to do when the step fails.
	// Options: "abort", "continue", "retry"
	Action string

	// RetryCount is the number of retry attempts (for retry action).
	RetryCount int

	// RetryDelay is the initial delay between retries.
	// Uses exponential backoff: delay * 2^attempt
	RetryDelay time.Duration

	// ContinueOnError indicates whether to continue workflow on error.
	ContinueOnError bool
}

// ElicitationConfig defines parameters for elicitation steps.
type ElicitationConfig struct {
	// Message is the prompt message shown to the user.
	Message string

	// Schema is the JSON Schema for the requested data.
	// Per MCP spec, must be a flat object with primitive properties.
	Schema map[string]any

	// Timeout is how long to wait for user response.
	// Default: 5 minutes.
	Timeout time.Duration

	// OnDecline defines what to do if user declines.
	OnDecline *ElicitationHandler

	// OnCancel defines what to do if user cancels.
	OnCancel *ElicitationHandler
}

// ElicitationHandler defines how to handle elicitation responses.
type ElicitationHandler struct {
	// Action defines what to do.
	// Options: "skip_remaining", "abort", "continue"
	Action string
}

// WorkflowResult contains the output of a workflow execution.
type WorkflowResult struct {
	// WorkflowID is the unique identifier for this execution.
	WorkflowID string

	// Status is the final workflow status.
	Status WorkflowStatusType

	// Output contains the workflow output data.
	// Typically the output of the last step.
	Output map[string]any

	// Steps contains the results of each step.
	Steps map[string]*StepResult

	// Error contains error information if the workflow failed.
	Error error

	// StartTime is when the workflow started.
	StartTime time.Time

	// EndTime is when the workflow completed.
	EndTime time.Time

	// Duration is the total execution time.
	Duration time.Duration

	// Metadata stores additional result information.
	Metadata map[string]string
}

// StepResult contains the result of a single workflow step.
type StepResult struct {
	// StepID identifies the step.
	StepID string

	// Status is the step status.
	Status StepStatusType

	// Output contains the step output data.
	Output map[string]any

	// Error contains error information if the step failed.
	Error error

	// StartTime is when the step started.
	StartTime time.Time

	// EndTime is when the step completed.
	EndTime time.Time

	// Duration is the step execution time.
	Duration time.Duration

	// RetryCount is the number of retries performed.
	RetryCount int
}

// WorkflowStatus represents the current state of a workflow execution.
type WorkflowStatus struct {
	// WorkflowID identifies the workflow.
	WorkflowID string

	// Status is the current workflow status.
	Status WorkflowStatusType

	// CurrentStep is the currently executing step (if running).
	CurrentStep string

	// CompletedSteps are the steps that have completed.
	CompletedSteps []string

	// PendingElicitations are elicitations waiting for user response.
	PendingElicitations []*PendingElicitation

	// StartTime is when the workflow started.
	StartTime time.Time

	// LastUpdateTime is when the status was last updated.
	LastUpdateTime time.Time
}

// PendingElicitation represents an elicitation awaiting user response.
type PendingElicitation struct {
	// StepID is the elicitation step ID.
	StepID string

	// Message is the elicitation message.
	Message string

	// Schema is the requested data schema.
	Schema map[string]any

	// ExpiresAt is when the elicitation times out.
	ExpiresAt time.Time
}

// WorkflowStatusType represents the state of a workflow.
type WorkflowStatusType string

const (
	// WorkflowStatusPending indicates the workflow is queued.
	WorkflowStatusPending WorkflowStatusType = "pending"

	// WorkflowStatusRunning indicates the workflow is executing.
	WorkflowStatusRunning WorkflowStatusType = "running"

	// WorkflowStatusWaitingForElicitation indicates the workflow is waiting for user input.
	WorkflowStatusWaitingForElicitation WorkflowStatusType = "waiting_for_elicitation"

	// WorkflowStatusCompleted indicates the workflow completed successfully.
	WorkflowStatusCompleted WorkflowStatusType = "completed"

	// WorkflowStatusFailed indicates the workflow failed.
	WorkflowStatusFailed WorkflowStatusType = "failed"

	// WorkflowStatusCancelled indicates the workflow was cancelled.
	WorkflowStatusCancelled WorkflowStatusType = "cancelled"

	// WorkflowStatusTimedOut indicates the workflow timed out.
	WorkflowStatusTimedOut WorkflowStatusType = "timed_out"
)

// StepStatusType represents the state of a workflow step.
type StepStatusType string

const (
	// StepStatusPending indicates the step is queued.
	StepStatusPending StepStatusType = "pending"

	// StepStatusRunning indicates the step is executing.
	StepStatusRunning StepStatusType = "running"

	// StepStatusCompleted indicates the step completed successfully.
	StepStatusCompleted StepStatusType = "completed"

	// StepStatusFailed indicates the step failed.
	StepStatusFailed StepStatusType = "failed"

	// StepStatusSkipped indicates the step was skipped (condition was false).
	StepStatusSkipped StepStatusType = "skipped"
)

// TemplateExpander handles template expansion for workflow arguments.
type TemplateExpander interface {
	// Expand evaluates templates in the given data using the workflow context.
	Expand(ctx context.Context, data map[string]any, workflowCtx *WorkflowContext) (map[string]any, error)

	// EvaluateCondition evaluates a condition template to a boolean.
	EvaluateCondition(ctx context.Context, condition string, workflowCtx *WorkflowContext) (bool, error)
}

// WorkflowContext contains the execution context for a workflow.
// Thread-safe for concurrent step execution.
type WorkflowContext struct {
	// WorkflowID is the unique workflow execution ID.
	WorkflowID string

	// Params are the input parameters.
	// This map is read-only after workflow initialization and does not require synchronization.
	Params map[string]any

	// Steps contains the results of completed steps.
	// Access must be synchronized using mu.
	Steps map[string]*StepResult

	// Variables stores workflow-scoped variables.
	// This map is read-only during workflow execution (populated before execution starts)
	// and does not require synchronization. Steps should not modify this map during execution.
	Variables map[string]any

	// Workflow contains workflow-level metadata (ID, start time, step count, status).
	// Access must be synchronized using mu.
	Workflow *WorkflowMetadata

	// mu protects concurrent access to Steps map and Workflow metadata during parallel execution.
	mu sync.RWMutex
}

// WorkflowMetadata contains workflow-level metadata available in templates.
type WorkflowMetadata struct {
	// ID is the unique workflow execution ID.
	ID string

	// StartTime is when the workflow started execution.
	StartTime time.Time

	// StepCount is the number of steps executed so far.
	StepCount int

	// Status is the current workflow status.
	Status WorkflowStatusType

	// DurationMs is the workflow duration in milliseconds.
	// This is calculated dynamically at template expansion time.
	DurationMs int64
}

// WorkflowStateStore manages workflow execution state.
// This enables persistence and recovery of long-running workflows.
type WorkflowStateStore interface {
	// SaveState persists workflow state.
	SaveState(ctx context.Context, workflowID string, state *WorkflowStatus) error

	// LoadState retrieves workflow state.
	LoadState(ctx context.Context, workflowID string) (*WorkflowStatus, error)

	// DeleteState removes workflow state.
	DeleteState(ctx context.Context, workflowID string) error

	// ListActiveWorkflows returns all active workflow IDs.
	ListActiveWorkflows(ctx context.Context) ([]string, error)
}

// ElicitationProtocolHandler handles MCP elicitation protocol interactions.
//
// This interface provides an SDK-agnostic abstraction for elicitation requests,
// enabling migration from mark3labs SDK to official SDK without changing workflow code.
//
// Per MCP 2025-06-18 spec: Elicitation is a synchronous request/response protocol
// where the server sends a request and blocks until the client responds.
type ElicitationProtocolHandler interface {
	// RequestElicitation sends an elicitation request to the client and waits for response.
	//
	// This is a synchronous blocking call that:
	//   1. Validates configuration and enforces security limits
	//   2. Sends the elicitation request to the client via underlying SDK
	//   3. Blocks until the client responds or timeout occurs
	//   4. Returns the user's response (accept/decline/cancel)
	//
	// Per MCP 2025-06-18: The SDK handles JSON-RPC ID correlation internally.
	// The workflowID and stepID are for internal tracking/logging only.
	//
	// Returns ElicitationResponse or error if timeout/cancelled/failed.
	RequestElicitation(
		ctx context.Context,
		workflowID string,
		stepID string,
		elicitConfig *ElicitationConfig,
	) (*ElicitationResponse, error)
}

// ElicitationResponse represents a user's response to an elicitation.
type ElicitationResponse struct {
	// Action is what the user did: "accept", "decline", "cancel"
	Action string

	// Content contains the user-provided data (for accept action).
	Content map[string]any

	// ReceivedAt is when the response was received.
	ReceivedAt time.Time
}

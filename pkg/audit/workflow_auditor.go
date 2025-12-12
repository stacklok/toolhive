// Package audit provides audit logging functionality for ToolHive.
package audit

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/stacklok/toolhive/pkg/auth"
)

// WorkflowAuditor provides audit logging for workflow execution.
// This interface abstracts workflow-specific audit operations from the
// HTTP middleware-based Auditor.
type WorkflowAuditor struct {
	auditLogger *slog.Logger
	config      *Config
	component   string
}

// NewWorkflowAuditor creates a new workflow auditor.
// If config is nil, creates a default configuration with stdout logging.
func NewWorkflowAuditor(config *Config) (*WorkflowAuditor, error) {
	if config == nil {
		config = DefaultConfig()
	}

	logWriter, err := config.GetLogWriter()
	if err != nil {
		return nil, fmt.Errorf("failed to create log writer: %w", err)
	}

	return &WorkflowAuditor{
		auditLogger: NewAuditLogger(logWriter),
		config:      config,
		component:   "vmcp-composer",
	}, nil
}

// LogWorkflowStarted logs the start of workflow execution.
func (w *WorkflowAuditor) LogWorkflowStarted(
	ctx context.Context,
	workflowID string,
	workflowName string,
	parameters map[string]any,
	timeout time.Duration,
) {
	if !w.config.ShouldAuditEvent(EventTypeWorkflowStarted) {
		return
	}

	source := w.extractSource(ctx)
	subjects := w.extractSubjects(ctx)

	event := NewAuditEvent(
		EventTypeWorkflowStarted,
		source,
		OutcomeSuccess,
		subjects,
		w.component,
	)

	target := map[string]string{
		TargetKeyWorkflowID:   workflowID,
		TargetKeyWorkflowName: workflowName,
		TargetKeyType:         TargetTypeWorkflow,
	}
	event.WithTarget(target)

	// Add workflow parameters as data (if configured)
	if w.config.IncludeRequestData {
		data := map[string]any{
			"parameters": parameters,
			"timeout_ms": timeout.Milliseconds(),
		}
		if dataBytes, err := json.Marshal(data); err == nil {
			rawMsg := json.RawMessage(dataBytes)
			event.WithData(&rawMsg)
		}
	}

	event.LogTo(ctx, w.auditLogger, LevelAudit)
}

// LogWorkflowCompleted logs successful workflow completion.
func (w *WorkflowAuditor) LogWorkflowCompleted(
	ctx context.Context,
	workflowID string,
	workflowName string,
	duration time.Duration,
	stepCount int,
	output map[string]any,
) {
	if !w.config.ShouldAuditEvent(EventTypeWorkflowCompleted) {
		return
	}

	source := w.extractSource(ctx)
	subjects := w.extractSubjects(ctx)

	event := NewAuditEvent(
		EventTypeWorkflowCompleted,
		source,
		OutcomeSuccess,
		subjects,
		w.component,
	)

	target := map[string]string{
		TargetKeyWorkflowID:   workflowID,
		TargetKeyWorkflowName: workflowName,
		TargetKeyType:         TargetTypeWorkflow,
	}
	event.WithTarget(target)

	// Add metadata
	event.Metadata.Extra = map[string]any{
		MetadataExtraKeyDuration:  duration.Milliseconds(),
		MetadataExtraKeyStepCount: stepCount,
	}

	// Add output data (if configured)
	if w.config.IncludeResponseData && output != nil {
		if dataBytes, err := json.Marshal(output); err == nil {
			rawMsg := json.RawMessage(dataBytes)
			event.WithData(&rawMsg)
		}
	}

	event.LogTo(ctx, w.auditLogger, LevelAudit)
}

// LogWorkflowFailed logs workflow failure.
func (w *WorkflowAuditor) LogWorkflowFailed(
	ctx context.Context,
	workflowID string,
	workflowName string,
	duration time.Duration,
	stepCount int,
	err error,
) {
	if !w.config.ShouldAuditEvent(EventTypeWorkflowFailed) {
		return
	}

	source := w.extractSource(ctx)
	subjects := w.extractSubjects(ctx)

	event := NewAuditEvent(
		EventTypeWorkflowFailed,
		source,
		OutcomeFailure,
		subjects,
		w.component,
	)

	target := map[string]string{
		TargetKeyWorkflowID:   workflowID,
		TargetKeyWorkflowName: workflowName,
		TargetKeyType:         TargetTypeWorkflow,
	}
	event.WithTarget(target)

	// Add metadata
	event.Metadata.Extra = map[string]any{
		MetadataExtraKeyDuration:  duration.Milliseconds(),
		MetadataExtraKeyStepCount: stepCount,
	}

	event.LogTo(ctx, w.auditLogger, LevelAudit)
}

// LogWorkflowTimedOut logs workflow timeout.
func (w *WorkflowAuditor) LogWorkflowTimedOut(
	ctx context.Context,
	workflowID string,
	workflowName string,
	duration time.Duration,
	stepCount int,
) {
	if !w.config.ShouldAuditEvent(EventTypeWorkflowTimedOut) {
		return
	}

	source := w.extractSource(ctx)
	subjects := w.extractSubjects(ctx)

	event := NewAuditEvent(
		EventTypeWorkflowTimedOut,
		source,
		OutcomeFailure,
		subjects,
		w.component,
	)

	target := map[string]string{
		TargetKeyWorkflowID:   workflowID,
		TargetKeyWorkflowName: workflowName,
		TargetKeyType:         TargetTypeWorkflow,
	}
	event.WithTarget(target)

	// Add metadata
	event.Metadata.Extra = map[string]any{
		MetadataExtraKeyDuration:  duration.Milliseconds(),
		MetadataExtraKeyStepCount: stepCount,
	}

	event.LogTo(ctx, w.auditLogger, LevelAudit)
}

// LogStepStarted logs the start of step execution.
func (w *WorkflowAuditor) LogStepStarted(
	ctx context.Context,
	workflowID string,
	stepID string,
	stepType string,
	toolName string,
) {
	if !w.config.ShouldAuditEvent(EventTypeWorkflowStepStarted) {
		return
	}

	source := w.extractSource(ctx)
	subjects := w.extractSubjects(ctx)

	event := NewAuditEvent(
		EventTypeWorkflowStepStarted,
		source,
		OutcomeSuccess,
		subjects,
		w.component,
	)

	target := map[string]string{
		TargetKeyWorkflowID: workflowID,
		TargetKeyStepID:     stepID,
		TargetKeyStepType:   stepType,
		TargetKeyType:       TargetTypeWorkflowStep,
	}
	if toolName != "" {
		target[TargetKeyToolName] = toolName
	}
	event.WithTarget(target)

	event.LogTo(ctx, w.auditLogger, LevelAudit)
}

// LogStepCompleted logs successful step completion.
func (w *WorkflowAuditor) LogStepCompleted(
	ctx context.Context,
	workflowID string,
	stepID string,
	duration time.Duration,
	retryCount int,
) {
	if !w.config.ShouldAuditEvent(EventTypeWorkflowStepCompleted) {
		return
	}

	source := w.extractSource(ctx)
	subjects := w.extractSubjects(ctx)

	event := NewAuditEvent(
		EventTypeWorkflowStepCompleted,
		source,
		OutcomeSuccess,
		subjects,
		w.component,
	)

	target := map[string]string{
		TargetKeyWorkflowID: workflowID,
		TargetKeyStepID:     stepID,
		TargetKeyType:       TargetTypeWorkflowStep,
	}
	event.WithTarget(target)

	event.Metadata.Extra = map[string]any{
		MetadataExtraKeyDuration:   duration.Milliseconds(),
		MetadataExtraKeyRetryCount: retryCount,
	}

	event.LogTo(ctx, w.auditLogger, LevelAudit)
}

// LogStepFailed logs step failure.
func (w *WorkflowAuditor) LogStepFailed(
	ctx context.Context,
	workflowID string,
	stepID string,
	duration time.Duration,
	retryCount int,
	err error,
) {
	if !w.config.ShouldAuditEvent(EventTypeWorkflowStepFailed) {
		return
	}

	source := w.extractSource(ctx)
	subjects := w.extractSubjects(ctx)

	event := NewAuditEvent(
		EventTypeWorkflowStepFailed,
		source,
		OutcomeFailure,
		subjects,
		w.component,
	)

	target := map[string]string{
		TargetKeyWorkflowID: workflowID,
		TargetKeyStepID:     stepID,
		TargetKeyType:       TargetTypeWorkflowStep,
	}
	event.WithTarget(target)

	event.Metadata.Extra = map[string]any{
		MetadataExtraKeyDuration:   duration.Milliseconds(),
		MetadataExtraKeyRetryCount: retryCount,
	}

	event.LogTo(ctx, w.auditLogger, LevelAudit)
}

// LogStepSkipped logs conditional step skip.
func (w *WorkflowAuditor) LogStepSkipped(
	ctx context.Context,
	workflowID string,
	stepID string,
	condition string,
) {
	if !w.config.ShouldAuditEvent(EventTypeWorkflowStepSkipped) {
		return
	}

	source := w.extractSource(ctx)
	subjects := w.extractSubjects(ctx)

	event := NewAuditEvent(
		EventTypeWorkflowStepSkipped,
		source,
		OutcomeSuccess,
		subjects,
		w.component,
	)

	target := map[string]string{
		TargetKeyWorkflowID: workflowID,
		TargetKeyStepID:     stepID,
		TargetKeyType:       TargetTypeWorkflowStep,
	}
	event.WithTarget(target)

	// Add condition as metadata
	if condition != "" {
		event.Metadata.Extra = map[string]any{
			"condition": condition,
		}
	}

	event.LogTo(ctx, w.auditLogger, LevelAudit)
}

// extractSource extracts source information from context.
// For workflows, source is always local since they're internal orchestration.
func (*WorkflowAuditor) extractSource(_ context.Context) EventSource {
	return EventSource{
		Type:  SourceTypeLocal,
		Value: "vmcp-composer",
		Extra: map[string]any{},
	}
}

// extractSubjects extracts subject information from context.
func (*WorkflowAuditor) extractSubjects(ctx context.Context) map[string]string {
	subjects := make(map[string]string)

	// Extract user information from Identity
	if identity, ok := auth.IdentityFromContext(ctx); ok {
		if identity.Subject != "" {
			subjects[SubjectKeyUserID] = identity.Subject
		}

		if identity.Name != "" {
			subjects[SubjectKeyUser] = identity.Name
		} else if identity.Email != "" {
			subjects[SubjectKeyUser] = identity.Email
		} else if preferredUsername, ok := identity.Claims["preferred_username"].(string); ok {
			subjects[SubjectKeyUser] = preferredUsername
		}

		// Add client information if available
		if clientName, ok := identity.Claims["client_name"].(string); ok {
			subjects[SubjectKeyClientName] = clientName
		}
		if clientVersion, ok := identity.Claims["client_version"].(string); ok {
			subjects[SubjectKeyClientVersion] = clientVersion
		}
	}

	// If no user found, set anonymous
	if subjects[SubjectKeyUser] == "" {
		subjects[SubjectKeyUser] = "anonymous"
	}

	return subjects
}

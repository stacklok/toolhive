// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package audit provides audit logging functionality for ToolHive.
package audit

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"time"

	"github.com/stacklok/toolhive/pkg/auth"
)

// WorkflowAuditor provides audit logging for workflow execution.
// This struct abstracts workflow-specific audit operations from the
// HTTP middleware-based Auditor.
type WorkflowAuditor struct {
	auditLogger *slog.Logger
	config      *Config
	component   string
	logWriter   io.Writer
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

	// Use configured component or default to vmcp-composer
	component := config.Component
	if component == "" {
		component = "vmcp-composer"
	}

	return &WorkflowAuditor{
		auditLogger: NewAuditLogger(logWriter),
		config:      config,
		component:   component,
		logWriter:   logWriter,
	}, nil
}

// Close closes the underlying log writer if it owns a closeable resource.
// This should be called when the workflow auditor is no longer needed.
func (w *WorkflowAuditor) Close() error {
	return closeLogWriter(w.logWriter)
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

	event := w.newEvent(ctx, EventTypeWorkflowStarted, OutcomeSuccess)

	target := map[string]string{
		TargetKeyWorkflowID:   workflowID,
		TargetKeyWorkflowName: workflowName,
		TargetKeyType:         TargetTypeWorkflow,
	}
	event.WithTarget(target)

	// Add timeout to metadata
	event.Metadata.Extra = map[string]any{
		MetadataExtraKeyTimeout: timeout.Milliseconds(),
	}

	// Add workflow parameters as data (if configured)
	// Using same structure as HTTP auditor for consistency
	if w.config.IncludeRequestData && parameters != nil {
		data := map[string]any{
			"request": parameters,
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

	event := w.newEvent(ctx, EventTypeWorkflowCompleted, OutcomeSuccess)

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
	// Using same structure as HTTP auditor for consistency
	if w.config.IncludeResponseData && output != nil {
		data := map[string]any{
			"response": output,
		}
		if dataBytes, err := json.Marshal(data); err == nil {
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
	_ error,
) {
	if !w.config.ShouldAuditEvent(EventTypeWorkflowFailed) {
		return
	}

	event := w.newEvent(ctx, EventTypeWorkflowFailed, OutcomeFailure)

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

	event := w.newEvent(ctx, EventTypeWorkflowTimedOut, OutcomeFailure)

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

	event := w.newEvent(ctx, EventTypeWorkflowStepStarted, OutcomeSuccess)

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

	event := w.newEvent(ctx, EventTypeWorkflowStepCompleted, OutcomeSuccess)

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
	_ error,
) {
	if !w.config.ShouldAuditEvent(EventTypeWorkflowStepFailed) {
		return
	}

	event := w.newEvent(ctx, EventTypeWorkflowStepFailed, OutcomeFailure)

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

	event := w.newEvent(ctx, EventTypeWorkflowStepSkipped, OutcomeSuccess)

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

// newEvent creates an audit event of the given type and outcome with the
// source, subjects, and RFC 8693 delegation chain extracted from the context.
// Every Log* method MUST build its event through this helper so that the
// delegation chain cannot be forgotten at any individual call site.
func (w *WorkflowAuditor) newEvent(ctx context.Context, eventType, outcome string) *AuditEvent {
	event := NewAuditEvent(
		eventType,
		w.extractSource(ctx),
		outcome,
		w.extractSubjects(ctx),
		w.component,
	)
	w.attachDelegation(ctx, event)
	return event
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
		subjects = extractSubjectsFromIdentity(identity)
	}

	// If no user found, set anonymous
	if subjects[SubjectKeyUser] == "" {
		subjects[SubjectKeyUser] = "anonymous"
	}

	return subjects
}

// attachDelegation attaches the RFC 8693 delegation chain from the context's
// identity to the event. It is a no-op when there is no identity or the
// identity carries no delegation chain.
func (w *WorkflowAuditor) attachDelegation(ctx context.Context, event *AuditEvent) {
	identity, ok := auth.IdentityFromContext(ctx)
	if !ok {
		return
	}
	event.WithDelegationChain(
		extractDelegationChainFromIdentity(identity, w.config.MaxDelegationDepthOrDefault()))
}

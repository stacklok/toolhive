// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package core

import (
	"context"
	"fmt"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"

	"github.com/stacklok/toolhive/pkg/telemetry"
	"github.com/stacklok/toolhive/pkg/vmcp/composer"
)

// instrumentationName is the OTEL scope used for all metrics and traces emitted
// by the core. Must match the scope used by pkg/vmcp/internal/backendtelemetry
// and pkg/vmcp/server/sessionmanager so they share the same Prometheus namespace.
const instrumentationName = "github.com/stacklok/toolhive/pkg/vmcp"

// workflowInstruments holds pre-built OTEL instruments for workflow execution
// telemetry. Created once in core.New when TelemetryProvider is set and reused
// across all per-call composer factories.
type workflowInstruments struct {
	tracer            trace.Tracer
	executionsTotal   metric.Int64Counter
	errorsTotal       metric.Int64Counter
	executionDuration metric.Float64Histogram
}

// newWorkflowInstruments creates the OTEL instruments for workflow execution
// telemetry. Returns nil if provider is nil (telemetry disabled).
// The metric names match those emitted by the session-factory composite-tool
// decorator in pkg/vmcp/server/sessionmanager so all telemetry appears under
// the same Prometheus metrics regardless of which path executed the workflow.
func newWorkflowInstruments(provider *telemetry.Provider) (*workflowInstruments, error) {
	if provider == nil {
		return nil, nil
	}

	meter := provider.MeterProvider().Meter(instrumentationName)

	executions, err := meter.Int64Counter(
		"toolhive_vmcp_workflow_executions",
		metric.WithDescription("Total number of workflow executions"),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create workflow executions counter: %w", err)
	}

	errors, err := meter.Int64Counter(
		"toolhive_vmcp_workflow_errors",
		metric.WithDescription("Total number of workflow execution errors"),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create workflow errors counter: %w", err)
	}

	duration, err := meter.Float64Histogram(
		"toolhive_vmcp_workflow_duration",
		metric.WithDescription("Duration of workflow executions in seconds"),
		metric.WithUnit("s"),
		metric.WithExplicitBucketBoundaries(telemetry.MCPHistogramBuckets...),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create workflow duration histogram: %w", err)
	}

	return &workflowInstruments{
		tracer:            provider.TracerProvider().Tracer(instrumentationName),
		executionsTotal:   executions,
		errorsTotal:       errors,
		executionDuration: duration,
	}, nil
}

// telemetryComposer wraps composer.Composer.ExecuteWorkflow with OTEL metrics
// and tracing. ValidateWorkflow is delegated to the base without instrumentation
// (validation is called once at startup, not on the hot path).
type telemetryComposer struct {
	base        composer.Composer
	instruments *workflowInstruments
}

var _ composer.Composer = (*telemetryComposer)(nil)

// ExecuteWorkflow records execution count, duration, and errors before delegating
// to the wrapped composer. Uses def.Name as the workflow_name metric attribute to
// match the label emitted by the session-factory path in sessionmanager.
func (c *telemetryComposer) ExecuteWorkflow(
	ctx context.Context, def *composer.WorkflowDefinition, params map[string]any,
) (*composer.WorkflowResult, error) {
	commonAttrs := []attribute.KeyValue{attribute.String("workflow.name", def.Name)}

	ctx, span := c.instruments.tracer.Start(ctx, "core.ExecuteWorkflow",
		trace.WithAttributes(commonAttrs...),
	)
	defer span.End()

	metricAttrs := metric.WithAttributes(commonAttrs...)
	start := time.Now()
	c.instruments.executionsTotal.Add(ctx, 1, metricAttrs)

	result, err := c.base.ExecuteWorkflow(ctx, def, params)

	c.instruments.executionDuration.Record(ctx, time.Since(start).Seconds(), metricAttrs)

	if err != nil {
		c.instruments.errorsTotal.Add(ctx, 1, metricAttrs)
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
	}

	return result, err
}

// ValidateWorkflow delegates to the base composer without instrumentation.
func (c *telemetryComposer) ValidateWorkflow(ctx context.Context, def *composer.WorkflowDefinition) error {
	return c.base.ValidateWorkflow(ctx, def)
}

// GetWorkflowStatus delegates to the base composer without instrumentation.
func (c *telemetryComposer) GetWorkflowStatus(ctx context.Context, workflowID string) (*composer.WorkflowStatus, error) {
	return c.base.GetWorkflowStatus(ctx, workflowID)
}

// CancelWorkflow delegates to the base composer without instrumentation.
func (c *telemetryComposer) CancelWorkflow(ctx context.Context, workflowID string) error {
	return c.base.CancelWorkflow(ctx, workflowID)
}

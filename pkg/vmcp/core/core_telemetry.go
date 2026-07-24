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

	coremetrics "github.com/stacklok/toolhive-core/telemetry/metrics"
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
		"stacklok.vmcp.composite_tool.executions",
		metric.WithDescription("Total number of workflow executions, split by outcome"),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create workflow executions counter: %w", err)
	}

	duration, err := meter.Float64Histogram(
		"stacklok.vmcp.composite_tool.duration",
		metric.WithDescription("Duration of workflow executions in seconds"),
		metric.WithUnit("s"),
		metric.WithExplicitBucketBoundaries(coremetrics.BucketsMCPProxy()...),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create workflow duration histogram: %w", err)
	}

	return &workflowInstruments{
		tracer:            provider.TracerProvider().Tracer(instrumentationName),
		executionsTotal:   executions,
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

// ExecuteWorkflow records execution count (split by outcome) and duration around
// the wrapped composer call. Uses def.Name as the composite_tool metric attribute
// to match the label emitted by the session-factory path in sessionmanager.
func (c *telemetryComposer) ExecuteWorkflow(
	ctx context.Context, def *composer.WorkflowDefinition, params map[string]any,
) (*composer.WorkflowResult, error) {
	ctx, span := c.instruments.tracer.Start(ctx, "core.ExecuteWorkflow",
		trace.WithAttributes(attribute.String("workflow.name", def.Name)),
	)
	defer span.End()

	durationAttrs := metric.WithAttributes(attribute.String(coremetrics.LabelCompositeTool, def.Name))
	start := time.Now()

	result, err := c.base.ExecuteWorkflow(ctx, def, params)

	c.instruments.executionDuration.Record(ctx, time.Since(start).Seconds(), durationAttrs)

	outcome := coremetrics.OutcomeSuccess
	if err != nil {
		outcome = coremetrics.OutcomeError
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
	}
	c.instruments.executionsTotal.Add(ctx, 1, metric.WithAttributes(
		attribute.String(coremetrics.LabelCompositeTool, def.Name),
		attribute.String(coremetrics.LabelOutcome, outcome),
	))

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

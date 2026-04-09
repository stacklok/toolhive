// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package sessionmanager

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"

	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/telemetry"
	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/composer"
	"github.com/stacklok/toolhive/pkg/vmcp/conversion"
	"github.com/stacklok/toolhive/pkg/vmcp/internal/compositetools"
	"github.com/stacklok/toolhive/pkg/vmcp/optimizer"
	vmcpsession "github.com/stacklok/toolhive/pkg/vmcp/session"
	"github.com/stacklok/toolhive/pkg/vmcp/session/optimizerdec"
	sessiontypes "github.com/stacklok/toolhive/pkg/vmcp/session/types"
)

const instrumentationName = "github.com/stacklok/toolhive/pkg/vmcp"

// FactoryConfig holds the session factory construction parameters that the
// session manager needs to build its decorating factory. It is separate from
// server.Config to avoid a circular import between the server and sessionmanager
// packages.
type FactoryConfig struct {
	// Base is the underlying session factory. Required.
	Base vmcpsession.MultiSessionFactory

	// WorkflowDefs are the composite tool workflow definitions.
	// If empty, composite tool decoration is skipped.
	WorkflowDefs map[string]*composer.WorkflowDefinition

	// ComposerFactory builds a per-session composer bound to the session's
	// routing table and tool list.
	ComposerFactory func(rt *vmcp.RoutingTable, tools []vmcp.Tool) composer.Composer

	// OptimizerConfig is optional optimizer configuration.
	// When non-nil and OptimizerFactory is nil, New() creates the optimizer
	// factory from this config and returns a cleanup function.
	OptimizerConfig *optimizer.Config

	// OptimizerFactory is an optional pre-built optimizer factory.
	// If set, takes precedence over OptimizerConfig.
	// If nil and OptimizerConfig is also nil, the optimizer is disabled.
	OptimizerFactory func(context.Context, []mcpserver.ServerTool) (optimizer.Optimizer, error)

	// TelemetryProvider is the optional telemetry provider.
	// If non-nil, the optimizer factory (whether derived from OptimizerConfig or
	// supplied via OptimizerFactory) and workflow executors are wrapped with telemetry.
	TelemetryProvider *telemetry.Provider

	// CacheCapacity is the maximum number of live MultiSession entries held in
	// the node-local ValidatingCache. When the cache is full the least-recently-used
	// session is evicted (its backend connections are closed via onEvict).
	// Must be >= 1; sessionmanager.New returns an error if this is zero or negative.
	CacheCapacity int
}

// resolveOptimizer wires the optimizer factory from cfg, applying telemetry
// wrapping when a provider is configured. Returns the factory (may be nil if
// optimizer is disabled) and a cleanup function.
func resolveOptimizer(cfg *FactoryConfig) (
	factory func(context.Context, []mcpserver.ServerTool) (optimizer.Optimizer, error),
	cleanup func(context.Context) error,
	err error,
) {
	noopCleanup := func(context.Context) error { return nil }

	switch {
	case cfg.OptimizerFactory != nil:
		factory = cfg.OptimizerFactory
		if cfg.TelemetryProvider != nil {
			factory, err = monitorOptimizer(
				cfg.TelemetryProvider.MeterProvider(),
				cfg.TelemetryProvider.TracerProvider(),
				factory,
			)
			if err != nil {
				return nil, nil, fmt.Errorf("failed to monitor optimizer: %w", err)
			}
		}
		return factory, noopCleanup, nil
	case cfg.OptimizerConfig != nil:
		var rawCleanup func(context.Context) error
		factory, rawCleanup, err = optimizer.NewOptimizerFactory(cfg.OptimizerConfig)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to create optimizer factory: %w", err)
		}
		cleanup = rawCleanup

		if cfg.TelemetryProvider != nil {
			factory, err = monitorOptimizer(
				cfg.TelemetryProvider.MeterProvider(),
				cfg.TelemetryProvider.TracerProvider(),
				factory,
			)
			if err != nil {
				if cleanupErr := rawCleanup(context.Background()); cleanupErr != nil {
					slog.Warn("failed to clean up optimizer after telemetry setup error", "error", cleanupErr)
				}
				return nil, nil, fmt.Errorf("failed to monitor optimizer: %w", err)
			}
		}
		return factory, cleanup, nil
	default:
		return nil, noopCleanup, nil
	}
}

// buildDecoratingFactory builds the decorating session factory from cfg.
// terminateSession is the session manager's own Terminate method, captured
// here to avoid the forward-reference dance previously needed in server.New().
func buildDecoratingFactory(
	cfg *FactoryConfig,
	optimizerFactory func(context.Context, []mcpserver.ServerTool) (optimizer.Optimizer, error),
	instruments *workflowExecutorInstruments,
	terminateSession func(string) (bool, error),
) vmcpsession.MultiSessionFactory {
	var decorators []vmcpsession.Decorator

	if len(cfg.WorkflowDefs) > 0 {
		decorators = append(decorators, compositeToolsDecorator(cfg.WorkflowDefs, cfg.ComposerFactory, instruments))
	}
	if optimizerFactory != nil {
		decorators = append(decorators, optimizerDecoratorFn(optimizerFactory, terminateSession))
	}

	return vmcpsession.NewDecoratingFactory(cfg.Base, decorators...)
}

// compositeToolsDecorator returns a Decorator that applies the composite tools
// wrapper to newly created sessions.
func compositeToolsDecorator(
	workflowDefs map[string]*composer.WorkflowDefinition,
	composerFactory func(rt *vmcp.RoutingTable, tools []vmcp.Tool) composer.Composer,
	instruments *workflowExecutorInstruments,
) vmcpsession.Decorator {
	return func(_ context.Context, sess vmcpsession.MultiSession) (vmcpsession.MultiSession, error) {
		sessionDefs := compositetools.FilterWorkflowDefsForSession(workflowDefs, sess.GetRoutingTable())
		if len(sessionDefs) == 0 {
			return sess, nil
		}

		compositeToolsMeta := compositetools.ConvertWorkflowDefsToTools(sessionDefs)
		if err := compositetools.ValidateNoToolConflicts(sess.Tools(), compositeToolsMeta); err != nil {
			slog.Warn("composite tool name conflict detected; skipping composite tools", "session_id", sess.ID(), "error", err)
			return sess, nil
		}

		sessionComposer := composerFactory(sess.GetRoutingTable(), sess.Tools())
		sessionExecutors := make(map[string]compositetools.WorkflowExecutor, len(sessionDefs))
		for _, def := range sessionDefs {
			ex := newComposerWorkflowExecutor(sessionComposer, def)
			if instruments != nil {
				ex = instruments.wrapExecutor(def.Name, ex)
			}
			sessionExecutors[def.Name] = ex
		}

		return compositetools.NewDecorator(sess, compositeToolsMeta, sessionExecutors), nil
	}
}

// optimizerDecoratorFn returns a Decorator that indexes all session tools into
// the optimizer and replaces the tool list with find_tool + call_tool.
func optimizerDecoratorFn(
	optimizerFactory func(context.Context, []mcpserver.ServerTool) (optimizer.Optimizer, error),
	terminateSession func(string) (bool, error),
) vmcpsession.Decorator {
	return func(ctx context.Context, sess vmcpsession.MultiSession) (vmcpsession.MultiSession, error) {
		sdkTools, err := adaptToolsForFactory(sess, terminateSession)
		if err != nil {
			return nil, fmt.Errorf("failed to adapt tools for optimizer: %w", err)
		}

		opt, err := optimizerFactory(ctx, sdkTools)
		if err != nil {
			return nil, fmt.Errorf("failed to create optimizer: %w", err)
		}

		slog.Info("session capabilities decorated (optimizer mode)", "indexed_tool_count", len(sdkTools))
		return optimizerdec.NewDecorator(sess, opt), nil
	}
}

// adaptToolsForFactory converts domain tools from sess to SDK-format ServerTools.
// Unlike GetAdaptedTools in session_manager.go, this version accepts an explicit
// terminateSession callback so that auth failures still terminate the session,
// preserving hijack-prevention parity with the non-optimizer tool path.
func adaptToolsForFactory(
	sess sessiontypes.MultiSession,
	terminateSession func(string) (bool, error),
) ([]mcpserver.ServerTool, error) {
	domainTools := sess.Tools()
	sdkTools := make([]mcpserver.ServerTool, 0, len(domainTools))

	for _, domainTool := range domainTools {
		schemaJSON, err := json.Marshal(domainTool.InputSchema)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal schema for tool %s: %w", domainTool.Name, err)
		}

		tool := mcp.Tool{
			Name:           domainTool.Name,
			Description:    domainTool.Description,
			RawInputSchema: schemaJSON,
			Annotations:    conversion.ToMCPToolAnnotations(domainTool.Annotations),
		}
		if domainTool.OutputSchema != nil {
			outputSchemaJSON, marshalErr := json.Marshal(domainTool.OutputSchema)
			if marshalErr != nil {
				slog.Warn("failed to marshal tool output schema", "tool", domainTool.Name, "error", marshalErr)
			} else {
				tool.RawOutputSchema = outputSchemaJSON
			}
		}

		capturedSess := sess
		capturedSessionID := sess.ID()
		capturedToolName := domainTool.Name
		handler := func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			args, ok := req.Params.Arguments.(map[string]any)
			if !ok {
				wrappedErr := fmt.Errorf("%w: arguments must be object, got %T", vmcp.ErrInvalidInput, req.Params.Arguments)
				slog.Warn("invalid arguments for tool", "tool", capturedToolName, "error", wrappedErr)
				return mcp.NewToolResultError(wrappedErr.Error()), nil
			}

			meta := conversion.FromMCPMeta(req.Params.Meta)
			caller, _ := auth.IdentityFromContext(ctx)

			result, callErr := capturedSess.CallTool(ctx, caller, capturedToolName, args, meta)
			if callErr != nil {
				if errors.Is(callErr, sessiontypes.ErrUnauthorizedCaller) || errors.Is(callErr, sessiontypes.ErrNilCaller) {
					slog.Warn("caller authorization failed, terminating session",
						"session_id", capturedSessionID, "tool", capturedToolName, "error", callErr)
					if _, termErr := terminateSession(capturedSessionID); termErr != nil {
						slog.Error("failed to terminate session after auth failure",
							"session_id", capturedSessionID, "error", termErr)
					}
					return mcp.NewToolResultError(fmt.Sprintf("Unauthorized: %v", callErr)), nil
				}
				return mcp.NewToolResultError(callErr.Error()), nil
			}

			return &mcp.CallToolResult{
				Result:            mcp.Result{Meta: conversion.ToMCPMeta(result.Meta)},
				Content:           conversion.ToMCPContents(result.Content),
				StructuredContent: result.StructuredContent,
				IsError:           result.IsError,
			}, nil
		}

		sdkTools = append(sdkTools, mcpserver.ServerTool{
			Tool:    tool,
			Handler: handler,
		})
	}

	return sdkTools, nil
}

// composerWorkflowExecutor adapts a composer.Composer + WorkflowDefinition
// to the compositetools.WorkflowExecutor interface.
type composerWorkflowExecutor struct {
	composer composer.Composer
	def      *composer.WorkflowDefinition
}

func newComposerWorkflowExecutor(c composer.Composer, def *composer.WorkflowDefinition) compositetools.WorkflowExecutor {
	return &composerWorkflowExecutor{composer: c, def: def}
}

func (e *composerWorkflowExecutor) ExecuteWorkflow(
	ctx context.Context, params map[string]any,
) (*compositetools.WorkflowResult, error) {
	result, err := e.composer.ExecuteWorkflow(ctx, e.def, params)
	if err != nil {
		return nil, err
	}
	return &compositetools.WorkflowResult{
		Output: result.Output,
		Error:  result.Error,
	}, nil
}

// workflowExecutorInstruments holds pre-created OTEL instruments for workflow
// telemetry. Created once at startup and reused across all session registrations.
type workflowExecutorInstruments struct {
	tracer            trace.Tracer
	executionsTotal   metric.Int64Counter
	errorsTotal       metric.Int64Counter
	executionDuration metric.Float64Histogram
}

func newWorkflowExecutorInstruments(
	meterProvider metric.MeterProvider,
	tracerProvider trace.TracerProvider,
) (*workflowExecutorInstruments, error) {
	meter := meterProvider.Meter(instrumentationName)

	executionsTotal, err := meter.Int64Counter(
		"toolhive_vmcp_workflow_executions",
		metric.WithDescription("Total number of workflow executions"),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create workflow executions counter: %w", err)
	}

	errorsTotal, err := meter.Int64Counter(
		"toolhive_vmcp_workflow_errors",
		metric.WithDescription("Total number of workflow execution errors"),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create workflow errors counter: %w", err)
	}

	executionDuration, err := meter.Float64Histogram(
		"toolhive_vmcp_workflow_duration",
		metric.WithDescription("Duration of workflow executions in seconds"),
		metric.WithUnit("s"),
		metric.WithExplicitBucketBoundaries(telemetry.MCPHistogramBuckets...),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create workflow duration histogram: %w", err)
	}

	return &workflowExecutorInstruments{
		tracer:            tracerProvider.Tracer(instrumentationName),
		executionsTotal:   executionsTotal,
		errorsTotal:       errorsTotal,
		executionDuration: executionDuration,
	}, nil
}

func (i *workflowExecutorInstruments) wrapExecutor(
	name string, ex compositetools.WorkflowExecutor,
) compositetools.WorkflowExecutor {
	return &telemetryWorkflowExecutor{
		name:              name,
		executor:          ex,
		tracer:            i.tracer,
		executionsTotal:   i.executionsTotal,
		errorsTotal:       i.errorsTotal,
		executionDuration: i.executionDuration,
	}
}

type telemetryWorkflowExecutor struct {
	name              string
	executor          compositetools.WorkflowExecutor
	tracer            trace.Tracer
	executionsTotal   metric.Int64Counter
	errorsTotal       metric.Int64Counter
	executionDuration metric.Float64Histogram
}

var _ compositetools.WorkflowExecutor = (*telemetryWorkflowExecutor)(nil)

func (t *telemetryWorkflowExecutor) ExecuteWorkflow(
	ctx context.Context, params map[string]any,
) (*compositetools.WorkflowResult, error) {
	commonAttrs := []attribute.KeyValue{attribute.String("workflow.name", t.name)}

	ctx, span := t.tracer.Start(ctx, "telemetryWorkflowExecutor.ExecuteWorkflow",
		trace.WithAttributes(commonAttrs...),
	)
	defer span.End()

	metricAttrs := metric.WithAttributes(commonAttrs...)
	start := time.Now()
	t.executionsTotal.Add(ctx, 1, metricAttrs)

	result, err := t.executor.ExecuteWorkflow(ctx, params)

	duration := time.Since(start)
	t.executionDuration.Record(ctx, duration.Seconds(), metricAttrs)

	if err != nil {
		t.errorsTotal.Add(ctx, 1, metricAttrs)
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
	}

	return result, err
}

// monitorOptimizer wraps an optimizer factory so that every Optimizer instance
// produced by it is decorated with telemetry (metrics + traces).
func monitorOptimizer(
	meterProvider metric.MeterProvider,
	tracerProvider trace.TracerProvider,
	factory func(context.Context, []mcpserver.ServerTool) (optimizer.Optimizer, error),
) (func(context.Context, []mcpserver.ServerTool) (optimizer.Optimizer, error), error) {
	meter := meterProvider.Meter(instrumentationName)

	findToolRequests, err := meter.Int64Counter(
		"toolhive_vmcp_optimizer_find_tool_requests",
		metric.WithDescription("Total number of FindTool calls"),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create find_tool requests counter: %w", err)
	}

	findToolErrors, err := meter.Int64Counter(
		"toolhive_vmcp_optimizer_find_tool_errors",
		metric.WithDescription("Total number of FindTool errors"),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create find_tool errors counter: %w", err)
	}

	findToolDuration, err := meter.Float64Histogram(
		"toolhive_vmcp_optimizer_find_tool_duration",
		metric.WithDescription("Duration of FindTool calls in seconds"),
		metric.WithUnit("s"),
		metric.WithExplicitBucketBoundaries(telemetry.MCPHistogramBuckets...),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create find_tool duration histogram: %w", err)
	}

	findToolResults, err := meter.Float64Histogram(
		"toolhive_vmcp_optimizer_find_tool_results",
		metric.WithDescription("Number of tools returned per FindTool call"),
		metric.WithUnit("{tools}"),
		metric.WithExplicitBucketBoundaries(0, 1, 2, 3, 5, 10, 20, 50),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create find_tool results histogram: %w", err)
	}

	tokenSavingsPercent, err := meter.Float64Histogram(
		"toolhive_vmcp_optimizer_token_savings_percent",
		metric.WithDescription("Token savings percentage per FindTool call"),
		metric.WithUnit("%"),
		metric.WithExplicitBucketBoundaries(0, 10, 20, 30, 40, 50, 60, 70, 80, 90, 95, 99, 100),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create token savings histogram: %w", err)
	}

	callToolRequests, err := meter.Int64Counter(
		"toolhive_vmcp_optimizer_call_tool_requests",
		metric.WithDescription("Total number of CallTool calls"),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create call_tool requests counter: %w", err)
	}

	callToolErrors, err := meter.Int64Counter(
		"toolhive_vmcp_optimizer_call_tool_errors",
		metric.WithDescription("Total number of CallTool Go errors"),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create call_tool errors counter: %w", err)
	}

	callToolNotFound, err := meter.Int64Counter(
		"toolhive_vmcp_optimizer_call_tool_not_found",
		metric.WithDescription("Total number of CallTool calls where result.IsError is true"),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create call_tool not_found counter: %w", err)
	}

	callToolDuration, err := meter.Float64Histogram(
		"toolhive_vmcp_optimizer_call_tool_duration",
		metric.WithDescription("Duration of CallTool calls in seconds"),
		metric.WithUnit("s"),
		metric.WithExplicitBucketBoundaries(telemetry.MCPHistogramBuckets...),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create call_tool duration histogram: %w", err)
	}

	tracer := tracerProvider.Tracer(instrumentationName)

	wrapped := func(ctx context.Context, tools []mcpserver.ServerTool) (optimizer.Optimizer, error) {
		opt, err := factory(ctx, tools)
		if err != nil {
			return nil, err
		}
		return &telemetryOptimizer{
			optimizer:           opt,
			tracer:              tracer,
			findToolRequests:    findToolRequests,
			findToolErrors:      findToolErrors,
			findToolDuration:    findToolDuration,
			findToolResults:     findToolResults,
			tokenSavingsPercent: tokenSavingsPercent,
			callToolRequests:    callToolRequests,
			callToolErrors:      callToolErrors,
			callToolNotFound:    callToolNotFound,
			callToolDuration:    callToolDuration,
		}, nil
	}

	return wrapped, nil
}

type telemetryOptimizer struct {
	optimizer optimizer.Optimizer
	tracer    trace.Tracer

	findToolRequests    metric.Int64Counter
	findToolErrors      metric.Int64Counter
	findToolDuration    metric.Float64Histogram
	findToolResults     metric.Float64Histogram
	tokenSavingsPercent metric.Float64Histogram

	callToolRequests metric.Int64Counter
	callToolErrors   metric.Int64Counter
	callToolNotFound metric.Int64Counter
	callToolDuration metric.Float64Histogram
}

var _ optimizer.Optimizer = (*telemetryOptimizer)(nil)

func (t *telemetryOptimizer) FindTool(ctx context.Context, input optimizer.FindToolInput) (*optimizer.FindToolOutput, error) {
	ctx, span := t.tracer.Start(ctx, "optimizer.FindTool",
		trace.WithAttributes(attribute.String("tool_description", input.ToolDescription)),
	)
	defer span.End()

	start := time.Now()
	t.findToolRequests.Add(ctx, 1)

	result, err := t.optimizer.FindTool(ctx, input)

	duration := time.Since(start)
	t.findToolDuration.Record(ctx, duration.Seconds())

	if err != nil {
		t.findToolErrors.Add(ctx, 1)
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return nil, err
	}

	t.findToolResults.Record(ctx, float64(len(result.Tools)))
	t.tokenSavingsPercent.Record(ctx, result.TokenMetrics.SavingsPercent)

	return result, nil
}

func (t *telemetryOptimizer) CallTool(ctx context.Context, input optimizer.CallToolInput) (*mcp.CallToolResult, error) {
	toolAttr := attribute.String("tool_name", input.ToolName)

	ctx, span := t.tracer.Start(ctx, "optimizer.CallTool",
		trace.WithAttributes(toolAttr),
	)
	defer span.End()

	metricAttrs := metric.WithAttributes(toolAttr)
	start := time.Now()
	t.callToolRequests.Add(ctx, 1, metricAttrs)

	result, err := t.optimizer.CallTool(ctx, input)

	duration := time.Since(start)
	t.callToolDuration.Record(ctx, duration.Seconds(), metricAttrs)

	if err != nil {
		t.callToolErrors.Add(ctx, 1, metricAttrs)
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return nil, err
	}

	if result != nil && result.IsError {
		t.callToolNotFound.Add(ctx, 1, metricAttrs)
	}

	return result, nil
}

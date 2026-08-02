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

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"

	"github.com/stacklok/toolhive-core/mcpcompat/mcp"
	mcpserver "github.com/stacklok/toolhive-core/mcpcompat/server"
	coremetrics "github.com/stacklok/toolhive-core/telemetry/metrics"
	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/telemetry"
	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/conversion"
	"github.com/stacklok/toolhive/pkg/vmcp/optimizer"
	vmcpsession "github.com/stacklok/toolhive/pkg/vmcp/session"
	"github.com/stacklok/toolhive/pkg/vmcp/session/optimizerdec"
	sessiontypes "github.com/stacklok/toolhive/pkg/vmcp/session/types"
)

const instrumentationName = "github.com/stacklok/toolhive/pkg/vmcp"

// defaultCacheCapacity is the fallback used when FactoryConfig.CacheCapacity is
// zero (the Go zero value). This ensures the cache is always bounded; omitting
// CacheCapacity from a config does not silently enable unbounded growth.
const defaultCacheCapacity = 1000

// outcomeNotFound extends the standard coremetrics.OutcomeSuccess/OutcomeError
// pair with a third CallTool-specific terminal state: the tool ran without
// error but reported IsError, meaning the optimizer couldn't resolve it.
const outcomeNotFound = "not_found"

// FactoryConfig holds the session factory construction parameters that the
// session manager needs to build its decorating factory. It is separate from
// server.Config to avoid a circular import between the server and sessionmanager
// packages.
type FactoryConfig struct {
	// Base is the underlying session factory. Required.
	Base vmcpsession.MultiSessionFactory

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
	// session is evicted (its backend connections are closed via onEvict). A value of
	// 0 uses defaultCacheCapacity (1000). Negative values are rejected by
	// sessionmanager.New.
	CacheCapacity int

	// AdvertiseFromCore signals that the advertised capability set is sourced from
	// the core (the Serve path), not from this factory's per-session aggregation.
	//
	// It is required whenever an optimizer is configured:
	//   - true:  New exposes the resolved factory via Manager.OptimizerFactory so
	//            the Serve layer builds a per-session optimizer over the core's
	//            tools — the single writer of the shared FTS5 store (the AC6
	//            no-double-index guarantee).
	//   - false: fine without an optimizer; with one, New rejects the config at
	//            construction, because the Serve layer discards the decorator's
	//            per-session tools (the optimizer would index the store yet serve
	//            nobody) and the Modern capability gate would fail open.
	// New resolves the optimizer factory and owns its store/cleanup. server.New
	// sets this unconditionally (server.go), so every in-tree composition
	// advertises from the core; the flag exists for direct-Serve embedders. The
	// decorator branch the false case used to select is now unreachable — its
	// deletion is tracked in #6103.
	AdvertiseFromCore bool
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
				cfg.TelemetryProvider.UseLegacyMetrics(),
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
				cfg.TelemetryProvider.UseLegacyMetrics(),
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
	terminateSession func(string) (bool, error),
) vmcpsession.MultiSessionFactory {
	var decorators []vmcpsession.Decorator

	// On the Serve path (AdvertiseFromCore) the optimizer is built by the Serve layer
	// over the core's advertised set, so the factory's optimizer decorator is skipped
	// to avoid double-indexing the shared store (see FactoryConfig.AdvertiseFromCore).
	// Composite tools and their telemetry are owned by the core, not the factory.
	// This branch is unreachable: New rejects an optimizer without AdvertiseFromCore,
	// so optimizerFactory is nil whenever the flag is false. Deleting the decorator
	// path is tracked in #6103.
	if optimizerFactory != nil && !cfg.AdvertiseFromCore {
		decorators = append(decorators, optimizerDecoratorFn(optimizerFactory, terminateSession))
	}

	return vmcpsession.NewDecoratingFactory(cfg.Base, decorators...)
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

// monitorOptimizer wraps an optimizer factory so that every Optimizer instance
// produced by it is decorated with telemetry (metrics + traces).
func monitorOptimizer(
	meterProvider metric.MeterProvider,
	tracerProvider trace.TracerProvider,
	factory func(context.Context, []mcpserver.ServerTool) (optimizer.Optimizer, error),
	useLegacyMetrics bool,
) (func(context.Context, []mcpserver.ServerTool) (optimizer.Optimizer, error), error) {
	meter := meterProvider.Meter(instrumentationName)

	findToolRequests, err := meter.Int64Counter(
		"stacklok.vmcp.optimizer.find_tool.requests",
		metric.WithDescription("Total number of FindTool calls, split by outcome"),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create find_tool requests counter: %w", err)
	}

	findToolDuration, err := meter.Float64Histogram(
		"stacklok.vmcp.optimizer.find_tool.duration",
		metric.WithDescription("Duration of FindTool calls in seconds"),
		metric.WithUnit("s"),
		metric.WithExplicitBucketBoundaries(coremetrics.BucketsMCPProxy()...),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create find_tool duration histogram: %w", err)
	}

	findToolResults, err := meter.Float64Histogram(
		"stacklok.vmcp.optimizer.find_tool.results",
		metric.WithDescription("Number of tools returned per FindTool call"),
		metric.WithUnit("{tools}"),
		metric.WithExplicitBucketBoundaries(0, 1, 2, 3, 5, 10, 20, 50),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create find_tool results histogram: %w", err)
	}

	tokenSavingsPercent, err := meter.Float64Histogram(
		"stacklok.vmcp.optimizer.token_savings",
		metric.WithDescription("Token savings percentage per FindTool call"),
		metric.WithUnit("%"),
		metric.WithExplicitBucketBoundaries(0, 10, 20, 30, 40, 50, 60, 70, 80, 90, 95, 99, 100),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create token savings histogram: %w", err)
	}

	callToolRequests, err := meter.Int64Counter(
		"stacklok.vmcp.optimizer.call_tool.requests",
		metric.WithDescription("Total number of CallTool calls, split by outcome (success, error, not_found)"),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create call_tool requests counter: %w", err)
	}

	callToolDuration, err := meter.Float64Histogram(
		"stacklok.vmcp.optimizer.call_tool.duration",
		metric.WithDescription("Duration of CallTool calls in seconds"),
		metric.WithUnit("s"),
		metric.WithExplicitBucketBoundaries(coremetrics.BucketsMCPProxy()...),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create call_tool duration histogram: %w", err)
	}

	tracer := tracerProvider.Tracer(instrumentationName)
	legacy := newLegacyOptimizerInstruments(meter, useLegacyMetrics)

	wrapped := func(ctx context.Context, tools []mcpserver.ServerTool) (optimizer.Optimizer, error) {
		opt, err := factory(ctx, tools)
		if err != nil {
			return nil, err
		}
		return &telemetryOptimizer{
			optimizer:           opt,
			tracer:              tracer,
			findToolRequests:    findToolRequests,
			findToolDuration:    findToolDuration,
			findToolResults:     findToolResults,
			tokenSavingsPercent: tokenSavingsPercent,
			callToolRequests:    callToolRequests,
			callToolDuration:    callToolDuration,

			legacyFindToolRequests: legacy.findToolRequests,
			legacyFindToolErrors:   legacy.findToolErrors,
			legacyFindToolDuration: legacy.findToolDuration,
			legacyFindToolResults:  legacy.findToolResults,
			legacyTokenSavings:     legacy.tokenSavings,
			legacyCallToolRequests: legacy.callToolRequests,
			legacyCallToolErrors:   legacy.callToolErrors,
			legacyCallToolNotFound: legacy.callToolNotFound,
			legacyCallToolDuration: legacy.callToolDuration,
		}, nil
	}

	return wrapped, nil
}

// legacyOptimizerInstruments holds the pre-rename optimizer aliases. They are
// built once alongside the current instruments rather than inside the per-session
// factory closure: the closure runs on every session decoration, so constructing
// them there would re-enter the SDK's instrument registry per session.
type legacyOptimizerInstruments struct {
	findToolRequests metric.Int64Counter
	findToolErrors   metric.Int64Counter
	findToolDuration metric.Float64Histogram
	findToolResults  metric.Float64Histogram
	tokenSavings     metric.Float64Histogram
	callToolRequests metric.Int64Counter
	callToolErrors   metric.Int64Counter
	callToolNotFound metric.Int64Counter
	callToolDuration metric.Float64Histogram
}

// newLegacyOptimizerInstruments builds the nine optimizer aliases, each pinned to
// the bucket layout its metric shipped with. The find_tool/call_tool errors and
// not_found counters are the un-merge of the current requests counter's outcome
// label, so each legacy counter tracks exactly one outcome value.
func newLegacyOptimizerInstruments(meter metric.Meter, enabled bool) legacyOptimizerInstruments {
	return legacyOptimizerInstruments{
		findToolRequests: telemetry.LegacyInt64Counter(meter, enabled,
			"toolhive_vmcp_optimizer_find_tool_requests",
			metric.WithDescription("DEPRECATED: use stacklok.vmcp.optimizer.find_tool.requests")),
		findToolErrors: telemetry.LegacyInt64Counter(meter, enabled,
			"toolhive_vmcp_optimizer_find_tool_errors",
			metric.WithDescription(
				`DEPRECATED: use stacklok.vmcp.optimizer.find_tool.requests{outcome="error"}`)),
		findToolDuration: telemetry.LegacyFloat64Histogram(meter, enabled,
			"toolhive_vmcp_optimizer_find_tool_duration",
			metric.WithDescription("DEPRECATED: use stacklok.vmcp.optimizer.find_tool.duration"),
			metric.WithUnit("s"),
			metric.WithExplicitBucketBoundaries(coremetrics.BucketsMCPSemconv()...)),
		findToolResults: telemetry.LegacyFloat64Histogram(meter, enabled,
			"toolhive_vmcp_optimizer_find_tool_results",
			metric.WithDescription("DEPRECATED: use stacklok.vmcp.optimizer.find_tool.results"),
			metric.WithUnit("{tools}"),
			metric.WithExplicitBucketBoundaries(0, 1, 2, 3, 5, 10, 20, 50)),
		tokenSavings: telemetry.LegacyFloat64Histogram(meter, enabled,
			"toolhive_vmcp_optimizer_token_savings_percent",
			metric.WithDescription("DEPRECATED: use stacklok.vmcp.optimizer.token_savings"),
			metric.WithUnit("%"),
			metric.WithExplicitBucketBoundaries(0, 10, 20, 30, 40, 50, 60, 70, 80, 90, 95, 99, 100)),
		callToolRequests: telemetry.LegacyInt64Counter(meter, enabled,
			"toolhive_vmcp_optimizer_call_tool_requests",
			metric.WithDescription("DEPRECATED: use stacklok.vmcp.optimizer.call_tool.requests")),
		callToolErrors: telemetry.LegacyInt64Counter(meter, enabled,
			"toolhive_vmcp_optimizer_call_tool_errors",
			metric.WithDescription(
				`DEPRECATED: use stacklok.vmcp.optimizer.call_tool.requests{outcome="error"}`)),
		callToolNotFound: telemetry.LegacyInt64Counter(meter, enabled,
			"toolhive_vmcp_optimizer_call_tool_not_found",
			metric.WithDescription(
				`DEPRECATED: use stacklok.vmcp.optimizer.call_tool.requests{outcome="not_found"}`)),
		callToolDuration: telemetry.LegacyFloat64Histogram(meter, enabled,
			"toolhive_vmcp_optimizer_call_tool_duration",
			metric.WithDescription("DEPRECATED: use stacklok.vmcp.optimizer.call_tool.duration"),
			metric.WithUnit("s"),
			metric.WithExplicitBucketBoundaries(coremetrics.BucketsMCPSemconv()...)),
	}
}

type telemetryOptimizer struct {
	optimizer optimizer.Optimizer
	tracer    trace.Tracer

	findToolRequests    metric.Int64Counter
	findToolDuration    metric.Float64Histogram
	findToolResults     metric.Float64Histogram
	tokenSavingsPercent metric.Float64Histogram

	callToolRequests metric.Int64Counter
	callToolDuration metric.Float64Histogram

	// Legacy aliases under the pre-rename toolhive_vmcp_optimizer_* names. The
	// outcome-label merges are split back into the separate counters they were,
	// so a dashboard reading them sees its original series. No-ops when disabled.
	legacyFindToolRequests metric.Int64Counter
	legacyFindToolErrors   metric.Int64Counter
	legacyFindToolDuration metric.Float64Histogram
	legacyFindToolResults  metric.Float64Histogram
	legacyTokenSavings     metric.Float64Histogram
	legacyCallToolRequests metric.Int64Counter
	legacyCallToolErrors   metric.Int64Counter
	legacyCallToolNotFound metric.Int64Counter
	legacyCallToolDuration metric.Float64Histogram
}

var _ optimizer.Optimizer = (*telemetryOptimizer)(nil)

func (t *telemetryOptimizer) FindTool(ctx context.Context, input optimizer.FindToolInput) (*optimizer.FindToolOutput, error) {
	ctx, span := t.tracer.Start(ctx, "optimizer.FindTool",
		trace.WithAttributes(attribute.String("tool_description", input.ToolDescription)),
	)
	defer span.End()

	start := time.Now()
	// The legacy counter was incremented before the call and carried no outcome
	// label, so it counts attempts rather than completions. Reproduced as-was.
	t.legacyFindToolRequests.Add(ctx, 1)

	result, err := t.optimizer.FindTool(ctx, input)

	duration := time.Since(start)
	t.findToolDuration.Record(ctx, duration.Seconds())
	t.legacyFindToolDuration.Record(ctx, duration.Seconds())

	if err != nil {
		t.findToolRequests.Add(ctx, 1, metric.WithAttributes(attribute.String(coremetrics.LabelOutcome, coremetrics.OutcomeError)))
		t.legacyFindToolErrors.Add(ctx, 1)
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return nil, err
	}

	t.findToolRequests.Add(ctx, 1, metric.WithAttributes(attribute.String(coremetrics.LabelOutcome, coremetrics.OutcomeSuccess)))
	t.findToolResults.Record(ctx, float64(len(result.Tools)))
	t.tokenSavingsPercent.Record(ctx, result.TokenMetrics.SavingsPercent)
	t.legacyFindToolResults.Record(ctx, float64(len(result.Tools)))
	t.legacyTokenSavings.Record(ctx, result.TokenMetrics.SavingsPercent)

	return result, nil
}

func (t *telemetryOptimizer) CallTool(ctx context.Context, input optimizer.CallToolInput) (*mcp.CallToolResult, error) {
	ctx, span := t.tracer.Start(ctx, "optimizer.CallTool",
		trace.WithAttributes(attribute.String("tool_name", input.ToolName)),
	)
	defer span.End()

	durationAttrs := metric.WithAttributes(attribute.String(coremetrics.LabelToolName, input.ToolName))
	start := time.Now()

	// The legacy counters carried only tool_name and were incremented before the
	// call, so they count attempts rather than completions. Reproduced as-was.
	legacyAttrs := metric.WithAttributes(attribute.String("tool_name", input.ToolName))
	t.legacyCallToolRequests.Add(ctx, 1, legacyAttrs)

	result, err := t.optimizer.CallTool(ctx, input)

	duration := time.Since(start)
	t.callToolDuration.Record(ctx, duration.Seconds(), durationAttrs)
	t.legacyCallToolDuration.Record(ctx, duration.Seconds(), legacyAttrs)

	outcome := coremetrics.OutcomeSuccess
	if err != nil {
		outcome = coremetrics.OutcomeError
		t.legacyCallToolErrors.Add(ctx, 1, legacyAttrs)
	} else if result != nil && result.IsError {
		outcome = outcomeNotFound
		t.legacyCallToolNotFound.Add(ctx, 1, legacyAttrs)
	}
	t.callToolRequests.Add(ctx, 1, metric.WithAttributes(
		attribute.String(coremetrics.LabelToolName, input.ToolName),
		attribute.String(coremetrics.LabelOutcome, outcome),
	))

	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return nil, err
	}

	return result, nil
}

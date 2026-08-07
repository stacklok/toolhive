// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package sessionmanager

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"

	"github.com/stacklok/toolhive-core/mcpcompat/mcp"
	mcpserver "github.com/stacklok/toolhive-core/mcpcompat/server"
	"github.com/stacklok/toolhive/pkg/telemetry"
	"github.com/stacklok/toolhive/pkg/vmcp/optimizer"
	vmcpsession "github.com/stacklok/toolhive/pkg/vmcp/session"
)

const instrumentationName = "github.com/stacklok/toolhive/pkg/vmcp"

// defaultCacheCapacity is the fallback used when FactoryConfig.CacheCapacity is
// zero (the Go zero value). This ensures the cache is always bounded; omitting
// CacheCapacity from a config does not silently enable unbounded growth.
const defaultCacheCapacity = 1000

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
	// legacy decorator branch the false case used to select has been removed;
	// optimizers are now built only by the Serve layer.
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

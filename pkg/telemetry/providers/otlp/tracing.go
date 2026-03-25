// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package otlp

import (
	"context"
	"fmt"

	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
	tracenoop "go.opentelemetry.io/otel/trace/noop"
)

func createTraceExporter(ctx context.Context, config Config) (sdktrace.SpanExporter, error) {
	opts := []otlptracehttp.Option{
		otlptracehttp.WithEndpoint(config.Endpoint),
	}

	if len(config.Headers) > 0 {
		opts = append(opts, otlptracehttp.WithHeaders(config.Headers))
	}

	if config.Insecure {
		opts = append(opts, otlptracehttp.WithInsecure())
	}

	exporter, err := otlptracehttp.New(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to create trace exporter: %w", err)
	}
	return exporter, nil
}

// NewTracerProviderWithShutdown creates an OTLP tracer provider with a shutdown function.
// Additional span processors (e.g. a Sentry bridge) can be registered via extraProcessors.
// When endpoint is empty but extra processors are provided, a real SDK provider is created
// without an OTLP exporter so the processors still receive spans.
func NewTracerProviderWithShutdown(
	ctx context.Context,
	config Config,
	res *resource.Resource,
	extraProcessors ...sdktrace.SpanProcessor,
) (trace.TracerProvider, func(context.Context) error, error) {
	// True no-op only when there is nothing at all to do.
	if config.Endpoint == "" && len(extraProcessors) == 0 {
		return tracenoop.NewTracerProvider(), nil, nil
	}

	opts := []sdktrace.TracerProviderOption{
		sdktrace.WithResource(res),
		sdktrace.WithSampler(sdktrace.TraceIDRatioBased(config.SamplingRate)),
	}

	// Only wire an OTLP exporter when an endpoint is actually configured.
	if config.Endpoint != "" {
		exporter, err := createTraceExporter(ctx, config)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to create trace provider: %w", err)
		}
		opts = append(opts, sdktrace.WithBatcher(exporter))
	}

	for _, p := range extraProcessors {
		opts = append(opts, sdktrace.WithSpanProcessor(p))
	}

	provider := sdktrace.NewTracerProvider(opts...)
	return provider, provider.Shutdown, nil
}

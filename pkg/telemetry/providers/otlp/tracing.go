// Package otlp provides OpenTelemetry Protocol (OTLP) provider implementations
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

// NewTracerProvider creates a standalone OTLP tracer provider
func NewTracerProvider(ctx context.Context, config Config, res *resource.Resource) (trace.TracerProvider, error) {
	if config.Endpoint == "" {
		return tracenoop.NewTracerProvider(), nil
	}

	exporter, err := createTraceExporter(ctx, config)
	if err != nil {
		return nil, fmt.Errorf("failed to create trace exporter: %w", err)
	}

	return sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(res),
		sdktrace.WithSampler(sdktrace.TraceIDRatioBased(config.SamplingRate)),
	), nil
}

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

	return otlptracehttp.New(ctx, opts...)
}

// NewTracerProviderWithShutdown creates an OTLP tracer provider with a shutdown function
func NewTracerProviderWithShutdown(
	ctx context.Context,
	config Config,
	res *resource.Resource,
) (trace.TracerProvider, func(context.Context) error, error) {
	provider, err := NewTracerProvider(ctx, config, res)
	if err != nil {
		return nil, nil, err
	}

	// Create shutdown function if it's an SDK provider
	var shutdown func(context.Context) error
	if sdkProvider, ok := provider.(*sdktrace.TracerProvider); ok {
		shutdown = sdkProvider.Shutdown
	}

	return provider, shutdown, nil
}

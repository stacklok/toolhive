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

// NewTracerProviderWithShutdown creates an OTLP tracer provider with a shutdown function
func NewTracerProviderWithShutdown(
	ctx context.Context,
	config Config,
	res *resource.Resource,
) (trace.TracerProvider, func(context.Context) error, error) {
	if config.Endpoint == "" {
		return tracenoop.NewTracerProvider(), nil, nil
	}

	exporter, err := createTraceExporter(ctx, config)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create trace provider: %w", err)
	}

	provider := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(res),
		sdktrace.WithSampler(sdktrace.TraceIDRatioBased(config.SamplingRate)),
	)

	return provider, provider.Shutdown, nil
}

// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package sentry

import (
	"context"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

// samplingSpanProcessor applies Sentry's own trace sample rate before handing
// spans to the wrapped processor.
//
// The OTEL SDK installs a single global sampler shared by every configured
// backend, so it cannot express "export every span to the OTLP collector but
// only 1% to Sentry". telemetry.NewServeProvider therefore runs the SDK sampler
// at 1.0 whenever a registered processor is active and relies on each processor
// to enforce its own rate here. Without this, --sentry-traces-sample-rate would
// be silently ignored and every span would be shipped to Sentry.
type samplingSpanProcessor struct {
	sdktrace.SpanProcessor
	sampler sdktrace.Sampler
}

// newSamplingSpanProcessor wraps next so that only spans within rate reach it.
// A rate of 1.0 or above needs no filtering at all, so next is returned
// unwrapped; a rate of 0 or below disables Sentry trace export entirely.
func newSamplingSpanProcessor(next sdktrace.SpanProcessor, rate float64) sdktrace.SpanProcessor {
	switch {
	case rate >= 1.0:
		return next
	case rate <= 0:
		return &samplingSpanProcessor{SpanProcessor: next, sampler: sdktrace.NeverSample()}
	default:
		// ParentBased mirrors the SDK sampler in toolhive-core: a trace already
		// sampled by an upstream service (e.g. ToolHive Studio) is kept even
		// when the local ratio would drop it, so distributed traces are not
		// truncated half way through.
		return &samplingSpanProcessor{
			SpanProcessor: next,
			sampler:       sdktrace.ParentBased(sdktrace.TraceIDRatioBased(rate)),
		}
	}
}

func (p *samplingSpanProcessor) OnStart(parent context.Context, s sdktrace.ReadWriteSpan) {
	if !p.sample(s.SpanContext().TraceID(), s.Parent(), s.Name(), s.SpanKind()) {
		return
	}
	p.SpanProcessor.OnStart(parent, s)
}

func (p *samplingSpanProcessor) OnEnd(s sdktrace.ReadOnlySpan) {
	if !p.sample(s.SpanContext().TraceID(), s.Parent(), s.Name(), s.SpanKind()) {
		return
	}
	p.SpanProcessor.OnEnd(s)
}

// sample derives the decision from the trace ID alone, which keeps it
// deterministic: OnStart and OnEnd always agree, and every span belonging to a
// trace is kept or dropped together rather than leaving partial traces in
// Sentry.
func (p *samplingSpanProcessor) sample(
	traceID trace.TraceID,
	parent trace.SpanContext,
	name string,
	kind trace.SpanKind,
) bool {
	result := p.sampler.ShouldSample(sdktrace.SamplingParameters{
		ParentContext: trace.ContextWithSpanContext(context.Background(), parent),
		TraceID:       traceID,
		Name:          name,
		Kind:          kind,
	})
	return result.Decision == sdktrace.RecordAndSample
}

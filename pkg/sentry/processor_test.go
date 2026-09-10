// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package sentry

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

// countingProcessor records how many spans made it past the sampling wrapper.
type countingProcessor struct {
	mu     sync.Mutex
	starts int
	ends   int
}

func (p *countingProcessor) OnStart(_ context.Context, _ sdktrace.ReadWriteSpan) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.starts++
}

func (p *countingProcessor) OnEnd(_ sdktrace.ReadOnlySpan) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.ends++
}

func (*countingProcessor) Shutdown(context.Context) error   { return nil }
func (*countingProcessor) ForceFlush(context.Context) error { return nil }

func (p *countingProcessor) counts() (int, int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.starts, p.ends
}

// recordSpans drives count root spans through a provider wired with proc.
func recordSpans(t *testing.T, proc sdktrace.SpanProcessor, count int) {
	t.Helper()
	provider := sdktrace.NewTracerProvider(
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
		sdktrace.WithSpanProcessor(proc),
	)
	t.Cleanup(func() {
		require.NoError(t, provider.Shutdown(context.Background()))
	})
	tracer := provider.Tracer("test-tracer")
	for range count {
		_, span := tracer.Start(context.Background(), "test-span")
		span.End()
	}
}

func TestNewSamplingSpanProcessor(t *testing.T) {
	t.Parallel()

	t.Run("returns the processor unwrapped at full sample rate", func(t *testing.T) {
		t.Parallel()
		next := &countingProcessor{}
		assert.Same(t, sdktrace.SpanProcessor(next), newSamplingSpanProcessor(next, 1.0),
			"a rate of 1.0 needs no filtering and should add no wrapper overhead")
	})

	t.Run("drops every span at a zero sample rate", func(t *testing.T) {
		t.Parallel()
		next := &countingProcessor{}
		recordSpans(t, newSamplingSpanProcessor(next, 0), 50)

		starts, ends := next.counts()
		assert.Zero(t, starts)
		assert.Zero(t, ends)
	})

	t.Run("drops most spans at a low sample rate", func(t *testing.T) {
		t.Parallel()
		// Regression test for --sentry-traces-sample-rate being ignored: a bare
		// BatchSpanProcessor exported all 200 spans regardless of the rate.
		const total = 200
		next := &countingProcessor{}
		recordSpans(t, newSamplingSpanProcessor(next, 0.05), total)

		_, ends := next.counts()
		assert.Less(t, ends, total/2,
			"a 5%% sample rate must drop the large majority of %d spans, got %d", total, ends)
	})

	t.Run("keeps OnStart and OnEnd paired for the same trace", func(t *testing.T) {
		t.Parallel()
		next := &countingProcessor{}
		recordSpans(t, newSamplingSpanProcessor(next, 0.5), 200)

		starts, ends := next.counts()
		assert.Equal(t, starts, ends,
			"the decision must be deterministic per trace ID so no span is started without being ended")
	})

	t.Run("keeps spans whose remote parent was already sampled", func(t *testing.T) {
		t.Parallel()
		// A trace sampled upstream (e.g. by ToolHive Studio) must survive the
		// local ratio, otherwise distributed traces are truncated mid-way.
		next := &countingProcessor{}
		proc := newSamplingSpanProcessor(next, 0.0001)
		provider := sdktrace.NewTracerProvider(
			sdktrace.WithSampler(sdktrace.AlwaysSample()),
			sdktrace.WithSpanProcessor(proc),
		)
		t.Cleanup(func() {
			require.NoError(t, provider.Shutdown(context.Background()))
		})

		remoteParent := trace.NewSpanContext(trace.SpanContextConfig{
			TraceID:    trace.TraceID{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08, 0x09, 0x0a, 0x0b, 0x0c, 0x0d, 0x0e, 0x0f, 0x10},
			SpanID:     trace.SpanID{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08},
			TraceFlags: trace.FlagsSampled,
			Remote:     true,
		})
		ctx := trace.ContextWithSpanContext(context.Background(), remoteParent)
		_, span := provider.Tracer("test-tracer").Start(ctx, "child-span")
		span.End()

		_, ends := next.counts()
		assert.Equal(t, 1, ends)
	})
}

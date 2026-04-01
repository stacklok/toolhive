// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package telemetry

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// countingSpanProcessor is a minimal sdktrace.SpanProcessor that counts
// how many times OnStart and OnEnd have been called.
type countingSpanProcessor struct {
	starts atomic.Int64
	ends   atomic.Int64
}

func (c *countingSpanProcessor) OnStart(_ context.Context, _ sdktrace.ReadWriteSpan) {
	c.starts.Add(1)
}

func (c *countingSpanProcessor) OnEnd(_ sdktrace.ReadOnlySpan) {
	c.ends.Add(1)
}

func (c *countingSpanProcessor) Shutdown(_ context.Context) error { return nil }
func (c *countingSpanProcessor) ForceFlush(_ context.Context) error { return nil }

// TestRegisterSpanProcessor_Dedup verifies that registering the same processor
// pointer twice does not result in duplicate OnStart/OnEnd callbacks.
//
//nolint:paralleltest // mutates global registry state
func TestRegisterSpanProcessor_Dedup(t *testing.T) {
	ResetSpanProcessorsForTesting()
	t.Cleanup(ResetSpanProcessorsForTesting)

	proc := &countingSpanProcessor{}
	RegisterSpanProcessor(proc)
	RegisterSpanProcessor(proc) // duplicate — must be ignored

	procs := registeredSpanProcessors()
	assert.Len(t, procs, 1, "duplicate registration should be silently ignored")
}

// TestRegisterSpanProcessor_Nil verifies that nil processors are not registered.
//
//nolint:paralleltest // mutates global registry state
func TestRegisterSpanProcessor_Nil(t *testing.T) {
	ResetSpanProcessorsForTesting()
	t.Cleanup(ResetSpanProcessorsForTesting)

	RegisterSpanProcessor(nil)
	assert.False(t, HasRegisteredSpanProcessors())
}

// TestNewProvider_PicksUpRegisteredProcessor is an end-to-end test that verifies
// a processor registered via RegisterSpanProcessor ends up receiving OnStart and
// OnEnd callbacks from spans created through a provider built by NewProvider.
//
//nolint:paralleltest // mutates global registry state
func TestNewProvider_PicksUpRegisteredProcessor(t *testing.T) {
	ResetSpanProcessorsForTesting()
	t.Cleanup(ResetSpanProcessorsForTesting)

	// Use the standard tracetest SpanRecorder so we can assert on recorded spans.
	recorder := tracetest.NewSpanRecorder()
	RegisterSpanProcessor(recorder)
	require.True(t, HasRegisteredSpanProcessors())

	ctx := context.Background()
	cfg := Config{
		ServiceName:    "test-svc",
		ServiceVersion: "0.0.1",
		TracingEnabled: true,
		SamplingRate:   "1.0",
		// No OTLP endpoint — processor-only mode.
	}

	provider, err := NewProvider(ctx, cfg)
	require.NoError(t, err)
	t.Cleanup(func() {
		shutdownCtx := context.Background()
		_ = provider.Shutdown(shutdownCtx)
	})

	tracer := provider.TracerProvider().Tracer("test-tracer")
	_, span := tracer.Start(ctx, "test-span")
	span.End()

	spans := recorder.Ended()
	require.Len(t, spans, 1, "the registered processor should have received OnEnd for the test span")
	assert.Equal(t, "test-span", spans[0].Name())
}

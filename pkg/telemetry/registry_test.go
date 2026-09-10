// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package telemetry

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
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

func (*countingSpanProcessor) Shutdown(_ context.Context) error   { return nil }
func (*countingSpanProcessor) ForceFlush(_ context.Context) error { return nil }

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

// TestNewProvider_PicksUpRegisteredResourceAttributes is an end-to-end test
// that verifies attributes registered via RegisterResourceAttributes actually
// reach the OTEL resource attached to exported spans. Integrations that export
// out of band (e.g. Sentry's OTLP exporter) depend on this for grouping.
//
//nolint:paralleltest // mutates global registry state
func TestNewProvider_PicksUpRegisteredResourceAttributes(t *testing.T) {
	ResetSpanProcessorsForTesting()
	t.Cleanup(ResetSpanProcessorsForTesting)

	recorder := tracetest.NewSpanRecorder()
	RegisterSpanProcessor(recorder)
	RegisterResourceAttributes(map[string]string{"sentry.environment": "staging"})

	ctx := context.Background()
	provider, err := NewProvider(ctx, Config{
		ServiceName:    "test-svc",
		ServiceVersion: "0.0.1",
		TracingEnabled: true,
		SamplingRate:   "1.0",
		// No OTLP endpoint — processor-only mode, as in Sentry-only serve.
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })

	_, span := provider.TracerProvider().Tracer("test-tracer").Start(ctx, "test-span")
	span.End()

	spans := recorder.Ended()
	require.Len(t, spans, 1)

	attrs := spans[0].Resource().Attributes()
	assert.Contains(t, attrs, attribute.String("sentry.environment", "staging"),
		"registered resource attributes must be present on the exported span's resource")
}

// TestRegisterResourceAttributes verifies merge and copy semantics of the
// resource attribute registry.
//
//nolint:paralleltest // mutates global registry state
func TestRegisterResourceAttributes(t *testing.T) {
	t.Run("returns nil when nothing is registered", func(t *testing.T) {
		ResetSpanProcessorsForTesting()
		t.Cleanup(ResetSpanProcessorsForTesting)

		RegisterResourceAttributes(nil)
		RegisterResourceAttributes(map[string]string{})
		assert.Nil(t, RegisteredResourceAttributes())
	})

	t.Run("merges successive registrations and overwrites repeated keys", func(t *testing.T) {
		ResetSpanProcessorsForTesting()
		t.Cleanup(ResetSpanProcessorsForTesting)

		RegisterResourceAttributes(map[string]string{"a": "1", "b": "2"})
		RegisterResourceAttributes(map[string]string{"b": "overwritten", "c": "3"})

		assert.Equal(t, map[string]string{"a": "1", "b": "overwritten", "c": "3"},
			RegisteredResourceAttributes())
	})

	t.Run("does not expose the registry to mutation by callers", func(t *testing.T) {
		ResetSpanProcessorsForTesting()
		t.Cleanup(ResetSpanProcessorsForTesting)

		input := map[string]string{"a": "1"}
		RegisterResourceAttributes(input)
		input["a"] = "mutated by caller"

		returned := RegisteredResourceAttributes()
		returned["a"] = "mutated by reader"

		assert.Equal(t, map[string]string{"a": "1"}, RegisteredResourceAttributes())
	})
}

// TestMergeRegisteredResourceAttributes verifies that explicitly configured
// attributes win over those a self-registered integration supplied.
//
//nolint:paralleltest // mutates global registry state
func TestMergeRegisteredResourceAttributes(t *testing.T) {
	tests := []struct {
		name       string
		registered map[string]string
		configured map[string]string
		want       map[string]string
	}{
		{
			name:       "passes configured attributes through when none are registered",
			configured: map[string]string{"a": "1"},
			want:       map[string]string{"a": "1"},
		},
		{
			name:       "surfaces registered attributes when none are configured",
			registered: map[string]string{"sentry.environment": "staging"},
			want:       map[string]string{"sentry.environment": "staging"},
		},
		{
			name:       "explicit configuration wins over registered defaults",
			registered: map[string]string{"sentry.environment": "staging", "a": "1"},
			configured: map[string]string{"sentry.environment": "operator-override"},
			want:       map[string]string{"sentry.environment": "operator-override", "a": "1"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ResetSpanProcessorsForTesting()
			t.Cleanup(ResetSpanProcessorsForTesting)
			RegisterResourceAttributes(tt.registered)

			assert.Equal(t, tt.want, mergeRegisteredResourceAttributes(tt.configured))
		})
	}
}

// TestRegisterSamplingRate_Overwrites covers the one behaviour
// TestApplyProcessorOnlySampling does not: a repeated registration replaces the
// previous rate. The unset default and an explicit zero are asserted there.
//
//nolint:paralleltest // mutates global registry state
func TestRegisterSamplingRate_Overwrites(t *testing.T) {
	ResetSpanProcessorsForTesting()
	t.Cleanup(ResetSpanProcessorsForTesting)

	RegisterSamplingRate(0.5)
	RegisterSamplingRate(0.01)
	assert.InDelta(t, 0.01, RegisteredSamplingRate(), 1e-9)
}

// TestApplyProcessorOnlySampling verifies that a rate registered by an
// integration replaces the hardcoded 100% sampling that processor-only mode
// used to apply unconditionally.
//
//nolint:paralleltest // mutates global registry state
func TestApplyProcessorOnlySampling(t *testing.T) {
	tests := []struct {
		name         string
		register     bool
		registerRate float64
		wantRate     float64
	}{
		{
			name:     "samples everything when the integration registered no rate",
			wantRate: 1.0,
		},
		{
			name:         "honours a low registered rate",
			register:     true,
			registerRate: 0.01,
			wantRate:     0.01,
		},
		{
			name:     "honours a registered zero rate",
			register: true,
			wantRate: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ResetSpanProcessorsForTesting()
			t.Cleanup(ResetSpanProcessorsForTesting)
			if tt.register {
				RegisterSamplingRate(tt.registerRate)
			}

			// Start from the 5% default NewServeProvider applies beforehand, to
			// prove the registered rate overrides it.
			cfg := Config{SamplingRate: "0.05"}
			applyProcessorOnlySampling(&cfg)

			assert.True(t, cfg.TracingEnabled,
				"registered processors are the only consumers, so tracing must be forced on")
			// Assert on the parsed value handed to the sampler, not its string form.
			assert.InDelta(t, tt.wantRate, cfg.GetSamplingRateFloat(), 1e-9)
		})
	}
}

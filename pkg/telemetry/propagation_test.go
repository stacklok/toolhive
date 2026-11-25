// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package telemetry

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/baggage"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

func TestMetaCarrier_GetSetKeys(t *testing.T) {
	t.Parallel()

	meta := map[string]interface{}{
		"existing": "value",
		"number":   42,
	}
	carrier := NewMetaCarrier(meta)

	// Table-driven test for Get operations
	getTests := []struct {
		name string
		key  string
		want string
	}{
		{
			name: "existing string value",
			key:  "existing",
			want: "value",
		},
		{
			name: "non-string value returns empty",
			key:  "number",
			want: "",
		},
		{
			name: "non-existent key returns empty",
			key:  "missing",
			want: "",
		},
	}

	for _, tt := range getTests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := carrier.Get(tt.key); got != tt.want {
				t.Errorf("Get(%q) = %q, want %q", tt.key, got, tt.want)
			}
		})
	}

	// Test Set
	carrier.Set("traceparent", "00-abc123-def456-01")
	if got := carrier.Get("traceparent"); got != "00-abc123-def456-01" {
		t.Errorf("Get(traceparent) after Set = %q, want %q", got, "00-abc123-def456-01")
	}

	// Verify set also updates underlying map
	if v, ok := meta["traceparent"]; !ok || v != "00-abc123-def456-01" {
		t.Errorf("underlying map not updated: got %v", v)
	}

	// Test Keys
	keys := carrier.Keys()
	if len(keys) != 3 { // existing, number, traceparent
		t.Errorf("Keys() returned %d keys, want 3", len(keys))
	}
	keyMap := make(map[string]bool)
	for _, k := range keys {
		keyMap[k] = true
	}
	for _, expected := range []string{"existing", "number", "traceparent"} {
		if !keyMap[expected] {
			t.Errorf("Keys() missing key %q", expected)
		}
	}
}

func TestNewMetaCarrier_NilMeta(t *testing.T) {
	t.Parallel()

	carrier := NewMetaCarrier(nil)
	if carrier.meta == nil {
		t.Error("NewMetaCarrier(nil) should create a non-nil map")
	}

	carrier.Set("key", "value")
	if got := carrier.Get("key"); got != "value" {
		t.Errorf("Get(key) = %q, want %q", got, "value")
	}
}

func TestMetaCarrier_Meta(t *testing.T) {
	t.Parallel()

	original := map[string]interface{}{"foo": "bar"}
	carrier := NewMetaCarrier(original)

	returned := carrier.Meta()
	if returned["foo"] != "bar" {
		t.Error("Meta() should return the underlying map")
	}

	// Verify it's the same map (not a copy)
	carrier.Set("new", "val")
	if returned["new"] != "val" {
		t.Error("Meta() should return the same map reference")
	}
}

// Tests below mutate the global OTEL propagator, so they must NOT use t.Parallel().

func TestInjectMetaTraceContext(t *testing.T) { //nolint:paralleltest // Mutates global OTEL propagator
	oldPropagator := otel.GetTextMapPropagator()
	otel.SetTextMapPropagator(propagation.TraceContext{})
	defer otel.SetTextMapPropagator(oldPropagator)

	tp := sdktrace.NewTracerProvider()
	defer func() { _ = tp.Shutdown(context.Background()) }()
	tracer := tp.Tracer("test")
	ctx, span := tracer.Start(context.Background(), "test-span")
	defer span.End()

	// InjectMetaTraceContext injects directly into the meta map
	meta := map[string]interface{}{
		"progressToken": "tok-456",
	}
	InjectMetaTraceContext(ctx, meta)

	// traceparent should be added directly as a key in the meta map
	traceparent, ok := meta["traceparent"]
	if !ok {
		t.Fatal("traceparent not found in meta after InjectMetaTraceContext")
	}
	if tp1, ok := traceparent.(string); !ok || tp1 == "" {
		t.Errorf("traceparent = %v, want non-empty string", traceparent)
	}

	// Existing fields should be preserved
	if meta["progressToken"] != "tok-456" {
		t.Error("existing progressToken was overwritten by InjectMetaTraceContext")
	}
}

func TestInjectMetaTraceContext_NilMeta(t *testing.T) {
	t.Parallel()

	// Should not panic
	InjectMetaTraceContext(context.Background(), nil)
}

//nolint:paralleltest // Mutates global OTEL propagator
func TestMetaWithTraceContext(t *testing.T) {
	oldPropagator := otel.GetTextMapPropagator()
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{}))
	defer otel.SetTextMapPropagator(oldPropagator)

	tp := sdktrace.NewTracerProvider()
	defer func() { _ = tp.Shutdown(context.Background()) }()
	tracer := tp.Tracer("test")

	spanCtx, span := tracer.Start(context.Background(), "test-span")
	defer span.End()

	t.Run("active span with nil meta returns traceparent", func(t *testing.T) {
		got := MetaWithTraceContext(spanCtx, nil)
		require.NotNil(t, got)
		tp, ok := got["traceparent"].(string)
		require.True(t, ok, "expected traceparent to be present")
		assert.NotEmpty(t, tp)
	})

	t.Run("active span with caller meta preserves caller fields without mutating input", func(t *testing.T) {
		input := map[string]any{"progressToken": "tok-123"}
		got := MetaWithTraceContext(spanCtx, input)

		require.NotNil(t, got)
		assert.Equal(t, "tok-123", got["progressToken"])
		tp, ok := got["traceparent"].(string)
		require.True(t, ok, "expected traceparent to be present")
		assert.NotEmpty(t, tp)

		// Copy-before-mutate: the caller's original map must be untouched.
		assert.Equal(t, map[string]any{"progressToken": "tok-123"}, input)
		_, present := input["traceparent"]
		assert.False(t, present, "input map must not be mutated with traceparent")
	})

	t.Run("no active span with nil meta returns nil", func(t *testing.T) {
		got := MetaWithTraceContext(context.Background(), nil)
		assert.Nil(t, got)
	})

	t.Run("no active span with caller meta returns unmutated clone", func(t *testing.T) {
		input := map[string]any{"progressToken": "tok-456"}
		got := MetaWithTraceContext(context.Background(), input)

		require.NotNil(t, got)
		assert.Equal(t, input, got)
		_, present := got["traceparent"]
		assert.False(t, present, "no traceparent expected without an active trace context")
	})

	t.Run("baggage present in ctx is propagated", func(t *testing.T) {
		member, err := baggage.NewMember("user.id", "42")
		require.NoError(t, err)
		bag, err := baggage.New(member)
		require.NoError(t, err)
		ctx := baggage.ContextWithBaggage(spanCtx, bag)

		got := MetaWithTraceContext(ctx, nil)
		require.NotNil(t, got)
		bgKey, ok := got["baggage"].(string)
		require.True(t, ok, "expected baggage key to be present")
		assert.Contains(t, bgKey, "user.id=42")
	})
}

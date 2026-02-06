// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package telemetry

import (
	"context"
	"testing"

	"go.opentelemetry.io/otel"
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

	// Test Get with existing string value
	if got := carrier.Get("existing"); got != "value" {
		t.Errorf("Get(existing) = %q, want %q", got, "value")
	}

	// Test Get with non-string value returns empty
	if got := carrier.Get("number"); got != "" {
		t.Errorf("Get(number) = %q, want empty string", got)
	}

	// Test Get with non-existent key
	if got := carrier.Get("missing"); got != "" {
		t.Errorf("Get(missing) = %q, want empty string", got)
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

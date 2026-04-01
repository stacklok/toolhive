// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package telemetry

import (
	"sync"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

var (
	globalProcessors   []sdktrace.SpanProcessor
	globalProcessorsMu sync.Mutex
)

// RegisterSpanProcessor registers an extra OTEL span processor to be included
// in any provider created via NewProvider. This allows optional integrations
// (e.g. a Sentry bridge, Datadog exporter) to self-register during their own
// Init without coupling to the caller that creates the OTEL provider.
//
// Registration must happen before NewProvider is called; processors registered
// after provider creation will not be included in the already-created provider.
//
// Duplicate registrations of the same processor pointer are silently ignored
// to prevent OnStart/OnEnd from firing twice on a single span when Init is
// called more than once (e.g. during tests or config reload).
func RegisterSpanProcessor(p sdktrace.SpanProcessor) {
	if p == nil {
		return
	}
	globalProcessorsMu.Lock()
	defer globalProcessorsMu.Unlock()
	for _, existing := range globalProcessors {
		if existing == p {
			return
		}
	}
	globalProcessors = append(globalProcessors, p)
}

// HasRegisteredSpanProcessors returns true if any extra span processors have
// been registered. Callers can use this to decide whether to initialise an
// OTEL provider even when no OTLP endpoint is configured.
func HasRegisteredSpanProcessors() bool {
	globalProcessorsMu.Lock()
	defer globalProcessorsMu.Unlock()
	return len(globalProcessors) > 0
}

// ResetSpanProcessorsForTesting clears all registered span processors.
// For use in tests only.
func ResetSpanProcessorsForTesting() {
	globalProcessorsMu.Lock()
	defer globalProcessorsMu.Unlock()
	globalProcessors = nil
}

// registeredSpanProcessors returns a snapshot of all registered processors.
func registeredSpanProcessors() []sdktrace.SpanProcessor {
	globalProcessorsMu.Lock()
	defer globalProcessorsMu.Unlock()
	if len(globalProcessors) == 0 {
		return nil
	}
	result := make([]sdktrace.SpanProcessor, len(globalProcessors))
	copy(result, globalProcessors)
	return result
}

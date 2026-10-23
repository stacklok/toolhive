// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package sentry

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	gosentry "github.com/getsentry/sentry-go"
	sentryotel "github.com/getsentry/sentry-go/otel"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"

	"github.com/stacklok/toolhive/pkg/telemetry"
	"github.com/stacklok/toolhive/pkg/versions"
)

// These tests are deliberately NOT parallel because they mutate the package-level
// `initialized` atomic, which is global shared state.

//nolint:paralleltest // mutates global initialized and telemetry registry state
func TestInit(t *testing.T) {
	tests := []struct {
		name        string
		cfg         Config
		wantEnabled bool
		wantErr     bool
	}{
		{
			name:        "empty DSN is a no-op",
			cfg:         Config{},
			wantEnabled: false,
		},
		{
			name: "valid DSN initializes Sentry and registers its trace exporter",
			cfg: Config{
				DSN:              "https://examplePublicKey@o0.ingest.sentry.io/0",
				Environment:      "test",
				TracesSampleRate: 1.0,
			},
			wantEnabled: true,
		},
		{
			name: "invalid DSN returns error",
			cfg: Config{
				DSN: "not-a-valid-dsn",
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resetSentryForTest(t)

			err := Init(tt.cfg)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantEnabled, Enabled())
			assert.Equal(t, tt.wantEnabled, telemetry.HasRegisteredSpanProcessors(),
				"Sentry initialization should register its trace exporter")
		})
	}
}

//nolint:paralleltest // mutates global initialized and telemetry registry state
func TestInit_RegistersExactlyOneSpanProcessor(t *testing.T) {
	// Regression test: sdktrace.NewBatchSpanProcessor allocates a fresh
	// processor per call, so registering it directly defeated the registry's
	// pointer-identity dedup and double-exported every span on a second Init.
	resetSentryForTest(t)

	cfg := Config{
		DSN:              "https://examplePublicKey@o0.ingest.sentry.io/0",
		Environment:      "test",
		TracesSampleRate: 1.0,
	}
	require.NoError(t, Init(cfg))
	require.NoError(t, Init(cfg))

	assert.Equal(t, 1, telemetry.RegisteredSpanProcessorCount(),
		"repeated Init must reuse the same span processor, not register a second one")
}

//nolint:paralleltest // mutates global initialized and telemetry registry state
func TestInit_RegistersSamplingRate(t *testing.T) {
	// Regression test: spans are exported straight to Sentry's OTLP endpoint and
	// no longer pass through the Sentry client's sampler, so the rate has to
	// reach the OTEL sampler or --sentry-traces-sample-rate is silently ignored
	// and every span is exported.
	resetSentryForTest(t)

	require.NoError(t, Init(Config{
		DSN:              "https://examplePublicKey@o0.ingest.sentry.io/0",
		Environment:      "test",
		TracesSampleRate: 0.01,
	}))

	assert.InDelta(t, 0.01, telemetry.RegisteredSamplingRate(), 1e-9,
		"--sentry-traces-sample-rate must reach the OTEL sampler")
}

//nolint:paralleltest // mutates global initialized and telemetry registry state
func TestInit_RegistersResourceAttributes(t *testing.T) {
	// Regression test: spans are exported straight to Sentry's OTLP endpoint
	// and never pass through the Sentry client, so without these resource
	// attributes --sentry-environment no longer segregated Traces even though
	// it still segregated Issues.
	resetSentryForTest(t)

	require.NoError(t, Init(Config{
		DSN:              "https://examplePublicKey@o0.ingest.sentry.io/0",
		Environment:      "staging",
		TracesSampleRate: 1.0,
	}))

	attrs := telemetry.RegisteredResourceAttributes()
	require.NotNil(t, attrs)
	assert.Equal(t, "staging", attrs[environmentKey],
		"exported spans need the environment as a resource attribute to be grouped in Sentry")
	assert.Equal(t, "toolhive@"+versions.GetVersionInfo().Version, attrs[releaseKey],
		"Traces must report the same release string as Issues")
}

//nolint:paralleltest // mutates global initialized and telemetry registry state
func TestResourceAttributes(t *testing.T) {
	tests := []struct {
		name        string
		environment string
		release     string
		instanceID  string
		want        map[string]string
	}{
		{
			name: "omits empty values so blank attributes are not exported",
			want: map[string]string{},
		},
		{
			name:        "carries environment, release and instance ID",
			environment: "production",
			release:     "toolhive@1.2.3",
			instanceID:  "abc123",
			want: map[string]string{
				environmentKey: "production",
				releaseKey:     "toolhive@1.2.3",
				instanceIDKey:  "abc123",
			},
		},
		{
			name:        "omits the instance ID alone when it is unavailable",
			environment: "production",
			release:     "toolhive@1.2.3",
			want:        map[string]string{environmentKey: "production", releaseKey: "toolhive@1.2.3"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, resourceAttributes(tt.environment, tt.release, tt.instanceID))
		})
	}
}

//nolint:paralleltest // mutates global initialized and telemetry registry state
func TestClose(t *testing.T) {
	t.Run("no-op when not initialized", func(_ *testing.T) {
		initialized.Store(false)
		Close()
	})

	t.Run("flushes when initialized", func(t *testing.T) {
		resetSentryForTest(t)
		err := Init(Config{
			DSN:              "https://examplePublicKey@o0.ingest.sentry.io/0",
			Environment:      "test",
			TracesSampleRate: 1.0,
		})
		require.NoError(t, err)

		Close()
	})
}

//nolint:paralleltest // mutates global initialized state
func TestCaptureException(t *testing.T) {
	t.Run("no-op when not initialized", func(_ *testing.T) {
		initialized.Store(false)
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		CaptureException(req, errors.New("test error"))
	})

	t.Run("no-op with nil error", func(_ *testing.T) {
		initialized.Store(true)
		defer initialized.Store(false)
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		CaptureException(req, nil)
	})

	t.Run("captures exception linked to active OTEL trace", func(t *testing.T) {
		initialized.Store(false)

		transport := &gosentry.MockTransport{}
		err := gosentry.Init(gosentry.ClientOptions{
			Dsn:       "https://examplePublicKey@o0.ingest.sentry.io/0",
			Transport: transport,
			Integrations: func(integrations []gosentry.Integration) []gosentry.Integration {
				return append(integrations, sentryotel.NewOtelIntegration())
			},
		})
		require.NoError(t, err)
		initialized.Store(true)
		t.Cleanup(func() {
			initialized.Store(false)
		})

		tracerProvider := sdktrace.NewTracerProvider(sdktrace.WithSampler(sdktrace.AlwaysSample()))
		t.Cleanup(func() {
			require.NoError(t, tracerProvider.Shutdown(context.Background()))
		})
		ctx, span := tracerProvider.Tracer("test-tracer").Start(context.Background(), "test-span")
		defer span.End()

		req := httptest.NewRequest(http.MethodGet, "/", nil).WithContext(ctx)
		CaptureException(req, errors.New("test capture"))

		// CaptureEventWithHint enqueues the event; Flush delivers it to the transport.
		gosentry.Flush(flushTimeout)
		events := transport.Events()
		require.Len(t, events, 1)
		traceContext := events[0].Contexts["trace"]
		require.NotNil(t, traceContext)
		assert.Equal(t, span.SpanContext().TraceID().String(), traceContext["trace_id"])
		assert.Equal(t, span.SpanContext().SpanID().String(), traceContext["span_id"])
	})
}

//nolint:paralleltest // mutates global initialized state
func TestRecoverPanic(t *testing.T) {
	t.Run("no-op when not initialized", func(_ *testing.T) {
		initialized.Store(false)
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		RecoverPanic(req, "test panic")
	})

	t.Run("no-op with nil recovered value", func(_ *testing.T) {
		initialized.Store(true)
		defer initialized.Store(false)
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		RecoverPanic(req, nil)
	})

	t.Run("recovers panic and creates Sentry event", func(t *testing.T) {
		initialized.Store(false)
		telemetry.ResetSpanProcessorsForTesting()

		transport := &gosentry.MockTransport{}
		err := gosentry.Init(gosentry.ClientOptions{
			Dsn:       "https://examplePublicKey@o0.ingest.sentry.io/0",
			Transport: transport,
		})
		require.NoError(t, err)
		initialized.Store(true)
		defer func() {
			initialized.Store(false)
			telemetry.ResetSpanProcessorsForTesting()
		}()

		req := httptest.NewRequest(http.MethodGet, "/", nil)
		// RecoverPanic calls hub.Flush internally so events should be
		// immediately available on the transport after the call returns.
		RecoverPanic(req, "test panic value")

		assert.Equal(t, 1, len(transport.Events()))
	})
}

//nolint:paralleltest // mutates global initialized state
func TestEnabled(t *testing.T) {
	initialized.Store(false)
	assert.False(t, Enabled())

	initialized.Store(true)
	assert.True(t, Enabled())
	initialized.Store(false)
}

// TestNoPIIDataCollection guards the replacement for the deprecated
// SendDefaultPII=false against being loosened by accident. Every assertion here
// is a PII decision, not a style preference.
func TestNoPIIDataCollection(t *testing.T) {
	t.Parallel()

	dc := noPIIDataCollection()

	assert.True(t, dc.UserInfo.IsSet, "UserInfo must be set explicitly, or the SDK defaults it to true")
	assert.False(t, dc.UserInfo.Value, "user info must not be auto-populated")
	assert.Empty(t, dc.HTTPBodies, "request and response bodies must never be collected")
	assert.Equal(t, gosentry.CollectionOff, dc.Cookies.Mode, "cookies must not be collected")

	// Headers and query params are collected but scrubbed, so each needs the
	// extended deny-list the SDK only applies via the deprecated flag.
	for name, behavior := range map[string]*gosentry.KeyValueCollectionBehavior{
		"request headers":  dc.HTTPHeaders.Request,
		"response headers": dc.HTTPHeaders.Response,
		"query params":     dc.QueryParams,
	} {
		assert.Equal(t, gosentry.CollectionDenyList, behavior.Mode, "%s: mode", name)
		assert.Equal(t, piiSensitiveTerms, behavior.Terms, "%s: deny-list terms", name)
	}
}

// resetSentryForTest clears the package and registry state Init mutates, both
// before the test runs and after it finishes.
func resetSentryForTest(t *testing.T) {
	t.Helper()
	reset := func() {
		initialized.Store(false)
		telemetry.ResetSpanProcessorsForTesting()
		spanProcessorMu.Lock()
		defer spanProcessorMu.Unlock()
		spanProcessor = nil
	}
	reset()
	t.Cleanup(reset)
}

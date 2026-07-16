// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package providers

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
)

// scrapePrometheus builds a Prometheus-only provider, records one metric, and
// returns the scraped /metrics body.
func scrapePrometheus(t *testing.T, extraOpts ...ProviderOption) string {
	t.Helper()
	ctx := context.Background()
	options := append([]ProviderOption{
		WithServiceName("test-service"),
		WithServiceVersion("1.0.0"),
		WithEnablePrometheusMetricsPath(true),
	}, extraOpts...)

	provider, err := NewCompositeProvider(ctx, options...)
	require.NoError(t, err)
	require.NotNil(t, provider)
	t.Cleanup(func() { _ = provider.Shutdown(ctx) })

	counter, err := provider.MeterProvider().Meter("test").Int64Counter("d8_test_counter")
	require.NoError(t, err)
	counter.Add(ctx, 1)

	require.NotNil(t, provider.PrometheusHandler())
	req := httptest.NewRequest("GET", "/metrics", nil)
	rec := httptest.NewRecorder()
	provider.PrometheusHandler().ServeHTTP(rec, req)
	require.Equal(t, 200, rec.Code)
	return rec.Body.String()
}

func TestStacklokComponent_PromotedToPerSeriesLabels(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		opts          []ProviderOption
		wantComponent string
	}{
		{
			name:          "defaults to toolhive when unset",
			opts:          nil,
			wantComponent: ComponentToolhive,
		},
		{
			name:          "empty component defaults to toolhive",
			opts:          []ProviderOption{WithStacklokComponent("")},
			wantComponent: ComponentToolhive,
		},
		{
			name:          "vmcp component honored",
			opts:          []ProviderOption{WithStacklokComponent(ComponentVMCP)},
			wantComponent: ComponentVMCP,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			body := scrapePrometheus(t, tt.opts...)

			// The emitted counter series (not only target_info) carries both labels.
			var counterLine string
			for line := range strings.SplitSeq(body, "\n") {
				if strings.HasPrefix(line, "d8_test_counter_total") {
					counterLine = line
					break
				}
			}
			require.NotEmpty(t, counterLine, "expected d8_test_counter_total series in:\n%s", body)
			assert.Contains(t, counterLine, `stacklok_component="`+tt.wantComponent+`"`)
			assert.Contains(t, counterLine, `stacklok_product="`+ProductStacklokEnterprise+`"`)
		})
	}
}

// TestStacklokLabels_DoNotLeakUnrelatedResourceAttrs is the cardinality guard:
// the promotion filter must admit ONLY the two stacklok.* keys, never host.*,
// process.*, or the OTEL_RESOURCE_ATTRIBUTES-provided attributes.
func TestStacklokLabels_DoNotLeakUnrelatedResourceAttrs(t *testing.T) {
	// Not parallel: sets a process-wide env var consumed by resource.WithFromEnv.
	t.Setenv("OTEL_RESOURCE_ATTRIBUTES", "deployment.environment=leak-canary")

	body := scrapePrometheus(t)

	for line := range strings.SplitSeq(body, "\n") {
		if !strings.HasPrefix(line, "d8_test_counter_total") {
			continue
		}
		// The env-provided attribute must not have been promoted onto the series.
		assert.NotContains(t, line, "leak-canary", "env resource attr leaked onto series")
		assert.NotContains(t, line, "deployment_environment", "env resource attr key leaked onto series")
		// Host/process attributes must not be promoted either.
		assert.NotContains(t, line, "host_name")
		assert.NotContains(t, line, "process_pid")
	}
}

func TestStacklokResourceLabelFilter_AdmitsExactlyTwoKeys(t *testing.T) {
	t.Parallel()

	filter := stacklokResourceLabelFilter()

	admitted := []attribute.KeyValue{
		attribute.String(AttrStacklokComponent, ComponentToolhive),
		attribute.String(AttrStacklokProduct, ProductStacklokEnterprise),
	}
	for _, kv := range admitted {
		assert.True(t, filter(kv), "filter should admit %q", kv.Key)
	}

	excluded := []attribute.KeyValue{
		attribute.String("service.name", "thv-api"),
		attribute.String("host.name", "node-1"),
		attribute.Int("process.pid", 42),
		attribute.String("deployment.environment", "prod"),
	}
	for _, kv := range excluded {
		assert.False(t, filter(kv), "filter should exclude %q", kv.Key)
	}
}

// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package ratelimit

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
)

func TestRateLimitMetrics_SharedDecisionsAndLatency(t *testing.T) {
	t.Parallel()
	client, _ := newTestClient(t)
	reader, meterProvider := newRateLimitMeterProvider()

	limiter, err := newLimiter(client, "test-ns", "test-server", &v1beta1.RateLimitConfig{
		Shared: &v1beta1.RateLimitBucket{
			MaxTokens:    10,
			RefillPeriod: metav1.Duration{Duration: time.Minute},
		},
		Tools: []v1beta1.ToolRateLimitConfig{
			{
				Name: "search",
				Shared: &v1beta1.RateLimitBucket{
					MaxTokens:    1,
					RefillPeriod: metav1.Duration{Duration: time.Minute},
				},
			},
		},
	}, meterProvider)
	require.NoError(t, err)

	first, err := limiter.Allow(t.Context(), "search", "")
	require.NoError(t, err)
	require.True(t, first.Allowed)

	second, err := limiter.Allow(t.Context(), "search", "")
	require.NoError(t, err)
	require.False(t, second.Allowed)

	metrics := collectRateLimitMetrics(t, reader)
	decisions := requireRateLimitMetric(t, metrics, "toolhive_rate_limit_decisions")
	assert.Equal(t, int64(1), counterValueWithAttributes(t, decisions, map[string]string{
		"namespace":      "test-ns",
		"server":         "test-server",
		"decision":       rateLimitDecisionAllowed,
		"scope":          rateLimitScopeShared,
		"operation_type": rateLimitOperationServer,
	}))
	assert.Equal(t, int64(1), counterValueWithAttributes(t, decisions, map[string]string{
		"namespace":      "test-ns",
		"server":         "test-server",
		"decision":       rateLimitDecisionAllowed,
		"scope":          rateLimitScopeShared,
		"operation_type": rateLimitOperationTool,
	}))
	assert.Equal(t, int64(1), counterValueWithAttributes(t, decisions, map[string]string{
		"namespace":      "test-ns",
		"server":         "test-server",
		"decision":       rateLimitDecisionRejected,
		"scope":          rateLimitScopeShared,
		"operation_type": rateLimitOperationTool,
	}))

	latency := requireRateLimitMetric(t, metrics, "toolhive_rate_limit_check_latency")
	assert.Equal(t, uint64(2), histogramCountWithAttributes(t, latency, map[string]string{
		"namespace": "test-ns",
		"server":    "test-server",
	}))
}

func TestRateLimitMetrics_PerUserDecisions(t *testing.T) {
	t.Parallel()
	client, _ := newTestClient(t)
	reader, meterProvider := newRateLimitMeterProvider()

	limiter, err := newLimiter(client, "test-ns", "test-server", &v1beta1.RateLimitConfig{
		PerUser: &v1beta1.RateLimitBucket{
			MaxTokens:    10,
			RefillPeriod: metav1.Duration{Duration: time.Minute},
		},
		Tools: []v1beta1.ToolRateLimitConfig{
			{
				Name: "search",
				PerUser: &v1beta1.RateLimitBucket{
					MaxTokens:    1,
					RefillPeriod: metav1.Duration{Duration: time.Minute},
				},
			},
		},
	}, meterProvider)
	require.NoError(t, err)

	first, err := limiter.Allow(t.Context(), "search", "alice")
	require.NoError(t, err)
	require.True(t, first.Allowed)

	second, err := limiter.Allow(t.Context(), "search", "alice")
	require.NoError(t, err)
	require.False(t, second.Allowed)

	metrics := collectRateLimitMetrics(t, reader)
	decisions := requireRateLimitMetric(t, metrics, "toolhive_rate_limit_decisions")
	assert.Equal(t, int64(1), counterValueWithAttributes(t, decisions, map[string]string{
		"namespace":      "test-ns",
		"server":         "test-server",
		"decision":       rateLimitDecisionAllowed,
		"scope":          rateLimitScopePerUser,
		"operation_type": rateLimitOperationServer,
	}))
	assert.Equal(t, int64(1), counterValueWithAttributes(t, decisions, map[string]string{
		"namespace":      "test-ns",
		"server":         "test-server",
		"decision":       rateLimitDecisionAllowed,
		"scope":          rateLimitScopePerUser,
		"operation_type": rateLimitOperationTool,
	}))
	assert.Equal(t, int64(1), counterValueWithAttributes(t, decisions, map[string]string{
		"namespace":      "test-ns",
		"server":         "test-server",
		"decision":       rateLimitDecisionRejected,
		"scope":          rateLimitScopePerUser,
		"operation_type": rateLimitOperationTool,
	}))
}

func TestRateLimitMetrics_RedisErrorAndFailedLatency(t *testing.T) {
	t.Parallel()
	client, redisServer := newTestClient(t)
	reader, meterProvider := newRateLimitMeterProvider()

	limiter, err := newLimiter(client, "test-ns", "test-server", &v1beta1.RateLimitConfig{
		Shared: &v1beta1.RateLimitBucket{
			MaxTokens:    1,
			RefillPeriod: metav1.Duration{Duration: time.Minute},
		},
	}, meterProvider)
	require.NoError(t, err)

	redisServer.Close()
	_, err = limiter.Allow(t.Context(), "", "")
	require.Error(t, err)

	metrics := collectRateLimitMetrics(t, reader)
	redisErrors := requireRateLimitMetric(t, metrics, "toolhive_rate_limit_redis_errors")
	assert.Equal(t, int64(1), counterValueWithAttributes(t, redisErrors, map[string]string{
		"namespace":  "test-ns",
		"server":     "test-server",
		"error_type": redisErrorTypeConnection,
	}))

	latency := requireRateLimitMetric(t, metrics, "toolhive_rate_limit_check_latency")
	assert.Equal(t, uint64(1), histogramCountWithAttributes(t, latency, map[string]string{
		"namespace": "test-ns",
		"server":    "test-server",
	}))
	assert.Nil(t, findRateLimitMetric(metrics, "toolhive_rate_limit_decisions"))
}

func TestRateLimitMetrics_NoApplicableBucketRecordsNothing(t *testing.T) {
	t.Parallel()
	client, _ := newTestClient(t)
	reader, meterProvider := newRateLimitMeterProvider()

	limiter, err := newLimiter(client, "test-ns", "test-server", &v1beta1.RateLimitConfig{
		Tools: []v1beta1.ToolRateLimitConfig{
			{
				Name: "search",
				Shared: &v1beta1.RateLimitBucket{
					MaxTokens:    1,
					RefillPeriod: metav1.Duration{Duration: time.Minute},
				},
			},
		},
	}, meterProvider)
	require.NoError(t, err)

	decision, err := limiter.Allow(t.Context(), "other-tool", "")
	require.NoError(t, err)
	require.True(t, decision.Allowed)

	metrics := collectRateLimitMetrics(t, reader)
	assert.Empty(t, metrics.ScopeMetrics)
}

func TestRateLimitMetrics_NilMeterProviderIsNoOp(t *testing.T) {
	t.Parallel()
	client, _ := newTestClient(t)

	limiter, err := newLimiter(client, "test-ns", "test-server", &v1beta1.RateLimitConfig{
		Shared: &v1beta1.RateLimitBucket{
			MaxTokens:    1,
			RefillPeriod: metav1.Duration{Duration: time.Minute},
		},
	}, nil)
	require.NoError(t, err)

	decision, err := limiter.Allow(t.Context(), "", "")
	require.NoError(t, err)
	assert.True(t, decision.Allowed)
}

func TestRateLimitSpanAttributes_NormalOutcomes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		scope         string
		operationType string
		toolName      string
		userID        string
		rejectedBy    string
	}{
		{
			name:          "shared server",
			scope:         rateLimitScopeShared,
			operationType: rateLimitOperationServer,
			rejectedBy:    "shared_server",
		},
		{
			name:          "shared tool",
			scope:         rateLimitScopeShared,
			operationType: rateLimitOperationTool,
			toolName:      "search",
			rejectedBy:    "shared_tool",
		},
		{
			name:          "per-user server",
			scope:         rateLimitScopePerUser,
			operationType: rateLimitOperationServer,
			userID:        "alice",
			rejectedBy:    "per_user_server",
		},
		{
			name:          "per-user tool",
			scope:         rateLimitScopePerUser,
			operationType: rateLimitOperationTool,
			toolName:      "search",
			userID:        "alice",
			rejectedBy:    "per_user_tool",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			client, _ := newTestClient(t)
			limiter, err := newLimiter(
				client,
				"test-ns",
				"test-server",
				newSpanTestRateLimitConfig(t, tt.scope, tt.operationType),
				nil,
			)
			require.NoError(t, err)

			tracerProvider, recorder := newRateLimitTracerProvider(t)
			tracer := tracerProvider.Tracer("rate-limit-test")

			allowedCtx, allowedSpan := tracer.Start(t.Context(), "request")
			decision, err := limiter.Allow(allowedCtx, tt.toolName, tt.userID)
			require.NoError(t, err)
			require.True(t, decision.Allowed)
			allowedSpan.End()

			rejectedCtx, rejectedSpan := tracer.Start(t.Context(), "request")
			decision, err = limiter.Allow(rejectedCtx, tt.toolName, tt.userID)
			require.NoError(t, err)
			require.False(t, decision.Allowed)
			rejectedSpan.End()

			spans := recorder.Ended()
			require.Len(t, spans, 2, "the limiter must annotate ambient spans without creating another span")
			requireRateLimitSpanAttributes(t, spans[0], "allowed", "none")
			requireRateLimitSpanAttributes(t, spans[1], "rejected", tt.rejectedBy)
		})
	}
}

func TestRateLimitSpanAttributes_NoApplicableBucketIsAllowed(t *testing.T) {
	t.Parallel()
	client, _ := newTestClient(t)
	limiter, err := newLimiter(
		client,
		"test-ns",
		"test-server",
		newSpanTestRateLimitConfig(t, rateLimitScopeShared, rateLimitOperationTool),
		nil,
	)
	require.NoError(t, err)

	tracerProvider, recorder := newRateLimitTracerProvider(t)
	ctx, span := tracerProvider.Tracer("rate-limit-test").Start(t.Context(), "request")
	decision, err := limiter.Allow(ctx, "other-tool", "")
	require.NoError(t, err)
	require.True(t, decision.Allowed)
	span.End()

	spans := recorder.Ended()
	require.Len(t, spans, 1)
	requireRateLimitSpanAttributes(t, spans[0], "allowed", "none")
}

func TestRateLimitSpanAttributes_RedisErrorLeavesOutcomeUnset(t *testing.T) {
	t.Parallel()
	client, redisServer := newTestClient(t)
	limiter, err := newLimiter(
		client,
		"test-ns",
		"test-server",
		newSpanTestRateLimitConfig(t, rateLimitScopeShared, rateLimitOperationServer),
		nil,
	)
	require.NoError(t, err)
	redisServer.Close()

	tracerProvider, recorder := newRateLimitTracerProvider(t)
	ctx, span := tracerProvider.Tracer("rate-limit-test").Start(t.Context(), "request")
	_, err = limiter.Allow(ctx, "", "")
	require.Error(t, err)
	span.End()

	spans := recorder.Ended()
	require.Len(t, spans, 1)
	attributes := spanAttributeMap(spans[0])
	assert.NotContains(t, attributes, "rate_limit.decision")
	assert.NotContains(t, attributes, "rate_limit.rejected_by")
	assert.NotContains(t, attributes, "rate_limit.fail_open")
}

func TestClassifyRedisError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
		want string
	}{
		{
			name: "deadline exceeded",
			err:  context.DeadlineExceeded,
			want: redisErrorTypeTimeout,
		},
		{
			name: "authentication",
			err:  errors.New("NOAUTH Authentication required"),
			want: redisErrorTypeAuth,
		},
		{
			name: "connection refused",
			err:  errors.New("dial tcp 127.0.0.1:6379: connection refused"),
			want: redisErrorTypeConnection,
		},
		{
			name: "other",
			err:  errors.New("ERR script failed"),
			want: redisErrorTypeOther,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, classifyRedisError(tt.err))
		})
	}
}

func TestRateLimitMetricNamesUseToolHivePrefix(t *testing.T) {
	t.Parallel()
	reader, meterProvider := newRateLimitMeterProvider()

	telemetry, err := newRateLimitTelemetry(meterProvider, "test-ns", "test-server")
	require.NoError(t, err)
	telemetry.recordRejected(t.Context(), limitCheck{
		scope:         rateLimitScopeShared,
		operationType: rateLimitOperationServer,
	})
	telemetry.recordRedisError(t.Context(), errors.New("ERR script failed"))
	telemetry.recordCheckLatency(t.Context(), time.Millisecond)

	metrics := collectRateLimitMetrics(t, reader)
	require.NotEmpty(t, metrics.ScopeMetrics)
	for _, scopeMetrics := range metrics.ScopeMetrics {
		for _, measured := range scopeMetrics.Metrics {
			assert.True(t, strings.HasPrefix(measured.Name, "toolhive_"), measured.Name)
		}
	}
}

func newRateLimitMeterProvider() (*sdkmetric.ManualReader, *sdkmetric.MeterProvider) {
	reader := sdkmetric.NewManualReader()
	return reader, sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
}

func newRateLimitTracerProvider(t *testing.T) (*sdktrace.TracerProvider, *tracetest.SpanRecorder) {
	t.Helper()
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(
		sdktrace.WithSpanProcessor(recorder),
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
	)
	t.Cleanup(func() {
		require.NoError(t, provider.Shutdown(context.Background()))
	})
	return provider, recorder
}

func newSpanTestRateLimitConfig(t *testing.T, scope, operationType string) *v1beta1.RateLimitConfig {
	t.Helper()
	bucket := &v1beta1.RateLimitBucket{
		MaxTokens:    1,
		RefillPeriod: metav1.Duration{Duration: time.Minute},
	}

	switch {
	case scope == rateLimitScopeShared && operationType == rateLimitOperationServer:
		return &v1beta1.RateLimitConfig{Shared: bucket}
	case scope == rateLimitScopeShared && operationType == rateLimitOperationTool:
		return &v1beta1.RateLimitConfig{
			Tools: []v1beta1.ToolRateLimitConfig{{Name: "search", Shared: bucket}},
		}
	case scope == rateLimitScopePerUser && operationType == rateLimitOperationServer:
		return &v1beta1.RateLimitConfig{PerUser: bucket}
	case scope == rateLimitScopePerUser && operationType == rateLimitOperationTool:
		return &v1beta1.RateLimitConfig{
			Tools: []v1beta1.ToolRateLimitConfig{{Name: "search", PerUser: bucket}},
		}
	default:
		t.Fatalf("unsupported rate limit span test dimensions: %s/%s", scope, operationType)
		return nil
	}
}

func requireRateLimitSpanAttributes(
	t *testing.T,
	span sdktrace.ReadOnlySpan,
	decision string,
	rejectedBy string,
) {
	t.Helper()
	attributes := spanAttributeMap(span)
	assert.Equal(t, decision, attributes["rate_limit.decision"])
	assert.Equal(t, rejectedBy, attributes["rate_limit.rejected_by"])
	assert.Equal(t, false, attributes["rate_limit.fail_open"])
}

func spanAttributeMap(span sdktrace.ReadOnlySpan) map[string]any {
	attributes := make(map[string]any, len(span.Attributes()))
	for _, attr := range span.Attributes() {
		attributes[string(attr.Key)] = attr.Value.AsInterface()
	}
	return attributes
}

func collectRateLimitMetrics(t *testing.T, reader *sdkmetric.ManualReader) metricdata.ResourceMetrics {
	t.Helper()
	var metrics metricdata.ResourceMetrics
	require.NoError(t, reader.Collect(context.Background(), &metrics))
	return metrics
}

func findRateLimitMetric(metrics metricdata.ResourceMetrics, name string) *metricdata.Metrics {
	for _, scopeMetrics := range metrics.ScopeMetrics {
		for i := range scopeMetrics.Metrics {
			if scopeMetrics.Metrics[i].Name == name {
				return &scopeMetrics.Metrics[i]
			}
		}
	}
	return nil
}

func requireRateLimitMetric(
	t *testing.T,
	metrics metricdata.ResourceMetrics,
	name string,
) *metricdata.Metrics {
	t.Helper()
	measured := findRateLimitMetric(metrics, name)
	require.NotNil(t, measured, "metric %q not found", name)
	return measured
}

func counterValueWithAttributes(
	t *testing.T,
	measured *metricdata.Metrics,
	want map[string]string,
) int64 {
	t.Helper()
	sum, ok := measured.Data.(metricdata.Sum[int64])
	require.True(t, ok, "metric %q is not an int64 sum", measured.Name)
	for _, point := range sum.DataPoints {
		if attributesMatch(point.Attributes, want) {
			return point.Value
		}
	}
	return 0
}

func histogramCountWithAttributes(
	t *testing.T,
	measured *metricdata.Metrics,
	want map[string]string,
) uint64 {
	t.Helper()
	histogram, ok := measured.Data.(metricdata.Histogram[float64])
	require.True(t, ok, "metric %q is not a float64 histogram", measured.Name)
	for _, point := range histogram.DataPoints {
		if attributesMatch(point.Attributes, want) {
			return point.Count
		}
	}
	return 0
}

func attributesMatch(attributes attribute.Set, want map[string]string) bool {
	for key, wantValue := range want {
		value, ok := attributes.Value(attribute.Key(key))
		if !ok || value.AsString() != wantValue {
			return false
		}
	}
	return true
}

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

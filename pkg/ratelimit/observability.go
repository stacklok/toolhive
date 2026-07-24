// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package ratelimit

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	coremetrics "github.com/stacklok/toolhive-core/telemetry/metrics"
)

const (
	rateLimitInstrumentationName = "github.com/stacklok/toolhive/pkg/ratelimit"

	rateLimitDecisionAllowed  = "allowed"
	rateLimitDecisionRejected = "rejected"

	rateLimitScopeShared  = "shared"
	rateLimitScopePerUser = "per_user"

	rateLimitOperationServer = "server"
	rateLimitOperationTool   = "tool"

	redisErrorTypeTimeout    = "timeout"
	redisErrorTypeConnection = "connection"
	redisErrorTypeAuth       = "auth"
	redisErrorTypeOther      = "other"
)

type rateLimitTelemetry struct {
	namespace  string
	serverName string

	decisions    metric.Int64Counter
	redisErrors  metric.Int64Counter
	checkLatency metric.Float64Histogram
}

func newRateLimitTelemetry(
	meterProvider metric.MeterProvider,
	namespace string,
	serverName string,
) (*rateLimitTelemetry, error) {
	if meterProvider == nil {
		return nil, nil
	}

	meter := meterProvider.Meter(rateLimitInstrumentationName)

	decisions, err := meter.Int64Counter(
		"toolhive_rate_limit_decisions",
		metric.WithDescription("Total number of rate limit bucket decisions"),
	)
	if err != nil {
		return nil, fmt.Errorf("create decision counter: %w", err)
	}

	redisErrors, err := meter.Int64Counter(
		"toolhive_rate_limit_redis_errors",
		metric.WithDescription("Total number of Redis errors during rate limit checks"),
	)
	if err != nil {
		return nil, fmt.Errorf("create Redis error counter: %w", err)
	}

	checkLatency, err := meter.Float64Histogram(
		"toolhive_rate_limit_check_latency",
		metric.WithDescription("Duration of Redis Lua rate limit checks in seconds"),
		metric.WithUnit("s"),
		metric.WithExplicitBucketBoundaries(coremetrics.BucketsMCPProxy()...),
	)
	if err != nil {
		return nil, fmt.Errorf("create check latency histogram: %w", err)
	}

	return &rateLimitTelemetry{
		namespace:    namespace,
		serverName:   serverName,
		decisions:    decisions,
		redisErrors:  redisErrors,
		checkLatency: checkLatency,
	}, nil
}

func (t *rateLimitTelemetry) recordAllowed(ctx context.Context, checks []limitCheck) {
	if t == nil {
		return
	}
	for _, check := range checks {
		t.recordDecision(ctx, rateLimitDecisionAllowed, check)
	}
}

func (t *rateLimitTelemetry) recordRejected(ctx context.Context, check limitCheck) {
	if t == nil {
		return
	}
	t.recordDecision(ctx, rateLimitDecisionRejected, check)
}

func (t *rateLimitTelemetry) recordDecision(ctx context.Context, decision string, check limitCheck) {
	t.decisions.Add(ctx, 1, metric.WithAttributes(
		attribute.String("namespace", t.namespace),
		attribute.String(coremetrics.LabelMCPServer, t.serverName),
		attribute.String("decision", decision),
		attribute.String("scope", check.scope),
		attribute.String("operation_type", check.operationType),
	))
}

func (t *rateLimitTelemetry) recordRedisError(ctx context.Context, err error) {
	if t == nil {
		return
	}
	t.redisErrors.Add(ctx, 1, metric.WithAttributes(
		attribute.String("namespace", t.namespace),
		attribute.String(coremetrics.LabelMCPServer, t.serverName),
		attribute.String(coremetrics.LabelErrorType, classifyRedisError(err)),
	))
}

func (t *rateLimitTelemetry) recordCheckLatency(ctx context.Context, duration time.Duration) {
	if t == nil {
		return
	}
	t.checkLatency.Record(ctx, duration.Seconds(), metric.WithAttributes(
		attribute.String("namespace", t.namespace),
		attribute.String(coremetrics.LabelMCPServer, t.serverName),
	))
}

func classifyRedisError(err error) string {
	if redis.IsAuthError(err) {
		return redisErrorTypeAuth
	}

	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, redis.ErrPoolTimeout) {
		return redisErrorTypeTimeout
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return redisErrorTypeTimeout
	}

	if errors.Is(err, redis.ErrClosed) ||
		errors.Is(err, net.ErrClosed) ||
		errors.Is(err, io.EOF) ||
		errors.Is(err, io.ErrUnexpectedEOF) {
		return redisErrorTypeConnection
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		return redisErrorTypeConnection
	}

	message := strings.ToLower(err.Error())
	if strings.Contains(message, "connection refused") ||
		strings.Contains(message, "connection reset") ||
		strings.Contains(message, "broken pipe") ||
		strings.Contains(message, "no such host") {
		return redisErrorTypeConnection
	}

	return redisErrorTypeOther
}

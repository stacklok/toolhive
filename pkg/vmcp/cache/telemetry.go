// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package cache

import (
	"context"
	"log/slog"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// instrumentationName is the OTEL scope shared by all vMCP instruments so cache
// metrics land in the same Prometheus namespace as the rest of vMCP.
const instrumentationName = "github.com/stacklok/toolhive/pkg/vmcp"

// Cache-result label. A miss is not an error, so this is a bounded result
// dimension rather than the success/error outcome convention.
const (
	labelResult      = "result"
	resultValueHit   = "hit"
	resultValueMiss  = "miss"
	tokenCacheEvents = "stacklok.vmcp.token_cache.requests" // #nosec G101 -- This is a metric name, not a hardcoded credential
)

// meteredTokenCache decorates a TokenCache with an OTEL counter that records a
// hit/miss result on every Get. It is a pure pass-through: cache behavior is
// unchanged. A Get is a hit only when it returns a non-nil, non-expired token.
type meteredTokenCache struct {
	base     TokenCache
	requests metric.Int64Counter
}

var _ TokenCache = (*meteredTokenCache)(nil)

// NewMeteredTokenCache wraps base with hit/miss instrumentation using the given
// meter provider. If instrument creation fails, base is returned unwrapped so
// caching keeps working without metrics.
//
// No concrete TokenCache implementation exists in this package yet — this
// decorator is metrics scaffolding ahead of that work. stacklok.vmcp.token_cache.requests
// is not emitted until a caller constructs a TokenCache and wraps it here.
func NewMeteredTokenCache(base TokenCache, meterProvider metric.MeterProvider) TokenCache {
	if meterProvider == nil {
		return base
	}

	requests, err := meterProvider.Meter(instrumentationName).Int64Counter(
		tokenCacheEvents,
		metric.WithDescription("Total number of vMCP token cache lookups, split by result"),
		metric.WithUnit("{request}"),
	)
	if err != nil {
		slog.Debug("failed to create token cache requests counter metric", "error", err)
		return base
	}

	return &meteredTokenCache{base: base, requests: requests}
}

// Get records a hit/miss result and delegates to the wrapped cache.
func (c *meteredTokenCache) Get(ctx context.Context, key string) (*CachedToken, error) {
	token, err := c.base.Get(ctx, key)

	result := resultValueMiss
	if err == nil && token != nil && !token.IsExpired() {
		result = resultValueHit
	}
	c.requests.Add(ctx, 1, metric.WithAttributes(attribute.String(labelResult, result)))

	return token, err
}

// Set delegates to the wrapped cache.
func (c *meteredTokenCache) Set(ctx context.Context, key string, token *CachedToken) error {
	return c.base.Set(ctx, key, token)
}

// Delete delegates to the wrapped cache.
func (c *meteredTokenCache) Delete(ctx context.Context, key string) error {
	return c.base.Delete(ctx, key)
}

// Clear delegates to the wrapped cache.
func (c *meteredTokenCache) Clear(ctx context.Context) error {
	return c.base.Clear(ctx)
}

// Close delegates to the wrapped cache.
func (c *meteredTokenCache) Close() error {
	return c.base.Close()
}

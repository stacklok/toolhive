// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package ratelimit provides token-bucket rate limiting for MCP servers.
//
// The public API consists of the Limiter interface, Decision result type,
// and NewLimiter constructor. The token bucket implementation is internal.
package ratelimit

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"

	v1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	"github.com/stacklok/toolhive/pkg/ratelimit/internal/bucket"
)

// Limiter checks rate limits for an MCP server.
type Limiter interface {
	// Allow checks whether a request is permitted.
	// toolName is the MCP tool being called (empty for non-tool requests).
	// userID is the authenticated user (reserved for #4550, currently no-op).
	Allow(ctx context.Context, toolName, userID string) (*Decision, error)
}

// Decision holds the result of a rate limit check.
type Decision struct {
	// Allowed is true when the request may proceed.
	Allowed bool

	// RetryAfter is populated when Allowed is false.
	// It indicates the minimum wait before the next request may succeed.
	RetryAfter time.Duration
}

// NewLimiter constructs a Limiter from CRD configuration.
// Returns a no-op limiter (always allows) when crd is nil.
// namespace and name identify the MCP server for Redis key derivation.
func NewLimiter(client redis.Cmdable, namespace, name string, crd *v1alpha1.RateLimitConfig) (Limiter, error) {
	if crd == nil {
		return noopLimiter{}, nil
	}

	l := &limiter{client: client}

	if crd.Shared != nil {
		b, err := newBucket(namespace, name, "shared", crd.Shared)
		if err != nil {
			return nil, fmt.Errorf("shared bucket: %w", err)
		}
		l.serverBucket = b
	}

	for _, t := range crd.Tools {
		if t.Shared != nil {
			b, err := newBucket(namespace, name, "shared:tool:"+t.Name, t.Shared)
			if err != nil {
				return nil, fmt.Errorf("tool %q shared bucket: %w", t.Name, err)
			}
			if l.toolBuckets == nil {
				l.toolBuckets = make(map[string]*bucket.TokenBucket)
			}
			l.toolBuckets[t.Name] = b
		}
	}

	return l, nil
}

// limiter is the concrete implementation of Limiter.
type limiter struct {
	client       redis.Cmdable
	serverBucket *bucket.TokenBucket            // nil when no global server limit
	toolBuckets  map[string]*bucket.TokenBucket // tool name -> bucket
}

// Allow atomically checks all applicable rate limit buckets for the request.
// Tokens are only consumed if ALL buckets have sufficient capacity, preventing
// a rejected per-tool call from draining the server-level budget.
func (l *limiter) Allow(ctx context.Context, toolName, _ string) (*Decision, error) {
	// TODO(#4550): per-user rate limiting — currently ignored.

	// Collect applicable buckets in priority order.
	var buckets []*bucket.TokenBucket
	if l.serverBucket != nil {
		buckets = append(buckets, l.serverBucket)
	}
	if toolName != "" && l.toolBuckets != nil {
		if tb, ok := l.toolBuckets[toolName]; ok {
			buckets = append(buckets, tb)
		}
	}

	if len(buckets) == 0 {
		return &Decision{Allowed: true}, nil
	}

	rejectedIdx, err := bucket.ConsumeAll(ctx, l.client, buckets)
	if err != nil {
		return nil, fmt.Errorf("rate limit check: %w", err)
	}
	if rejectedIdx >= 0 {
		return &Decision{
			Allowed:    false,
			RetryAfter: buckets[rejectedIdx].RetryAfter(),
		}, nil
	}

	return &Decision{Allowed: true}, nil
}

// noopLimiter always allows requests.
type noopLimiter struct{}

func (noopLimiter) Allow(context.Context, string, string) (*Decision, error) {
	return &Decision{Allowed: true}, nil
}

// newBucket validates a CRD bucket spec and creates a TokenBucket.
func newBucket(namespace, serverName, suffix string, b *v1alpha1.RateLimitBucket) (*bucket.TokenBucket, error) {
	if b.MaxTokens < 1 {
		return nil, fmt.Errorf("maxTokens must be >= 1, got %d", b.MaxTokens)
	}
	d := b.RefillPeriod.Duration
	if d <= 0 {
		return nil, fmt.Errorf("refillPeriod must be positive, got %s", d)
	}
	return bucket.New(namespace, serverName, suffix, b.MaxTokens, d), nil
}

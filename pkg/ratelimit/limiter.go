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

	rlconfig "github.com/stacklok/toolhive/pkg/ratelimit/config"
	"github.com/stacklok/toolhive/pkg/ratelimit/internal/bucket"
)

// Limiter checks rate limits for an MCP server.
type Limiter interface {
	// Allow checks whether a request is permitted.
	// toolName is the MCP tool being called (empty for non-tool requests).
	// userID is the authenticated user (empty for unauthenticated requests).
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
func NewLimiter(client redis.Cmdable, namespace, name string, crd *rlconfig.Config) (Limiter, error) {
	if crd == nil {
		return noopLimiter{}, nil
	}

	l := &limiter{client: client}

	if global, suffix := serverGlobalBucket(crd); global != nil {
		b, err := newBucket(namespace, name, suffix, global)
		if err != nil {
			return nil, fmt.Errorf("%s bucket: %w", suffix, err)
		}
		l.serverBucket = b
	}

	if crd.PerUser != nil {
		spec, err := newBucketSpec(namespace, name, crd.PerUser)
		if err != nil {
			return nil, fmt.Errorf("perUser bucket: %w", err)
		}
		l.perUserSpec = &spec
	}

	for _, t := range crd.Tools {
		if global, suffix := toolGlobalBucket(&t); global != nil {
			b, err := newBucket(namespace, name, suffix+":tool:"+t.Name, global)
			if err != nil {
				return nil, fmt.Errorf("tool %q %s bucket: %w", t.Name, suffix, err)
			}
			if l.toolBuckets == nil {
				l.toolBuckets = make(map[string]*bucket.TokenBucket)
			}
			l.toolBuckets[t.Name] = b
		}
		if t.PerUser != nil {
			spec, err := newBucketSpec(namespace, name, t.PerUser)
			if err != nil {
				return nil, fmt.Errorf("tool %q perUser bucket: %w", t.Name, err)
			}
			if l.perUserTools == nil {
				l.perUserTools = make(map[string]bucketSpec)
			}
			l.perUserTools[t.Name] = spec
		}
	}

	return l, nil
}

func serverGlobalBucket(crd *rlconfig.Config) (*rlconfig.Bucket, string) {
	if crd.Global != nil {
		return crd.Global, "global"
	}
	if crd.Shared != nil {
		return crd.Shared, "shared"
	}
	return nil, ""
}

func toolGlobalBucket(tool *rlconfig.ToolConfig) (*rlconfig.Bucket, string) {
	if tool.Global != nil {
		return tool.Global, "global"
	}
	if tool.Shared != nil {
		return tool.Shared, "shared"
	}
	return nil, ""
}

// bucketSpec holds deferred bucket parameters for per-user buckets that are
// created on the fly in Allow() because the userID is not known at construction time.
type bucketSpec struct {
	namespace    string
	serverName   string
	maxTokens    int32
	refillPeriod time.Duration
}

// limiter is the concrete implementation of Limiter.
type limiter struct {
	client       redis.Cmdable
	serverBucket *bucket.TokenBucket            // nil when no shared server limit
	toolBuckets  map[string]*bucket.TokenBucket // tool name -> shared bucket
	perUserSpec  *bucketSpec                    // nil when no server-level per-user limit
	perUserTools map[string]bucketSpec          // tool name -> per-user bucket spec; nil when none
}

// Allow atomically checks all applicable rate limit buckets for the request.
// Tokens are only consumed if ALL buckets have sufficient capacity, preventing
// a rejected per-tool or per-user call from draining other budgets.
func (l *limiter) Allow(ctx context.Context, toolName, userID string) (*Decision, error) {
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

	// Per-user buckets are created on the fly because userID is request-scoped.
	// bucket.New only allocates a struct — all state lives in Redis, so creating
	// a new TokenBucket per request is safe (no local state to lose).
	//
	// Key prefixes deviate from RFC THV-0057 to prevent cross-type collisions:
	// RFC uses "user:{userId}:tool:{toolName}" for both scopes, but a userID
	// containing ":tool:" would collide with the per-tool key. Instead we use
	// distinct prefixes: "user:" for server-level, "user-tool:" for tool-level.
	if userID != "" {
		if l.perUserSpec != nil {
			s := l.perUserSpec
			buckets = append(buckets, bucket.New(
				s.namespace, s.serverName,
				"user:"+userID,
				s.maxTokens, s.refillPeriod,
			))
		}
		if toolName != "" && l.perUserTools != nil {
			if s, ok := l.perUserTools[toolName]; ok {
				// Key prefix "user-tool:" is distinct from "user:" to prevent
				// collisions when a userID contains delimiter characters.
				buckets = append(buckets, bucket.New(
					s.namespace, s.serverName,
					"user-tool:"+toolName+":"+userID,
					s.maxTokens, s.refillPeriod,
				))
			}
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

// validateBucketCRD checks that a CRD bucket spec has valid parameters.
func validateBucketCRD(b *rlconfig.Bucket) (int32, time.Duration, error) {
	if b.MaxTokens < 1 {
		return 0, 0, fmt.Errorf("maxTokens must be >= 1, got %d", b.MaxTokens)
	}
	d := b.RefillPeriod.Duration
	if d <= 0 {
		return 0, 0, fmt.Errorf("refillPeriod must be positive, got %s", d)
	}
	return b.MaxTokens, d, nil
}

// newBucket validates a CRD bucket spec and creates a TokenBucket.
func newBucket(namespace, serverName, suffix string, b *rlconfig.Bucket) (*bucket.TokenBucket, error) {
	maxTokens, refillPeriod, err := validateBucketCRD(b)
	if err != nil {
		return nil, err
	}
	return bucket.New(namespace, serverName, suffix, maxTokens, refillPeriod), nil
}

// newBucketSpec validates a CRD bucket spec and creates a deferred bucketSpec
// for per-user buckets that are materialized at Allow() time.
func newBucketSpec(namespace, serverName string, b *rlconfig.Bucket) (bucketSpec, error) {
	maxTokens, refillPeriod, err := validateBucketCRD(b)
	if err != nil {
		return bucketSpec{}, err
	}
	return bucketSpec{
		namespace:    namespace,
		serverName:   serverName,
		maxTokens:    maxTokens,
		refillPeriod: refillPeriod,
	}, nil
}

// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package ratelimit provides token-bucket rate limiting for MCP servers.
//
// The public API consists of the Limiter interface, Decision and
// RejectionSource result types, and NewLimiter constructor. The token bucket
// implementation is internal.
package ratelimit

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"

	v1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/ratelimit/internal/bucket"
)

// Limiter checks rate limits for an MCP server.
type Limiter interface {
	// Allow checks whether a request is permitted.
	// toolName is the MCP tool being called (empty for non-tool requests).
	// userID is the authenticated user (empty for unauthenticated requests).
	Allow(ctx context.Context, toolName, userID string) (*Decision, error)
}

// RejectionSource identifies the rate limit bucket that rejected a request.
type RejectionSource string

const (
	// RejectionSourceSharedServer identifies the server-level shared bucket.
	RejectionSourceSharedServer RejectionSource = "shared_server"
	// RejectionSourceSharedTool identifies a tool-level shared bucket.
	RejectionSourceSharedTool RejectionSource = "shared_tool"
	// RejectionSourcePerUserServer identifies a server-level per-user bucket.
	RejectionSourcePerUserServer RejectionSource = "per_user_server"
	// RejectionSourcePerUserTool identifies a tool-level per-user bucket.
	RejectionSourcePerUserTool RejectionSource = "per_user_tool"
)

// Decision holds the result of a rate limit check.
type Decision struct {
	// Allowed is true when the request may proceed.
	Allowed bool

	// RetryAfter is populated when Allowed is false.
	// It indicates the minimum wait before the next request may succeed.
	RetryAfter time.Duration

	// RejectedBy identifies the first rate limit bucket that rejected the
	// request. It is empty when Allowed is true.
	RejectedBy RejectionSource
}

// Allow checks whether identity may call toolName through limiter.
func Allow(ctx context.Context, limiter Limiter, identity *auth.Identity, toolName string) error {
	if limiter == nil {
		return nil
	}

	// When no identity is present (unauthenticated), userID stays empty and
	// per-user buckets are skipped — only shared limits apply. CEL validation
	// ensures perUser rate limits require auth to be enabled.
	var userID string
	if identity != nil {
		userID = identity.Subject
	}

	decision, err := limiter.Allow(ctx, toolName, userID)
	if err != nil {
		return err
	}
	if !decision.Allowed {
		return &RateLimitedError{
			RetryAfter: decision.RetryAfter,
			RejectedBy: decision.RejectedBy,
		}
	}
	return nil
}

// NewLimiter constructs a Limiter from CRD configuration.
// Returns a no-op limiter (always allows) when crd is nil.
// namespace and name identify the MCP server for Redis key derivation.
func NewLimiter(client redis.Cmdable, namespace, name string, crd *v1beta1.RateLimitConfig) (Limiter, error) {
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

	if crd.PerUser != nil {
		spec, err := newBucketSpec(namespace, name, crd.PerUser)
		if err != nil {
			return nil, fmt.Errorf("perUser bucket: %w", err)
		}
		l.perUserSpec = &spec
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

// bucketSpec holds deferred bucket parameters for per-user buckets that are
// created on the fly in Allow() because the userID is not known at construction time.
type bucketSpec struct {
	namespace    string
	serverName   string
	maxTokens    int32
	refillPeriod time.Duration
}

// limitCheck keeps a bucket paired with the stable identifier used to report
// which check rejected a request.
type limitCheck struct {
	bucket     *bucket.TokenBucket
	rejectedBy RejectionSource
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
	var checks []limitCheck
	if l.serverBucket != nil {
		checks = append(checks, limitCheck{
			bucket:     l.serverBucket,
			rejectedBy: RejectionSourceSharedServer,
		})
	}
	if toolName != "" && l.toolBuckets != nil {
		if tb, ok := l.toolBuckets[toolName]; ok {
			checks = append(checks, limitCheck{
				bucket:     tb,
				rejectedBy: RejectionSourceSharedTool,
			})
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
			checks = append(checks, limitCheck{
				bucket: bucket.New(
					s.namespace, s.serverName,
					"user:"+userID,
					s.maxTokens, s.refillPeriod,
				),
				rejectedBy: RejectionSourcePerUserServer,
			})
		}
		if toolName != "" && l.perUserTools != nil {
			if s, ok := l.perUserTools[toolName]; ok {
				// Key prefix "user-tool:" is distinct from "user:" to prevent
				// collisions when a userID contains delimiter characters.
				checks = append(checks, limitCheck{
					bucket: bucket.New(
						s.namespace, s.serverName,
						"user-tool:"+toolName+":"+userID,
						s.maxTokens, s.refillPeriod,
					),
					rejectedBy: RejectionSourcePerUserTool,
				})
			}
		}
	}

	if len(checks) == 0 {
		return &Decision{Allowed: true}, nil
	}

	buckets := make([]*bucket.TokenBucket, len(checks))
	for i, check := range checks {
		buckets[i] = check.bucket
	}

	rejectedIdx, err := bucket.ConsumeAll(ctx, l.client, buckets)
	if err != nil {
		return nil, fmt.Errorf("rate limit check: %w", err)
	}
	if rejectedIdx >= 0 {
		return &Decision{
			Allowed:    false,
			RetryAfter: buckets[rejectedIdx].RetryAfter(),
			RejectedBy: checks[rejectedIdx].rejectedBy,
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
func validateBucketCRD(b *v1beta1.RateLimitBucket) (int32, time.Duration, error) {
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
func newBucket(namespace, serverName, suffix string, b *v1beta1.RateLimitBucket) (*bucket.TokenBucket, error) {
	maxTokens, refillPeriod, err := validateBucketCRD(b)
	if err != nil {
		return nil, err
	}
	return bucket.New(namespace, serverName, suffix, maxTokens, refillPeriod), nil
}

// newBucketSpec validates a CRD bucket spec and creates a deferred bucketSpec
// for per-user buckets that are materialized at Allow() time.
func newBucketSpec(namespace, serverName string, b *v1beta1.RateLimitBucket) (bucketSpec, error) {
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

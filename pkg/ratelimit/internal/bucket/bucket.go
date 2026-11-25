// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package bucket implements the token bucket algorithm backed by Redis.
package bucket

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/redis/go-redis/v9"
)

// consumeAllScript atomically refills and attempts to consume one token from
// each of N buckets. Each bucket is a Redis hash with "tokens" and
// "last_refill" fields (microsecond precision).
//
// The script checks ALL buckets first, then only consumes from ALL if every
// bucket has sufficient tokens. This prevents draining a server-level bucket
// when a more restrictive per-tool bucket would reject.
//
// KEYS[1..N]   = bucket keys
// ARGV[1]      = number of buckets (N)
// ARGV[2..N+1] = maxTokens for each bucket
// ARGV[N+2..2N+1] = refillRate (tokens/sec, float) for each bucket
// ARGV[2N+2..3N+1] = expireSeconds for each bucket
//
// Returns: 0 if all consumed (allowed), or the 1-based index of the first
// bucket that rejected.
var consumeAllScript = redis.NewScript(`
local numBuckets = tonumber(ARGV[1])

-- Use Redis server clock for consistency across replicas
local timeResp = redis.call('TIME')
local now = tonumber(timeResp[1]) * 1000000 + tonumber(timeResp[2])

-- Phase 1: refill all buckets, check if each has >= 1 token
local refilled = {}
local rejectIdx = 0
for i = 1, numBuckets do
    local key = KEYS[i]
    local maxTokens = tonumber(ARGV[1 + i])
    local refillRate = tonumber(ARGV[1 + numBuckets + i])

    local data = redis.call('HMGET', key, 'tokens', 'last_refill')
    local tokens = tonumber(data[1])
    local lastRefill = tonumber(data[2])

    if tokens == nil then
        refilled[i] = maxTokens
    else
        local elapsedSec = math.max(0, (now - lastRefill) / 1000000)
        refilled[i] = math.min(maxTokens, tokens + elapsedSec * refillRate)
    end

    if refilled[i] < 1 and rejectIdx == 0 then
        rejectIdx = i
    end
end

-- Phase 2: if any bucket rejected, update state without consuming
if rejectIdx ~= 0 then
    for i = 1, numBuckets do
        local key = KEYS[i]
        local expireSec = tonumber(ARGV[1 + 2 * numBuckets + i])
        redis.call('HSET', key, 'tokens', refilled[i], 'last_refill', now)
        redis.call('EXPIRE', key, expireSec)
    end
    return rejectIdx
end

-- Phase 3: all buckets have tokens — consume one from each
for i = 1, numBuckets do
    local key = KEYS[i]
    local expireSec = tonumber(ARGV[1 + 2 * numBuckets + i])
    redis.call('HSET', key, 'tokens', refilled[i] - 1, 'last_refill', now)
    redis.call('EXPIRE', key, expireSec)
end
return 0
`)

// TokenBucket represents a single rate limit bucket backed by Redis.
type TokenBucket struct {
	key           string
	maxTokens     int32
	refillRate    float64 // tokens per second
	expireSeconds int64   // ceil(2 * refillPeriod) in seconds
}

// New creates a TokenBucket. The Redis key is derived from namespace, server
// name, and suffix (e.g., "shared" or "shared:tool:search").
func New(namespace, serverName, suffix string, maxTokens int32, refillPeriod time.Duration) *TokenBucket {
	refillSec := refillPeriod.Seconds()
	return &TokenBucket{
		key:           deriveKeyPrefix(namespace, serverName) + suffix,
		maxTokens:     maxTokens,
		refillRate:    float64(maxTokens) / refillSec,
		expireSeconds: int64(math.Ceil(2 * refillSec)),
	}
}

// deriveKeyPrefix creates the rate limit key prefix for a given namespace and server.
// Format: "thv:rl:{ns:name}:" where {ns:name} is a Redis hash tag ensuring all keys
// for a server land on the same Redis Cluster slot.
func deriveKeyPrefix(namespace, name string) string {
	return fmt.Sprintf("thv:rl:{%s:%s}:", namespace, name)
}

// ConsumeAll atomically checks and consumes one token from each bucket.
// All buckets are checked first; tokens are only consumed if ALL buckets
// have sufficient tokens.
//
// Returns the index of the first bucket that rejected (0-based), or -1 if
// all allowed.
func ConsumeAll(ctx context.Context, client redis.Cmdable, buckets []*TokenBucket) (int, error) {
	if len(buckets) == 0 {
		return -1, nil
	}

	keys := make([]string, len(buckets))
	// ARGV layout: [numBuckets, maxTokens..., refillRates..., expireSeconds...]
	args := make([]any, 1+3*len(buckets))
	args[0] = len(buckets)
	for i, b := range buckets {
		keys[i] = b.key
		args[1+i] = b.maxTokens
		args[1+len(buckets)+i] = fmt.Sprintf("%.6f", b.refillRate)
		args[1+2*len(buckets)+i] = b.expireSeconds
	}

	result, err := consumeAllScript.Run(ctx, client, keys, args...).Int64()
	if err != nil {
		return -1, fmt.Errorf("rate limit script error: %w", err)
	}
	if result == 0 {
		return -1, nil // all allowed
	}
	return int(result) - 1, nil // convert 1-based to 0-based index
}

// RetryAfter returns the minimum wait time for one token to become available.
func (b *TokenBucket) RetryAfter() time.Duration {
	d := time.Duration(float64(time.Second) / b.refillRate)
	// Round up to nearest second (minimum 1s) for Retry-After header
	if d < time.Second {
		return time.Second
	}
	return time.Duration(math.Ceil(d.Seconds())) * time.Second
}

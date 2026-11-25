// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package toolstore

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/stacklok/toolhive-core/redisconn"
)

// EmbeddingCache holds encoded tool embeddings outside the process, keyed by
// embedding cache key (see embeddingCacheKey). A key names the embedded text
// and the backend identity, so its vector is valid wherever it was computed.
type EmbeddingCache interface {
	// GetEmbeddings returns the stored vectors among keys; absent keys are
	// omitted. A value longer than maxSharedEmbeddingBytes may be returned
	// truncated, one byte past the cap.
	GetEmbeddings(ctx context.Context, keys []string) (map[string][]byte, error)
	// PutEmbeddings stores each vector under its key.
	PutEmbeddings(ctx context.Context, entries map[string][]byte) error
	// Close releases the cache's connections. It may be called more than once.
	Close() error
}

// sharedEmbeddingTTL only reclaims space from tools and models no replica
// serves any more: a vector never goes stale under its key.
const sharedEmbeddingTTL = 7 * 24 * time.Hour

// maxSharedEmbeddingBytes caps what a lookup reads of one shared value: 16,384
// float32 dimensions, four times the widest common embedding model. A longer
// value is not a vector this store wrote, and a lookup reads one byte past the
// cap, however large the value is, so the store can reject it.
const maxSharedEmbeddingBytes = 64 << 10

type redisEmbeddingCache struct {
	client    redis.UniversalClient
	keyPrefix string
}

// NewRedisEmbeddingCache connects to Redis and returns an EmbeddingCache that
// stores each vector as a raw value under keyPrefix + "optimizer:embedding:" +
// key. keyPrefix must end with ':'. The caller must call Close when done.
func NewRedisEmbeddingCache(ctx context.Context, cfg redisconn.Config, keyPrefix string) (EmbeddingCache, error) {
	if !strings.HasSuffix(keyPrefix, ":") {
		return nil, fmt.Errorf("embedding cache key prefix must end with ':' (got %q)", keyPrefix)
	}
	client, err := redisconn.NewClient(ctx, &cfg)
	if err != nil {
		return nil, err
	}
	return redisEmbeddingCache{client: client, keyPrefix: keyPrefix + "optimizer:embedding:"}, nil
}

// GetEmbeddings pipelines one GETRANGE per key rather than sending one MGET,
// which a Redis Cluster rejects when the keys hash to different slots.
// GETRANGE reads at most maxSharedEmbeddingBytes+1 bytes of a value, so a
// planted oversized value costs no more than that to reject. It reads an
// absent key as empty, so empty values are omitted like absent ones.
func (c redisEmbeddingCache) GetEmbeddings(ctx context.Context, keys []string) (map[string][]byte, error) {
	cmds := make([]*redis.StringCmd, len(keys))
	// Pipelined reports the first failed command, so each command is checked
	// below instead.
	_, _ = c.client.Pipelined(ctx, func(p redis.Pipeliner) error {
		for i, key := range keys {
			cmds[i] = p.GetRange(ctx, c.keyPrefix+key, 0, maxSharedEmbeddingBytes)
		}
		return nil
	})
	found := make(map[string][]byte, len(keys))
	for i, cmd := range cmds {
		blob, err := cmd.Bytes()
		switch {
		case err != nil:
			return nil, fmt.Errorf("shared embedding lookup failed: %w", err)
		case len(blob) > 0:
			found[keys[i]] = blob
		}
	}
	return found, nil
}

// PutEmbeddings writes the entries in one pipeline, each with the shared TTL.
func (c redisEmbeddingCache) PutEmbeddings(ctx context.Context, entries map[string][]byte) error {
	if _, err := c.client.Pipelined(ctx, func(p redis.Pipeliner) error {
		for key, blob := range entries {
			p.Set(ctx, c.keyPrefix+key, blob, sharedEmbeddingTTL)
		}
		return nil
	}); err != nil {
		return fmt.Errorf("shared embedding write failed: %w", err)
	}
	return nil
}

// Close closes the Redis client. Closing it again is not an error.
func (c redisEmbeddingCache) Close() error {
	if err := c.client.Close(); err != nil && !errors.Is(err, redis.ErrClosed) {
		return err
	}
	return nil
}

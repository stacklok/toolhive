// Package cache provides token caching interfaces for Virtual MCP Server.
//
// Token caching reduces authentication overhead by caching exchanged tokens
// with proper TTL management. The package provides pluggable cache backends
// (memory, Redis) through the TokenCache interface.
package cache

import (
	"context"
	"time"
)

// TokenCache provides caching for exchanged authentication tokens.
// This reduces the number of token exchanges and improves performance.
//
// Cache key format: {backend}:{hash(subject_token)}:{audience}
// This ensures proper token isolation per (user, backend) pair.
type TokenCache interface {
	// Get retrieves a cached token.
	// Returns nil if the token doesn't exist or has expired.
	Get(ctx context.Context, key string) (*CachedToken, error)

	// Set stores a token in the cache with TTL.
	Set(ctx context.Context, key string, token *CachedToken) error

	// Delete removes a token from the cache.
	Delete(ctx context.Context, key string) error

	// Clear removes all tokens from the cache.
	Clear(ctx context.Context) error

	// Close closes the cache and releases resources.
	Close() error
}

// CachedToken represents a cached authentication token.
type CachedToken struct {
	// Token is the access token value.
	Token string

	// TokenType is the token type (e.g., "Bearer").
	TokenType string

	// ExpiresAt is when the token expires.
	ExpiresAt time.Time

	// RefreshToken is the refresh token (if available).
	RefreshToken string

	// Scopes are the token scopes.
	Scopes []string

	// Metadata stores additional token information.
	Metadata map[string]string
}

// IsExpired checks if the token has expired.
func (t *CachedToken) IsExpired() bool {
	return time.Now().After(t.ExpiresAt)
}

// ShouldRefresh checks if the token should be refreshed.
// Tokens should be refreshed before they expire.
func (t *CachedToken) ShouldRefresh(offset time.Duration) bool {
	return time.Now().After(t.ExpiresAt.Add(-offset))
}

// KeyBuilder builds cache keys for tokens.
type KeyBuilder interface {
	// BuildKey creates a cache key for a token.
	// Inputs:
	//   - backend: Backend identifier
	//   - subjectToken: User's authentication token (will be hashed)
	//   - audience: Requested token audience
	BuildKey(backend string, subjectToken string, audience string) string
}

// Stats provides cache statistics.
type Stats struct {
	// Hits is the number of cache hits.
	Hits int64

	// Misses is the number of cache misses.
	Misses int64

	// Evictions is the number of evicted entries.
	Evictions int64

	// Size is the current cache size.
	Size int

	// MaxSize is the maximum cache size.
	MaxSize int
}

// StatsProvider provides cache statistics.
type StatsProvider interface {
	// Stats returns current cache statistics.
	Stats(ctx context.Context) (*Stats, error)
}

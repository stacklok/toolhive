// Package discovery provides lazy per-user capability discovery for vMCP servers.
//
// This package implements per-request capability discovery with user-specific
// authentication context, enabling truly multi-tenant operation where different
// users may see different capabilities based on their permissions.
package discovery

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/logger"
	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/aggregator"
)

//go:generate mockgen -destination=mocks/mock_manager.go -package=mocks -source=manager.go Manager

const (
	// cacheTTL is the time-to-live for cached capability entries.
	cacheTTL = 5 * time.Minute
	// maxCacheSize is the maximum number of entries allowed in the cache.
	maxCacheSize = 1000
	// cleanupInterval is how often expired cache entries are removed.
	cleanupInterval = 1 * time.Minute
)

var (
	// ErrAggregatorNil is returned when aggregator is nil.
	ErrAggregatorNil = errors.New("aggregator cannot be nil")
	// ErrDiscoveryFailed is returned when capability discovery fails.
	ErrDiscoveryFailed = errors.New("capability discovery failed")
	// ErrNoIdentity is returned when user identity is not found in context.
	ErrNoIdentity = errors.New("user identity not found in context")
)

// Manager performs capability discovery with user context.
type Manager interface {
	// Discover performs capability aggregation for the given backends with user context.
	Discover(ctx context.Context, backends []vmcp.Backend) (*aggregator.AggregatedCapabilities, error)
	// Stop gracefully stops the manager and cleans up resources.
	Stop()
}

// cacheEntry represents a cached capability discovery result.
type cacheEntry struct {
	capabilities *aggregator.AggregatedCapabilities
	expiresAt    time.Time
}

// DefaultManager is the default implementation of Manager.
type DefaultManager struct {
	aggregator aggregator.Aggregator
	cache      map[string]*cacheEntry
	cacheMu    sync.RWMutex
	stopCh     chan struct{}
	stopOnce   sync.Once
	wg         sync.WaitGroup
}

// NewManager creates a new discovery manager with the given aggregator.
func NewManager(agg aggregator.Aggregator) (Manager, error) {
	if agg == nil {
		return nil, ErrAggregatorNil
	}

	m := &DefaultManager{
		aggregator: agg,
		cache:      make(map[string]*cacheEntry),
		stopCh:     make(chan struct{}),
	}

	// Start background cleanup goroutine
	m.wg.Add(1)
	go m.cleanupExpiredEntries()

	return m, nil
}

// Discover performs capability aggregation with per-user caching.
// Results are cached by (user, backend-set) combination for improved performance.
//
// The context must contain an authenticated user identity (set by auth middleware).
// Returns ErrNoIdentity if user identity is not found in context.
func (m *DefaultManager) Discover(ctx context.Context, backends []vmcp.Backend) (*aggregator.AggregatedCapabilities, error) {
	// Validate user identity is present (set by auth middleware)
	// This ensures discovery happens with proper user authentication context
	identity, ok := auth.IdentityFromContext(ctx)
	if !ok {
		return nil, fmt.Errorf("%w: ensure auth middleware runs before discovery middleware", ErrNoIdentity)
	}

	// Generate cache key from user identity and backend set
	cacheKey := m.generateCacheKey(identity.Subject, backends)

	// Check cache first
	if caps := m.getCachedCapabilities(cacheKey); caps != nil {
		logger.Debugf("Cache hit for user %s (key: %s)", identity.Subject, cacheKey)
		return caps, nil
	}

	logger.Debugf("Cache miss - performing capability discovery for user: %s", identity.Subject)

	// Cache miss - perform aggregation
	caps, err := m.aggregator.AggregateCapabilities(ctx, backends)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrDiscoveryFailed, err)
	}

	// Cache the result (skips caching if at capacity and key doesn't exist)
	m.cacheCapabilities(cacheKey, caps)

	return caps, nil
}

// Stop gracefully stops the manager and cleans up resources.
// This method is safe to call multiple times.
func (m *DefaultManager) Stop() {
	m.stopOnce.Do(func() {
		close(m.stopCh)
	})
	m.wg.Wait()
}

// generateCacheKey creates a cache key from user ID and backend set.
// The key format is: userID:hash(sorted-backend-ids)
func (*DefaultManager) generateCacheKey(userID string, backends []vmcp.Backend) string {
	// Extract and sort backend IDs for stable hashing
	backendIDs := make([]string, len(backends))
	for i, b := range backends {
		backendIDs[i] = b.ID
	}
	sort.Strings(backendIDs)

	// Hash the sorted backend IDs
	h := sha256.New()
	for _, id := range backendIDs {
		h.Write([]byte(id))
		h.Write([]byte{0}) // Separator to avoid collisions
	}
	backendHash := hex.EncodeToString(h.Sum(nil))

	return fmt.Sprintf("%s:%s", userID, backendHash)
}

// getCachedCapabilities retrieves capabilities from cache if valid and not expired.
func (m *DefaultManager) getCachedCapabilities(key string) *aggregator.AggregatedCapabilities {
	m.cacheMu.RLock()
	defer m.cacheMu.RUnlock()

	entry, ok := m.cache[key]
	if !ok {
		return nil
	}

	// Check if entry has expired
	if time.Now().After(entry.expiresAt) {
		return nil
	}

	return entry.capabilities
}

// cacheCapabilities stores capabilities in cache if under size limit.
func (m *DefaultManager) cacheCapabilities(key string, caps *aggregator.AggregatedCapabilities) {
	m.cacheMu.Lock()
	defer m.cacheMu.Unlock()

	// Simple eviction: reject caching when at capacity
	if len(m.cache) >= maxCacheSize {
		_, exists := m.cache[key]
		if !exists {
			logger.Debugf("Cache at capacity (%d entries), not caching new entry", maxCacheSize)
			return
		}
	}

	m.cache[key] = &cacheEntry{
		capabilities: caps,
		expiresAt:    time.Now().Add(cacheTTL),
	}
}

// cleanupExpiredEntries periodically removes expired cache entries.
func (m *DefaultManager) cleanupExpiredEntries() {
	defer m.wg.Done()

	ticker := time.NewTicker(cleanupInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			m.removeExpiredEntries()
		case <-m.stopCh:
			return
		}
	}
}

// removeExpiredEntries removes all expired entries from the cache.
func (m *DefaultManager) removeExpiredEntries() {
	m.cacheMu.Lock()
	defer m.cacheMu.Unlock()

	now := time.Now()
	removed := 0

	for key, entry := range m.cache {
		if now.After(entry.expiresAt) {
			delete(m.cache, key)
			removed++
		}
	}

	if removed > 0 {
		logger.Debugf("Removed %d expired cache entries (%d remaining)", removed, len(m.cache))
	}
}

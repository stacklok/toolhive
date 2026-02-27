// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

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
	"log/slog"
	"sort"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/stacklok/toolhive/pkg/auth"
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
	capabilities    *aggregator.AggregatedCapabilities
	expiresAt       time.Time
	registryVersion uint64 // Version when this entry was computed
}

// DefaultManager is the default implementation of Manager.
type DefaultManager struct {
	aggregator aggregator.Aggregator
	registry   vmcp.DynamicRegistry // Optional: enables version-based cache invalidation
	cache      map[string]*cacheEntry
	cacheMu    sync.RWMutex
	stopCh     chan struct{}
	stopOnce   sync.Once
	wg         sync.WaitGroup
	// singleFlight ensures only one aggregation happens per cache key at a time
	// This prevents concurrent requests from all triggering aggregation
	singleFlight singleflight.Group
}

// NewManager creates a new discovery manager with the given aggregator.
// For static backends (immutable registry), use this constructor.
func NewManager(agg aggregator.Aggregator) (Manager, error) {
	if agg == nil {
		return nil, ErrAggregatorNil
	}

	m := &DefaultManager{
		aggregator: agg,
		registry:   nil, // No version-based invalidation for static mode
		cache:      make(map[string]*cacheEntry),
		stopCh:     make(chan struct{}),
	}

	// Start background cleanup goroutine
	m.wg.Add(1)
	go m.cleanupExpiredEntries()

	return m, nil
}

// NewManagerWithRegistry creates a new discovery manager with version-based cache invalidation.
// For dynamic backends (DynamicRegistry), use this constructor to enable lazy cache invalidation
// when backends change.
//
// Parameters:
//   - agg: The aggregator to use for capability discovery
//   - registry: The dynamic registry to track version changes (can be nil for static mode)
//
// Returns:
//   - Manager: The discovery manager instance
//   - error: Returns ErrAggregatorNil if aggregator is nil
//
// Example:
//
//	registry := vmcp.NewDynamicRegistry(nil)
//	manager, err := discovery.NewManagerWithRegistry(agg, registry)
func NewManagerWithRegistry(agg aggregator.Aggregator, registry vmcp.DynamicRegistry) (Manager, error) {
	if agg == nil {
		return nil, ErrAggregatorNil
	}

	m := &DefaultManager{
		aggregator: agg,
		registry:   registry, // Enables version-based cache invalidation
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
//
// This method uses singleflight to ensure that concurrent requests for the same
// cache key only trigger one aggregation, preventing duplicate work.
func (m *DefaultManager) Discover(ctx context.Context, backends []vmcp.Backend) (*aggregator.AggregatedCapabilities, error) {
	// Validate user identity is present (set by auth middleware)
	// This ensures discovery happens with proper user authentication context
	identity, ok := auth.IdentityFromContext(ctx)
	if !ok {
		return nil, fmt.Errorf("%w: ensure auth middleware runs before discovery middleware", ErrNoIdentity)
	}

	// Generate cache key from user identity and backend set
	cacheKey := m.generateCacheKey(identity.Subject, backends)

	// Check cache first (with read lock)
	if caps := m.getCachedCapabilities(cacheKey); caps != nil {
		//nolint:gosec // G706: identity.Subject and cacheKey are internal identifiers for diagnostics
		slog.Debug("cache hit for user", "user", identity.Subject, "key", cacheKey)
		return caps, nil
	}

	slog.Debug("cache miss - performing capability discovery", "user", identity.Subject)

	// Use singleflight to ensure only one aggregation happens per cache key
	// Even if multiple requests come in concurrently, they'll all wait for the same result
	result, err, _ := m.singleFlight.Do(cacheKey, func() (interface{}, error) {
		// Double-check cache after acquiring singleflight lock
		// Another goroutine might have populated it while we were waiting
		if caps := m.getCachedCapabilities(cacheKey); caps != nil {
			logger.Debugf("Cache populated while waiting - returning cached result for user %s", identity.Subject)
			return caps, nil
		}

		// Perform aggregation
		caps, err := m.aggregator.AggregateCapabilities(ctx, backends)
		if err != nil {
			return nil, fmt.Errorf("%w: %w", ErrDiscoveryFailed, err)
		}

		// Cache the result (skips caching if at capacity and key doesn't exist)
		m.cacheCapabilities(cacheKey, caps)

		return caps, nil
	})

	if err != nil {
		return nil, err
	}

	return result.(*aggregator.AggregatedCapabilities), nil
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

// getCachedCapabilities retrieves capabilities from cache if valid, not expired,
// and registry version matches (for dynamic registries).
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

	// Check registry version if using dynamic registry
	// Cache is stale if registry version changed (lazy invalidation)
	if m.registry != nil {
		currentVersion := m.registry.Version()
		if entry.registryVersion != currentVersion {
			//nolint:gosec // G706: version numbers are internal metadata for diagnostics
			slog.Debug("cache entry stale", "current_version", currentVersion, "entry_version", entry.registryVersion)
			return nil
		}
	}

	return entry.capabilities
}

// cacheCapabilities stores capabilities in cache if under size limit.
// Tags the entry with the current registry version for lazy invalidation.
func (m *DefaultManager) cacheCapabilities(key string, caps *aggregator.AggregatedCapabilities) {
	m.cacheMu.Lock()
	defer m.cacheMu.Unlock()

	// Simple eviction: reject caching when at capacity
	if len(m.cache) >= maxCacheSize {
		_, exists := m.cache[key]
		if !exists {
			slog.Debug("cache at capacity, not caching new entry", "capacity", maxCacheSize)
			return
		}
	}

	// Get current registry version if available
	var registryVersion uint64
	if m.registry != nil {
		registryVersion = m.registry.Version()
	}

	m.cache[key] = &cacheEntry{
		capabilities:    caps,
		expiresAt:       time.Now().Add(cacheTTL),
		registryVersion: registryVersion,
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
		slog.Debug("removed expired cache entries", "removed", removed, "remaining", len(m.cache))
	}
}

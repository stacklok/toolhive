package vmcp

import (
	"context"
	"fmt"
	"sync"
)

// BackendRegistry provides thread-safe access to discovered backends.
// This is a shared kernel interface used across vmcp bounded contexts
// (aggregator, router, health monitoring).
//
// The registry serves as the single source of truth for backend information
// during the lifecycle of a virtual MCP server instance. It supports both
// immutable (Phase 1) and mutable (future phases) implementations.
//
// Design Philosophy:
//   - Phase 1: Immutable registry (backends discovered once, never change)
//   - Future: Mutable registry with health monitoring and dynamic updates
//   - Thread-safe for concurrent reads across all implementations
//   - Implementations may support concurrent writes with appropriate locking
//
//go:generate mockgen -destination=mocks/mock_registry.go -package=mocks -source=registry.go BackendRegistry DynamicRegistry
type BackendRegistry interface {
	// Get retrieves a backend by ID.
	// Returns nil if the backend is not found.
	// This method is safe for concurrent reads.
	//
	// Example:
	//   backend := registry.Get(ctx, "github-mcp")
	//   if backend == nil {
	//       return fmt.Errorf("backend not found")
	//   }
	Get(ctx context.Context, backendID string) *Backend

	// List returns all registered backends.
	// The returned slice is a snapshot and safe to iterate without additional locking.
	// Order is not guaranteed unless specified by the implementation.
	//
	// Example:
	//   backends := registry.List(ctx)
	//   for _, backend := range backends {
	//       fmt.Printf("Backend: %s\n", backend.Name)
	//   }
	List(ctx context.Context) []Backend

	// Count returns the number of registered backends.
	// This is more efficient than len(List()) for large registries.
	Count() int
}

// immutableRegistry is a Phase 1 implementation that stores a static list
// of backends discovered at startup. It's thread-safe for concurrent reads
// and never changes after construction.
//
// Use NewImmutableRegistry() to create instances.
type immutableRegistry struct {
	// backends maps backend ID to backend information.
	// This map is built once at construction and never modified.
	backends map[string]Backend
}

// NewImmutableRegistry creates a registry from a static list of backends.
//
// This implementation is used in Phase 1 where backends are discovered once
// at startup and don't change during the virtual MCP server's lifetime.
// The registry is thread-safe for concurrent reads.
//
// Parameters:
//   - backends: List of discovered backends to register
//
// Returns:
//   - BackendRegistry: An immutable registry instance
//
// Example:
//
//	backends := discoverer.Discover(ctx, "engineering-team")
//	registry := vmcp.NewImmutableRegistry(backends)
//	backend := registry.Get(ctx, "github-mcp")
func NewImmutableRegistry(backends []Backend) BackendRegistry {
	reg := &immutableRegistry{
		backends: make(map[string]Backend, len(backends)),
	}
	for _, b := range backends {
		reg.backends[b.ID] = b
	}
	return reg
}

// Get retrieves a backend by ID from the immutable registry.
// Returns nil if the backend is not found.
func (r *immutableRegistry) Get(_ context.Context, backendID string) *Backend {
	if b, exists := r.backends[backendID]; exists {
		// Return a copy to prevent external modifications
		return &b
	}
	return nil
}

// List returns all registered backends as a slice.
// The order is not guaranteed. The returned slice is a copy and safe to modify.
func (r *immutableRegistry) List(_ context.Context) []Backend {
	backends := make([]Backend, 0, len(r.backends))
	for _, b := range r.backends {
		backends = append(backends, b)
	}
	return backends
}

// Count returns the number of registered backends.
func (r *immutableRegistry) Count() int {
	return len(r.backends)
}

// DynamicRegistry extends BackendRegistry with mutable operations.
// This implementation supports thread-safe backend updates with version-based
// cache invalidation for dynamic discovery mode.
//
// The registry maintains a monotonic version counter that increments on every
// mutation (Upsert/Remove). This enables lazy cache invalidation in the
// discovery manager without thundering herd problems.
//
// Design Philosophy:
//   - Thread-safe for concurrent reads and writes using RWMutex
//   - Idempotent operations (Upsert/Remove safe to call multiple times)
//   - Version-based cache invalidation (no event callbacks needed)
//   - Used in dynamic discovery mode for K8s-aware vMCP servers
type DynamicRegistry interface {
	BackendRegistry

	// Upsert adds or updates a backend atomically.
	// Idempotent - calling with the same backend multiple times is safe.
	// Increments Version() on every call.
	//
	// Parameters:
	//   - backend: The backend to add or update
	//
	// Returns:
	//   - error: Returns error if backend has empty ID
	//
	// Example:
	//   err := registry.Upsert(vmcp.Backend{
	//       ID: "github-mcp",
	//       Name: "GitHub MCP",
	//       BaseURL: "http://github-mcp.default.svc.cluster.local:8080",
	//   })
	Upsert(backend Backend) error

	// Remove deletes a backend by ID.
	// Idempotent - removing non-existent backends returns nil.
	// Increments Version() on every call.
	//
	// Parameters:
	//   - backendID: The ID of the backend to remove
	//
	// Returns:
	//   - error: Always returns nil (operation is always successful)
	//
	// Example:
	//   err := registry.Remove("github-mcp")
	Remove(backendID string) error

	// Version returns the current registry version.
	// Increments on every mutation (Upsert/Remove).
	// Used for cache invalidation in discovery manager.
	//
	// Returns:
	//   - uint64: Monotonic version counter
	//
	// Example:
	//   version := registry.Version()
	//   // Cache entries tagged with this version
	Version() uint64
}

// dynamicRegistry is a mutable implementation that supports thread-safe
// backend updates with version tracking for cache invalidation.
//
// Use NewDynamicRegistry() to create instances.
type dynamicRegistry struct {
	mu       sync.RWMutex
	backends map[string]Backend
	version  uint64
}

// NewDynamicRegistry creates a new mutable registry with optional initial backends.
//
// This implementation is used in dynamic discovery mode where backends can change
// during the virtual MCP server's lifetime (e.g., K8s-aware vMCP servers).
// The registry is thread-safe for concurrent reads and writes.
//
// Version Tracking:
// The registry starts at version 0 regardless of the number of initial backends.
// Initial backends are considered the baseline state, not mutations. This ensures:
//   - Cache coherence: Discovery manager caches capabilities with version 0 at startup
//   - Consistency: All servers starting with same backends have same initial version
//   - Predictability: Version only increments for runtime changes (Upsert/Remove)
//
// Version increments only occur when backends are added/removed AFTER initialization,
// which triggers cache invalidation in the discovery manager.
//
// Parameters:
//   - backends: Optional list of initial backends to register
//
// Returns:
//   - DynamicRegistry: A mutable registry instance starting at version 0
//
// Example:
//
//	// Start with 2 backends, version = 0
//	registry := vmcp.NewDynamicRegistry([]Backend{backend1, backend2})
//	// Add a third backend, version = 1 (triggers cache invalidation)
//	err := registry.Upsert(backend3)
func NewDynamicRegistry(backends []Backend) DynamicRegistry {
	reg := &dynamicRegistry{
		backends: make(map[string]Backend),
		version:  0, // Initial state is version 0, regardless of backend count
	}
	for _, b := range backends {
		// Store by value - Go will automatically make a copy when storing in the map
		reg.backends[b.ID] = b
	}
	return reg
}

// Get retrieves a backend by ID from the dynamic registry.
// Returns nil if the backend is not found.
// Thread-safe for concurrent access.
func (r *dynamicRegistry) Get(_ context.Context, backendID string) *Backend {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if b, exists := r.backends[backendID]; exists {
		// Go automatically makes a copy when retrieving from map of values
		return &b
	}
	return nil
}

// List returns all registered backends as a slice.
// The order is not guaranteed. The returned slice is a copy and safe to modify.
// Thread-safe for concurrent access.
func (r *dynamicRegistry) List(_ context.Context) []Backend {
	r.mu.RLock()
	defer r.mu.RUnlock()

	backends := make([]Backend, 0, len(r.backends))
	for _, b := range r.backends {
		// Go automatically makes a copy when iterating over map of values
		backends = append(backends, b)
	}
	return backends
}

// Count returns the number of registered backends.
// Thread-safe for concurrent access.
func (r *dynamicRegistry) Count() int {
	r.mu.RLock()
	defer r.mu.RUnlock()

	return len(r.backends)
}

// Upsert adds or updates a backend atomically.
//
// Idempotent: Calling with the same backend multiple times is safe (no error).
// Version Behavior: Increments version on EVERY call, even if backend data is identical.
//
// Design Trade-off:
// This implementation prioritizes simplicity over cache efficiency by always incrementing
// the version. This means identical updates will trigger cache invalidation.
//
// Rationale:
//   - Simpler implementation (no deep equality checks)
//   - Conservative correctness (won't miss actual changes)
//   - Version semantics: tracks mutations (calls), not changes (data diffs)
//
// Impact on Cache Efficiency:
// High-frequency updates with identical data (e.g., health status polling) will cause
// cache invalidation even when nothing changed. For such scenarios, consider:
//  1. Track dynamic state (like health) separately from backend membership
//  2. Implement equality check before incrementing version (if cache efficiency is critical)
//  3. Rate-limit updates at the source to reduce mutation frequency
//
// Example:
//
//	registry.Upsert(backend1)  // version = 1
//	registry.Upsert(backend1)  // identical data, but version = 2 (cache invalidated)
func (r *dynamicRegistry) Upsert(backend Backend) error {
	if backend.ID == "" {
		return fmt.Errorf("backend ID cannot be empty")
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	// Go automatically makes a copy when passing by value and storing in the map
	r.backends[backend.ID] = backend
	r.version++ // Always increment - see design trade-off in godoc

	return nil
}

// Remove deletes a backend by ID atomically.
//
// Idempotent: Removing non-existent backends returns nil (no error).
// Version Behavior: Increments version on EVERY call, even if the backend doesn't exist.
//
// Design Trade-off:
// This implementation prioritizes simplicity and consistency over cache efficiency by always
// incrementing the version. This means duplicate removals will trigger cache invalidation.
//
// Rationale:
//   - Consistent semantics with Upsert (both always increment)
//   - Simpler implementation (no existence checks needed)
//   - Conservative correctness (won't miss actual changes)
//   - Predictable behavior (version always tracks operation calls, not state changes)
//
// Impact on Cache Efficiency:
// If the K8s watcher or reconciliation loop calls Remove multiple times for the same backend
// (e.g., due to event replays or duplicate notifications), each call will increment the version
// and invalidate cached capabilities even though the registry state hasn't changed.
//
// For scenarios where duplicate removals are common, consider:
//  1. Deduplicate remove operations at the watcher level
//  2. Check existence before calling Remove (if cache efficiency is critical)
//  3. Implement equality check before incrementing version (adds complexity)
//
// Example:
//
//	registry.Remove("backend1")  // backend exists, version = 1
//	registry.Remove("backend1")  // backend already gone, but version = 2 (cache invalidated)
func (r *dynamicRegistry) Remove(backendID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	delete(r.backends, backendID)
	r.version++

	return nil
}

// Version returns the current registry version.
// Increments on every mutation (Upsert/Remove).
// Thread-safe for concurrent access.
func (r *dynamicRegistry) Version() uint64 {
	r.mu.RLock()
	defer r.mu.RUnlock()

	return r.version
}

// BackendToTarget converts a Backend to a BackendTarget for routing.
// This helper is used when populating routing tables during capability aggregation.
//
// The BackendTarget contains all information needed to forward requests to
// a specific backend workload, including authentication strategy and metadata.
func BackendToTarget(backend *Backend) *BackendTarget {
	if backend == nil {
		return nil
	}

	return &BackendTarget{
		WorkloadID:      backend.ID,
		WorkloadName:    backend.Name,
		BaseURL:         backend.BaseURL,
		TransportType:   backend.TransportType,
		AuthConfig:      backend.AuthConfig,
		SessionAffinity: false, // TODO: Add session affinity support in future phases
		HealthStatus:    backend.HealthStatus,
		Metadata:        backend.Metadata,
	}
}

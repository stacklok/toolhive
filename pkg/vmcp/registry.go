package vmcp

import (
	"context"
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
//go:generate mockgen -destination=mocks/mock_backend_registry.go -package=mocks -source=registry.go BackendRegistry
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
		AuthStrategy:    backend.AuthStrategy,
		AuthMetadata:    backend.AuthMetadata,
		SessionAffinity: false, // TODO: Add session affinity support in future phases
		HealthStatus:    backend.HealthStatus,
		Metadata:        backend.Metadata,
	}
}

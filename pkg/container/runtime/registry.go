// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package runtime

import (
	"fmt"
	"sort"
	"sync"
)

// Registry holds a set of registered runtimes. It is safe for concurrent use.
// Tests should create isolated instances via NewRegistry() rather than using
// the DefaultRegistry, so they can run fully in parallel.
type Registry struct {
	mu       sync.RWMutex
	runtimes map[string]*Info
}

// NewRegistry creates a new, empty Registry.
func NewRegistry() *Registry {
	return &Registry{
		runtimes: make(map[string]*Info),
	}
}

// Register adds a runtime to the registry.
// It panics if info is nil, has an empty name, has a nil initializer,
// or if a runtime with the same name is already registered.
func (r *Registry) Register(info *Info) {
	if info == nil {
		panic("runtime info cannot be nil")
	}
	if info.Name == "" {
		panic("runtime name cannot be empty")
	}
	if info.Initializer == nil {
		panic("runtime initializer cannot be nil")
	}
	if info.Priority < 0 {
		panic("runtime priority must be non-negative")
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.runtimes[info.Name]; exists {
		panic(fmt.Sprintf("runtime already registered: %s", info.Name))
	}
	r.runtimes[info.Name] = info
}

// Get returns a copy of the Info for the given name, or nil if not found.
func (r *Registry) Get(name string) *Info {
	r.mu.RLock()
	defer r.mu.RUnlock()

	info, ok := r.runtimes[name]
	if !ok {
		return nil
	}
	cp := *info
	return &cp
}

// IsRegistered returns true if a runtime with the given name is registered.
func (r *Registry) IsRegistered(name string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()

	_, exists := r.runtimes[name]
	return exists
}

// All returns copies of all registered runtimes as a slice.
func (r *Registry) All() []*Info {
	r.mu.RLock()
	defer r.mu.RUnlock()

	result := make([]*Info, 0, len(r.runtimes))
	for _, info := range r.runtimes {
		cp := *info
		result = append(result, &cp)
	}
	return result
}

// ByPriority returns all registered runtimes sorted by priority (ascending).
// When two runtimes share the same priority, they are sorted by name for
// deterministic ordering.
func (r *Registry) ByPriority() []*Info {
	runtimes := r.All()
	sort.SliceStable(runtimes, func(i, j int) bool {
		if runtimes[i].Priority != runtimes[j].Priority {
			return runtimes[i].Priority < runtimes[j].Priority
		}
		return runtimes[i].Name < runtimes[j].Name
	})
	return runtimes
}

// DefaultRegistry is the global registry used by init()-time self-registration.
// Production code should use this (typically via the package-level convenience
// functions). Tests should create isolated registries with NewRegistry().
var DefaultRegistry = NewRegistry()

// RegisterRuntime registers an Info in the DefaultRegistry.
// This is typically called from an init() function in each runtime package.
func RegisterRuntime(info *Info) {
	DefaultRegistry.Register(info)
}

// GetRegisteredRuntime returns the Info from the DefaultRegistry for the given name.
func GetRegisteredRuntime(name string) *Info {
	return DefaultRegistry.Get(name)
}

// IsRuntimeRegistered returns true if a runtime is registered in the DefaultRegistry.
func IsRuntimeRegistered(name string) bool {
	return DefaultRegistry.IsRegistered(name)
}

// RegisteredRuntimes returns all runtimes from the DefaultRegistry.
func RegisteredRuntimes() []*Info {
	return DefaultRegistry.All()
}

// RegisteredRuntimesByPriority returns all runtimes from the DefaultRegistry
// sorted by priority.
func RegisteredRuntimesByPriority() []*Info {
	return DefaultRegistry.ByPriority()
}

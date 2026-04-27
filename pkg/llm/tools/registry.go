// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package tools

import "sync"

// defaultRegistry is the process-wide registry populated by init() functions
// in each tool file.
var defaultRegistry = &Registry{} //nolint:gochecknoglobals

// Register adds an adapter to the default registry.
// It is intended to be called from init() functions in tool files.
func Register(a Adapter) {
	defaultRegistry.Register(a)
}

// Default returns the default adapter registry.
func Default() *Registry { return defaultRegistry }

// Registry holds a set of tool adapters.
type Registry struct {
	mu       sync.RWMutex
	adapters []Adapter
}

// Register adds an adapter to the registry.
func (r *Registry) Register(a Adapter) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.adapters = append(r.adapters, a)
}

// All returns all registered adapters in registration order.
func (r *Registry) All() []Adapter {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make([]Adapter, len(r.adapters))
	copy(result, r.adapters)
	return result
}

// Detected returns adapters whose Detect() returns true (i.e. tools that are
// installed on the current machine).
func (r *Registry) Detected() []Adapter {
	snapshot := r.All()
	var result []Adapter
	for _, a := range snapshot {
		if a.Detect() {
			result = append(result, a)
		}
	}
	return result
}

// Get returns the adapter with the given name, or nil if not found.
func (r *Registry) Get(name string) Adapter {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, a := range r.adapters {
		if a.Name() == name {
			return a
		}
	}
	return nil
}

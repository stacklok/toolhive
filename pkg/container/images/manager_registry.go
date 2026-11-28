// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package images

import (
	"context"
	"fmt"
	"sort"
	"sync"
)

// ManagerInitializer creates a new ImageManager instance.
type ManagerInitializer func(ctx context.Context) (ImageManager, error)

// ManagerInfo holds registration metadata for a runtime-specific ImageManager.
type ManagerInfo struct {
	// Name matches the runtime name (e.g., "docker", "apple", "kubernetes").
	Name string
	// Priority determines auto-detection order (lowest value tried first),
	// mirroring pkg/container/runtime.Info.Priority.
	Priority int
	// Initializer creates the ImageManager instance for this runtime.
	Initializer ManagerInitializer
	// AutoDetector reports whether this runtime is available on the host.
	// If nil, the runtime is always considered available.
	AutoDetector func() bool
}

// ManagerRegistry holds registered ImageManager factories. Safe for concurrent use.
type ManagerRegistry struct {
	mu       sync.RWMutex
	managers map[string]*ManagerInfo
}

// NewManagerRegistry creates an empty ManagerRegistry (useful for isolated tests).
func NewManagerRegistry() *ManagerRegistry {
	return &ManagerRegistry{
		managers: make(map[string]*ManagerInfo),
	}
}

// Register adds an ImageManager factory to the registry.
func (r *ManagerRegistry) Register(info *ManagerInfo) {
	if info == nil {
		panic("image manager info cannot be nil")
	}
	if info.Name == "" {
		panic("image manager name cannot be empty")
	}
	if info.Initializer == nil {
		panic("image manager initializer cannot be nil")
	}
	if info.Priority < 0 {
		panic("image manager priority must be non-negative")
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.managers[info.Name]; exists {
		panic(fmt.Sprintf("image manager already registered: %s", info.Name))
	}
	r.managers[info.Name] = info
}

// Get returns a copy of the ManagerInfo for name, or nil if not registered.
func (r *ManagerRegistry) Get(name string) *ManagerInfo {
	r.mu.RLock()
	defer r.mu.RUnlock()

	info, ok := r.managers[name]
	if !ok {
		return nil
	}
	cp := *info
	return &cp
}

// ByPriority returns all registered managers sorted by ascending Priority (then Name).
func (r *ManagerRegistry) ByPriority() []*ManagerInfo {
	r.mu.RLock()
	defer r.mu.RUnlock()

	result := make([]*ManagerInfo, 0, len(r.managers))
	for _, info := range r.managers {
		cp := *info
		result = append(result, &cp)
	}

	sort.SliceStable(result, func(i, j int) bool {
		if result[i].Priority != result[j].Priority {
			return result[i].Priority < result[j].Priority
		}
		return result[i].Name < result[j].Name
	})
	return result
}

// DefaultManagerRegistry is the global registry populated by runtime packages via init().
var DefaultManagerRegistry = NewManagerRegistry()

// RegisterManager registers an ImageManager factory in DefaultManagerRegistry.
func RegisterManager(info *ManagerInfo) {
	DefaultManagerRegistry.Register(info)
}

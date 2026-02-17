// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package container provides utilities for managing containers,
// including creating, starting, stopping, and monitoring containers.
package container

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"

	"github.com/stacklok/toolhive/pkg/container/docker"
	"github.com/stacklok/toolhive/pkg/container/runtime"
)

// Factory creates container runtimes with pluggable runtime support
type Factory struct {
	mu       sync.RWMutex
	runtimes map[string]*runtime.Info
}

// NewFactory creates a new container factory seeded from the DefaultRegistry.
// Runtimes register themselves via init() in their respective packages;
// the container/runtimes.go collector file ensures all built-in runtimes are imported.
func NewFactory() *Factory {
	return NewFactoryFromRegistry(runtime.DefaultRegistry)
}

// NewFactoryFromRegistry creates a new container factory seeded from the given registry.
// This is useful for testing with isolated registry instances.
func NewFactoryFromRegistry(reg *runtime.Registry) *Factory {
	f := &Factory{
		runtimes: make(map[string]*runtime.Info),
	}

	for _, info := range reg.All() {
		f.runtimes[info.Name] = info
	}

	return f
}

// Register registers a new runtime with the factory
func (f *Factory) Register(info *runtime.Info) error {
	if info == nil {
		return fmt.Errorf("runtime info cannot be nil")
	}
	if info.Name == "" {
		return fmt.Errorf("runtime name cannot be empty")
	}
	if info.Initializer == nil {
		return fmt.Errorf("runtime initializer cannot be nil")
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	f.runtimes[info.Name] = info
	return nil
}

// Unregister removes a runtime from the factory
func (f *Factory) Unregister(name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.runtimes, name)
}

// GetRuntime retrieves a runtime info by name
func (f *Factory) GetRuntime(name string) (*runtime.Info, bool) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	info, exists := f.runtimes[name]
	return info, exists
}

// ListRuntimes returns all registered runtimes
func (f *Factory) ListRuntimes() map[string]*runtime.Info {
	f.mu.RLock()
	defer f.mu.RUnlock()

	result := make(map[string]*runtime.Info, len(f.runtimes))
	for name, info := range f.runtimes {
		result[name] = info
	}
	return result
}

// ListAvailableRuntimes returns all runtimes that are currently available
func (f *Factory) ListAvailableRuntimes() map[string]*runtime.Info {
	f.mu.RLock()
	defer f.mu.RUnlock()

	result := make(map[string]*runtime.Info)
	for name, info := range f.runtimes {
		if info.AutoDetector == nil || info.AutoDetector() {
			result[name] = info
		}
	}
	return result
}

// Create creates a container runtime
// It first checks the TOOLHIVE_RUNTIME environment variable for a specific runtime,
// otherwise falls back to auto-detection
func (f *Factory) Create(ctx context.Context) (runtime.Runtime, error) {
	return f.CreateWithRuntimeName(ctx, f.getRuntimeFromEnv())
}

// CreateWithRuntimeName creates a container runtime with a specific runtime name
// If runtimeName is empty, it falls back to auto-detection
func (f *Factory) CreateWithRuntimeName(ctx context.Context, runtimeName string) (runtime.Runtime, error) {
	var runtimeInfo *runtime.Info
	var selectedRuntimeName string

	if runtimeName != "" {
		// Use specified runtime
		info, exists := f.GetRuntime(runtimeName)
		if !exists {
			available := f.ListRuntimes()
			var availableNames []string
			for n := range available {
				availableNames = append(availableNames, n)
			}
			return nil, fmt.Errorf("runtime %q not found. Available runtimes: %s",
				runtimeName, strings.Join(availableNames, ", "))
		}

		// Check if the runtime is available
		if info.AutoDetector != nil && !info.AutoDetector() {
			return nil, fmt.Errorf("runtime %q is not available on this system", runtimeName)
		}

		selectedRuntimeName = runtimeName
		runtimeInfo = info
	} else {
		// Auto-detect runtime
		detectedName, detectedInfo := f.autoDetectRuntime()
		if detectedInfo == nil {
			return nil, fmt.Errorf("no available runtime found")
		}
		selectedRuntimeName = detectedName
		runtimeInfo = detectedInfo
	}

	rt, err := runtimeInfo.Initializer(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize %s runtime: %w", selectedRuntimeName, err)
	}

	return rt, nil
}

// NewMonitor creates a new container monitor
func NewMonitor(rt runtime.Runtime, containerName string) runtime.Monitor {
	return docker.NewMonitor(rt, containerName)
}

// CheckRuntimeAvailable checks if any container runtime is available
// and returns a user-friendly error message if none are found
func CheckRuntimeAvailable() error {
	factory := NewFactory()
	available := factory.ListAvailableRuntimes()

	if len(available) == 0 {
		registered := runtime.RegisteredRuntimesByPriority()
		names := make([]string, 0, len(registered))
		for _, r := range registered {
			names = append(names, r.Name)
		}
		return fmt.Errorf("no container runtime available. ToolHive requires a Docker-compatible container runtime "+
			"(Docker, Podman, or Colima) or Kubernetes to run MCP servers. Registered runtimes: [%s]",
			strings.Join(names, ", "))
	}

	return nil
}

// autoDetectRuntime returns the first available runtime based on auto-detection.
// Runtimes are tried in priority order (lowest first); the first one whose
// AutoDetector is nil or returns true is selected.
func (f *Factory) autoDetectRuntime() (string, *runtime.Info) {
	f.mu.RLock()
	ordered := make([]*runtime.Info, 0, len(f.runtimes))
	for _, info := range f.runtimes {
		ordered = append(ordered, info)
	}
	f.mu.RUnlock()

	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].Priority != ordered[j].Priority {
			return ordered[i].Priority < ordered[j].Priority
		}
		return ordered[i].Name < ordered[j].Name
	})

	for _, info := range ordered {
		if info.AutoDetector == nil || info.AutoDetector() {
			return info.Name, info
		}
	}

	return "", nil
}

// Clear removes all registered runtimes
// This is useful for testing or when you want to start with a clean slate
func (f *Factory) Clear() {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.runtimes = make(map[string]*runtime.Info)
}

// getRuntimeFromEnv gets the runtime name from the TOOLHIVE_RUNTIME environment variable
// This is separated for easier testing
func (*Factory) getRuntimeFromEnv() string {
	return strings.TrimSpace(os.Getenv("TOOLHIVE_RUNTIME"))
}

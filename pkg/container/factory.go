// Package container provides utilities for managing containers,
// including creating, starting, stopping, and monitoring containers.
package container

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/stacklok/toolhive/pkg/container/docker"
	"github.com/stacklok/toolhive/pkg/container/kubernetes"
	"github.com/stacklok/toolhive/pkg/container/runtime"
)

// RuntimeInitializer is a function that creates a new runtime instance
type RuntimeInitializer func(ctx context.Context) (runtime.Runtime, error)

// RuntimeInfo contains metadata about a runtime
type RuntimeInfo struct {
	// Name is the runtime name (e.g., "docker", "kubernetes")
	Name string
	// Initializer is the function to create the runtime instance
	Initializer RuntimeInitializer
	// AutoDetector is an optional function to detect if this runtime is available
	// If nil, the runtime is always considered available
	AutoDetector func() bool
}

// Factory creates container runtimes with pluggable runtime support
type Factory struct {
	mu       sync.RWMutex
	runtimes map[string]*RuntimeInfo
}

// NewFactory creates a new container factory with default runtimes registered
func NewFactory() *Factory {
	f := &Factory{
		runtimes: make(map[string]*RuntimeInfo),
	}

	// Register default runtimes
	f.registerDefaultRuntimes()

	return f
}

// registerDefaultRuntimes registers the built-in docker and kubernetes runtimes
func (f *Factory) registerDefaultRuntimes() {
	// Register Docker runtime
	f.Register(&RuntimeInfo{ //nolint:gosec // Built-in runtime registration cannot fail
		Name: docker.RuntimeName,
		Initializer: func(ctx context.Context) (runtime.Runtime, error) {
			return docker.NewClient(ctx)
		},
		AutoDetector: func() bool {
			// Check if Docker daemon is actually available
			return docker.IsAvailable()
		},
	})

	// Register Kubernetes runtime
	f.Register(&RuntimeInfo{ //nolint:gosec // Built-in runtime registration cannot fail
		Name: kubernetes.RuntimeName,
		Initializer: func(ctx context.Context) (runtime.Runtime, error) {
			return kubernetes.NewClient(ctx)
		},
		AutoDetector: func() bool {
			// Kubernetes is available if we're in a Kubernetes environment
			return runtime.IsKubernetesRuntime()
		},
	})
}

// Register registers a new runtime with the factory
func (f *Factory) Register(info *RuntimeInfo) error {
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
func (f *Factory) GetRuntime(name string) (*RuntimeInfo, bool) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	info, exists := f.runtimes[name]
	return info, exists
}

// ListRuntimes returns all registered runtimes
func (f *Factory) ListRuntimes() map[string]*RuntimeInfo {
	f.mu.RLock()
	defer f.mu.RUnlock()

	result := make(map[string]*RuntimeInfo, len(f.runtimes))
	for name, info := range f.runtimes {
		result[name] = info
	}
	return result
}

// ListAvailableRuntimes returns all runtimes that are currently available
func (f *Factory) ListAvailableRuntimes() map[string]*RuntimeInfo {
	f.mu.RLock()
	defer f.mu.RUnlock()

	result := make(map[string]*RuntimeInfo)
	for name, info := range f.runtimes {
		if info.AutoDetector == nil || info.AutoDetector() {
			result[name] = info
		}
	}
	return result
}

// autoDetectRuntime returns the first available runtime based on auto-detection
// This checks runtimes in a predictable order: Docker first, then Kubernetes
func (f *Factory) autoDetectRuntime() (string, *RuntimeInfo) {
	available := f.ListAvailableRuntimes()

	// Define the preferred order of runtime detection
	preferredOrder := []string{
		docker.RuntimeName,     // "docker"
		kubernetes.RuntimeName, // "kubernetes"
	}

	// Check runtimes in the preferred order
	for _, runtimeName := range preferredOrder {
		if info, exists := available[runtimeName]; exists {
			return runtimeName, info
		}
	}

	// Fallback: if none of the preferred runtimes are available,
	// return any other available runtime (for extensibility)
	for name, info := range available {
		return name, info
	}

	return "", nil
}

// Clear removes all registered runtimes
// This is useful for testing or when you want to start with a clean slate
func (f *Factory) Clear() {
	f.mu.Lock()
	defer f.mu.Unlock()

	// Clear all runtimes
	f.runtimes = make(map[string]*RuntimeInfo)
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
	var runtimeInfo *RuntimeInfo
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

// getRuntimeFromEnv gets the runtime name from the TOOLHIVE_RUNTIME environment variable
// This is separated for easier testing
func (*Factory) getRuntimeFromEnv() string {
	return strings.TrimSpace(os.Getenv("TOOLHIVE_RUNTIME"))
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
		return fmt.Errorf("no container runtime available. ToolHive requires Docker, Podman, Colima, " +
			"or a Kubernetes environment to run MCP servers")
	}

	return nil
}

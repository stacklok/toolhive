package state

import (
	"github.com/stacklok/toolhive/pkg/container/runtime"
)

const (
	// RunConfigsDir is the directory name for storing run configurations
	RunConfigsDir = "runconfigs"

	// GroupConfigsDir is the directory name for storing group configurations
	GroupConfigsDir = "groups"
)

// NewRunConfigStore creates a local store for run configuration state
func NewRunConfigStore(appName string) (Store, error) {
	return NewRunConfigStoreWithDetector(appName, nil)
}

// NewRunConfigStoreWithDetector creates a local store (detector parameter ignored)
func NewRunConfigStoreWithDetector(appName string, _ any) (Store, error) {
	if runtime.IsKubernetesRuntime() {
		return nil, nil // No store needed in Kubernetes environments
	}
	return NewLocalStore(appName, RunConfigsDir)
}

// NewGroupConfigStore creates a local store for group configurations
func NewGroupConfigStore(appName string) (Store, error) {
	return NewGroupConfigStoreWithDetector(appName, nil)
}

// NewGroupConfigStoreWithDetector creates a local store (detector parameter ignored)
func NewGroupConfigStoreWithDetector(appName string, _ any) (Store, error) {
	if runtime.IsKubernetesRuntime() {
		return nil, nil // No store needed in Kubernetes environments
	}
	return NewLocalStore(appName, GroupConfigsDir)
}

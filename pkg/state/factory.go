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

// NewRunConfigStore creates a store for run configuration state
func NewRunConfigStore(appName string) (Store, error) {
	return NewRunConfigStoreWithDetector(appName)
}

// NewRunConfigStoreWithDetector creates a store
func NewRunConfigStoreWithDetector(appName string) (Store, error) {
	if runtime.IsKubernetesRuntime() {
		return NewKubernetesStore(), nil
	}
	return NewLocalStore(appName, RunConfigsDir)
}

// NewGroupConfigStore creates a store for group configurations
func NewGroupConfigStore(appName string) (Store, error) {
	return NewGroupConfigStoreWithDetector(appName)
}

// NewGroupConfigStoreWithDetector creates a store
func NewGroupConfigStoreWithDetector(appName string) (Store, error) {
	if runtime.IsKubernetesRuntime() {
		return NewKubernetesStore(), nil
	}
	return NewLocalStore(appName, GroupConfigsDir)
}

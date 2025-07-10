package workloads

import (
	"context"

	ct "github.com/stacklok/toolhive/pkg/container"
	rt "github.com/stacklok/toolhive/pkg/container/runtime"
)

// StatusManager is an interface for fetching and retrieving workload statuses.
type StatusManager interface {
	// SetWorkloadStatus sets the status of a workload by its name.
	SetWorkloadStatus(ctx context.Context, workloadName string, status WorkloadStatus, contextMsg string) error
	// DeleteWorkloadStatus removes the status of a workload by its name.
	DeleteWorkloadStatus(ctx context.Context, workloadName string) error
}

// NewStatusManagerFromRuntime creates a new instance of StatusManager from an existing runtime.
func NewStatusManagerFromRuntime(runtime rt.Runtime) StatusManager {
	return &runtimeStatusManager{
		runtime: runtime,
	}
}

// NewStatusManager creates a new container manager instance.
// It instantiates a runtime as part of creating the manager.
func NewStatusManager(ctx context.Context) (StatusManager, error) {
	runtime, err := ct.NewFactory().Create(ctx)
	if err != nil {
		return nil, err
	}

	return &runtimeStatusManager{
		runtime: runtime,
	}, nil
}

// runtimeStatusManager is an implementation of StatusManager that uses the state
// returned by the underlying runtime. This reflects the existing behaviour of
// ToolHive at the time of writing.
type runtimeStatusManager struct {
	runtime rt.Runtime
}

func (*runtimeStatusManager) SetWorkloadStatus(_ context.Context, _ string, _ WorkloadStatus, _ string) error {
	// Noop
	return nil
}

func (*runtimeStatusManager) DeleteWorkloadStatus(_ context.Context, _ string) error {
	// Noop
	return nil
}

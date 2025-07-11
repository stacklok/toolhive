package workloads

import (
	"context"

	rt "github.com/stacklok/toolhive/pkg/container/runtime"
	"github.com/stacklok/toolhive/pkg/logger"
)

// StatusManager is an interface for fetching and retrieving workload statuses.
type StatusManager interface {
	// CreateWorkloadStatus creates the initial `starting` status for a new workload.
	// Unlike SetWorkloadStatus, this will create a new entry in the status store,
	// but will do nothing if the workload already exists.
	CreateWorkloadStatus(ctx context.Context, workloadName string) error
	// GetWorkloadStatus retrieves the status of a workload by its name.
	GetWorkloadStatus(ctx context.Context, workloadName string) (*WorkloadStatus, string, error)
	// SetWorkloadStatus sets the status of a workload by its name.
	// Note that this does not return errors, but logs them instead.
	// This method will do nothing if the workload does not exist.
	SetWorkloadStatus(ctx context.Context, workloadName string, status WorkloadStatus, contextMsg string)
	// DeleteWorkloadStatus removes the status of a workload by its name.
	DeleteWorkloadStatus(ctx context.Context, workloadName string) error
}

// NewStatusManagerFromRuntime creates a new instance of StatusManager from an existing runtime.
func NewStatusManagerFromRuntime(runtime rt.Runtime) StatusManager {
	return &runtimeStatusManager{
		runtime: runtime,
	}
}

// runtimeStatusManager is an implementation of StatusManager that uses the state
// returned by the underlying runtime. This reflects the existing behaviour of
// ToolHive at the time of writing.
type runtimeStatusManager struct {
	runtime rt.Runtime
}

func (*runtimeStatusManager) CreateWorkloadStatus(_ context.Context, workloadName string) error {
	// TODO: This will need to handle concurrent updates.
	logger.Debugf("workload %s created", workloadName)
	return nil
}

func (*runtimeStatusManager) GetWorkloadStatus(_ context.Context, _ string) (*WorkloadStatus, string, error) {
	return nil, "", nil
}

func (*runtimeStatusManager) SetWorkloadStatus(_ context.Context, workloadName string, status WorkloadStatus, contextMsg string) {
	// TODO: This will need to handle concurrent updates.
	logger.Debugf("workload %s set to status %s (context: %s)", workloadName, status, contextMsg)
}

func (*runtimeStatusManager) DeleteWorkloadStatus(_ context.Context, _ string) error {
	// TODO: This will need to handle concurrent updates.
	// Noop
	return nil
}

// Package statuses provides an interface and implementation for managing workload statuses.
package statuses

import (
	"context"
	"fmt"

	rt "github.com/stacklok/toolhive/pkg/container/runtime"
	"github.com/stacklok/toolhive/pkg/core"
	"github.com/stacklok/toolhive/pkg/env"
	"github.com/stacklok/toolhive/pkg/logger"
	"github.com/stacklok/toolhive/pkg/workloads/types"
)

// StatusManager is an interface for fetching and retrieving workload statuses.
//
//go:generate mockgen -destination=mocks/mock_status_manager.go -package=mocks -source=status.go StatusManager
type StatusManager interface {
	// GetWorkload retrieves details of a workload by its name.
	GetWorkload(ctx context.Context, workloadName string) (core.Workload, error)
	// ListWorkloads returns details of all workloads.
	ListWorkloads(ctx context.Context, listAll bool, labelFilters []string) ([]core.Workload, error)
	// SetWorkloadStatus sets the status of a workload by its name.
	// Note that this does not return errors, but logs them instead.
	// This method will do nothing if the workload does not exist.
	// This method will preserve the PID of the workload if it was previously set.
	SetWorkloadStatus(ctx context.Context, workloadName string, status rt.WorkloadStatus, contextMsg string) error
	// DeleteWorkloadStatus removes the status of a workload by its name.
	DeleteWorkloadStatus(ctx context.Context, workloadName string) error
	// SetWorkloadPID sets the PID of a workload by its name.
	// This method will do nothing if the workload does not exist.
	SetWorkloadPID(ctx context.Context, workloadName string, pid int) error
	// ResetWorkloadPID resets the PID of a workload to 0.
	// This method will do nothing if the workload does not exist.
	ResetWorkloadPID(ctx context.Context, workloadName string) error
}

// NewStatusManagerFromRuntime creates a new instance of StatusManager from an existing runtime.
func NewStatusManagerFromRuntime(runtime rt.Runtime) StatusManager {
	return &runtimeStatusManager{
		runtime: runtime,
	}
}

// NewStatusManager creates a new status manager instance using the appropriate implementation
// based on the runtime environment. If running in Kubernetes, it returns the runtime-based
// implementation. Otherwise, it returns the file-based implementation.
func NewStatusManager(runtime rt.Runtime) (StatusManager, error) {
	return NewStatusManagerWithEnv(runtime, &env.OSReader{})
}

// NewStatusManagerWithEnv creates a new status manager instance using the provided environment reader.
// This allows for dependency injection of environment variable access for testing.
func NewStatusManagerWithEnv(runtime rt.Runtime, envReader env.Reader) (StatusManager, error) {
	if rt.IsKubernetesRuntimeWithEnv(envReader) {
		return NewStatusManagerFromRuntime(runtime), nil
	}
	return NewFileStatusManager(runtime)
}

// runtimeStatusManager is an implementation of StatusManager that uses the state
// returned by the underlying runtime. This reflects the existing behaviour of
// ToolHive at the time of writing.
type runtimeStatusManager struct {
	runtime rt.Runtime
}

func (r *runtimeStatusManager) GetWorkload(ctx context.Context, workloadName string) (core.Workload, error) {
	if err := types.ValidateWorkloadName(workloadName); err != nil {
		return core.Workload{}, err
	}

	info, err := r.runtime.GetWorkloadInfo(ctx, workloadName)
	if err != nil {
		// The error from the runtime is already wrapped in context.
		return core.Workload{}, err
	}

	return types.WorkloadFromContainerInfo(&info)
}

func (r *runtimeStatusManager) ListWorkloads(ctx context.Context, listAll bool, labelFilters []string) ([]core.Workload, error) {
	// List containers
	containers, err := r.runtime.ListWorkloads(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list containers: %v", err)
	}

	// Parse the filters into a format we can use for matching.
	parsedFilters, err := types.ParseLabelFilters(labelFilters)
	if err != nil {
		return nil, fmt.Errorf("failed to parse label filters: %v", err)
	}

	// Filter containers to only show those managed by ToolHive
	var workloads []core.Workload
	for _, c := range containers {
		// If the caller did not set `listAll` to true, only include running containers.
		if c.IsRunning() || listAll {
			workload, err := types.WorkloadFromContainerInfo(&c)
			if err != nil {
				return nil, err
			}
			// If label filters are provided, check if the workload matches them.
			if types.MatchesLabelFilters(workload.Labels, parsedFilters) {
				workloads = append(workloads, workload)
			}
		}
	}

	return workloads, nil
}

func (*runtimeStatusManager) SetWorkloadStatus(
	_ context.Context,
	workloadName string,
	status rt.WorkloadStatus,
	contextMsg string,
) error {
	// TODO: This will need to handle concurrent updates.
	logger.Debugf("workload %s set to status %s (context: %s)", workloadName, status, contextMsg)
	return nil
}

func (*runtimeStatusManager) DeleteWorkloadStatus(_ context.Context, _ string) error {
	// TODO: This will need to handle concurrent updates.
	// Noop
	return nil
}

func (*runtimeStatusManager) SetWorkloadPID(_ context.Context, workloadName string, pid int) error {
	// Noop for runtime status manager
	logger.Debugf("workload %s PID set to %d (noop for runtime status manager)", workloadName, pid)
	return nil
}

func (*runtimeStatusManager) ResetWorkloadPID(_ context.Context, workloadName string) error {
	// Noop for runtime status manager
	logger.Debugf("workload %s PID reset (noop for runtime status manager)", workloadName)
	return nil
}

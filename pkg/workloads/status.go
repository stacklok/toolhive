package workloads

import (
	"context"
	"fmt"

	ct "github.com/stacklok/toolhive/pkg/container"
	rt "github.com/stacklok/toolhive/pkg/container/runtime"
	"github.com/stacklok/toolhive/pkg/labels"
)

// StatusManager is an interface for fetching and retrieving workload statuses.
type StatusManager interface {
	// GetWorkload retrieves details of the named workload including its status.
	GetWorkload(ctx context.Context, workloadName string) (Workload, error)
	// ListWorkloads retrieves the states of all workloads.
	// The `listAll` parameter determines whether to include workloads that are not running.
	ListWorkloads(ctx context.Context, listAll bool) ([]Workload, error)
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

func (r *runtimeStatusManager) GetWorkload(ctx context.Context, workloadName string) (Workload, error) {
	// Validate workload name to prevent path traversal attacks
	if err := validateWorkloadName(workloadName); err != nil {
		return Workload{}, err
	}

	container, err := r.findContainerByName(ctx, workloadName)
	if err != nil {
		// Note that `findContainerByName` already wraps the error with a more specific message.
		return Workload{}, err
	}

	return WorkloadFromContainerInfo(container)
}

func (r *runtimeStatusManager) ListWorkloads(ctx context.Context, listAll bool) ([]Workload, error) {
	// List containers
	containers, err := r.runtime.ListWorkloads(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list containers: %v", err)
	}

	// Filter containers to only show those managed by ToolHive
	var workloads []Workload
	for _, c := range containers {
		// If the caller did not set `listAll` to true, only include running containers.
		if labels.IsToolHiveContainer(c.Labels) && (isContainerRunning(&c) || listAll) {
			workload, err := WorkloadFromContainerInfo(&c)
			if err != nil {
				return nil, err
			}
			workloads = append(workloads, workload)
		}
	}

	return workloads, nil
}

func (*runtimeStatusManager) SetWorkloadStatus(_ context.Context, _ string, _ WorkloadStatus, _ string) error {
	// Noop
	return nil
}

func (*runtimeStatusManager) DeleteWorkloadStatus(_ context.Context, _ string) error {
	// Noop
	return nil
}

// Duplicated from the original code - need to de-dupe at some point.
func (r *runtimeStatusManager) findContainerByName(ctx context.Context, name string) (*rt.ContainerInfo, error) {
	// List containers to find the one with the given name
	containers, err := r.runtime.ListWorkloads(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list containers: %v", err)
	}

	// Find the container with the given name
	for _, c := range containers {
		// Check if the container is managed by ToolHive
		if !labels.IsToolHiveContainer(c.Labels) {
			continue
		}

		// Check if the container name matches
		containerName := labels.GetContainerName(c.Labels)
		if containerName == "" {
			name = c.Name // Fallback to container name
		}

		// Check if the name matches (exact match or prefix match)
		if containerName == name || c.ID == name {
			return &c, nil
		}
	}

	return nil, fmt.Errorf("%w: %s", ErrWorkloadNotFound, name)
}

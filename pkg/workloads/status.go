package workloads

import (
	"context"
	"fmt"

	rt "github.com/stacklok/toolhive/pkg/container/runtime"
	"github.com/stacklok/toolhive/pkg/core"
	"github.com/stacklok/toolhive/pkg/labels"
	"github.com/stacklok/toolhive/pkg/logger"
)

// StatusManager is an interface for fetching and retrieving workload statuses.
//
//go:generate mockgen -destination=mocks/mock_status_manager.go -package=mocks -source=status.go StatusManager
type StatusManager interface {
	// CreateWorkloadStatus creates the initial `starting` status for a new workload.
	// Unlike SetWorkloadStatus, this will create a new entry in the status store,
	// but will do nothing if the workload already exists.
	CreateWorkloadStatus(ctx context.Context, workloadName string) error
	// GetWorkload retrieves details of a workload by its name.
	GetWorkload(ctx context.Context, workloadName string) (core.Workload, error)
	// ListWorkloads returns details of all workloads.
	ListWorkloads(ctx context.Context, listAll bool, labelFilters []string) ([]core.Workload, error)
	// SetWorkloadStatus sets the status of a workload by its name.
	// Note that this does not return errors, but logs them instead.
	// This method will do nothing if the workload does not exist.
	SetWorkloadStatus(ctx context.Context, workloadName string, status rt.WorkloadStatus, contextMsg string)
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

func (r *runtimeStatusManager) GetWorkload(ctx context.Context, workloadName string) (core.Workload, error) {
	if err := validateWorkloadName(workloadName); err != nil {
		return core.Workload{}, err
	}

	info, err := r.runtime.GetWorkloadInfo(ctx, workloadName)
	if err != nil {
		// The error from the runtime is already wrapped in context.
		return core.Workload{}, err
	}

	return WorkloadFromContainerInfo(&info)
}

func (r *runtimeStatusManager) ListWorkloads(ctx context.Context, listAll bool, labelFilters []string) ([]core.Workload, error) {
	// List containers
	containers, err := r.runtime.ListWorkloads(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list containers: %v", err)
	}

	// Parse the filters into a format we can use for matching.
	parsedFilters, err := parseLabelFilters(labelFilters)
	if err != nil {
		return nil, fmt.Errorf("failed to parse label filters: %v", err)
	}

	// Filter containers to only show those managed by ToolHive
	var workloads []core.Workload
	for _, c := range containers {
		// If the caller did not set `listAll` to true, only include running containers.
		if c.IsRunning() || listAll {
			workload, err := WorkloadFromContainerInfo(&c)
			if err != nil {
				return nil, err
			}
			// If label filters are provided, check if the workload matches them.
			if matchesLabelFilters(workload.Labels, parsedFilters) {
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
) {
	// TODO: This will need to handle concurrent updates.
	logger.Debugf("workload %s set to status %s (context: %s)", workloadName, status, contextMsg)
}

func (*runtimeStatusManager) DeleteWorkloadStatus(_ context.Context, _ string) error {
	// TODO: This will need to handle concurrent updates.
	// Noop
	return nil
}

// parseLabelFilters parses label filters from a slice of strings and validates them.
func parseLabelFilters(labelFilters []string) (map[string]string, error) {
	filters := make(map[string]string, len(labelFilters))
	for _, filter := range labelFilters {
		key, value, err := labels.ParseLabel(filter)
		if err != nil {
			return nil, fmt.Errorf("invalid label filter '%s': %v", filter, err)
		}
		filters[key] = value
	}
	return filters, nil
}

// matchesLabelFilters checks if workload labels match all the specified filters
func matchesLabelFilters(workloadLabels, filters map[string]string) bool {
	for filterKey, filterValue := range filters {
		workloadValue, exists := workloadLabels[filterKey]
		if !exists || workloadValue != filterValue {
			return false
		}
	}
	return true
}

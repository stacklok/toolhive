package workloads

import (
	"context"
	"fmt"

	"github.com/stacklok/toolhive/pkg/core"
	"github.com/stacklok/toolhive/pkg/errors"
	"github.com/stacklok/toolhive/pkg/groups"
)

// FilterByGroups filters workloads to only include those in the specified groups
func FilterByGroups(
	ctx context.Context, workloadList []core.Workload, groupNames []string,
) ([]core.Workload, error) {
	if len(groupNames) == 0 {
		// No groups specified, return all workloads
		return workloadList, nil
	}

	// Create group manager
	groupManager, err := groups.NewManager()
	if err != nil {
		return nil, fmt.Errorf("failed to create group manager: %v", err)
	}

	// Validate all groups exist
	for _, groupName := range groupNames {
		exists, err := groupManager.Exists(ctx, groupName)
		if err != nil {
			return nil, fmt.Errorf("failed to check if group %s exists: %v", groupName, err)
		}
		if !exists {
			return nil, errors.NewGroupNotFoundError(fmt.Sprintf("group '%s' does not exist", groupName), nil)
		}
	}

	// Get all workload names for all specified groups
	groupWorkloadMap := make(map[string]bool)
	for _, groupName := range groupNames {
		groupWorkloadNames, err := groupManager.ListWorkloadsInGroup(ctx, groupName)
		if err != nil {
			return nil, fmt.Errorf("failed to list workloads in group %s: %v", groupName, err)
		}

		// Add to map for efficient lookup
		for _, name := range groupWorkloadNames {
			groupWorkloadMap[name] = true
		}
	}

	// Filter workloads that belong to any of the specified groups
	var filteredWorkloads []core.Workload
	for _, workload := range workloadList {
		if groupWorkloadMap[workload.Name] {
			filteredWorkloads = append(filteredWorkloads, workload)
		}
	}

	return filteredWorkloads, nil
}

// FilterByGroup filters workloads to only include those in the specified group
func FilterByGroup(
	ctx context.Context, workloadList []core.Workload, groupName string,
) ([]core.Workload, error) {
	return FilterByGroups(ctx, workloadList, []string{groupName})
}

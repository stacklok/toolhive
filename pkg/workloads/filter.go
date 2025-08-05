package workloads

import (
	"context"
	"fmt"
	"github.com/stacklok/toolhive/pkg/core"

	"github.com/stacklok/toolhive/pkg/errors"
	"github.com/stacklok/toolhive/pkg/groups"
)

// FilterByGroup filters workloads to only include those in the specified group
func FilterByGroup(
	ctx context.Context, workloadList []core.Workload, groupName string,
) ([]core.Workload, error) {
	// Create group manager
	groupManager, err := groups.NewManager()
	if err != nil {
		return nil, fmt.Errorf("failed to create group manager: %v", err)
	}

	// Check if the group exists
	exists, err := groupManager.Exists(ctx, groupName)
	if err != nil {
		return nil, fmt.Errorf("failed to check if group exists: %v", err)
	}
	if !exists {
		return nil, errors.NewGroupNotFoundError(fmt.Sprintf("group '%s' does not exist", groupName), nil)
	}

	// Get all workload names in the specified group
	groupWorkloadNames, err := groupManager.ListWorkloadsInGroup(ctx, groupName)
	if err != nil {
		return nil, fmt.Errorf("failed to list workloads in group: %v", err)
	}

	// Create a map for efficient lookup
	groupWorkloadMap := make(map[string]bool)
	for _, name := range groupWorkloadNames {
		groupWorkloadMap[name] = true
	}

	// Filter workloads that belong to the specified group
	var filteredWorkloads []core.Workload
	for _, workload := range workloadList {
		if groupWorkloadMap[workload.Name] {
			filteredWorkloads = append(filteredWorkloads, workload)
		}
	}

	return filteredWorkloads, nil
}

package workloads

import (
	"github.com/stacklok/toolhive/pkg/core"
)

// FilterByGroups filters workloads to only include those in the specified groups
func FilterByGroups(workloadList []core.Workload, groupNames []string) ([]core.Workload, error) {
	if len(groupNames) == 0 {
		// No groups specified, return all workloads
		return workloadList, nil
	}

	// Create a set of group names for efficient lookup
	groupSet := make(map[string]bool, len(groupNames))
	for _, groupName := range groupNames {
		groupSet[groupName] = true
	}

	// Filter workloads that belong to any of the specified groups
	var filteredWorkloads []core.Workload
	for _, workload := range workloadList {
		if groupSet[workload.Group] {
			filteredWorkloads = append(filteredWorkloads, workload)
		}
	}

	return filteredWorkloads, nil
}

// FilterByGroup filters workloads to only include those in the specified group
func FilterByGroup(workloadList []core.Workload, groupName string) ([]core.Workload, error) {
	return FilterByGroups(workloadList, []string{groupName})
}

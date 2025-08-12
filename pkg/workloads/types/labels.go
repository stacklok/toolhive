package types

import (
	"fmt"

	"github.com/stacklok/toolhive/pkg/labels"
)

// ParseLabelFilters parses label filters from a slice of strings and validates them.
func ParseLabelFilters(labelFilters []string) (map[string]string, error) {
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

// MatchesLabelFilters checks if workload labels match all the specified filters
func MatchesLabelFilters(workloadLabels, filters map[string]string) bool {
	for filterKey, filterValue := range filters {
		workloadValue, exists := workloadLabels[filterKey]
		if !exists || workloadValue != filterValue {
			return false
		}
	}
	return true
}

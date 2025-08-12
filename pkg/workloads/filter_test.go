package workloads

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/core"
)

func TestFilterByGroups(t *testing.T) {
	t.Parallel()

	testWorkloads := []core.Workload{
		{Name: "workload1", Group: "frontend"},
		{Name: "workload2", Group: "backend"},
		{Name: "workload3", Group: "frontend"},
		{Name: "workload4", Group: "database"},
		{Name: "workload5", Group: ""}, // empty group
	}

	tests := []struct {
		name          string
		workloadList  []core.Workload
		groupNames    []string
		expectedNames []string
		expectError   bool
	}{
		{
			name:          "empty groups returns all workloads",
			workloadList:  testWorkloads,
			groupNames:    []string{},
			expectedNames: []string{"workload1", "workload2", "workload3", "workload4", "workload5"},
			expectError:   false,
		},
		{
			name:          "single group filter",
			workloadList:  testWorkloads,
			groupNames:    []string{"frontend"},
			expectedNames: []string{"workload1", "workload3"},
			expectError:   false,
		},
		{
			name:          "multiple groups filter",
			workloadList:  testWorkloads,
			groupNames:    []string{"frontend", "database"},
			expectedNames: []string{"workload1", "workload3", "workload4"},
			expectError:   false,
		},
		{
			name:          "non-existent group returns empty",
			workloadList:  testWorkloads,
			groupNames:    []string{"nonexistent"},
			expectedNames: []string{},
			expectError:   false,
		},
		{
			name:          "filter by empty group",
			workloadList:  testWorkloads,
			groupNames:    []string{""},
			expectedNames: []string{"workload5"},
			expectError:   false,
		},
		{
			name:          "empty workload list",
			workloadList:  []core.Workload{},
			groupNames:    []string{"frontend"},
			expectedNames: []string{},
			expectError:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result, err := FilterByGroups(tt.workloadList, tt.groupNames)

			if tt.expectError {
				require.Error(t, err)
				return
			}

			require.NoError(t, err)

			// Extract names from result for easier comparison
			var resultNames []string
			for _, workload := range result {
				resultNames = append(resultNames, workload.Name)
			}

			assert.ElementsMatch(t, tt.expectedNames, resultNames)
		})
	}
}

func TestFilterByGroup(t *testing.T) {
	t.Parallel()

	testWorkloads := []core.Workload{
		{Name: "workload1", Group: "frontend"},
		{Name: "workload2", Group: "backend"},
		{Name: "workload3", Group: "frontend"},
	}

	result, err := FilterByGroup(testWorkloads, "frontend")
	require.NoError(t, err)

	var resultNames []string
	for _, workload := range result {
		resultNames = append(resultNames, workload.Name)
	}

	assert.ElementsMatch(t, []string{"workload1", "workload3"}, resultNames)
}

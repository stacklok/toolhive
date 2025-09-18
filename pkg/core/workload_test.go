package core

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/stacklok/toolhive/pkg/container/runtime"
	"github.com/stacklok/toolhive/pkg/transport/types"
)

func TestSortWorkloadsByName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    []Workload
		expected []string // Expected order of names after sorting
	}{
		{
			name:     "empty slice",
			input:    []Workload{},
			expected: []string{},
		},
		{
			name: "single workload",
			input: []Workload{
				{Name: "single-workload"},
			},
			expected: []string{"single-workload"},
		},
		{
			name: "already sorted workloads",
			input: []Workload{
				{Name: "a-workload"},
				{Name: "b-workload"},
				{Name: "c-workload"},
			},
			expected: []string{"a-workload", "b-workload", "c-workload"},
		},
		{
			name: "reverse sorted workloads",
			input: []Workload{
				{Name: "z-workload"},
				{Name: "y-workload"},
				{Name: "x-workload"},
			},
			expected: []string{"x-workload", "y-workload", "z-workload"},
		},
		{
			name: "mixed case workloads",
			input: []Workload{
				{Name: "Zebra"},
				{Name: "apple"},
				{Name: "Banana"},
				{Name: "cherry"},
			},
			expected: []string{"Banana", "Zebra", "apple", "cherry"}, // ASCII sort: uppercase before lowercase
		},
		{
			name: "workloads with numbers",
			input: []Workload{
				{Name: "workload-10"},
				{Name: "workload-2"},
				{Name: "workload-1"},
			},
			expected: []string{"workload-1", "workload-10", "workload-2"}, // Lexicographic sort
		},
		{
			name: "workloads with special characters",
			input: []Workload{
				{Name: "workload_underscore"},
				{Name: "workload-dash"},
				{Name: "workload.dot"},
			},
			expected: []string{"workload-dash", "workload.dot", "workload_underscore"},
		},
		{
			name: "duplicate names",
			input: []Workload{
				{Name: "duplicate"},
				{Name: "another"},
				{Name: "duplicate"},
			},
			expected: []string{"another", "duplicate", "duplicate"},
		},
		{
			name: "complex workloads with all fields",
			input: []Workload{
				{
					Name:          "zebra-server",
					Package:       "zebra-pkg",
					URL:           "http://localhost:8080",
					Port:          8080,
					ToolType:      "mcp",
					TransportType: types.TransportTypeSSE,
					Status:        runtime.WorkloadStatusRunning,
					StatusContext: "healthy",
					CreatedAt:     time.Now(),
					Labels:        map[string]string{"env": "prod"},
					Group:         "production",
					ToolsFilter:   []string{"tool1"},
					Remote:        false,
				},
				{
					Name:          "alpha-server",
					Package:       "alpha-pkg",
					URL:           "http://localhost:8081",
					Port:          8081,
					ToolType:      "mcp",
					TransportType: types.TransportTypeStdio,
					Status:        runtime.WorkloadStatusStopped,
					StatusContext: "stopped",
					CreatedAt:     time.Now().Add(-time.Hour),
					Labels:        map[string]string{"env": "dev"},
					Group:         "development",
					ToolsFilter:   []string{"tool2"},
					Remote:        true,
				},
			},
			expected: []string{"alpha-server", "zebra-server"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Make a copy to avoid modifying the original test data
			workloads := make([]Workload, len(tt.input))
			copy(workloads, tt.input)

			// Sort the workloads
			SortWorkloadsByName(workloads)

			// Extract names for comparison
			actualNames := make([]string, len(workloads))
			for i, w := range workloads {
				actualNames[i] = w.Name
			}

			assert.Equal(t, tt.expected, actualNames, "Workloads should be sorted by name in ascending order")

			// Verify that all other fields are preserved
			if len(tt.input) > 0 {
				// Find the original workload for each sorted workload and verify fields are preserved
				for _, sortedWorkload := range workloads {
					var originalWorkload *Workload
					for i := range tt.input {
						if tt.input[i].Name == sortedWorkload.Name {
							originalWorkload = &tt.input[i]
							break
						}
					}
					assert.NotNil(t, originalWorkload, "Should find original workload")

					// Verify all fields are preserved (except Name which we already checked)
					assert.Equal(t, originalWorkload.Package, sortedWorkload.Package)
					assert.Equal(t, originalWorkload.URL, sortedWorkload.URL)
					assert.Equal(t, originalWorkload.Port, sortedWorkload.Port)
					assert.Equal(t, originalWorkload.ToolType, sortedWorkload.ToolType)
					assert.Equal(t, originalWorkload.TransportType, sortedWorkload.TransportType)
					assert.Equal(t, originalWorkload.Status, sortedWorkload.Status)
					assert.Equal(t, originalWorkload.StatusContext, sortedWorkload.StatusContext)
					assert.Equal(t, originalWorkload.CreatedAt, sortedWorkload.CreatedAt)
					assert.Equal(t, originalWorkload.Labels, sortedWorkload.Labels)
					assert.Equal(t, originalWorkload.Group, sortedWorkload.Group)
					assert.Equal(t, originalWorkload.ToolsFilter, sortedWorkload.ToolsFilter)
					assert.Equal(t, originalWorkload.Remote, sortedWorkload.Remote)
				}
			}
		})
	}
}

func TestSortWorkloadsByName_InPlace(t *testing.T) {
	t.Parallel()

	// Test that the function sorts in-place (modifies the original slice)
	workloads := []Workload{
		{Name: "charlie"},
		{Name: "alpha"},
		{Name: "bravo"},
	}

	originalSlice := workloads // Same underlying array
	SortWorkloadsByName(workloads)

	// Verify the original slice was modified
	assert.Equal(t, "alpha", originalSlice[0].Name)
	assert.Equal(t, "bravo", originalSlice[1].Name)
	assert.Equal(t, "charlie", originalSlice[2].Name)
}

func TestSortWorkloadsByName_StableSort(t *testing.T) {
	t.Parallel()

	// Test with workloads that have the same name but different other fields
	// to verify that the sort is stable (preserves relative order of equal elements)
	now := time.Now()
	workloads := []Workload{
		{Name: "same", Port: 8080, CreatedAt: now},
		{Name: "different", Port: 8081, CreatedAt: now.Add(time.Hour)},
		{Name: "same", Port: 8082, CreatedAt: now.Add(2 * time.Hour)},
	}

	SortWorkloadsByName(workloads)

	// After sorting, "different" should be first, then the two "same" entries
	// The two "same" entries should maintain their original relative order
	assert.Equal(t, "different", workloads[0].Name)
	assert.Equal(t, 8081, workloads[0].Port)

	assert.Equal(t, "same", workloads[1].Name)
	assert.Equal(t, 8080, workloads[1].Port) // First "same" entry

	assert.Equal(t, "same", workloads[2].Name)
	assert.Equal(t, 8082, workloads[2].Port) // Second "same" entry
}

func TestSortWorkloadsByName_EdgeCases(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    []Workload
		expected []string
	}{
		{
			name: "empty strings",
			input: []Workload{
				{Name: ""},
				{Name: "a"},
				{Name: ""},
			},
			expected: []string{"", "", "a"}, // Empty strings sort first
		},
		{
			name: "whitespace names",
			input: []Workload{
				{Name: " space"},
				{Name: "nospace"},
				{Name: "\ttab"},
			},
			expected: []string{"\ttab", " space", "nospace"}, // Tab < space < letter
		},
		{
			name: "unicode names",
			input: []Workload{
				{Name: "ñoño"},
				{Name: "zebra"},
				{Name: "café"},
			},
			expected: []string{"café", "zebra", "ñoño"}, // ASCII characters sort before extended Unicode
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			workloads := make([]Workload, len(tt.input))
			copy(workloads, tt.input)

			SortWorkloadsByName(workloads)

			actualNames := make([]string, len(workloads))
			for i, w := range workloads {
				actualNames[i] = w.Name
			}

			assert.Equal(t, tt.expected, actualNames)
		})
	}
}

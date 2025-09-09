package types

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestParseLabelFilters(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		labelFilters   []string
		expectedResult map[string]string
		expectError    bool
	}{
		{
			name:           "empty filters",
			labelFilters:   []string{},
			expectedResult: map[string]string{},
			expectError:    false,
		},
		{
			name:         "single valid filter",
			labelFilters: []string{"env=production"},
			expectedResult: map[string]string{
				"env": "production",
			},
			expectError: false,
		},
		{
			name:         "multiple valid filters",
			labelFilters: []string{"env=production", "team=backend", "version=1.0"},
			expectedResult: map[string]string{
				"env":     "production",
				"team":    "backend",
				"version": "1.0",
			},
			expectError: false,
		},
		{
			name:         "filter with empty value",
			labelFilters: []string{"env="},
			expectedResult: map[string]string{
				"env": "",
			},
			expectError: false,
		},
		{
			name:         "valid filter with allowed characters",
			labelFilters: []string{"config=app-config.yaml"},
			expectedResult: map[string]string{
				"config": "app-config.yaml",
			},
			expectError: false,
		},
		{
			name:           "invalid filter - special characters in value",
			labelFilters:   []string{"path=/var/lib/app"},
			expectedResult: nil,
			expectError:    true,
		},
		{
			name:         "filter with numbers and underscores",
			labelFilters: []string{"port_number=8080", "max_connections=100"},
			expectedResult: map[string]string{
				"port_number":     "8080",
				"max_connections": "100",
			},
			expectError: false,
		},
		{
			name:           "invalid filter - no equals sign",
			labelFilters:   []string{"invalid-filter"},
			expectedResult: nil,
			expectError:    true,
		},
		{
			name:           "invalid filter - empty key",
			labelFilters:   []string{"=value"},
			expectedResult: nil,
			expectError:    true,
		},
		{
			name:           "mixed valid and invalid filters",
			labelFilters:   []string{"env=production", "invalid-filter"},
			expectedResult: nil,
			expectError:    true,
		},
		{
			name:           "invalid filter - multiple equals signs",
			labelFilters:   []string{"key=value=extra"},
			expectedResult: nil,
			expectError:    true,
		},
		{
			name:           "invalid filter - spaces in value",
			labelFilters:   []string{"description=My Application"},
			expectedResult: nil,
			expectError:    true,
		},
		{
			name:         "duplicate keys - last one wins",
			labelFilters: []string{"env=dev", "env=prod"},
			expectedResult: map[string]string{
				"env": "prod",
			},
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result, err := ParseLabelFilters(tt.labelFilters)

			if tt.expectError {
				assert.Error(t, err)
				assert.Nil(t, result)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.expectedResult, result)
			}
		})
	}
}

func TestMatchesLabelFilters(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		workloadLabels map[string]string
		filters        map[string]string
		expected       bool
	}{
		{
			name:           "empty filters - should match any workload",
			workloadLabels: map[string]string{"env": "prod", "team": "backend"},
			filters:        map[string]string{},
			expected:       true,
		},
		{
			name:           "empty workload labels - should not match non-empty filters",
			workloadLabels: map[string]string{},
			filters:        map[string]string{"env": "prod"},
			expected:       false,
		},
		{
			name:           "both empty - should match",
			workloadLabels: map[string]string{},
			filters:        map[string]string{},
			expected:       true,
		},
		{
			name:           "exact match - single filter",
			workloadLabels: map[string]string{"env": "production", "team": "backend"},
			filters:        map[string]string{"env": "production"},
			expected:       true,
		},
		{
			name:           "exact match - multiple filters",
			workloadLabels: map[string]string{"env": "production", "team": "backend", "version": "1.0"},
			filters:        map[string]string{"env": "production", "team": "backend"},
			expected:       true,
		},
		{
			name:           "no match - wrong value",
			workloadLabels: map[string]string{"env": "development", "team": "backend"},
			filters:        map[string]string{"env": "production"},
			expected:       false,
		},
		{
			name:           "no match - missing key",
			workloadLabels: map[string]string{"team": "backend"},
			filters:        map[string]string{"env": "production"},
			expected:       false,
		},
		{
			name:           "partial match - one filter matches, one doesn't",
			workloadLabels: map[string]string{"env": "production", "team": "frontend"},
			filters:        map[string]string{"env": "production", "team": "backend"},
			expected:       false,
		},
		{
			name:           "workload has extra labels - should still match",
			workloadLabels: map[string]string{"env": "prod", "team": "backend", "version": "1.0", "region": "us-east"},
			filters:        map[string]string{"env": "prod", "team": "backend"},
			expected:       true,
		},
		{
			name:           "case sensitive matching",
			workloadLabels: map[string]string{"env": "Production"},
			filters:        map[string]string{"env": "production"},
			expected:       false,
		},
		{
			name:           "empty string values",
			workloadLabels: map[string]string{"env": "", "team": "backend"},
			filters:        map[string]string{"env": ""},
			expected:       true,
		},
		{
			name:           "empty string value mismatch",
			workloadLabels: map[string]string{"env": "prod"},
			filters:        map[string]string{"env": ""},
			expected:       false,
		},
		{
			name:           "special characters in values",
			workloadLabels: map[string]string{"config": "app-config.yaml", "path": "/var/lib/app"},
			filters:        map[string]string{"config": "app-config.yaml"},
			expected:       true,
		},
		{
			name:           "numeric values",
			workloadLabels: map[string]string{"port": "8080", "replicas": "3"},
			filters:        map[string]string{"port": "8080", "replicas": "3"},
			expected:       true,
		},
		{
			name:           "numeric value mismatch",
			workloadLabels: map[string]string{"port": "8080"},
			filters:        map[string]string{"port": "9090"},
			expected:       false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result := MatchesLabelFilters(tt.workloadLabels, tt.filters)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestParseLabelFilters_Integration(t *testing.T) {
	t.Parallel()

	// Test the integration between ParseLabelFilters and MatchesLabelFilters
	labelFilters := []string{"env=production", "team=backend"}

	filters, err := ParseLabelFilters(labelFilters)
	assert.NoError(t, err)
	assert.Equal(t, map[string]string{"env": "production", "team": "backend"}, filters)

	// Test workload that should match
	matchingWorkload := map[string]string{
		"env":     "production",
		"team":    "backend",
		"version": "1.0", // Extra label should not affect matching
	}
	assert.True(t, MatchesLabelFilters(matchingWorkload, filters))

	// Test workload that should not match
	nonMatchingWorkload := map[string]string{
		"env":  "development", // Wrong value
		"team": "backend",
	}
	assert.False(t, MatchesLabelFilters(nonMatchingWorkload, filters))
}

func TestMatchesLabelFilters_EdgeCases(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		workloadLabels map[string]string
		filters        map[string]string
		expected       bool
	}{
		{
			name:           "nil workload labels",
			workloadLabels: nil,
			filters:        map[string]string{"env": "prod"},
			expected:       false,
		},
		{
			name:           "nil filters",
			workloadLabels: map[string]string{"env": "prod"},
			filters:        nil,
			expected:       true,
		},
		{
			name:           "both nil",
			workloadLabels: nil,
			filters:        nil,
			expected:       true,
		},
		{
			name:           "whitespace in keys and values",
			workloadLabels: map[string]string{" env ": " prod ", "team": "backend"},
			filters:        map[string]string{" env ": " prod "},
			expected:       true,
		},
		{
			name:           "unicode characters",
			workloadLabels: map[string]string{"环境": "生产", "team": "backend"},
			filters:        map[string]string{"环境": "生产"},
			expected:       true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result := MatchesLabelFilters(tt.workloadLabels, tt.filters)
			assert.Equal(t, tt.expected, result)
		})
	}
}

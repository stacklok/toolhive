package filtering

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNewDefaultNameFilter(t *testing.T) {
	t.Parallel()

	filter := NewDefaultNameFilter()
	assert.NotNil(t, filter)
	assert.IsType(t, &DefaultNameFilter{}, filter)
}

func TestDefaultNameFilter_ShouldInclude(t *testing.T) {
	t.Parallel()

	filter := NewDefaultNameFilter()

	tests := []struct {
		name       string
		serverName string
		include    []string
		exclude    []string
		expected   bool
		reason     string
	}{
		// No filters specified - default include
		{
			name:       "no filters - should include",
			serverName: "any-server",
			include:    []string{},
			exclude:    []string{},
			expected:   true,
			reason:     "no filters means default include",
		},
		{
			name:       "nil filters - should include",
			serverName: "any-server",
			include:    nil,
			exclude:    nil,
			expected:   true,
			reason:     "nil filters means default include",
		},
		// Include-only patterns
		{
			name:       "exact match include",
			serverName: "postgres-server",
			include:    []string{"postgres-server"},
			exclude:    []string{},
			expected:   true,
			reason:     "exact match should be included",
		},
		{
			name:       "glob pattern include match",
			serverName: "postgres-server",
			include:    []string{"postgres-*"},
			exclude:    []string{},
			expected:   true,
			reason:     "glob pattern match should be included",
		},
		{
			name:       "multiple include patterns - first match",
			serverName: "postgres-server",
			include:    []string{"postgres-*", "mysql-*"},
			exclude:    []string{},
			expected:   true,
			reason:     "should match first pattern",
		},
		{
			name:       "multiple include patterns - second match",
			serverName: "mysql-server",
			include:    []string{"postgres-*", "mysql-*"},
			exclude:    []string{},
			expected:   true,
			reason:     "should match second pattern",
		},
		{
			name:       "include pattern no match",
			serverName: "redis-server",
			include:    []string{"postgres-*", "mysql-*"},
			exclude:    []string{},
			expected:   false,
			reason:     "no pattern match should exclude",
		},
		// Exclude-only patterns
		{
			name:       "exclude pattern match",
			serverName: "server-experimental",
			include:    []string{},
			exclude:    []string{"*-experimental", "*-deprecated"},
			expected:   false,
			reason:     "exclude pattern match should exclude",
		},
		{
			name:       "exclude pattern no match",
			serverName: "stable-server",
			include:    []string{},
			exclude:    []string{"*-experimental", "*-deprecated"},
			expected:   true,
			reason:     "no exclude pattern match should include",
		},
		// Both include and exclude patterns - exclude takes precedence
		{
			name:       "exclude takes precedence over include",
			serverName: "postgres-experimental",
			include:    []string{"postgres-*"},
			exclude:    []string{"*-experimental"},
			expected:   false,
			reason:     "exclude should take precedence over include",
		},
		{
			name:       "include match with non-matching exclude",
			serverName: "postgres-stable",
			include:    []string{"postgres-*"},
			exclude:    []string{"*-experimental"},
			expected:   true,
			reason:     "include match with no exclude match should include",
		},
		{
			name:       "no include match with non-matching exclude",
			serverName: "redis-stable",
			include:    []string{"postgres-*"},
			exclude:    []string{"*-experimental"},
			expected:   false,
			reason:     "no include match should exclude even if exclude doesn't match",
		},
		// Complex glob patterns
		{
			name:       "question mark wildcard",
			serverName: "db1",
			include:    []string{"db?"},
			exclude:    []string{},
			expected:   true,
			reason:     "question mark should match single character",
		},
		{
			name:       "question mark wildcard no match",
			serverName: "database",
			include:    []string{"db?"},
			exclude:    []string{},
			expected:   false,
			reason:     "question mark should not match multiple characters",
		},
		{
			name:       "character class match",
			serverName: "db1",
			include:    []string{"db[0-9]"},
			exclude:    []string{},
			expected:   true,
			reason:     "character class should match digit",
		},
		{
			name:       "character class no match",
			serverName: "dba",
			include:    []string{"db[0-9]"},
			exclude:    []string{},
			expected:   false,
			reason:     "character class should not match letter",
		},
		// Edge cases
		{
			name:       "empty server name",
			serverName: "",
			include:    []string{"*"},
			exclude:    []string{},
			expected:   true,
			reason:     "wildcard should match empty string",
		},
		{
			name:       "empty pattern in include",
			serverName: "server",
			include:    []string{""},
			exclude:    []string{},
			expected:   false,
			reason:     "empty pattern should only match empty string",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result, reason := filter.ShouldInclude(tt.serverName, tt.include, tt.exclude)
			assert.Equal(t, tt.expected, result, tt.reason)
			if !result {
				assert.NotEmpty(t, reason, "Reason should be provided when excluding")
			}
		})
	}
}

func TestDefaultNameFilter_ShouldInclude_InvalidGlobPatterns(t *testing.T) {
	t.Parallel()

	filter := NewDefaultNameFilter()

	// Test with malformed glob patterns - should handle gracefully
	tests := []struct {
		name       string
		serverName string
		include    []string
		exclude    []string
		expected   bool
	}{
		{
			name:       "malformed bracket pattern in include",
			serverName: "test",
			include:    []string{"["},
			exclude:    []string{},
			expected:   false, // filepath.Match returns false for malformed patterns
		},
		{
			name:       "malformed bracket pattern in exclude",
			serverName: "test",
			include:    []string{},
			exclude:    []string{"["},
			expected:   true, // exclude pattern fails to match, so include
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result, reason := filter.ShouldInclude(tt.serverName, tt.include, tt.exclude)
			assert.Equal(t, tt.expected, result)
			if !result {
				assert.NotEmpty(t, reason, "Reason should be provided when excluding")
			}
		})
	}
}

package filtering

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNewDefaultTagFilter(t *testing.T) {
	t.Parallel()

	filter := NewDefaultTagFilter()
	assert.NotNil(t, filter)
	assert.IsType(t, &DefaultTagFilter{}, filter)
}

func TestDefaultTagFilter_ShouldInclude(t *testing.T) {
	t.Parallel()

	filter := NewDefaultTagFilter()

	tests := []struct {
		name     string
		tags     []string
		include  []string
		exclude  []string
		expected bool
		reason   string
	}{
		// No filters specified - default include
		{
			name:     "no filters - should include",
			tags:     []string{"database", "sql"},
			include:  []string{},
			exclude:  []string{},
			expected: true,
			reason:   "no filters means default include",
		},
		{
			name:     "nil filters - should include",
			tags:     []string{"database", "sql"},
			include:  nil,
			exclude:  nil,
			expected: true,
			reason:   "nil filters means default include",
		},
		{
			name:     "empty tags with no filters",
			tags:     []string{},
			include:  []string{},
			exclude:  []string{},
			expected: true,
			reason:   "empty tags with no filters should include",
		},
		{
			name:     "nil tags with no filters",
			tags:     nil,
			include:  []string{},
			exclude:  []string{},
			expected: true,
			reason:   "nil tags with no filters should include",
		},
		// Include-only filters
		{
			name:     "single tag matches include",
			tags:     []string{"database"},
			include:  []string{"database"},
			exclude:  []string{},
			expected: true,
			reason:   "matching tag should be included",
		},
		{
			name:     "multiple tags, one matches include",
			tags:     []string{"database", "web", "api"},
			include:  []string{"database"},
			exclude:  []string{},
			expected: true,
			reason:   "any matching tag should include",
		},
		{
			name:     "multiple include tags, first matches",
			tags:     []string{"database"},
			include:  []string{"database", "web"},
			exclude:  []string{},
			expected: true,
			reason:   "first include match should include",
		},
		{
			name:     "multiple include tags, second matches",
			tags:     []string{"web"},
			include:  []string{"database", "web"},
			exclude:  []string{},
			expected: true,
			reason:   "second include match should include",
		},
		{
			name:     "no tag matches include",
			tags:     []string{"cache", "storage"},
			include:  []string{"database", "web"},
			exclude:  []string{},
			expected: false,
			reason:   "no matching include tag should exclude",
		},
		{
			name:     "empty tags with include filters",
			tags:     []string{},
			include:  []string{"database"},
			exclude:  []string{},
			expected: false,
			reason:   "empty tags should not match include filters",
		},
		// Exclude-only filters
		{
			name:     "single tag matches exclude",
			tags:     []string{"deprecated"},
			include:  []string{},
			exclude:  []string{"deprecated"},
			expected: false,
			reason:   "matching exclude tag should exclude",
		},
		{
			name:     "multiple tags, one matches exclude",
			tags:     []string{"database", "deprecated", "sql"},
			include:  []string{},
			exclude:  []string{"deprecated"},
			expected: false,
			reason:   "any matching exclude tag should exclude",
		},
		{
			name:     "no tag matches exclude",
			tags:     []string{"database", "sql"},
			include:  []string{},
			exclude:  []string{"deprecated", "experimental"},
			expected: true,
			reason:   "no matching exclude tag should include",
		},
		{
			name:     "empty tags with exclude filters",
			tags:     []string{},
			include:  []string{},
			exclude:  []string{"deprecated"},
			expected: true,
			reason:   "empty tags should not match exclude filters",
		},
		// Both include and exclude filters - exclude takes precedence
		{
			name:     "exclude takes precedence over include",
			tags:     []string{"database", "deprecated"},
			include:  []string{"database"},
			exclude:  []string{"deprecated"},
			expected: false,
			reason:   "exclude should take precedence over include",
		},
		{
			name:     "include match with non-matching exclude",
			tags:     []string{"database", "stable"},
			include:  []string{"database"},
			exclude:  []string{"deprecated"},
			expected: true,
			reason:   "include match with no exclude match should include",
		},
		{
			name:     "no include match with non-matching exclude",
			tags:     []string{"cache", "stable"},
			include:  []string{"database"},
			exclude:  []string{"deprecated"},
			expected: false,
			reason:   "no include match should exclude even if exclude doesn't match",
		},
		{
			name:     "multiple excludes, first matches",
			tags:     []string{"database", "deprecated"},
			include:  []string{"database"},
			exclude:  []string{"deprecated", "experimental"},
			expected: false,
			reason:   "first exclude match should take precedence",
		},
		{
			name:     "multiple excludes, second matches",
			tags:     []string{"database", "experimental"},
			include:  []string{"database"},
			exclude:  []string{"deprecated", "experimental"},
			expected: false,
			reason:   "second exclude match should take precedence",
		},
		// Case sensitivity
		{
			name:     "case sensitive exact match required",
			tags:     []string{"Database"},
			include:  []string{"database"},
			exclude:  []string{},
			expected: false,
			reason:   "case should matter for tag matching",
		},
		{
			name:     "case sensitive exclude",
			tags:     []string{"Deprecated"},
			include:  []string{},
			exclude:  []string{"deprecated"},
			expected: true,
			reason:   "case should matter for exclude matching",
		},
		// Complex scenarios
		{
			name:     "many tags with complex filters",
			tags:     []string{"database", "sql", "postgresql", "backend", "stable"},
			include:  []string{"database", "web"},
			exclude:  []string{"deprecated", "experimental"},
			expected: true,
			reason:   "complex scenario with include match and no exclude match",
		},
		{
			name:     "many tags with exclude match",
			tags:     []string{"database", "sql", "postgresql", "backend", "experimental"},
			include:  []string{"database", "web"},
			exclude:  []string{"deprecated", "experimental"},
			expected: false,
			reason:   "exclude should override include even with many tags",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result, reason := filter.ShouldInclude(tt.tags, tt.include, tt.exclude)
			assert.Equal(t, tt.expected, result, tt.reason)
			if !result {
				assert.NotEmpty(t, reason, "Reason should be provided when excluding")
			}
		})
	}
}

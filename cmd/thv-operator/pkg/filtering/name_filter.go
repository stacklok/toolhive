package filtering

import (
	"fmt"
	"path/filepath"
)

// NameFilter handles name-based filtering using glob patterns
type NameFilter interface {
	// ShouldInclude determines if a server name should be included based on include/exclude patterns
	// Returns (shouldInclude bool, reason string)
	ShouldInclude(name string, include, exclude []string) (bool, string)
}

// DefaultNameFilter implements name filtering using Go's filepath.Match for glob patterns
type DefaultNameFilter struct{}

// NewDefaultNameFilter creates a new DefaultNameFilter
func NewDefaultNameFilter() *DefaultNameFilter {
	return &DefaultNameFilter{}
}

// ShouldInclude determines if a server name should be included based on include/exclude patterns
//
// Logic:
// 1. If exclude patterns are specified and name matches any exclude pattern -> exclude (exclude takes precedence)
// 2. If include patterns are specified and name matches any include pattern -> include
// 3. If include patterns are specified and name doesn't match any -> exclude
// 4. If only exclude patterns are specified (no include) and name doesn't match exclude -> include
// 5. If no patterns are specified -> include (default behavior)
func (*DefaultNameFilter) ShouldInclude(name string, include, exclude []string) (bool, string) {
	// Check exclude patterns first (exclude takes precedence)
	if len(exclude) > 0 {
		for _, pattern := range exclude {
			matches, err := filepath.Match(pattern, name)
			if err != nil {
				return false, fmt.Sprintf("invalid exclude pattern '%s': %v", pattern, err)
			}
			if matches {
				return false, fmt.Sprintf("excluded by pattern '%s'", pattern)
			}
		}
	}

	// If include patterns are specified, name must match at least one
	if len(include) > 0 {
		for _, pattern := range include {
			matches, err := filepath.Match(pattern, name)
			if err != nil {
				return false, fmt.Sprintf("invalid include pattern '%s': %v", pattern, err)
			}
			if matches {
				return true, fmt.Sprintf("included by pattern '%s'", pattern)
			}
		}
		// Include patterns specified but no match found
		return false, fmt.Sprintf("no match found in include patterns %v", include)
	}

	// No include patterns specified (or empty), and didn't match exclude patterns
	if len(exclude) > 0 {
		return true, fmt.Sprintf("no match in exclude patterns %v", exclude)
	}
	return true, "no name filters specified"
}

package filtering

import "fmt"

// TagFilter handles tag-based filtering using exact string matching
type TagFilter interface {
	// ShouldInclude determines if a server with given tags should be included based on include/exclude tag lists
	// Returns (shouldInclude bool, reason string)
	ShouldInclude(tags []string, include, exclude []string) (bool, string)
}

// DefaultTagFilter implements tag filtering using exact string matching
type DefaultTagFilter struct{}

// NewDefaultTagFilter creates a new DefaultTagFilter
func NewDefaultTagFilter() *DefaultTagFilter {
	return &DefaultTagFilter{}
}

// ShouldInclude determines if a server with given tags should be included based on include/exclude tag lists
//
// Logic:
// 1. If exclude tags are specified and any server tag matches any exclude tag -> exclude (exclude takes precedence)
// 2. If include tags are specified and any server tag matches any include tag -> include
// 3. If include tags are specified and no server tags match any include tag -> exclude
// 4. If only exclude tags are specified (no include) and no server tags match exclude -> include
// 5. If no tag filters are specified -> include (default behavior)
func (*DefaultTagFilter) ShouldInclude(tags []string, include, exclude []string) (bool, string) {
	// Check exclude tags first (exclude takes precedence)
	if len(exclude) > 0 {
		for _, serverTag := range tags {
			for _, excludeTag := range exclude {
				if serverTag == excludeTag {
					return false, fmt.Sprintf("excluded by tag '%s'", excludeTag)
				}
			}
		}
	}

	// If include tags are specified, at least one server tag must match
	if len(include) > 0 {
		for _, serverTag := range tags {
			for _, includeTag := range include {
				if serverTag == includeTag {
					return true, fmt.Sprintf("included by tag '%s'", includeTag)
				}
			}
		}
		// Include tags specified but no match found
		return false, fmt.Sprintf("no matching tags found in include list %v (server tags: %v)", include, tags)
	}

	// No include tags specified (or empty), and didn't match exclude tags
	if len(exclude) > 0 {
		return true, fmt.Sprintf("no matching tags in exclude list %v (server tags: %v)", exclude, tags)
	}
	return true, "no tag filters specified"
}

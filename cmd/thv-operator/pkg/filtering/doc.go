// Package filtering provides server filtering capabilities for MCPRegistry resources.
//
// This package implements a comprehensive filtering system that allows MCPRegistry
// controllers to selectively include or exclude servers based on name patterns
// and tags. The filtering system supports both include and exclude rules with
// exclude taking precedence over include.
//
// # Architecture
//
// The filtering system consists of three main components:
//
//   - NameFilter: Handles server name filtering using glob patterns
//   - TagFilter: Handles tag-based filtering using exact string matching
//   - FilterService: Coordinates both name and tag filtering
//
// # Name Filtering
//
// Name filtering uses Go's filepath.Match for glob pattern matching, supporting
// wildcards like '*', '?', and character classes '[...]'. Examples:
//
//   - "postgres-*" matches "postgres-server", "postgres-client"
//   - "db?" matches "db1", "db2" but not "database"
//   - "server[1-3]" matches "server1", "server2", "server3"
//
// # Tag Filtering
//
// Tag filtering uses exact string matching against server tags. A server is
// included if any of its tags match any include tag, and excluded if any of
// its tags match any exclude tag.
//
// # Filtering Logic
//
// Both name and tag filters follow the same precedence rules:
//
//  1. If exclude patterns/tags are specified and match -> exclude (precedence)
//  2. If include patterns/tags are specified and match -> include
//  3. If include patterns/tags are specified but no match -> exclude
//  4. If only exclude patterns/tags specified and no match -> include
//  5. If no filters specified -> include (default behavior)
//
// For a server to be included in the final registry, it must pass BOTH
// name and tag filtering (logical AND).
//
// # Usage Example
//
//	service := NewDefaultFilterService()
//	filter := &mcpv1alpha1.RegistryFilter{
//		NameFilters: &mcpv1alpha1.NameFilter{
//			Include: []string{"postgres-*", "mysql-*"},
//			Exclude: []string{"*-experimental"},
//		},
//		Tags: &mcpv1alpha1.TagFilter{
//			Include: []string{"database", "sql"},
//			Exclude: []string{"deprecated"},
//		},
//	}
//
//	filteredRegistry, err := service.ApplyFilters(ctx, originalRegistry, filter)
//
// # Detailed Logging
//
// The filtering system provides detailed logging with specific reasons for
// inclusion or exclusion decisions, making it easy to debug filtering
// configurations and understand filtering behavior.
package filtering
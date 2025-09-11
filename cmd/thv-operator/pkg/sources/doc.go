// Package sources provides interfaces and implementations for retrieving
// MCP registry data from various external sources.
//
// The package defines the SourceHandler interface which abstracts the
// process of validating source configurations and fetching registry data
// from external sources such as ConfigMaps, HTTP endpoints, Git repositories,
// or external registries.
//
// Architecture:
//   - SourceHandler: Interface for fetching and validating registry data
//   - StorageManager: Interface for persisting registry data to ConfigMaps
//   - SourceDataValidator: Validates and parses registry data in different formats
//   - FetchResult: Strongly-typed result containing Registry instances with metadata
//
// Current implementations:
//   - ConfigMapSourceHandler: Retrieves registry data from Kubernetes ConfigMaps
//     Supports both ToolHive and Upstream registry formats with format validation
//   - ConfigMapStorageManager: Persists Registry data to Kubernetes ConfigMaps
//
// Future implementations may include:
//   - URLSourceHandler: HTTP/HTTPS endpoints
//   - GitSourceHandler: Git repositories
//   - RegistrySourceHandler: External registries
//
// The package provides a factory pattern for creating appropriate
// source handlers based on the source type configuration, and uses
// strongly-typed Registry instances throughout for type safety.
package sources

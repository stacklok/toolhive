// Package sources provides interfaces and implementations for retrieving
// MCP registry data from various external sources.
//
// The package defines the SourceHandler interface which abstracts the
// process of validating source configurations and synchronizing data
// from external sources such as ConfigMaps, HTTP endpoints, Git repositories,
// or external registries.
//
// Current implementations:
//   - ConfigMapSourceHandler: Retrieves registry data from Kubernetes ConfigMaps
//     Currently supports only ToolHive registry format. Upstream format support
//     is planned for future releases.
//
// Future implementations may include:
//   - URLSourceHandler: HTTP/HTTPS endpoints
//   - GitSourceHandler: Git repositories
//   - RegistrySourceHandler: External registries
//   - Enhanced ConfigMapSourceHandler: Support for upstream registry format
//
// The package also provides a factory pattern for creating appropriate
// source handlers based on the source type configuration.
package sources

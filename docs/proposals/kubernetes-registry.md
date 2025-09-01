# Kubernetes Registry Implementation for ToolHive Operator

## Problem Statement

The ToolHive operator currently supports managing individual MCP servers through `MCPServer` CRDs, but lacks a centralized registry mechanism within Kubernetes. This creates challenges in discoverability, catalog management, upstream compatibility, and operational complexity.

## Goals

- **Native Kubernetes Registry**: Implement registry functionality using Custom Resource Definitions
- **Upstream Format Support**: Leverage existing upstream conversion capabilities for ecosystem compatibility
- **Multi-Registry Support**: Support both local registry entries and external registry synchronization. 
- **Registry Hierarchy**: Support the multi-registry hierarchy defined in the upstream model
- **Application Integration**: Provide REST API for programmatic access to registry data
- **GitOps Compatibility**: Enable declarative registry management through CRD-based operations

## Architecture Overview

The Kubernetes registry implementation extends the operator with the `MCPRegistry` CRD and supporting controllers that work with the existing MCPServer CRD to provide a complete registry-to-deployment workflow.

## CRD Design Overview

### MCPRegistry CRD

The `MCPRegistry` CRD represents a registry source and synchronization configuration with these key components:

- **Source Configuration**: Support for ConfigMap, URL, Git, and Registry API sources
- **Format Specification**: Handle both ToolHive and upstream registry formats  
- **Sync Policy**: Automatic and manual synchronization with configurable intervals
- **Filtering**: Include/exclude servers based on names, tags, tiers, and transports

### Job CRDs (Phase 3)

Declarative operation CRDs for GitOps compatibility:
- `MCPRegistryImportJob`: Declarative import operations
- `MCPRegistryExportJob`: Declarative export operations  
- `MCPRegistrySyncJob`: Declarative synchronization operations

**Detailed specifications**: See [kubernetes-registry/crd-specifications.md](kubernetes-registry/crd-specifications.md)

## Key Features and Capabilities

### 1. Registry Management

The ToolHive operator provides comprehensive registry management through specialized components:

#### Registry Controller
- **Synchronization**: Automatic and manual synchronization with external registry sources
- **Format Conversion**: Bidirectional conversion between ToolHive and upstream registry formats
- **Filtering**: Include/exclude servers based on configurable criteria (names, tags, tiers, transports)
- **Status Tracking**: Monitor sync status, error conditions, and statistics
- **Server Labeling**: Automatically apply registry relationship labels to discovered servers

#### Registry API Service
- **REST API**: HTTP endpoints for programmatic registry and server discovery
- **Authentication**: Integration with Kubernetes RBAC and service account tokens
- **Filtering**: Query servers by registry, category, transport type, and custom labels
- **Format Support**: Return data in both ToolHive and upstream registry formats

### 2. Registry Sources

The implementation supports multiple registry source types, all with both ToolHive and upstream formats. 
All sources support configurable synchronization policies including automatic sync intervals, retry behavior, and update strategies.

Registry sources can be organized in hierarchies as defined in the [MCP Registry Ecosystem Diagram](https://github.com/modelcontextprotocol/registry/blob/main/docs/ecosystem-diagram.excalidraw.svg), enabling upstream registries to aggregate from multiple sources.

This aggregation approach, combined with maintaining the ToolHive registry schema, addresses provenance data handling by extracting it from upstream registry extensions during format conversion.

#### ConfigMap Source
- Store registry data directly in Kubernetes ConfigMaps
- Ideal for small, manually managed registries
- Immediate updates when ConfigMap changes

#### URL Source
- Fetch registry data from HTTP/HTTPS endpoints
- Support for authentication via Secret references
- Custom headers for API integration

#### Git Source
- Clone registry data from Git repositories
- Branch and path specification
- Authentication via SSH keys or tokens
- Version tracking and change detection

#### Registry Source
- Reference another registry's REST API endpoint as a data source
- Enables registry hierarchies and aggregation patterns across clusters
- Supports filtering and transformation of upstream registry data
- Works with any registry implementation that exposes the standard API
- Useful for creating curated subsets or company-specific views of upstream registries

### 3. Server-Registry Relationships

#### Automatic Labeling
When deployed servers are created from registries, the controller automatically applies standardized labels during resource creation:

```yaml
labels:
  toolhive.stacklok.io/registry-name: upstream-community
  toolhive.stacklok.io/registry-namespace: toolhive-system
  toolhive.stacklok.io/server-name: filesystem-server
  toolhive.stacklok.io/tier: Official
  toolhive.stacklok.io/category: filesystem
```

These labels enable filtering, grouping, and querying servers by their registry source.

#### Pre-deployed Server Association
Existing MCPServer resources can be associated with registries by applying the standard labels, enabling unified management across manually deployed and registry-synchronized servers.

## Quick Start Example

```yaml
apiVersion: toolhive.stacklok.io/v1alpha1
kind: MCPRegistry
metadata:
  name: upstream-community
  namespace: toolhive-system
spec:
  displayName: "MCP Community Registry"
  format: upstream
  source:
    type: url
    url:
      url: "https://registry.modelcontextprotocol.io/servers.json"
  syncPolicy:
    enabled: true
    interval: "1h"
```

**Comprehensive examples**: See [kubernetes-registry/usage-examples.md](kubernetes-registry/usage-examples.md)

## Implementation Overview

The implementation follows a phased approach:

1. **Phase 1**: Core Registry CRD and basic synchronization
2. **Phase 2**: External sources, REST API for applications
3. **Phase 3**: CRD-based operations, automatic labeling
4. **Phase 4**: Production features and filtering
5. **Phase 5**: Advanced integration (optional)

**Detailed implementation plan**: See [kubernetes-registry/implementation-plan.md](kubernetes-registry/implementation-plan.md)

## CLI Integration

New registry management commands:
- `thv registry list/add/sync/remove` - Registry lifecycle management
- `thv registry import/export` - Data migration operations  
- `thv search/show` - Enhanced server discovery across registries

**Complete CLI reference**: See [kubernetes-registry/usage-examples.md](kubernetes-registry/usage-examples.md)

## Security and Operations

### Security Model
- **RBAC Integration**: Granular permissions for registry operations
- **Source Validation**: URL restrictions and content validation
- **Authentication**: Secure handling of external source credentials
- **Audit Logging**: Comprehensive operation tracking

### Success Metrics
- **Adoption**: Registry resource creation and server association rates
- **Performance**: <30s sync time, >99% success rate, <100MB memory usage
- **Usability**: Reduced manual configuration complexity
- **Ecosystem**: Upstream registry coverage and format conversion accuracy

**Complete details**: See [kubernetes-registry/implementation-plan.md](kubernetes-registry/implementation-plan.md)

## Future Enhancements

1. **Catalog System** (see [kubernetes-registry/catalog-design.md](kubernetes-registry/catalog-design.md))
   - MCPCatalog CRD for curated server collections
   - Approval workflows and validation pipelines
   - OCI artifact distribution for catalog sharing
   - Role-based catalog access and governance

2. **Advanced Registry Features**
   - Registry federation and cross-cluster synchronization
   - Webhook-based real-time registry updates
   - Registry analytics and usage metrics
   - Content verification and signature validation

3. **Template System** (see [../mcp-server-template-system.md](../mcp-server-template-system.md))
   - Comprehensive template parameter system
   - Template versioning and inheritance
   - Integration with Helm and Kustomize
   - Interactive template wizards and validation

4. **Integration Expansions**
   - GitOps workflow integration with ArgoCD/Flux
   - CI/CD pipeline integration
   - Service mesh integration for advanced networking
   - Multi-cluster registry synchronization

5. **Community Features**
   - Community ratings and reviews for registry entries
   - Automated server discovery from popular repositories
   - Registry contribution workflows and governance

## Conclusion

The Kubernetes Registry implementation provides a cloud-native approach to MCP server management that:

- **Leverages Kubernetes APIs** for native resource management and RBAC
- **Integrates with existing tooling** through standard kubectl and custom CLI commands
- **Supports ecosystem growth** through upstream format compatibility and conversion
- **Enables GitOps workflows** through declarative resource definitions
- **Scales operationally** with automated synchronization and registry-based deployment

This implementation transforms ToolHive into a comprehensive Kubernetes-native platform for MCP server lifecycle management, maintaining backward compatibility while enabling ecosystem integration through upstream format support.
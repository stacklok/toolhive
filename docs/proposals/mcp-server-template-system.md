# MCPServerTemplate System Design

## Overview

This document explores the design considerations for an MCPServerTemplate system that would enable template-based deployment of MCP servers in Kubernetes. This analysis is separated from the core registry proposal to allow focused evaluation of the template system's complexity and customer value.

## Problem Analysis

### Current Server Deployment

Users currently deploy MCP servers by:
1. Creating MCPServer YAML manifests manually
2. Using `thv run` CLI commands with parameters
3. Copying and modifying existing server configurations

### Proposed Template Benefits

Templates could potentially provide:
- **Standardization**: Consistent server configurations across deployments
- **Parameter Validation**: Type checking and constraint validation
- **Reusability**: Shared configurations across teams and projects
- **Documentation**: Self-documenting server configurations

## Key Design Questions

### 1. Template Instance Requirements

**Question**: How many instances of the same MCP server template do customers actually need?

**Analysis**:
- Most MCP servers are designed for single-instance usage per client
- Multiple instances typically differ only in:
  - Environment variables (API keys, database connections)
  - Resource limits and networking
  - Namespace/environment placement

**Implications**:
- Complex parameter systems may be over-engineering
- Simple environment variable substitution may be sufficient
- Focus should be on configuration variation, not structural changes

### 2. Complexity vs. Usability Trade-offs

**Current Complexity Concerns**:
- 15+ template parameter fields with validation rules
- Complex parameter substitution syntax (`${parameter-name}`)
- Type system with string/integer/boolean/array types
- Nested validation rules (pattern, min/max, enum)

**Alternative Approaches**:
- **Simple Substitution**: Basic environment variable replacement
- **Kustomize Integration**: Leverage existing Kubernetes templating
- **Helm Integration**: Use Helm charts for complex deployments
- **ConfigMap Templates**: Store simple templates in ConfigMaps

### 3. Integration with Existing Solutions

**Kubernetes Ecosystem**:
- **Helm**: Mature templating with complex parameter systems
- **Kustomize**: Patch-based configuration management
- **ArgoCD ApplicationSets**: Template-based multi-environment deployment
- **Crossplane Compositions**: Infrastructure-as-code templating

**Questions**:
- Should ToolHive reinvent templating or integrate with existing solutions?
- What unique value does MCPServerTemplate provide over Helm charts?
- How does this interact with GitOps workflows?

## Simplified Design Alternatives

### Alternative 1: ConfigMap-Based Templates

```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: filesystem-server-template
  namespace: toolhive-system
  labels:
    template.toolhive.io/type: mcpserver
data:
  mcpserver.yaml: |
    apiVersion: toolhive.stacklok.io/v1alpha1
    kind: MCPServer
    metadata:
      name: "${NAME}"
      labels:
        app: "${NAME}"
    spec:
      image: "mcpproject/filesystem-server:${VERSION:-latest}"
      transport: "${TRANSPORT:-stdio}"
      env:
        - name: FILESYSTEM_ROOT
          value: "${FILESYSTEM_ROOT}"
        - name: READONLY_MODE
          value: "${READONLY_MODE:-false}"
```

**Benefits**:
- Simple implementation using existing Kubernetes resources
- Easy to understand and modify
- No new CRDs required
- Integrates with standard tooling

**Usage**:
```bash
thv template apply filesystem-server-template \
  --name my-fs-server \
  --param VERSION=1.0.0 \
  --param FILESYSTEM_ROOT=/data \
  --namespace production
```

### Alternative 2: Helm Chart Integration

```yaml
apiVersion: toolhive.stacklok.io/v1alpha1
kind: MCPRegistry
metadata:
  name: helm-based-servers
spec:
  source:
    type: helm
    helm:
      repository: "https://charts.stacklok.io/mcp-servers"
      charts:
        - name: filesystem-server
          version: "1.0.0"
        - name: database-server
          version: "2.1.0"
```

**Benefits**:
- Leverages mature Helm ecosystem
- Complex parameter validation and templating
- Established packaging and distribution
- Community familiarity

### Alternative 3: Registry Metadata Enhancement

Instead of complex templates, enhance registry data with deployment hints:

```yaml
# Registry entry with deployment metadata
{
  "name": "filesystem-server",
  "description": "Filesystem access server",
  "image": "mcpproject/filesystem-server",
  "transport": "stdio",
  "deployment": {
    "parameters": [
      {
        "name": "FILESYSTEM_ROOT",
        "description": "Root path for filesystem access",
        "required": true,
        "type": "path"
      }
    ],
    "defaults": {
      "READONLY_MODE": "false"
    }
  }
}
```

## Recommended Approach

### Phase 1: Registry-Only Implementation

1. **Focus on Registry**: Implement MCPRegistry without templates
2. **Enhanced CLI**: Improve `thv run` with registry integration
3. **Parameter Hints**: Use registry metadata for parameter suggestions
4. **Documentation**: Generate usage examples from registry data

### Phase 2: Simple Template System

If customer demand justifies templates:

1. **ConfigMap Templates**: Start with simple ConfigMap-based approach
2. **Basic Substitution**: Support environment variable substitution only
3. **CLI Integration**: Add `thv template` commands for ConfigMap templates
4. **Validation**: Basic parameter type checking and requirements

### Phase 3: Advanced Integration (If Needed)

Based on real usage patterns:

1. **Helm Integration**: Support Helm charts in registries
2. **Kustomize Support**: Generate kustomization files
3. **GitOps Integration**: Template-based ApplicationSets

## Implementation Considerations

### Customer Research Needed

Before implementing templates, research:
- How many MCP server instances do customers actually deploy?
- What configuration variations are most common?
- How do customers currently manage multiple environments?
- What templating tools are they already using?

### Technical Considerations

1. **Parameter Complexity**: Start simple, add complexity based on demand
2. **Validation Strategy**: Use Kubernetes ValidatingAdmissionWebhooks
3. **Storage**: ConfigMaps vs CRDs vs external systems
4. **Versioning**: How to handle template updates and migrations
5. **Security**: Parameter validation and privilege escalation prevention

### Migration Path

1. **Optional Feature**: Templates should be additive, not required
2. **Backward Compatibility**: Existing MCPServer resources unaffected
3. **Gradual Adoption**: Users can adopt templates incrementally
4. **Export Capability**: Generate templates from existing servers

## Success Metrics for Template System

If implemented, measure:
- **Template Creation Rate**: How many templates are created vs used
- **Parameter Usage**: Which parameters are most commonly customized
- **Error Rates**: Template instantiation failures and causes
- **Adoption Patterns**: Which teams/projects use templates vs direct YAML
- **Support Requests**: Template-related issues and complexity

## Conclusion

The MCPServerTemplate system represents significant complexity for potentially limited customer value. The recommendation is to:

1. **Start with registry-only implementation** to validate customer interest
2. **Research actual customer templating needs** through usage patterns
3. **Consider integration with existing tools** (Helm, Kustomize) before building custom solutions
4. **Implement simple alternatives** (ConfigMap templates) before complex CRD-based systems

This approach allows validation of the template concept while avoiding over-engineering and provides a clear path for future enhancement based on real customer demand.
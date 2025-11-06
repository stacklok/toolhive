# MCPGroup CRD for Kubernetes Operator

## Problem Statement

The CLI supports runtime groups for organizing MCP servers, but this is missing in Kubernetes. The Virtual MCP Server feature (PR #2106) requires groups to discover and aggregate backend servers. Without groups, Kubernetes users cannot use the Virtual MCP or organize their servers logically.

## Goals

- Add MCPGroup support to Kubernetes matching CLI runtime group behavior
- Enable Virtual MCP Server to discover servers in a group
- Maintain API consistency between CLI and Kubernetes
- Keep implementation simple and predictable

## Non-Goals

- Registry groups (CLI-only feature)
- Cross-namespace groups
- Multi-group membership per server
- Client configuration management (not applicable in Kubernetes)

## Design

### Design Decision: MCPGroup CRD vs Labels/Annotations

**Question:** Could we use labels/annotations on MCPServer instead of creating an MCPGroup CRD?

**Answer:** We need MCPGroup as a first-class construct for several reasons:

1. **Meta-MCP and Virtual MCP requirements**: These features need to aggregate multiple MCP servers. They need a way to:
   - Discover which servers belong to a group
   - Reference groups in their configuration
   - Watch for group membership changes

2. **Seamless CLI-to-Kubernetes transition**: The CLI has an explicit Group concept that workloads belong to. Users migrating from CLI to Kubernetes expect the same mental model and API patterns.

3. **Growing ecosystem of constructs**: As we build more features on top of ToolHive (meta-mcp, virtual MCP, future aggregation patterns), we need a consistent way to represent server collections.

4. **Group as an explicit concept**: Labels are meant for flexible, ad-hoc categorization. Groups are a core organizational concept in ToolHive's architecture, deserving explicit representation.

While labels could technically provide grouping, they lack:
- Discoverability (no list of available groups without scanning all servers)
- A place for group-level metadata or status
- Explicit lifecycle management
- Ability to validate references before use

**Conclusion:** MCPGroup CRD provides the foundation for meta-mcp, virtual MCP, and future aggregation features while maintaining consistency with CLI semantics.

### MCPGroup CRD

Simple CRD for grouping servers:

```yaml
apiVersion: mcp.toolhive.stacklok.io/v1alpha1
kind: MCPGroup
metadata:
  name: engineering-team
  namespace: default
spec:
  # Optional human-readable description
  description: "Engineering team MCP servers"

status:
  # Number of servers in this group
  serverCount: 3

  # List of server names for quick reference
  servers:
    - github-server
    - jira-server
    - slack-server

  phase: Ready
  conditions:
    - type: Ready
      status: "True"
      lastTransitionTime: "2025-10-15T10:30:00Z"
```

### MCPServer Spec Addition

Add explicit group field to MCPServer:

```yaml
apiVersion: mcp.toolhive.stacklok.io/v1alpha1
kind: MCPServer
metadata:
  name: github-server
  namespace: default
spec:
  # Existing fields...
  image: ghcr.io/stackloklabs/github-server:latest

  # New: explicit group membership
  groupRef: engineering-team
```

**Rationale for explicit groupRef field:**
- Matches CLI behavior (workload has `Group` field)
- Follows Kubernetes naming conventions for references (`groupRef` instead of `group`)
- Simple and predictable
- Easy to query: `list MCPServers where spec.groupRef = X`
- No confusion about membership
- API consistency with CLI

### API Consistency

CLI runtime groups store membership on the workload:
```go
type Workload struct {
    Name  string
    Group string  // Explicit group membership
}
```

Kubernetes should match this pattern:
```go
type MCPServerSpec struct {
    // Existing fields...

    // GroupRef is the name of the MCPGroup this server belongs to
    // +optional
    GroupRef string `json:"groupRef,omitempty"`
}
```

### Controller Behavior

**MCPGroup Controller:**
- Watches MCPGroup and MCPServer resources
- Updates `status.servers` list when servers join/leave group
- Updates `status.serverCount`
- Validates referenced group exists when MCPServer is created

**MCPServer Controller:**
- Existing reconciliation logic
- Validates `spec.groupRef` references an existing MCPGroup (if specified)
- Adds condition if group reference is invalid

### Discovery API

Virtual MCP (and other features) can discover servers in a group:

```go
// List all MCPServers in a group
servers, err := clientset.McpV1alpha1().MCPServers(namespace).List(ctx, metav1.ListOptions{
    FieldSelector: "spec.groupRef=engineering-team",
})
```

## Implementation

### Phase 1: Core CRD
1. Add `GroupRef` field to MCPServer spec
2. Create MCPGroup CRD types
3. Implement MCPGroup controller
4. Add field selector support for group queries
5. Update CRD manifests and documentation

### Phase 2: Integration
1. Virtual MCP integration with groups
2. kubectl plugin support

## Examples

### Standalone MCPServer (No Group)

MCPServers can run without belonging to a group:

```yaml
apiVersion: mcp.toolhive.stacklok.io/v1alpha1
kind: MCPServer
metadata:
  name: standalone-server
  namespace: default
spec:
  image: ghcr.io/stackloklabs/filesystem:latest
  # No groupRef - server runs independently
```

### MCPServer with Group Membership

```yaml
# Create group
apiVersion: mcp.toolhive.stacklok.io/v1alpha1
kind: MCPGroup
metadata:
  name: engineering-team
  namespace: default
spec:
  description: "Engineering team servers"
---
# Create servers in group
apiVersion: mcp.toolhive.stacklok.io/v1alpha1
kind: MCPServer
metadata:
  name: github-server
spec:
  image: ghcr.io/stackloklabs/github:latest
  groupRef: engineering-team
---
apiVersion: mcp.toolhive.stacklok.io/v1alpha1
kind: MCPServer
metadata:
  name: jira-server
spec:
  image: ghcr.io/company/jira:latest
  groupRef: engineering-team
```

### Virtual MCP Usage

```yaml
# Virtual MCP references the group
# NOTE: This is an example of future MCPVirtualServer API (not yet implemented)
apiVersion: mcp.toolhive.stacklok.io/v1alpha1
kind: MCPVirtualServer
metadata:
  name: engineering-virtual
spec:
  # References existing group
  groupRef: engineering-team

  # Virtual MCP configuration
  aggregation:
    conflictResolution: prefix
```

### Querying Servers in Group

```bash
# List all servers in a group
kubectl get mcpservers -n default --field-selector spec.groupRef=engineering-team

# Check group status
kubectl get mcpgroup engineering-team -o jsonpath='{.status.servers}'
```

## Migration from CLI

CLI groups and Kubernetes groups are separate concepts:
- **CLI groups**: Local runtime groups (`.toolhive/` directory)
- **K8s groups**: Namespace-scoped groups (etcd)

**Key differences from CLI:**
- In CLI: All servers must belong to a group (defaults to "default" group if not specified)
- In K8s: Servers can optionally belong to a group (`spec.groupRef` is optional)

No automatic migration - users manually create MCPGroup resources and set `spec.groupRef` on MCPServers.

## Type Definitions

```go
// MCPGroupSpec defines the desired state of MCPGroup
type MCPGroupSpec struct {
    // Description provides human-readable context
    // +optional
    Description string `json:"description,omitempty"`
}

// MCPGroupStatus defines observed state
type MCPGroupStatus struct {
    // Phase indicates current state
    // +optional
    Phase MCPGroupPhase `json:"phase,omitempty"`

    // Servers lists server names in this group
    // +optional
    Servers []string `json:"servers,omitempty"`

    // ServerCount is the number of servers
    // +optional
    ServerCount int `json:"serverCount"`

    // Conditions represent observations
    // +optional
    Conditions []metav1.Condition `json:"conditions,omitempty"`
}

type MCPGroupPhase string

const (
    MCPGroupPhaseReady MCPGroupPhase = "Ready"
)

// Add to MCPServerSpec
type MCPServerSpec struct {
    // Existing fields...

    // GroupRef is the MCPGroup this server belongs to
    // Must reference an existing MCPGroup in the same namespace
    // +optional
    GroupRef string `json:"groupRef,omitempty"`
}
```

## Open Questions

1. **Should groupRef be immutable after creation?**
   - Recommendation: Allow changes, easier for user corrections

2. **What happens if group is deleted?**
   - Recommendation: Servers continue running, `spec.groupRef` becomes dangling reference
   - Controller will log errors and add conditions to affected MCPServer resources

3. **Should we validate group exists on MCPServer create?**
   - Recommendation: Yes, via controller reconciliation
   - Controller validates groupRef and adds status conditions if invalid
   - No webhook needed - keep implementation simple

## Future Enhancements

- Group-level policies and authorization
- Cross-namespace groups (with security review)
- Group quotas and resource limits

## Testing

- **Unit**: Group validation, status updates
- **Integration (envtest)**: Controller reconciliation, field selectors
- **E2E (Chainsaw)**: Complete group lifecycle, Virtual MCP integration

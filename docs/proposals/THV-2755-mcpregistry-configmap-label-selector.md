# MCPRegistry ConfigMap Label Selector Support

## Problem Statement

The MCPRegistry CRD currently requires explicit ConfigMap references for registry data sources. When managing multiple registries across teams or environments, users must manually update the MCPRegistry resource each time a new ConfigMap is added. This approach:

- Requires tight coupling between MCPRegistry and ConfigMap resources
- Doesn't scale well when multiple teams manage their own registry data
- Requires central coordination to update the MCPRegistry spec

A more Kubernetes-native approach would allow MCPRegistry to dynamically discover ConfigMaps using label selectors, similar to how Services discover Pods.

## Goals

- Add label selector support for ConfigMap discovery in MCPRegistry
- Enable dynamic discovery of registry ConfigMaps without modifying MCPRegistry spec
- Maintain backward compatibility with existing `configMapRef` approach
- Handle server name conflicts gracefully when merging multiple ConfigMaps
- Follow Kubernetes patterns for label-based selection

## Non-Goals

- Cross-namespace ConfigMap selection (security boundary)
- Support for `matchExpressions` (keep initial implementation simple)
- Detailed per-ConfigMap status reporting
- Webhook-based validation (use CEL rules instead)
- **Adding sync/fetch logic to the operator** (sync remains in the registry server)
- **Making the registry server Kubernetes-aware** (it remains agnostic)

## Architecture

This proposal maintains a clear separation of concerns between the operator and the registry server:

```
┌─────────────────────────────────────────────────────────────────────┐
│                    Kubernetes Operator                               │
│  ┌───────────────────────────────────────────────────────────────┐  │
│  │  MCPRegistry Controller                                        │  │
│  │  - Watches ConfigMaps matching label selector                  │  │
│  │  - Aggregates registry data from multiple ConfigMaps           │  │
│  │  - Applies conflict resolution (prefixing)                     │  │
│  │  - Outputs aggregated ConfigMap                                │  │
│  └───────────────────────────────────────────────────────────────┘  │
└─────────────────────────────────────────────────────────────────────┘
                                  │
                                  │ ConfigMap mounted as volume
                                  ▼
┌─────────────────────────────────────────────────────────────────────┐
│                    Registry Server                                   │
│  ┌───────────────────────────────────────────────────────────────┐  │
│  │  File Source Handler                                           │  │
│  │  - Reads registry data from mounted file                       │  │
│  │  - No knowledge of Kubernetes ConfigMaps                       │  │
│  │  - Existing sync/refresh logic unchanged                       │  │
│  └───────────────────────────────────────────────────────────────┘  │
└─────────────────────────────────────────────────────────────────────┘
```

### Key Architectural Principles

1. **Operator handles Kubernetes primitives**: The operator is responsible for watching ConfigMaps, label selection, and aggregation. This is Kubernetes-native work that belongs in the operator.

2. **Registry server remains Kubernetes-agnostic**: The registry server continues to read from file sources. It has no knowledge of ConfigMaps, label selectors, or Kubernetes APIs. This keeps the server portable and testable.

3. **Clear data flow**: Operator discovers → aggregates → outputs ConfigMap → mounted as file → server reads file. The sync interval and refresh logic in the registry server remain unchanged.

4. **No sync logic in operator**: The operator does NOT fetch from git, APIs, or perform periodic syncs. It only reacts to Kubernetes resource changes (ConfigMap create/update/delete events).

## Design

### Design Decision: New Field vs Extending ConfigMapRef

**Question:** Should we extend `configMapRef` or add a new field?

**Answer:** Add a new `configMapSelector` field for these reasons:

1. **Clear semantics**: `configMapRef` references a single, specific ConfigMap. A selector matches multiple ConfigMaps dynamically.
2. **Mutual exclusivity**: Users should choose one approach per registry config entry, not mix them.
3. **API clarity**: Separate fields make the API self-documenting.

### ConfigMapSelector Type

New field in `MCPRegistryConfig`:

```go
type MCPRegistryConfig struct {
    // ... existing fields (Name, Format, ConfigMapRef, Git, API, SyncPolicy, Filter) ...

    // ConfigMapSelector selects ConfigMaps by labels
    // Mutually exclusive with ConfigMapRef, Git, and API
    // +optional
    ConfigMapSelector *ConfigMapSelector `json:"configMapSelector,omitempty"`
}

// ConfigMapSelector defines label-based ConfigMap selection
type ConfigMapSelector struct {
    // MatchLabels is a map of label key-value pairs to match.
    // All labels must match for a ConfigMap to be selected.
    // +kubebuilder:validation:Required
    // +kubebuilder:validation:MinProperties=1
    MatchLabels map[string]string `json:"matchLabels"`

    // Key is the key within each ConfigMap containing registry data.
    // +kubebuilder:default=registry.json
    // +optional
    Key string `json:"key,omitempty"`
}
```

### Conflict Resolution

**Question:** What happens when multiple ConfigMaps define a server with the same name?

**Decision:** Both conflicting servers get prefixed with their ConfigMap name using the fixed format `{configmap-name}/{server-name}`.

Example:
- `configmap-a` defines `github-mcp`
- `configmap-b` defines `github-mcp`
- Result: `configmap-a/github-mcp` and `configmap-b/github-mcp`

Servers without conflicts retain their original names:
- `configmap-a` defines `slack-mcp` (unique)
- Result: `slack-mcp` (no prefix needed)

**Rationale:**
- Explicit: You can always identify the source of conflicting servers
- Non-breaking: Unique server names are unaffected
- Deterministic: Same inputs always produce same outputs
- Simple: Fixed prefix format avoids configuration complexity

### Namespace Scope

**Decision:** Same namespace only.

The selector only matches ConfigMaps in the same namespace as the MCPRegistry. This follows Kubernetes security patterns - resources shouldn't implicitly reference other namespaces.

### Filter Application

**Decision:** Post-merge filtering.

Filters are applied after:
1. All matching ConfigMaps are discovered
2. Registry data is merged
3. Conflict resolution prefixes are applied

This means filter patterns like `configmap-a/*` can match prefixed server names.

### Watch Behavior

The controller watches Kubernetes resources and reacts to changes:

1. Watch ConfigMaps in the MCPRegistry's namespace
2. Filter watches by the selector labels for efficiency
3. Re-reconcile MCPRegistry when matching ConfigMaps are added, modified, or deleted
4. Handle ConfigMaps being added/removed dynamically

**Important**: The controller does NOT poll or periodically sync. It reacts to Kubernetes watch events only. The `syncPolicy.interval` field applies to the registry server's file watching, not to the operator's ConfigMap discovery.

### Partial Failure Handling

When some ConfigMaps are valid and others are not (missing key, invalid JSON, etc.):
- Log warnings for each invalid ConfigMap
- Continue processing valid ConfigMaps
- Update MCPRegistry status with partial success message listing failed ConfigMaps
- Emit a Kubernetes Event for visibility (type: Warning, reason: PartialSyncFailure)

This approach ensures resilience - one misconfigured ConfigMap doesn't block the entire registry.

### Validation

CEL validation rules:
- Mutual exclusivity: `configMapSelector` cannot be used with `configMapRef`, `git`, or `api`
- `matchLabels` must have at least one entry (empty would match all ConfigMaps)

## Implementation

### Phase 1: Core Implementation (Operator Only)

Changes are scoped to the **operator** - no changes to the registry server.

1. **CRD Changes**
   - Add `ConfigMapSelector` type to `mcpregistry_types.go`
   - Add CEL validation for mutual exclusivity
   - Run `task operator-generate` and `task operator-manifests`

2. **ConfigMap Selector Handler** (in operator)
   - Implement `ConfigMapSelectorHandler` in operator's `pkg/sources/`
   - List ConfigMaps matching labels using Kubernetes client
   - Parse registry data from each ConfigMap
   - Implement conflict detection and prefixing logic
   - Output aggregated data to a ConfigMap that the registry server mounts

3. **Controller Updates** (in operator)
   - Add ConfigMap watch with label predicates
   - Trigger reconciliation on matching ConfigMap changes
   - Update the output ConfigMap when source ConfigMaps change

4. **Registry Server Deployment**
   - Mount the aggregated ConfigMap as a volume
   - Configure registry server to use `file` source type pointing to the mounted path
   - No code changes to registry server required

5. **Testing**
   - Unit tests for selector matching and conflict resolution
   - Integration tests with envtest
   - E2E tests with Chainsaw

6. **Documentation**
   - Update CRD reference docs
   - Add usage examples showing the full flow
   - Bump Helm chart version

### What This Does NOT Change

- **Registry server code**: No modifications needed. It continues to read from file sources.
- **Sync logic**: The registry server's sync/refresh mechanism is unchanged.
- **Other source types**: Git and API sources continue to work as before.

### Alternative Considered: Multiple File Sources

The registry server supports multiple registries in a single config, each with its own file source. An alternative approach would be:

1. Mount each discovered ConfigMap as a separate file
2. Dynamically update the server's config with multiple file registries
3. Let the server handle merging

We chose **aggregation in the operator** instead because:
- Server config doesn't need dynamic updates
- Simpler deployment (single file mount)
- Conflict resolution logic is centralized and testable
- Server remains statically configured

### Phase 2: Future Enhancements

**Operator enhancements:**
- `matchExpressions` support for complex selection logic
- Per-ConfigMap status reporting (if needed)
- Configurable conflict resolution strategies (error, prefix, priority)

**Registry server enhancement (separate work):**
- **Directory source support**: Allow the registry server to read all files from a directory instead of a single file. This would enable mounting multiple ConfigMaps to a shared directory, with the server handling aggregation. This simplifies the operator (no aggregation needed) and moves merging logic to the server where it naturally belongs. This change would be in the [toolhive-registry-server](https://github.com/stacklok/toolhive-registry-server) repository, not the operator.

## Examples

### Basic Label Selection

```yaml
# ConfigMaps with registry data
apiVersion: v1
kind: ConfigMap
metadata:
  name: team-a-mcp-servers
  namespace: toolhive-system
  labels:
    toolhive.stacklok.dev/registry: "true"
    team: "platform"
data:
  registry.json: |
    {
      "servers": [
        {"name": "github-mcp", "image": "ghcr.io/github/mcp-server:latest"},
        {"name": "slack-mcp", "image": "ghcr.io/slack/mcp-server:latest"}
      ]
    }
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: team-b-mcp-servers
  namespace: toolhive-system
  labels:
    toolhive.stacklok.dev/registry: "true"
    team: "data"
data:
  registry.json: |
    {
      "servers": [
        {"name": "github-mcp", "image": "ghcr.io/github/mcp-server:v2"},
        {"name": "snowflake-mcp", "image": "ghcr.io/data/snowflake:latest"}
      ]
    }
---
# MCPRegistry that discovers both ConfigMaps
apiVersion: toolhive.stacklok.dev/v1alpha1
kind: MCPRegistry
metadata:
  name: all-teams-registry
  namespace: toolhive-system
spec:
  registries:
    - name: team-registries
      configMapSelector:
        matchLabels:
          toolhive.stacklok.dev/registry: "true"
        key: registry.json
      syncPolicy:
        interval: "5m"
```

**Resulting servers:**
- `team-a-mcp-servers/github-mcp` (prefixed - conflict)
- `team-b-mcp-servers/github-mcp` (prefixed - conflict)
- `slack-mcp` (no prefix - unique)
- `snowflake-mcp` (no prefix - unique)

### With Filtering

```yaml
apiVersion: toolhive.stacklok.dev/v1alpha1
kind: MCPRegistry
metadata:
  name: filtered-registry
  namespace: toolhive-system
spec:
  registries:
    - name: filtered-team-registries
      configMapSelector:
        matchLabels:
          toolhive.stacklok.dev/registry: "true"
        key: registry.json
      filter:
        names:
          include: ["slack-*", "snowflake-*"]
          exclude: ["*-deprecated"]
```

### Multiple Label Matching

```yaml
apiVersion: toolhive.stacklok.dev/v1alpha1
kind: MCPRegistry
metadata:
  name: platform-only-registry
  namespace: toolhive-system
spec:
  registries:
    - name: platform-registries
      configMapSelector:
        matchLabels:
          toolhive.stacklok.dev/registry: "true"
          team: "platform"  # Only match platform team ConfigMaps
        key: registry.json
```

### Mixed Sources

```yaml
apiVersion: toolhive.stacklok.dev/v1alpha1
kind: MCPRegistry
metadata:
  name: mixed-sources-registry
  namespace: toolhive-system
spec:
  registries:
    # Dynamic discovery via labels
    - name: team-registries
      configMapSelector:
        matchLabels:
          toolhive.stacklok.dev/registry: "true"
        key: registry.json
    # Explicit ConfigMap reference
    - name: core-registry
      configMapRef:
        name: core-mcp-servers
        key: registry.json
    # Git source
    - name: community-registry
      git:
        repository: https://github.com/org/mcp-registry
        branch: main
        path: registry.json
```

## Type Definitions

```go
// ConfigMapSelector defines label-based ConfigMap selection for registry data.
// When specified, the controller discovers ConfigMaps matching the labels
// and merges their registry data.
type ConfigMapSelector struct {
    // MatchLabels is a map of label key-value pairs to match.
    // A ConfigMap must have ALL specified labels to be selected.
    // At least one label must be specified.
    // +kubebuilder:validation:Required
    // +kubebuilder:validation:MinProperties=1
    MatchLabels map[string]string `json:"matchLabels"`

    // Key is the key within each matched ConfigMap that contains registry data.
    // The data must be valid ToolHive registry JSON format.
    // +kubebuilder:default=registry.json
    // +optional
    Key string `json:"key,omitempty"`
}
```

CEL validation rule for mutual exclusivity (add to MCPRegistryConfig):

```go
// +kubebuilder:validation:XValidation:rule="[has(self.configMapRef), has(self.configMapSelector), has(self.git), has(self.api)].filter(x, x).size() == 1",message="exactly one source type must be specified (configMapRef, configMapSelector, git, or api)"
```

## Testing

- **Unit tests**: Label matching, conflict detection, prefix logic, merge behavior
- **Integration tests (envtest)**: Controller watches, ConfigMap discovery, reconciliation triggers
- **E2E tests (Chainsaw)**: Full lifecycle with dynamic ConfigMap creation/deletion

## References

- [MCPRegistry CRD](../../cmd/thv-operator/api/v1alpha1/mcpregistry_types.go)
- [MCPRegistry Controller](../../cmd/thv-operator/controllers/mcpregistry_controller.go)
- [Kubernetes Label Selectors](https://kubernetes.io/docs/concepts/overview/working-with-objects/labels/)

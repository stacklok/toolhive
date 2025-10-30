# VirtualMCPServer API Reference

## Overview

The `VirtualMCPServer` CRD enables aggregation of multiple backend MCPServers into a unified virtual endpoint. This allows clients to interact with multiple MCP servers through a single interface, with features like:

- **Unified authentication**: Single authentication point for clients
- **Backend discovery**: Automatic discovery of backend authentication configurations
- **Tool aggregation**: Intelligent conflict resolution when multiple backends expose tools with the same name
- **Composite tools**: Define workflows that orchestrate calls across multiple backends
- **Token caching**: Efficient token exchange and caching for improved performance

## API Group and Version

- **Group**: `toolhive.stacklok.dev`
- **Version**: `v1alpha1`
- **Kind**: `VirtualMCPServer`

## Resource Names

- **Singular**: `virtualmcpserver`
- **Plural**: `virtualmcpservers`
- **Short Names**: `vmcp`, `virtualmcp`

## Spec Fields

### `.spec.groupRef` (required)

References an existing `MCPGroup` that defines the backend workloads to aggregate.

**Type**: `GroupRef`

**Fields**:
- `name` (string, required): Name of the MCPGroup resource in the same namespace

**Example**:
```yaml
spec:
  groupRef:
    name: engineering-team
```

### `.spec.incomingAuth` (optional)

Configures authentication for clients connecting to the Virtual MCP server. Reuses MCPServer OIDC and authorization patterns.

**Type**: `IncomingAuthConfig`

**Fields**:
- `oidcConfig` (OIDCConfigRef, optional): OIDC authentication configuration
- `authzConfig` (AuthzConfigRef, optional): Authorization policy configuration

**Example**:
```yaml
spec:
  incomingAuth:
    oidcConfig:
      type: kubernetes
      kubernetes:
        audience: vmcp
    authzConfig:
      type: inline
      inline:
        policies:
          - |
            permit(
              principal,
              action == Action::"tools/call",
              resource
            );
```

### `.spec.outgoingAuth` (optional)

Configures authentication from Virtual MCP to backend MCPServers.

**Type**: `OutgoingAuthConfig`

**Fields**:
- `source` (string, optional): How backend authentication configurations are determined
  - `discovered` (default): Automatically discover from backend's `MCPServer.spec.externalAuthConfigRef`
  - `inline`: Explicit per-backend configuration in VirtualMCPServer
  - `mixed`: Discover most, override specific backends
- `default` (BackendAuthConfig, optional): Default behavior for backends without explicit auth config
- `backends` (map[string]BackendAuthConfig, optional): Per-backend authentication overrides

**Example (discovered mode)**:
```yaml
spec:
  outgoingAuth:
    source: discovered
    default:
      type: pass_through
```

**Example (inline mode)**:
```yaml
spec:
  outgoingAuth:
    source: inline
    backends:
      github:
        type: external_auth_config_ref
        externalAuthConfigRef:
          name: github-token-exchange
      slack:
        type: service_account
        serviceAccount:
          credentialsRef:
            name: slack-bot-token
            key: token
          headerName: Authorization
          headerFormat: "Bearer {token}"
```

#### BackendAuthConfig

**Fields**:
- `type` (string, required): Authentication type
  - `discovered`: Automatically discover from backend
  - `pass_through`: Forward client token unchanged
  - `service_account`: Use service account credentials
  - `external_auth_config_ref`: Reference an MCPExternalAuthConfig resource
- `serviceAccount` (ServiceAccountAuth, optional): Service account configuration (when type=service_account)
- `externalAuthConfigRef` (ExternalAuthConfigRef, optional): Auth config reference (when type=external_auth_config_ref)

### `.spec.aggregation` (optional)

Defines tool aggregation and conflict resolution strategies.

**Type**: `AggregationConfig`

**Fields**:
- `conflictResolution` (string, optional, default: "prefix"): Strategy for resolving tool name conflicts
  - `prefix`: Automatically prefix tool names with workload identifier
  - `priority`: First workload in priority order wins
  - `manual`: Explicitly define overrides for all conflicts
- `conflictResolutionConfig` (ConflictResolutionConfig, optional): Configuration for the chosen strategy
- `tools` ([]WorkloadToolConfig, optional): Per-workload tool filtering and overrides

**Example (prefix strategy)**:
```yaml
spec:
  aggregation:
    conflictResolution: prefix
    conflictResolutionConfig:
      prefixFormat: "{workload}_"
    tools:
      - workload: github
        filter: ["create_pr", "merge_pr"]
      - workload: jira
        toolConfigRef:
          name: jira-tool-config
```

**Example (priority strategy)**:
```yaml
spec:
  aggregation:
    conflictResolution: priority
    conflictResolutionConfig:
      priorityOrder: ["github", "jira", "slack"]
```

#### WorkloadToolConfig

**Fields**:
- `workload` (string, required): Name of the backend MCPServer workload
- `toolConfigRef` (ToolConfigRef, optional): Reference to MCPToolConfig resource
- `filter` ([]string, optional): Inline list of tool names to allow (only used if toolConfigRef not specified)
- `overrides` (map[string]ToolOverride, optional): Inline tool overrides (only used if toolConfigRef not specified)

### `.spec.compositeTools` (optional)

Defines inline composite tool workflows. For complex workflows, reference VirtualMCPCompositeToolDefinition resources instead.

**Type**: `[]CompositeToolSpec`

**Fields**:
- `name` (string, required): Name of the composite tool
- `description` (string, required): Description of the composite tool
- `parameters` (map[string]ParameterSpec, optional): Input parameters
- `steps` ([]WorkflowStep, required): Workflow steps
- `timeout` (string, optional, default: "30m"): Maximum execution time

**Example**:
```yaml
spec:
  compositeTools:
    - name: deploy_and_notify
      description: Deploy PR with user confirmation and notification
      parameters:
        pr_number:
          type: integer
          required: true
      steps:
        - id: merge
          tool: github.merge_pr
          arguments:
            pr: "{{.params.pr_number}}"
        - id: confirm_deploy
          type: elicitation
          message: "PR {{.params.pr_number}} merged. Proceed with deployment?"
          dependsOn: ["merge"]
        - id: deploy
          tool: kubernetes.deploy
          arguments:
            pr: "{{.params.pr_number}}"
          dependsOn: ["confirm_deploy"]
```

### `.spec.tokenCache` (optional)

Configures token caching behavior.

**Type**: `TokenCacheConfig`

**Fields**:
- `provider` (string, optional, default: "memory"): Cache provider type (`memory` or `redis`)
- `memory` (MemoryCacheConfig, optional): In-memory cache configuration
- `redis` (RedisCacheConfig, optional): Redis cache configuration

**Example (memory)**:
```yaml
spec:
  tokenCache:
    provider: memory
    memory:
      maxEntries: 1000
      ttlOffset: 5m
```

**Example (redis)**:
```yaml
spec:
  tokenCache:
    provider: redis
    redis:
      address: redis:6379
      db: 0
      passwordRef:
        name: redis-secret
        key: password
```

### `.spec.operational` (optional)

Defines operational settings like timeouts and health checks.

**Type**: `OperationalConfig`

**Fields**:
- `timeouts` (TimeoutConfig, optional): Timeout configuration
- `failureHandling` (FailureHandlingConfig, optional): Failure handling configuration

**Example**:
```yaml
spec:
  operational:
    timeouts:
      default: 30s
      perWorkload:
        github: 45s
    failureHandling:
      healthCheckInterval: 30s
      unhealthyThreshold: 3
      partialFailureMode: fail
      circuitBreaker:
        enabled: true
        failureThreshold: 5
        timeout: 60s
```

### `.spec.podTemplateSpec` (optional)

Defines the pod template for customizing the Virtual MCP server pod configuration. Use the `vmcp` container name to modify the Virtual MCP server container.

**Type**: `runtime.RawExtension`

**Example**:
```yaml
spec:
  podTemplateSpec:
    spec:
      containers:
        - name: vmcp
          resources:
            requests:
              memory: "256Mi"
              cpu: "500m"
            limits:
              memory: "512Mi"
              cpu: "1000m"
```

## Status Fields

### `.status.conditions`

Standard Kubernetes conditions representing the latest observations of the VirtualMCPServer's state.

**Type**: `[]metav1.Condition`

**Standard Condition Types**:
- `Ready`: Indicates whether the VirtualMCPServer is ready
- `AuthConfigured`: Indicates whether authentication is configured
- `BackendsDiscovered`: Indicates whether backends have been discovered
- `GroupRefValidated`: Indicates whether the GroupRef is valid

### `.status.discoveredBackends`

Lists discovered backend configurations when `source=discovered`.

**Type**: `[]DiscoveredBackend`

**Fields**:
- `name` (string): Name of the backend MCPServer
- `authConfigRef` (string): Name of the discovered MCPExternalAuthConfig
- `authType` (string): Type of authentication configured
- `status` (string): Current status (`ready`, `degraded`, `unavailable`)
- `lastHealthCheck` (metav1.Time): Timestamp of the last health check
- `url` (string): URL of the backend MCPServer

### `.status.capabilities`

Summarizes aggregated capabilities from all backends.

**Type**: `CapabilitiesSummary`

**Fields**:
- `toolCount` (int): Total number of tools exposed
- `resourceCount` (int): Total number of resources exposed
- `promptCount` (int): Total number of prompts exposed
- `compositeToolCount` (int): Number of composite tools defined

### `.status.phase`

Current phase of the VirtualMCPServer.

**Type**: `VirtualMCPServerPhase`

**Values**:
- `Pending`: VirtualMCPServer is being initialized
- `Ready`: VirtualMCPServer is ready and serving requests
- `Degraded`: VirtualMCPServer is running but some backends are unavailable
- `Failed`: VirtualMCPServer has failed

### `.status.message`

Provides additional information about the current phase.

**Type**: `string`

### `.status.url`

URL where the Virtual MCP server can be accessed.

**Type**: `string`

### `.status.observedGeneration`

The most recent generation observed for this VirtualMCPServer.

**Type**: `int64`

## Complete Example

```yaml
apiVersion: toolhive.stacklok.dev/v1alpha1
kind: VirtualMCPServer
metadata:
  name: engineering-vmcp
  namespace: default
spec:
  # Reference to MCPGroup defining backend workloads
  groupRef:
    name: engineering-team

  # Client authentication
  incomingAuth:
    oidcConfig:
      type: kubernetes
      kubernetes:
        audience: vmcp
    authzConfig:
      type: inline
      inline:
        policies:
          - |
            permit(
              principal,
              action == Action::"tools/call",
              resource
            );

  # Backend authentication (discovered mode)
  outgoingAuth:
    source: discovered
    default:
      type: pass_through
    backends:
      slack:  # Override for specific backend
        type: service_account
        serviceAccount:
          credentialsRef:
            name: slack-bot-token
            key: token

  # Tool aggregation
  aggregation:
    conflictResolution: prefix
    conflictResolutionConfig:
      prefixFormat: "{workload}_"
    tools:
      - workload: github
        filter: ["create_pr", "merge_pr"]
      - workload: jira
        toolConfigRef:
          name: jira-tool-config

  # Composite tools
  compositeTools:
    - name: investigate_incident
      description: Gather logs and metrics for incident analysis
      parameters:
        incident_id:
          type: string
          required: true
      steps:
        - id: fetch_logs
          tool: fetch.fetch
          arguments:
            url: "https://logs.company.com/api/query?incident={{.params.incident_id}}"
        - id: create_report
          tool: jira.create_issue
          arguments:
            title: "Incident {{.params.incident_id}} Analysis"
            description: "{{.steps.fetch_logs.output}}"
          dependsOn: ["fetch_logs"]

  # Token caching
  tokenCache:
    provider: memory
    memory:
      maxEntries: 1000
      ttlOffset: 5m

  # Operational settings
  operational:
    timeouts:
      default: 30s
      perWorkload:
        github: 45s
    failureHandling:
      healthCheckInterval: 30s
      unhealthyThreshold: 3
      partialFailureMode: fail
      circuitBreaker:
        enabled: true
        failureThreshold: 5
        timeout: 60s

status:
  phase: Ready
  message: "Virtual MCP serving 3 backends with 15 tools"
  url: "http://engineering-vmcp.default.svc.cluster.local:8080"
  observedGeneration: 1

  conditions:
    - type: Ready
      status: "True"
      lastTransitionTime: "2025-10-20T10:00:00Z"
      reason: AllBackendsReady
      message: "Virtual MCP is ready and serving requests"
    - type: AuthConfigured
      status: "True"
      reason: IncomingAuthValid
      message: "Incoming authentication configured"
    - type: BackendsDiscovered
      status: "True"
      reason: DiscoveryComplete
      message: "Discovered 3 backends with authentication"

  discoveredBackends:
    - name: github
      authConfigRef: github-token-exchange
      authType: token_exchange
      status: ready
      lastHealthCheck: "2025-10-20T10:05:00Z"
      url: "http://github-mcp.default.svc.cluster.local:8080"
    - name: jira
      authConfigRef: jira-token-exchange
      authType: token_exchange
      status: ready
      lastHealthCheck: "2025-10-20T10:05:00Z"
      url: "http://jira-mcp.default.svc.cluster.local:8080"
    - name: slack
      authConfigRef: ""
      authType: service_account
      status: ready
      lastHealthCheck: "2025-10-20T10:05:00Z"
      url: "http://slack-mcp.default.svc.cluster.local:8080"

  capabilities:
    toolCount: 15
    resourceCount: 3
    promptCount: 2
    compositeToolCount: 1
```

## Validation

The VirtualMCPServer CRD includes comprehensive validation:

1. **Required Fields**: `spec.groupRef.name` must be specified
2. **Reference Validation**: All references (groupRef, authConfigRef, toolConfigRef) must be valid
3. **Conflict Resolution**: Priority strategy requires `priorityOrder` configuration
4. **Composite Tools**: Must have unique names, valid steps with IDs, and proper dependencies
5. **Token Cache**: Redis provider requires valid address configuration
6. **Same-Namespace References**: All references must be in the same namespace for security

## Related Resources

- [MCPGroup](./mcpgroup-api.md): Defines groups of MCPServers
- [MCPServer](./mcpserver-api.md): Individual MCP server instances
- [MCPExternalAuthConfig](./mcpexternalauthconfig-api.md): External authentication configuration
- [MCPToolConfig](./toolconfig-api.md): Tool filtering and renaming configuration
- [Virtual MCP Proposal](../proposals/THV-2106-virtual-mcp-server.md): Complete design proposal

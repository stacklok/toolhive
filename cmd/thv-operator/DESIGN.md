# Design & Decisions

This document captures architectural decisions and design patterns for the ToolHive Operator.

## Operator Design Principles

### CRD Attribute vs `PodTemplateSpec`

When building operators, the decision of when to use a `podTemplateSpec` and when to use a CRD attribute is always disputed. For the ToolHive Operator we have a defined rule of thumb.

#### Use Dedicated CRD Attributes For:
- **Business logic** that affects your operator's behavior
- **Validation requirements** (ranges, formats, constraints)
- **Cross-resource coordination** (affects Services, ConfigMaps, etc.)
- **Operator decision making** (triggers different reconciliation paths)

#### Use PodTemplateSpec For:
- **Infrastructure concerns** (node selection, resources, affinity)
- **Sidecar containers**
- **Standard Kubernetes pod configuration**
- **Things a cluster admin would typically configure**

#### Quick Decision Test:
1. **"Does this affect my operator's reconciliation logic?"** -> Dedicated attribute
2. **"Is this standard Kubernetes pod configuration?"** -> PodTemplateSpec
3. **"Do I need to validate this beyond basic Kubernetes validation?"** -> Dedicated attribute

## MCPRegistry Architecture Decisions

### Status Management Design

**Decision**: Use batched status updates via StatusCollector pattern instead of individual field updates.

**Rationale**:
- Prevents race conditions between multiple status updates
- Reduces API server load with fewer update calls
- Ensures consistent status across reconciliation cycles
- Handles resource version conflicts gracefully

**Implementation**: StatusCollector interface collects all changes and applies them atomically.

### Sync Operation Design

**Decision**: Separate sync decision logic from sync execution with clear interfaces.

**Rationale**:
- Testability: Mock sync decisions independently from execution
- Flexibility: Different sync strategies without changing core logic
- Maintainability: Clear separation of concerns

**Key Patterns**:
- Idempotent operations for safe retry
- Manual vs automatic sync distinction
- Data preservation on failures

### Storage Architecture

**Decision**: Abstract storage via StorageManager interface with ConfigMap as default implementation.

**Rationale**:
- Future flexibility: Easy addition of new storage backends (OCI, databases)
- Testability: Mock storage for unit tests
- Consistency: Single interface for all storage operations

**Current Implementation**: ConfigMap-based with owner references for automatic cleanup.

### Registry API Service Pattern

**Decision**: Deploy individual API service per MCPRegistry rather than shared service.

**Rationale**:
- **Isolation**: Each registry has independent lifecycle and scaling
- **Security**: Per-registry access control possible
- **Reliability**: Failure of one registry doesn't affect others
- **Lifecycle Management**: Automatic cleanup via owner references

**Trade-offs**: More resources consumed but better isolation and security.

### Error Handling Strategy

**Decision**: Structured error types with progressive retry backoff.

**Rationale**:
- Different error types need different handling strategies
- Progressive backoff prevents thundering herd problems
- Structured errors enable better observability

**Implementation**: 5m initial retry, exponential backoff with cap, manual sync bypass.

### Performance Design Decisions

#### Resource Optimization
- **Status Updates**: Batched to reduce API calls (implemented)
- **Source Fetching**: Planned caching to avoid repeated downloads
- **API Deployment**: Lazy creation only when needed (implemented)

#### Memory Management
- **Git Operations**: Shallow clones to minimize disk usage (implemented)
- **Large Registries**: Stream processing planned for future
- **Status Objects**: Efficient field-level updates (implemented)

### Security Architecture

#### Permission Model
Minimal required permissions following principle of least privilege:
- ConfigMaps: For storage management
- Services/Deployments: For API service management
- MCPRegistry: For status updates

#### Network Security
Optional network policies for registry API access control in security-sensitive environments.

## VirtualMCPServer Status Ownership

### Decision: Split Status Ownership Between Controller and Runtime

**Context**: The VirtualMCPServer controller and the vMCP runtime both need to report status, but they have access to different types of information at different times.

**Decision**: Implement a clear ownership boundary for status fields:
- **Controller owns infrastructure status** (reconcile-time concerns)
- **Runtime owns operational status** (request-time concerns)

### Controller Responsibilities (Infrastructure Status)

The controller manages infrastructure-related status at reconcile time:

**Owned Status Fields**:
- `status.url` - VirtualMCPServer service URL
- Infrastructure conditions:
  - `DeploymentReady` - Deployment health
  - `ServiceReady` - Service availability
  - `GroupRefValid` - MCPGroup validation
  - `AuthConfigured` - Authentication setup

**Owned Resources**:
- Deployment with vMCP container
- Service for client access
- ConfigMap with vMCP configuration
- RBAC resources (ServiceAccount, Role, RoleBinding)

**Key Pattern**: Controller validates CRD configuration and ensures infrastructure resources exist and are healthy.

### Runtime Responsibilities (Operational Status)

The vMCP runtime reports operational status via the StatusReporter interface:

**Owned Status Fields**:
- `status.phase` - Overall operational state (Ready/Degraded/Failed/Pending)
- `status.message` - Human-readable status description
- `status.backendCount` - Number of healthy backends
- `status.discoveredBackends[]` - List of backend servers with:
  - Backend name, status (ready/degraded/unavailable), URL
  - Last health check timestamp
  - Auth configuration reference
- `status.conditions[]` - Operational conditions:
  - `ServerReady` - vMCP server health
  - `BackendsDiscovered` - Backend discovery status
  - Backend connectivity conditions
- `status.observedGeneration` - Generation tracking

**Key Pattern**: Runtime discovers backends dynamically at request time, performs actual MCP connectivity checks, and reports real operational health.

### Rationale

**Why Split Ownership?**

1. **Different Information Access**:
   - Controller knows about K8s resource state (Deployments, Services)
   - Runtime knows about MCP connectivity and backend health
   - Only runtime can perform actual MCP protocol health checks

2. **Different Timing**:
   - Controller operates at reconcile intervals (when resources change)
   - Runtime operates continuously (health checks every N seconds)
   - Backend health changes don't trigger reconciliation

3. **Avoid Duplication**:
   - Previous design had controller infer backend health from MCPServer.Status
   - Runtime had actual connectivity information
   - "Last writer wins" created inconsistent status

4. **Separation of Concerns**:
   - Infrastructure concerns (Deployment ready?) vs operational concerns (Backend responding?)
   - Controller: "Is the infrastructure deployed correctly?"
   - Runtime: "Is the application working correctly?"

### Implementation Details

**StatusReporter Interface** (`pkg/vmcp/status/reporter.go`):
```go
type Reporter interface {
    Report(ctx context.Context, status *RuntimeStatus) error
    Start(ctx context.Context, interval time.Duration, statusFunc func() *RuntimeStatus) error
    Stop()
}
```

**K8sReporter Implementation** (`pkg/vmcp/status/k8s_reporter.go`):
- Creates K8s client using in-cluster config
- Identifies VirtualMCPServer resource via `VMCP_NAME` and `VMCP_NAMESPACE` env vars
- Periodically calls status function to get current runtime state
- Updates `VirtualMCPServer.Status` subresource with operational fields

**Environment Variables** (set by controller in Deployment):
- `VMCP_NAME` - Name of the VirtualMCPServer resource
- `VMCP_NAMESPACE` - Namespace of the VirtualMCPServer resource

**Status Update Flow**:
1. Controller reconciles VirtualMCPServer CRD
2. Controller creates/updates Deployment with VMCP_NAME/VMCP_NAMESPACE env vars
3. vMCP runtime starts and creates K8sReporter
4. K8sReporter starts periodic status reporting (e.g., every 30 seconds)
5. Runtime discovers backends, performs health checks
6. K8sReporter updates VirtualMCPServer.Status with operational data
7. Controller and runtime updates are independent and don't conflict

### Benefits

- **Accurate Status**: Status reflects actual operational health, not inferred state
- **Real-time Updates**: Runtime reports health changes immediately
- **No Reconciliation Loops**: Backend health changes don't trigger controller reconciliation
- **Clear Ownership**: No conflicts about who writes which fields
- **Better Observability**: Status shows both infrastructure AND operational health

### Trade-offs

- **Complexity**: Two systems writing to status requires clear boundaries
- **Eventual Consistency**: Brief inconsistency possible during updates
- **Runtime Permissions**: vMCP needs RBAC to update its own status

**Mitigation**: Clear documentation, comprehensive e2e tests, and StatusReporter interface abstraction.

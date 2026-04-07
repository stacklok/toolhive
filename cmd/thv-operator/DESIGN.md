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

**Decision**: Use standard Kubernetes workload status pattern matching MCPServer — flat `Phase` + `Ready` condition + `ReadyReplicas` + `URL`.

**Rationale**:
- Consistency with MCPServer and standard Kubernetes workload patterns
- Enables `kubectl wait --for=condition=Ready` and standard monitoring
- The operator only needs to track deployment readiness, not internal registry server state
- Tracking internal sync/API states would require the operator to call the registry server, which with auth enabled is not feasible

**Implementation**: Controller sets `Phase`, `Message`, `URL`, `ReadyReplicas`, and a `Ready` condition directly based on the API deployment's readiness. The latest resource version is refetched before status updates to avoid conflicts.

**History**: The original design used a `StatusCollector` pattern (`mcpregistrystatus` package) that batched status changes from multiple independent sources — an `APIStatusCollector` for deployment state and originally a sync collector — then applied them atomically via a single `Status().Update()`. A `StatusDeriver` computed the overall phase from sub-phases (`SyncPhase` + `APIPhase` → `MCPRegistryPhase`). This was removed because with sync operations moved to the registry server itself, only one status source remained (deployment readiness), making the batching/derivation indirection unnecessary. The new approach produces the same number of API server calls with less abstraction.

### Registry API Service Pattern

**Decision**: Deploy individual API service per MCPRegistry rather than shared service.

**Rationale**:
- **Isolation**: Each registry has independent lifecycle and scaling
- **Security**: Per-registry access control possible
- **Reliability**: Failure of one registry doesn't affect others
- **Lifecycle Management**: Automatic cleanup via owner references

**Trade-offs**: More resources consumed but better isolation and security.

### Error Handling Strategy

**Decision**: Structured error types (`registryapi.Error`) with condition metadata.

**Rationale**:
- Different error types need different handling strategies
- Structured errors carry `ConditionReason` for setting Kubernetes conditions with specific failure reasons (e.g., `ConfigMapFailed`, `DeploymentFailed`)
- Enables better observability via condition reasons

**Implementation**: `registryapi.Error` carries `ConditionType`, `ConditionReason`, and `Message`. The controller uses `errors.As` to extract structured fields when available, falling back to generic `NotReady` reason for unstructured errors.

### Performance Design Decisions

#### Resource Optimization
- **Status Updates**: Single refetch-then-update per reconciliation cycle
- **API Deployment**: Lazy creation only when needed (implemented)

### Security Architecture

#### Permission Model
Minimal required permissions following principle of least privilege:
- ConfigMaps: For storage management
- Services/Deployments: For API service management
- MCPRegistry: For status updates

#### Network Security
Optional network policies for registry API access control in security-sensitive environments.

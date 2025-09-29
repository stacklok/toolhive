# Design & Decisions

This document captures architectural decisions and design patterns for the ToolHive Operator. For user-facing documentation, see [REGISTRY.md](REGISTRY.md).

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

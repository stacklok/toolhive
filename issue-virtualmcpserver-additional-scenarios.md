# Add Additional Integration Test Scenarios for VirtualMCPServer Controller

## Priority
🟡 **MEDIUM**

## Description
While VirtualMCPServer has some integration test coverage, key scenarios are missing. This issue tracks the addition of missing test coverage for important functionality.

## Background
As identified in issue #1761, the VirtualMCPServer controller has moderate coverage but is missing tests for several critical scenarios including GroupRef validation, backend discovery, authentication configuration, and composite tool references.

## Existing Coverage
Currently tested:
- PodTemplateSpec validation and updates
- ExternalAuth watch tests
- CompositeTool watch tests
- Elicitation integration tests

## Missing Test Coverage

### GroupRef and Backend Discovery
- [ ] **GroupRef validation** - invalid group references
- [ ] **GroupRef validation** - valid group references
- [ ] **GroupRef validation** - cross-namespace group references
- [ ] **Backend discovery from MCPGroup** - verify DiscoveredBackends status
- [ ] Backend discovery updates when group membership changes
- [ ] Multiple groups referenced (if supported)

### Authentication Configuration
- [ ] **IncomingAuth configuration** - anonymous mode
- [ ] **IncomingAuth configuration** - OIDC mode
- [ ] **IncomingAuth configuration** - local mode
- [ ] **OutgoingAuth configuration** - discovered mode (from backends)
- [ ] **OutgoingAuth configuration** - inline mode (explicit configs)
- [ ] Multiple outgoing auth configs for different backends
- [ ] Auth config updates and propagation

### Composite Tool References
- [ ] **CompositeToolRefs** - valid reference validation
- [ ] **CompositeToolRefs** - invalid reference validation
- [ ] **CompositeToolRefs** - missing reference handling
- [ ] Multiple composite tool references
- [ ] Composite tool definition updates triggering reconciliation

### Aggregation Configuration
- [ ] **Aggregation strategy** - prefix-based conflict resolution
- [ ] **Aggregation strategy** - priority-based conflict resolution
- [ ] **Aggregation strategy** - manual conflict resolution
- [ ] Capability merging from multiple backends
- [ ] Tool/resource/prompt aggregation from backends

### Resource Management
- [ ] Service creation with correct ports
- [ ] ConfigMap creation with vmcp config JSON
- [ ] ConfigMap updates when configuration changes
- [ ] Deployment creation and configuration
- [ ] Resource cleanup on deletion (verify owner references work)

### Status Conditions
- [ ] Status condition: BackendsDiscovered
- [ ] Status condition: GroupRefValidated
- [ ] Status condition: CompositeToolsValidated
- [ ] Status condition: AuthConfigured
- [ ] Status condition: Ready

## Implementation Details

**Suggested location:** `cmd/thv-operator/test-integration/virtualmcp/`

**Recommended approach:**
1. Extend existing tests in `cmd/thv-operator/test-integration/virtualmcp/`
2. Create new test files for logical groupings:
   - `virtualmcp_groupref_integration_test.go` - GroupRef and backend discovery tests
   - `virtualmcp_auth_integration_test.go` - Authentication configuration tests
   - `virtualmcp_aggregation_integration_test.go` - Aggregation strategy tests
3. Create helper functions for:
   - Building VirtualMCPServer with various configurations
   - Creating MCPGroup with backend members
   - Verifying backend discovery status
   - Verifying ConfigMap content
4. Follow existing patterns from MCPServer tests for similar scenarios

## Acceptance Criteria
- [ ] All missing test scenarios are implemented
- [ ] Tests verify GroupRef validation and backend discovery
- [ ] Tests cover all authentication modes
- [ ] Tests validate composite tool references
- [ ] Tests verify aggregation strategies
- [ ] Tests follow existing integration test patterns
- [ ] Tests pass reliably in CI/CD
- [ ] Code coverage for VirtualMCPServer controller increases significantly

## Related Issues
- Parent issue: #1761 - Add Integration Tests for ToolHive Operator

## References
- Controller implementation: `cmd/thv-operator/internal/controller/virtualmcpserver_controller.go`
- CRD definition: `cmd/thv-operator/api/v1alpha1/virtualmcpserver_types.go`
- Existing tests: `cmd/thv-operator/test-integration/virtualmcp/`
- Backend discovery logic: `pkg/vmcp/aggregator/`
- Auth implementation: `pkg/vmcp/auth/`

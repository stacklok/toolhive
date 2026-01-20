# Add Cross-Resource Integration Tests for Operator

## Priority
🟡 **MEDIUM**

## Description
Add integration tests that verify multiple operator resources working together in realistic end-to-end scenarios. These tests validate the interactions and cascading updates between different CRDs.

## Background
As identified in issue #1761, while we have good coverage for individual controllers, we need tests that verify resources work correctly together in realistic workflows involving multiple CRDs.

## Recommended Test Scenarios

### End-to-End vMCP Workflow
- [ ] **Complete vMCP setup**:
  1. Create MCPGroup
  2. Create MCPServer (stdio transport)
  3. Create VirtualMCPServer with GroupRef to the group
  4. Verify backend discovery flows correctly
  5. Verify VirtualMCPServer status shows discovered backends
  6. Verify all Kubernetes resources created correctly (Deployments, Services, ConfigMaps)

- [ ] **vMCP with multiple backend types**:
  1. Create MCPGroup
  2. Create MCPServer (backend 1)
  3. Create MCPRemoteProxy (backend 2)
  4. Create VirtualMCPServer referencing the group
  5. Verify both backends are discovered
  6. Verify aggregation works correctly

### Auth Config Cascade
- [ ] **MCPExternalAuthConfig propagation to MCPServer**:
  1. Create MCPExternalAuthConfig
  2. Create MCPServer referencing the auth config
  3. Verify MCPServer RunConfig includes token exchange settings
  4. Update MCPExternalAuthConfig
  5. Verify MCPServer reconciles and updates ConfigMap
  6. Verify ConfigMap checksum changes

- [ ] **MCPExternalAuthConfig propagation to MCPRemoteProxy**:
  1. Create MCPExternalAuthConfig
  2. Create MCPRemoteProxy referencing the auth config
  3. Verify MCPRemoteProxy RunConfig includes token exchange settings
  4. Update MCPExternalAuthConfig
  5. Verify MCPRemoteProxy reconciles and updates

- [ ] **Shared auth config across multiple resources**:
  1. Create single MCPExternalAuthConfig
  2. Create multiple MCPServers referencing it
  3. Create multiple MCPRemoteProxies referencing it
  4. Update the auth config
  5. Verify ALL referencing resources reconcile

### ToolConfig Cascade
- [ ] **MCPToolConfig propagation to MCPServer**:
  1. Create MCPToolConfig with filtering rules
  2. Create MCPServer with ToolConfigRef
  3. Verify MCPServer status includes tool config hash
  4. Update MCPToolConfig rules
  5. Verify MCPServer reconciles and updates
  6. Verify status hash changes

- [ ] **MCPToolConfig propagation to MCPRemoteProxy**:
  1. Create MCPToolConfig with rename rules
  2. Create MCPRemoteProxy with ToolConfigRef
  3. Verify tool config applied
  4. Update MCPToolConfig
  5. Verify MCPRemoteProxy reconciles

### Group Membership Cascade
- [ ] **MCPGroup with multiple member types**:
  1. Create MCPGroup
  2. Add MCPServer to namespace (should auto-discover via labels)
  3. Add MCPRemoteProxy to namespace (should auto-discover)
  4. Verify both appear in MCPGroup status
  5. Create VirtualMCPServer referencing the group
  6. Verify VirtualMCPServer discovers both backends

- [ ] **Dynamic group membership updates**:
  1. Create MCPGroup
  2. Create VirtualMCPServer referencing it
  3. Verify initial state (no backends)
  4. Add MCPServer with matching labels
  5. Verify MCPGroup status updates
  6. Verify VirtualMCPServer discovers new backend
  7. Delete MCPServer
  8. Verify MCPGroup status updates
  9. Verify VirtualMCPServer removes backend

### CompositeTool Workflow Integration
- [ ] **CompositeTool definition with VirtualMCPServer**:
  1. Create VirtualMCPCompositeToolDefinition (valid workflow)
  2. Create VirtualMCPServer with CompositeToolRefs
  3. Verify VirtualMCPServer status validates references
  4. Verify ConfigMap includes composite tool workflow
  5. Update composite tool definition
  6. Verify VirtualMCPServer reconciles
  7. Verify ConfigMap updates

- [ ] **Invalid CompositeTool reference handling**:
  1. Create VirtualMCPServer with CompositeToolRefs to non-existent definition
  2. Verify status condition shows validation error
  3. Create the missing definition
  4. Verify VirtualMCPServer reconciles and status clears error

### Multi-Layer Dependency Chain
- [ ] **Complete dependency chain**:
  1. Create MCPExternalAuthConfig
  2. Create MCPToolConfig
  3. Create MCPRegistry with ConfigMap source
  4. Create MCPGroup
  5. Create MCPServer with: ExternalAuthConfigRef, ToolConfigRef, GroupRef
  6. Create VirtualMCPCompositeToolDefinition
  7. Create VirtualMCPServer with: GroupRef, CompositeToolRefs, IncomingAuth
  8. Verify entire chain reconciles correctly
  9. Update MCPExternalAuthConfig (bottom of chain)
  10. Verify cascade reconciliation up to VirtualMCPServer
  11. Update MCPToolConfig
  12. Verify cascade reconciliation
  13. Update CompositeTool definition
  14. Verify VirtualMCPServer reconciles

## Implementation Details

**Suggested location:** `cmd/thv-operator/test-integration/cross-resource/`

**Recommended approach:**
1. Create new test suite specifically for cross-resource scenarios
2. Use longer timeouts for `Eventually` since multiple reconciliations are involved
3. Create comprehensive helper functions:
   - `CreateCompleteVMCPSetup()` - creates full vMCP stack
   - `VerifyAuthConfigCascade()` - verifies auth updates propagate
   - `VerifyToolConfigCascade()` - verifies tool config updates propagate
   - `WaitForMultipleReconciliations()` - waits for cascade reconciliations
4. Test files organization:
   - `cross_resource_vmcp_workflow_test.go` - vMCP end-to-end scenarios
   - `cross_resource_auth_cascade_test.go` - auth config propagation
   - `cross_resource_toolconfig_cascade_test.go` - tool config propagation
   - `cross_resource_group_membership_test.go` - group membership dynamics
   - `cross_resource_dependency_chain_test.go` - complex multi-layer scenarios

## Acceptance Criteria
- [ ] All recommended cross-resource scenarios are implemented
- [ ] Tests verify cascading updates work correctly
- [ ] Tests verify watch triggers propagate through resource chains
- [ ] Tests handle realistic timing scenarios (eventual consistency)
- [ ] Tests follow existing integration test patterns
- [ ] Tests pass reliably in CI/CD
- [ ] Tests provide confidence that resources work together correctly

## Related Issues
- Parent issue: #1761 - Add Integration Tests for ToolHive Operator

## References
- Existing integration tests: `cmd/thv-operator/test-integration/`
- Watch implementation patterns in all controllers
- Owner reference patterns for cascade deletion
- Group discovery: `pkg/vmcp/aggregator/`

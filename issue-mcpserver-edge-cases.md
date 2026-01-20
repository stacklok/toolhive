# Add Edge Case Integration Tests for MCPServer Controller

## Priority
🟢 **MEDIUM**

## Description
While MCPServer has excellent integration test coverage, several edge cases and additional scenarios would improve test completeness and confidence in the controller's handling of various configurations.

## Background
As identified in issue #1761, the MCPServer controller has strong test coverage but is missing tests for some edge cases, alternative configurations, and error scenarios.

## Existing Coverage
Currently well-tested:
- Basic creation with stdio transport
- Deployment, Service, ConfigMap creation
- RBAC resource creation
- PodTemplateSpec validation (invalid/valid/with resources/with securityContext/updates)
- RunConfig ConfigMap content and checksum
- GroupRef validation
- Spec updates and reconciliation

## Missing Test Coverage

### ServiceAccount Handling
- [ ] **ServiceAccount override** - when user provides custom ServiceAccount name
  - Verify RBAC resources (ServiceAccount, Role, RoleBinding) are NOT created
  - Verify Deployment uses the custom ServiceAccount
  - Verify controller respects user's existing RBAC setup

### ToolConfig Integration
- [ ] **ToolConfigRef integration** - basic reference
  - Create MCPToolConfig with filtering rules
  - Create MCPServer with ToolConfigRef
  - Verify tool config hash propagation to status
  - Verify RunConfig includes tool filtering configuration

- [ ] **ToolConfig updates** - cascading changes
  - Create MCPServer with ToolConfigRef
  - Update the referenced MCPToolConfig
  - Verify MCPServer reconciles
  - Verify status hash updates
  - Verify ConfigMap checksum changes

- [ ] **ToolConfig reference validation**
  - Create MCPServer with invalid ToolConfigRef (non-existent)
  - Verify appropriate status condition/error
  - Create the missing ToolConfig
  - Verify MCPServer reconciles and clears error

### Transport Type Variations
- [ ] **SSE transport** - Server-Sent Events
  - Create MCPServer with SSE transport
  - Verify Deployment environment variables
  - Verify Service configuration
  - Verify port mappings

- [ ] **Streamable HTTP transport**
  - Create MCPServer with streamable-http transport
  - Verify configuration differences from stdio
  - Verify Service port configuration

- [ ] **HTTP transport** - traditional HTTP
  - Create MCPServer with HTTP transport
  - Verify Service and port configuration

### Port Configurations
- [ ] **ProxyPort customization**
  - Create MCPServer with custom ProxyPort
  - Verify Service uses correct port
  - Verify Deployment configuration

- [ ] **McpPort customization**
  - Create MCPServer with custom McpPort
  - Verify port configuration in deployment

- [ ] **TargetPort customization**
  - Create MCPServer with custom TargetPort
  - Verify Service targetPort configuration

- [ ] **Multiple port configurations** - ProxyPort + McpPort + TargetPort
  - Verify all ports configured correctly
  - Verify no port conflicts

### Volume Mounts
- [ ] **Volume mounts configuration**
  - Create MCPServer with volumes in PodTemplateSpec
  - Verify volumes are properly mounted in Deployment
  - Verify volume mount paths in containers

- [ ] **ConfigMap volumes**
  - Create MCPServer with ConfigMap volume mounts
  - Verify ConfigMap references in Deployment

- [ ] **Secret volumes**
  - Create MCPServer with Secret volume mounts
  - Verify Secret references in Deployment

### Update Scenarios
- [ ] **Environment variable updates**
  - Create MCPServer with env vars
  - Update env vars in spec
  - Verify Deployment updates
  - Verify rolling update occurs

- [ ] **Args updates**
  - Create MCPServer with command args
  - Update args in spec
  - Verify Deployment updates

- [ ] **Resource updates** (already tested but ensure comprehensive)
  - CPU limits/requests updates
  - Memory limits/requests updates
  - Verify Deployment reflects changes

- [ ] **Image updates**
  - Update container image in spec
  - Verify Deployment updates
  - Verify rolling update behavior

### Error Handling
- [ ] **Invalid container image**
  - Create MCPServer with non-existent image
  - Verify status reflects image pull error
  - Verify appropriate status conditions

- [ ] **Missing Secret references**
  - Create MCPServer referencing non-existent Secret
  - Verify appropriate error status
  - Create the Secret
  - Verify recovery and reconciliation

- [ ] **Invalid resource requests**
  - Create MCPServer with invalid resource specifications
  - Verify validation error handling

- [ ] **Namespace quota exceeded**
  - Create MCPServer in namespace with resource quota
  - Attempt to exceed quota
  - Verify appropriate status condition

### Registry Integration
- [ ] **Registry reference with MCPServer**
  - Create MCPRegistry
  - Create MCPServer using image from registry
  - Verify registry configuration applied

- [ ] **Registry updates affecting MCPServer**
  - Create MCPServer using registry
  - Update registry configuration
  - Verify MCPServer reconciles if needed

### Multiple ExternalAuthConfigs
- [ ] **Multiple auth config references** (if supported)
  - Test behavior when multiple auth configs could apply
  - Verify precedence/merge behavior

### Finalizer and Deletion
- [ ] **Graceful deletion with active connections** (if applicable)
  - Create MCPServer in use
  - Initiate deletion
  - Verify cleanup process

- [ ] **Deletion with dependent resources**
  - Create MCPServer
  - Verify finalizer handling
  - Verify cascade deletion of owned resources

## Implementation Details

**Suggested location:** `cmd/thv-operator/test-integration/mcp-server/`

**Recommended approach:**
1. Extend existing MCPServer integration tests
2. Group tests logically by feature area:
   - `mcpserver_serviceaccount_integration_test.go`
   - `mcpserver_toolconfig_integration_test.go`
   - `mcpserver_transports_integration_test.go`
   - `mcpserver_ports_integration_test.go`
   - `mcpserver_volumes_integration_test.go`
   - `mcpserver_updates_integration_test.go`
   - `mcpserver_errors_integration_test.go`
3. Reuse existing helper patterns
4. Add helpers for:
   - Creating MCPServers with various transport types
   - Verifying port configurations
   - Checking volume mount configurations

## Acceptance Criteria
- [ ] All edge cases and additional scenarios are implemented
- [ ] Tests cover error conditions and recovery
- [ ] Tests verify various transport types work correctly
- [ ] Tests verify custom configurations (ports, volumes, service accounts)
- [ ] Tests follow existing integration test patterns
- [ ] Tests pass reliably in CI/CD
- [ ] Code coverage for MCPServer controller edge cases increases

## Related Issues
- Parent issue: #1761 - Add Integration Tests for ToolHive Operator

## References
- Controller implementation: `cmd/thv-operator/internal/controller/mcpserver_controller.go`
- CRD definition: `cmd/thv-operator/api/v1alpha1/mcpserver_types.go`
- Existing tests: `cmd/thv-operator/test-integration/mcp-server/`
- Transport types: `pkg/transport/`

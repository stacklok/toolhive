# VirtualMCPServer Kubernetes Guide

This comprehensive guide covers deploying, configuring, and troubleshooting Virtual MCP Servers in Kubernetes. For detailed API field definitions, see the [VirtualMCPServer API Reference](virtualmcpserver-api.md).

## Table of Contents

- [Overview](#overview)
- [Architecture](#architecture)
- [Deployment Guide](#deployment-guide)
- [Common Deployment Patterns](#common-deployment-patterns)
- [Troubleshooting](#troubleshooting)

## Overview

VirtualMCPServer is a Kubernetes Custom Resource that enables aggregation of multiple backend MCP servers into a unified virtual endpoint. This allows clients to interact with multiple MCP servers through a single interface with:

- **Unified authentication**: Single authentication point for clients
- **Backend discovery**: Automatic discovery of backend authentication configurations
- **Tool aggregation**: Intelligent conflict resolution when multiple backends expose tools with the same name
- **Composite tools**: Multi-step workflows that orchestrate calls across multiple backends
- **Token caching**: Efficient token exchange and caching for improved performance

## Architecture

VirtualMCPServer aggregates multiple backend MCP servers into a unified virtual endpoint. For detailed architecture diagrams and component interactions, see [Operator Architecture: VirtualMCPServer](../arch/09-operator-architecture.md#virtualmcpserver).

### Key Components

- **VirtualMCPServer**: Main CRD that defines the aggregation configuration
- **MCPGroup**: Logical collection of backend MCPServers
- **Backend MCPServers**: Individual MCP server instances providing tools/resources
- **VirtualMCP Proxy**: Kubernetes Deployment that handles request routing, auth, and aggregation
- **Service**: Kubernetes Service exposing the VirtualMCP endpoint

### Request Flow

1. Client sends MCP request to VirtualMCPServer service
2. VirtualMCP proxy authenticates the client (OIDC, anonymous, etc.)
3. Proxy authorizes the request (Cedar policies)
4. Proxy resolves tool name to appropriate backend server
5. Proxy forwards request to backend (with token exchange if needed)
6. Backend processes request and returns response
7. Proxy aggregates and returns response to client

## Deployment Guide

### Prerequisites

Before deploying a VirtualMCPServer, ensure you have:

1. **Kubernetes Cluster**: v1.24+ with kubectl configured
2. **Helm** (optional but recommended): For operator installation
3. **ToolHive Operator**: The operator must be installed in your cluster

### Quick Start

```bash
# Install the operator
helm install toolhive-operator-crds oci://ghcr.io/stacklok/toolhive/toolhive-operator-crds
helm install toolhive-operator oci://ghcr.io/stacklok/toolhive/toolhive-operator \
  -n toolhive-system --create-namespace

# Deploy a simple Virtual MCP Server
kubectl apply -f https://raw.githubusercontent.com/stacklok/toolhive/main/examples/operator/virtual-mcps/vmcp_simple_discovered.yaml

# Check status
kubectl get virtualmcpserver simple-vmcp
```

### Step-by-Step Deployment

#### Step 1: Install the Operator

**Using Helm (Recommended):**

```bash
# Install CRDs
helm install toolhive-operator-crds \
  oci://ghcr.io/stacklok/toolhive/toolhive-operator-crds

# Install operator
helm install toolhive-operator \
  oci://ghcr.io/stacklok/toolhive/toolhive-operator \
  -n toolhive-system \
  --create-namespace
```

**Verify Installation:**

```bash
# Check operator pod status
kubectl get pods -n toolhive-system

# Verify CRDs are installed
kubectl get crds | grep toolhive.stacklok.dev
```

You should see CRDs including:
- `virtualmcpservers.toolhive.stacklok.dev`
- `mcpgroups.toolhive.stacklok.dev`
- `mcpservers.toolhive.stacklok.dev`
- `mcpexternalauthconfigs.toolhive.stacklok.dev`
- `virtualmcpcompositetooldefinitions.toolhive.stacklok.dev`

For detailed operator installation instructions, see [Deploying ToolHive Operator](../kind/deploying-toolhive-operator.md).

#### Step 2: Create Backend MCP Servers

Create the backend MCP servers that the VirtualMCPServer will aggregate:

```yaml
# backend-servers.yaml
apiVersion: toolhive.stacklok.dev/v1alpha1
kind: MCPServer
metadata:
  name: yardstick-server
  namespace: default
spec:
  image: ghcr.io/stackloklabs/yardstick/yardstick-server:0.0.2
  transport: streamable-http
  env:
    - name: TRANSPORT
      value: streamable-http
  proxyPort: 8080
  mcpPort: 8080
  resources:
    requests:
      cpu: "50m"
      memory: "64Mi"
    limits:
      cpu: "100m"
      memory: "128Mi"

---
apiVersion: toolhive.stacklok.dev/v1alpha1
kind: MCPServer
metadata:
  name: fetch-server
  namespace: default
spec:
  image: ghcr.io/stackloklabs/gofetch/server
  transport: streamable-http
  proxyPort: 8080
  mcpPort: 8080
  resources:
    requests:
      cpu: "50m"
      memory: "64Mi"
    limits:
      cpu: "100m"
      memory: "128Mi"
```

Apply and verify:

```bash
kubectl apply -f backend-servers.yaml
kubectl get mcpservers
```

#### Step 3: Create an MCPGroup

MCPGroups organize backend MCPServers into logical collections:

```yaml
# mcp-group.yaml
apiVersion: toolhive.stacklok.dev/v1alpha1
kind: MCPGroup
metadata:
  name: my-services
  namespace: default
spec:
  description: Backend services for Virtual MCP Server
```

Link backend servers to the group by adding `groupRef: my-services` to each MCPServer spec:

```bash
kubectl apply -f mcp-group.yaml

# Update backend servers with groupRef
kubectl patch mcpserver yardstick-server -p '{"spec":{"groupRef":"my-services"}}'
kubectl patch mcpserver fetch-server -p '{"spec":{"groupRef":"my-services"}}'
```

Verify group membership:

```bash
kubectl get mcpgroup my-services -o yaml
kubectl get mcpserver -o custom-columns=NAME:.metadata.name,GROUP:.spec.groupRef
```

#### Step 4: Deploy VirtualMCPServer

Create the VirtualMCPServer that aggregates your backend servers:

```yaml
# virtual-mcp-server.yaml
apiVersion: toolhive.stacklok.dev/v1alpha1
kind: VirtualMCPServer
metadata:
  name: my-vmcp
  namespace: default
spec:
  # Reference to the MCPGroup
  groupRef:
    name: my-services

  # Incoming authentication (client -> Virtual MCP)
  incomingAuth:
    type: anonymous  # For testing; use OIDC in production
    authzConfig:
      type: inline
      inline:
        policies:
          - 'permit(principal, action, resource);'

  # Outgoing authentication (Virtual MCP -> backends)
  outgoingAuth:
    source: discovered

  # Tool aggregation and conflict resolution
  aggregation:
    conflictResolution: prefix
    conflictResolutionConfig:
      prefixFormat: "{workload}_"
```

Apply and verify:

```bash
kubectl apply -f virtual-mcp-server.yaml
kubectl get virtualmcpserver my-vmcp
```

For all available configuration fields, see the [VirtualMCPServer API Reference](virtualmcpserver-api.md).

#### Step 5: Verify Deployment

Check the VirtualMCPServer status:

```bash
# Get overall status
kubectl get virtualmcpserver my-vmcp

# Detailed status with conditions
kubectl get virtualmcpserver my-vmcp -o yaml | grep -A 20 status
```

Look for:
- `phase: Ready`
- `conditions` with `Ready: True`
- `discoveredBackends` showing all backends
- `capabilities` summary

Check created resources:

```bash
# Deployment for Virtual MCP proxy
kubectl get deployment my-vmcp

# Service exposing Virtual MCP
kubectl get service my-vmcp

# Pods
kubectl get pods -l app.kubernetes.io/name=my-vmcp
```

#### Step 6: Access the Virtual MCP Server

There are several ways to access your VirtualMCPServer:

**Option 1: Port Forward (Development)**

```bash
kubectl port-forward service/my-vmcp 8080:8080
# Access at http://localhost:8080
```

**Option 2: ClusterIP Service (In-cluster)**

Other applications in the cluster can access via:
- `my-vmcp.default.svc.cluster.local:8080`
- `my-vmcp:8080` (same namespace)

**Option 3: LoadBalancer Service (Production)**

Update the VirtualMCPServer spec:

```yaml
spec:
  serviceType: LoadBalancer
```

Get the external IP:

```bash
kubectl apply -f virtual-mcp-server.yaml
kubectl get service my-vmcp
```

**Option 4: Ingress (Production with TLS)**

Create an Ingress resource:

```yaml
apiVersion: networking.k8s.io/v1
kind: Ingress
metadata:
  name: my-vmcp-ingress
  namespace: default
  annotations:
    cert-manager.io/cluster-issuer: "letsencrypt-prod"
spec:
  tls:
    - hosts:
        - vmcp.example.com
      secretName: vmcp-tls
  rules:
    - host: vmcp.example.com
      http:
        paths:
          - path: /
            pathType: Prefix
            backend:
              service:
                name: my-vmcp
                port:
                  number: 8080
```

## Common Deployment Patterns

### Pattern 1: Simple Aggregation (Development)

**Use Case**: Local testing with minimal security

```yaml
apiVersion: toolhive.stacklok.dev/v1alpha1
kind: VirtualMCPServer
metadata:
  name: dev-vmcp
  namespace: default
spec:
  groupRef:
    name: dev-services
  incomingAuth:
    type: anonymous
    authzConfig:
      type: inline
      inline:
        policies:
          - 'permit(principal, action, resource);'
  outgoingAuth:
    source: discovered
  aggregation:
    conflictResolution: prefix
    conflictResolutionConfig:
      prefixFormat: "{workload}_"
```

**When to Use**:
- Development and testing
- Internal tools without sensitive data
- Proof-of-concept deployments

**Considerations**:
- No authentication - anyone in cluster can access
- Use prefix strategy to avoid tool conflicts
- **Not suitable for production**

**Example**: See [vmcp_simple_discovered.yaml](../../examples/operator/virtual-mcps/vmcp_simple_discovered.yaml)

### Pattern 2: Production with OIDC Authentication

**Use Case**: Production deployment with user authentication

**Step 1: Create OIDC Secret**

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: oidc-client-secret
  namespace: default
type: Opaque
stringData:
  clientSecret: "YOUR_CLIENT_SECRET"
```

**Step 2: Deploy VirtualMCPServer**

For the complete production configuration with OIDC, see: [vmcp_production_full.yaml](../../examples/operator/virtual-mcps/vmcp_production_full.yaml)

Key configuration highlights:

- OIDC authentication for client requests
- Cedar policies for role-based authorization
- Priority-based conflict resolution
- LoadBalancer service type
- Resource limits and operational settings

**When to Use**:
- Production environments
- Multi-tenant deployments
- When user authentication is required

**Considerations**:
- Requires OIDC provider setup
- Use role-based authorization with Cedar policies
- Configure appropriate resource limits
- Use `degrade` mode to continue serving healthy backends

**Complete Example**: [vmcp_production_full.yaml](../../examples/operator/virtual-mcps/vmcp_production_full.yaml)

### Pattern 3: Multi-Backend with Token Exchange

**Use Case**: Aggregating backends with different authentication mechanisms

**Step 1: Create MCPExternalAuthConfig**

```yaml
apiVersion: toolhive.stacklok.dev/v1alpha1
kind: MCPExternalAuthConfig
metadata:
  name: backend-token-exchange
  namespace: default
spec:
  type: tokenExchange
  tokenExchange:
    token_url: https://auth.example.com/token
    client_id: toolhive-client
    client_secret_ref:
      name: oauth-secret
      key: client-secret
    audience: backend-api
    scope: "openid profile"
```

**Step 2: Link to Backend MCPServer**

```yaml
apiVersion: toolhive.stacklok.dev/v1alpha1
kind: MCPServer
metadata:
  name: authenticated-backend
  namespace: default
spec:
  image: ghcr.io/example/backend-server
  transport: streamable-http
  groupRef: prod-services
  externalAuthConfigRef:
    name: backend-token-exchange
  proxyPort: 8080
  mcpPort: 8080
```

**Step 3: Deploy VirtualMCPServer**

```yaml
apiVersion: toolhive.stacklok.dev/v1alpha1
kind: VirtualMCPServer
metadata:
  name: multi-auth-vmcp
  namespace: default
spec:
  groupRef:
    name: prod-services
  incomingAuth:
    type: oidc
    # ... OIDC config
  outgoingAuth:
    source: discovered  # Automatically discovers token exchange configs
  aggregation:
    conflictResolution: prefix
```

**When to Use**:
- Backends require different authentication
- Token exchange scenarios
- External API integration

**Considerations**:
- Each backend can have its own auth config
- VirtualMCPServer automatically discovers auth requirements
- Test each backend auth independently first

**Complete Example**: [complete_example.yaml](../../examples/operator/external-auth/complete_example.yaml)

### Pattern 4: Tool Filtering and Customization

**Use Case**: Expose only specific tools from backends, with custom naming

```yaml
apiVersion: toolhive.stacklok.dev/v1alpha1
kind: VirtualMCPServer
metadata:
  name: filtered-vmcp
  namespace: default
spec:
  groupRef:
    name: services
  incomingAuth:
    type: anonymous
    authzConfig:
      type: inline
      inline:
        policies:
          - 'permit(principal, action, resource);'
  outgoingAuth:
    source: discovered
  aggregation:
    conflictResolution: manual
    tools:
      - workload: github-backend
        filter: ["create_pr", "merge_pr", "list_repos"]
        overrides:
          create_pr:
            name: github_create_pr
            description: "Create a GitHub pull request"
      - workload: jira-backend
        filter: ["create_issue", "update_issue"]
        overrides:
          create_issue:
            name: jira_create_issue
            description: "Create a Jira issue"
      - workload: slack-backend
        filter: ["send_message"]
```

**When to Use**:
- Limit tool exposure for security
- Custom tool naming for clarity
- Prevent tool name conflicts explicitly
- Multi-team environments with different tool access needs

**Considerations**:
- Manual strategy requires explicit tool mapping for all backends
- Use `filter` to allowlist specific tools
- `overrides` provide custom names and descriptions
- Must resolve all tool conflicts manually

**Complete Example**: [vmcp_conflict_resolution.yaml](../../examples/operator/virtual-mcps/vmcp_conflict_resolution.yaml) (shows all three conflict resolution strategies)

### Pattern 5: Composite Workflows

**Use Case**: Multi-step workflows that orchestrate multiple backends

**Step 1: Create VirtualMCPCompositeToolDefinition**

See complete workflow examples:

- Simple workflow: [composite_tool_simple.yaml](../../examples/operator/virtual-mcps/composite_tool_simple.yaml)
- Complex workflow: [composite_tool_complex.yaml](../../examples/operator/virtual-mcps/composite_tool_complex.yaml)

**Step 2: Reference in VirtualMCPServer**

Add `compositeToolRefs` to your VirtualMCPServer spec:

```yaml
spec:
  compositeToolRefs:
    - name: deploy-and-notify
```

**When to Use**:
- Orchestrate multi-backend operations
- Create reusable workflows
- Automate complex multi-step tasks
- Standardize procedures across teams

**Considerations**:
- Workflows can span multiple backends
- Use `dependsOn` for sequential execution
- Independent steps run in parallel (DAG execution)
- Configure appropriate timeouts per step
- Use error handling strategies (`abort`, `continue`, `retry`)

For detailed workflow documentation and more examples, see [VirtualMCPCompositeToolDefinition Guide](virtualmcpcompositetooldefinition-guide.md).

## Migration Guide: CLI to Kubernetes

### Overview

Migrating from CLI (`thv`) to Kubernetes deployment provides several benefits:
- **Scalability**: Run multiple instances, automatic restarts
- **Multi-tenancy**: Isolate workloads by namespace
- **GitOps**: Declarative configuration management
- **High availability**: Kubernetes self-healing and scheduling

This guide covers migrating both individual MCPServers and VirtualMCPServers.

### Migrating Individual MCP Servers

#### Step 1: Export from CLI

Export your existing workload configuration:

```bash
# Export as Kubernetes YAML (recommended)
thv export my-server ./my-server.yaml --format k8s

# Or export as RunConfig JSON for manual conversion
thv export my-server ./my-server-config.json --format json
```

The `--format k8s` option automatically converts to MCPServer CRD format.

#### Step 2: Review and Adjust

Review the exported YAML and make any necessary adjustments:

```yaml
apiVersion: toolhive.stacklok.dev/v1alpha1
kind: MCPServer
metadata:
  name: my-server
  namespace: default  # Adjust namespace if needed
spec:
  image: ghcr.io/example/my-server:latest
  transport: streamable-http
  proxyPort: 8080
  mcpPort: 8080
  # Review and adjust these fields:
  resources:
    requests:
      cpu: "100m"
      memory: "128Mi"
    limits:
      cpu: "200m"
      memory: "256Mi"
```

**Key adjustments**:
- **Namespace**: Choose appropriate namespace
- **Resources**: Set CPU/memory limits for Kubernetes
- **Service Type**: Defaults to ClusterIP (change to LoadBalancer if needed)
- **Authentication**: OIDC configs may need URLs updated for cluster context

#### Step 3: Deploy to Kubernetes

```bash
# Install operator if not already installed
helm install toolhive-operator-crds oci://ghcr.io/stacklok/toolhive/toolhive-operator-crds
helm install toolhive-operator oci://ghcr.io/stacklok/toolhive/toolhive-operator \
  -n toolhive-system --create-namespace

# Apply the MCPServer
kubectl apply -f my-server.yaml

# Verify deployment
kubectl get mcpserver my-server
kubectl get pods -l app.kubernetes.io/name=my-server
```

#### Step 4: Update Clients

Update MCP clients to use the new Kubernetes service endpoint:

**Before (CLI)**:
```
http://localhost:8080
```

**After (Kubernetes - in cluster)**:
```
http://my-server.default.svc.cluster.local:8080
```

**After (Kubernetes - external)**:
```bash
# Option 1: Port-forward for testing
kubectl port-forward service/my-server 8080:8080

# Option 2: Use LoadBalancer
kubectl get service my-server
# Use EXTERNAL-IP from output

# Option 3: Use Ingress
https://my-server.example.com
```

#### Step 5: Decommission CLI Instance

Once verified in Kubernetes:

```bash
# Stop and remove CLI workload
thv stop my-server
thv rm my-server
```

### Migrating VirtualMCPServers

#### Understanding the Migration

A VirtualMCPServer in Kubernetes aggregates multiple backend MCPServers. The CLI equivalent would be running multiple `thv` instances with a group.

**CLI Setup Example**:
```bash
# CLI: Running multiple servers
thv run github --image ghcr.io/example/github-mcp
thv run jira --image ghcr.io/example/jira-mcp
thv run slack --image ghcr.io/example/slack-mcp

# CLI: Creating a group
thv group create my-services
thv group add my-services github jira slack
```

**Kubernetes Equivalent**: VirtualMCPServer + MCPGroup + MCPServers

#### Step 1: Export Backend Servers

Export each backend server individually:

```bash
thv export github ./github.yaml --format k8s
thv export jira ./jira.yaml --format k8s
thv export slack ./slack.yaml --format k8s
```

#### Step 2: Create MCPGroup

Create an MCPGroup to organize the backends:

```yaml
# mcp-group.yaml
apiVersion: toolhive.stacklok.dev/v1alpha1
kind: MCPGroup
metadata:
  name: my-services
  namespace: default
spec:
  description: Migrated from CLI group 'my-services'
```

#### Step 3: Link Backends to Group

Add `groupRef` to each exported MCPServer:

```yaml
# github.yaml
apiVersion: toolhive.stacklok.dev/v1alpha1
kind: MCPServer
metadata:
  name: github
  namespace: default
spec:
  groupRef: my-services  # Add this line
  image: ghcr.io/example/github-mcp
  transport: streamable-http
  proxyPort: 8080
  mcpPort: 8080
```

Repeat for `jira.yaml` and `slack.yaml`.

#### Step 4: Create VirtualMCPServer

Create a VirtualMCPServer to aggregate the backends:

```yaml
# virtual-mcp-server.yaml
apiVersion: toolhive.stacklok.dev/v1alpha1
kind: VirtualMCPServer
metadata:
  name: my-vmcp
  namespace: default
spec:
  groupRef:
    name: my-services

  # Configure authentication (adjust from CLI if using OIDC)
  incomingAuth:
    type: anonymous  # Or configure OIDC
    authzConfig:
      type: inline
      inline:
        policies:
          - 'permit(principal, action, resource);'

  # Backend authentication discovery
  outgoingAuth:
    source: discovered

  # Tool aggregation strategy
  aggregation:
    conflictResolution: prefix
    conflictResolutionConfig:
      prefixFormat: "{workload}_"
```

#### Step 5: Deploy Everything

```bash
# Deploy in order: Group → Backends → VirtualMCP
kubectl apply -f mcp-group.yaml
kubectl apply -f github.yaml
kubectl apply -f jira.yaml
kubectl apply -f slack.yaml
kubectl apply -f virtual-mcp-server.yaml

# Verify deployment
kubectl get mcpgroup my-services
kubectl get mcpserver
kubectl get virtualmcpserver my-vmcp
```

#### Step 6: Verify and Test

Check that the VirtualMCPServer discovered all backends:

```bash
# Check discovered backends
kubectl get virtualmcpserver my-vmcp -o jsonpath='{.status.discoveredBackends}' | jq

# Check aggregated capabilities
kubectl get virtualmcpserver my-vmcp -o jsonpath='{.status.capabilities}' | jq

# Test connectivity
kubectl port-forward service/my-vmcp 8080:8080
# Test with MCP client at http://localhost:8080
```

#### Step 7: Update Clients and Decommission CLI

Update clients to use the VirtualMCPServer endpoint and remove CLI instances:

```bash
# Stop CLI instances
thv stop github jira slack

# Remove CLI instances
thv rm github jira slack

# Remove CLI group
thv group rm my-services
```

### Migration Checklist

Use this checklist to ensure complete migration:

**Pre-Migration**:
- [ ] Document all running CLI workloads (`thv list`)
- [ ] Export configurations for all workloads
- [ ] Note any custom authentication or middleware configurations
- [ ] Identify workload dependencies and groups
- [ ] Plan namespace strategy for Kubernetes

**During Migration**:
- [ ] Install ToolHive operator in Kubernetes
- [ ] Create namespaces if needed
- [ ] Deploy MCPGroups (if using VirtualMCPServers)
- [ ] Deploy all backend MCPServers
- [ ] Link MCPServers to MCPGroups
- [ ] Deploy VirtualMCPServers
- [ ] Verify all resources are Ready

**Post-Migration**:
- [ ] Test all MCP server endpoints
- [ ] Verify tool/resource/prompt availability
- [ ] Update client configurations
- [ ] Test authentication flows
- [ ] Monitor for errors or issues
- [ ] Decommission CLI instances
- [ ] Update documentation with new endpoints

### Common Migration Scenarios

#### Scenario 1: Simple MCP Server

**CLI**:
```bash
thv run weather --image ghcr.io/example/weather:latest
```

**Kubernetes**:
```bash
thv export weather ./weather.yaml --format k8s
kubectl apply -f weather.yaml
```

#### Scenario 2: MCP Server with OIDC

**CLI** (with local OIDC config):

```bash
thv run github \
  --image ghcr.io/example/github-mcp \
  --oidc-issuer https://auth.example.com \
  --oidc-client-id github-client
```

**Kubernetes**:

Export and adjust URLs for cluster context. See example configurations:

- [mcpserver_with_inline_oidc.yaml](../../examples/operator/mcp-servers/mcpserver_with_inline_oidc.yaml)
- [mcpserver_with_kubernetes_oidc.yaml](../../examples/operator/mcp-servers/mcpserver_with_kubernetes_oidc.yaml)

#### Scenario 3: Grouped Servers (CLI) → VirtualMCPServer (K8s)

**CLI**:
```bash
thv run backend1 --image ghcr.io/example/backend1
thv run backend2 --image ghcr.io/example/backend2
thv group create services
thv group add services backend1 backend2
```

**Kubernetes**:
```bash
# Export backends
thv export backend1 ./backend1.yaml --format k8s
thv export backend2 ./backend2.yaml --format k8s

# Create manifests (add groupRef to each backend YAML)
cat > resources.yaml <<EOF
apiVersion: toolhive.stacklok.dev/v1alpha1
kind: MCPGroup
metadata:
  name: services
---
# Include backend1.yaml content with groupRef: services
# Include backend2.yaml content with groupRef: services
---
apiVersion: toolhive.stacklok.dev/v1alpha1
kind: VirtualMCPServer
metadata:
  name: services-vmcp
spec:
  groupRef:
    name: services
  incomingAuth:
    type: anonymous
  outgoingAuth:
    source: discovered
  aggregation:
    conflictResolution: prefix
EOF

kubectl apply -f resources.yaml
```

### Troubleshooting Migration Issues

#### Issue: Exported YAML fails validation

**Solution**: Check for CLI-specific fields that need adjustment:
- Update URLs from `localhost` to cluster DNS names
- Add namespace to metadata
- Set appropriate resource limits
- Remove CLI-specific configurations

#### Issue: OIDC authentication not working

**Solution**: Update OIDC URLs for Kubernetes context:
- `resourceUrl` should use cluster service DNS
- `issuer` should be accessible from pods
- Verify secrets are in the same namespace
- Check RBAC permissions for service accounts

#### Issue: Backend servers not discovered by VirtualMCPServer

**Solution**:
- Verify all MCPServers have `groupRef` set
- Ensure all resources are in the same namespace
- Check MCPServer status: `kubectl get mcpserver`
- Review VirtualMCPServer conditions: `kubectl describe virtualmcpserver <name>`

#### Issue: Performance degradation after migration

**Solution**:
- Increase pod resources (CPU/memory)
- Adjust timeout configurations
- Check network policies aren't blocking traffic
- Monitor pod metrics: `kubectl top pod`

### Best Practices

1. **Test in Staging First**: Migrate to a staging Kubernetes cluster before production
2. **Gradual Migration**: Migrate one workload at a time, verify before proceeding
3. **Keep CLI Running**: Run CLI and K8s in parallel during testing
4. **Document Endpoints**: Maintain a mapping of old (CLI) to new (K8s) endpoints
5. **Monitor Closely**: Watch logs and metrics after migration
6. **Plan Rollback**: Keep CLI configurations as backup until migration is stable
7. **Use GitOps**: Store Kubernetes manifests in Git for versioning and rollback

## Troubleshooting

### Deployment Issues

#### VirtualMCPServer Stuck in "Pending" Phase

**Symptoms**:

```bash
kubectl get virtualmcpserver my-vmcp
# NAME      PHASE     AGE
# my-vmcp   Pending   5m
```

**Common Causes and Solutions**:

**1. MCPGroup Not Found**

```bash
kubectl get virtualmcpserver my-vmcp -o yaml | grep -A 5 conditions
# Look for: GroupRefValidated: False
```

**Solution**: Verify the MCPGroup exists:

```bash
kubectl get mcpgroup <group-name>
```

Create if missing or fix the `groupRef.name` in VirtualMCPServer spec.

**2. No Backend MCPServers in Group**

```bash
kubectl get mcpserver -o custom-columns=NAME:.metadata.name,GROUP:.spec.groupRef
```

**Solution**: Create MCPServers and link them to the group:

```yaml
spec:
  groupRef: <group-name>
```

**3. Backend MCPServers Not Ready**

```bash
kubectl get mcpserver
# Check STATUS column
```

**Solution**: Check backend server logs:

```bash
kubectl logs -l app.kubernetes.io/name=<mcpserver-name>
kubectl describe mcpserver <mcpserver-name>
```

#### VirtualMCPServer in "Degraded" Phase

**Symptoms**:

```bash
kubectl get virtualmcpserver my-vmcp -o jsonpath='{.status.phase}'
# Degraded
```

**Common Causes and Solutions**:

**1. Some Backends Unhealthy**

```bash
kubectl get virtualmcpserver my-vmcp -o jsonpath='{.status.discoveredBackends}' | jq
# Check "status" field for each backend
```

**Solution**: Investigate unhealthy backends:

```bash
kubectl get mcpserver <backend-name>
kubectl logs <backend-pod-name>
kubectl describe pod <backend-pod-name>
```

**2. Partial Failure Mode Configuration**

Check your configuration:

```yaml
spec:
  operational:
    failureHandling:
      partialFailureMode: degrade  # vs fail
```

**Solution**: If using `degrade` mode, this is expected behavior when some backends are down. VirtualMCPServer continues serving healthy backends.

To require all backends to be healthy, use `partialFailureMode: fail`.

#### Authentication Failures

**Symptoms**:
- Clients cannot connect to VirtualMCPServer
- 401 Unauthorized errors
- 403 Forbidden errors

**Common Causes and Solutions**:

**1. Missing OIDC Client Secret**

```bash
kubectl get secret oidc-client-secret
```

**Solution**: Create the secret:

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: oidc-client-secret
  namespace: default
type: Opaque
stringData:
  clientSecret: "YOUR_SECRET"
```

**2. Incorrect OIDC Configuration**

Check VirtualMCPServer events:

```bash
kubectl describe virtualmcpserver my-vmcp
```

**Solution**: Verify OIDC settings:
- `issuer`: Must match your OIDC provider URL exactly
- `clientId`: Must match the registered client in OIDC provider
- `audience`: Must match the expected audience claim
- `resourceUrl`: Must match the VirtualMCPServer's accessible URL

**3. Authorization Policy Errors**

**Solution**: Test with a permissive policy first:

```yaml
authzConfig:
  type: inline
  inline:
    policies:
      - 'permit(principal, action, resource);'
```

Then gradually add restrictions. Common Cedar policy issues:
- Check syntax is correct
- Verify attribute names match token claims
- Test policies with different user roles

### Backend Discovery Issues

#### Backends Not Discovered

**Symptoms**:

```bash
kubectl get virtualmcpserver my-vmcp -o jsonpath='{.status.discoveredBackends}' | jq
# Empty array or missing backends
```

**Common Causes and Solutions**:

**1. Backend Not in MCPGroup**

```bash
kubectl get mcpserver <backend-name> -o yaml | grep groupRef
```

**Solution**: Verify backend has correct `groupRef`:

```bash
kubectl patch mcpserver <backend-name> -p '{"spec":{"groupRef":"<group-name>"}}'
```

**2. Namespace Mismatch**

**Solution**: Ensure VirtualMCPServer, MCPGroup, and all MCPServers are in the same namespace (security requirement):

```bash
kubectl get virtualmcpserver,mcpgroup,mcpserver -n <namespace>
```

All resources must be in the same namespace. Move resources if needed.

**3. Backend Authentication Config Not Found**

When using `outgoingAuth.source: discovered`:

```bash
kubectl get mcpserver <backend-name> -o yaml | grep externalAuthConfigRef
```

**Solution**: Either:
- Create MCPExternalAuthConfig if backend requires auth
- Remove `externalAuthConfigRef` from backend if no auth required
- Use `outgoingAuth.source: inline` and configure explicitly

### Tool Conflict Issues

#### Tool Name Conflicts Not Resolved

**Symptoms**:
- Error messages about unresolved tool conflicts
- Tools missing from aggregated capabilities
- VirtualMCPServer status shows validation errors

**Common Causes and Solutions**:

**1. Priority Strategy Missing Order**

```yaml
aggregation:
  conflictResolution: priority
  # Missing: conflictResolutionConfig.priorityOrder
```

**Solution**: Add priority order with all backend names:

```yaml
aggregation:
  conflictResolution: priority
  conflictResolutionConfig:
    priorityOrder:
      - backend1
      - backend2
      - backend3
```

**2. Manual Strategy Missing Tool Configuration**

**Solution**: Add explicit tool configuration for all backends:

```yaml
aggregation:
  conflictResolution: manual
  tools:
    - workload: backend1
      filter: ["tool1", "tool2"]
    - workload: backend2
      filter: ["tool3", "tool4"]
```

**3. Invalid Tool Names in Filter**

**Solution**: Verify actual tool names from backend:

```bash
# Port-forward to backend
kubectl port-forward service/<backend-name> 8080:8080

# Query tools endpoint (method depends on transport)
# Or check backend logs during startup
kubectl logs <backend-pod-name> | grep -i tool
```

### Composite Workflow Issues

#### Workflow Validation Errors

**Symptoms**:

```bash
kubectl get virtualmcpcompositetooldefinition <name> -o jsonpath='{.status.validationStatus}'
# Invalid
```

Check validation errors:

```bash
kubectl get virtualmcpcompositetooldefinition <name> -o jsonpath='{.status.validationErrors}' | jq
```

**Common Causes and Solutions**:

**1. Circular Dependencies**

```yaml
steps:
  - id: step1
    dependsOn: [step2]
  - id: step2
    dependsOn: [step1]  # Circular!
```

**Solution**: Remove circular dependencies. Draw dependency graph if needed.

**2. Invalid Tool References**

```yaml
steps:
  - id: deploy
    tool: invalid-format  # Should be: workload.tool_name
```

**Solution**: Use correct format: `<workload>.<tool_name>`

Check available tools:

```bash
kubectl get virtualmcpserver <name> -o jsonpath='{.status.capabilities}' | jq
```

**3. Missing Step Dependencies**

```yaml
steps:
  - id: step2
    dependsOn: [step1]  # step1 doesn't exist
```

**Solution**: Ensure all referenced steps exist and are defined before they're referenced.

### Performance Issues

#### Slow Tool Execution

**Common Causes and Solutions**:

**1. Backend Timeouts Too Short**

**Solution**: Increase timeouts:

```yaml
spec:
  operational:
    timeouts:
      default: 60s
      perWorkload:
        slow-backend: 120s
```

**2. Resource Constraints**

Check pod resources:

```bash
kubectl top pod -l app.kubernetes.io/name=<vmcp-name>
```

**Solution**: Increase pod resources:

```yaml
spec:
  podTemplateSpec:
    spec:
      containers:
        - name: vmcp
          resources:
            requests:
              cpu: "1000m"
              memory: "1Gi"
            limits:
              cpu: "2000m"
              memory: "2Gi"
```

**3. Too Many Backends**

**Solution**: Consider splitting into multiple VirtualMCPServers by function or team.

**4. Network Latency**

Check backend connectivity:

```bash
kubectl exec -it <vmcp-pod> -- sh
# Inside pod:
ping <backend-service-name>
curl http://<backend-service-name>:8080/health
```

### Monitoring and Debugging

#### Viewing Logs

```bash
# VirtualMCPServer proxy logs
kubectl logs -l app.kubernetes.io/name=<vmcp-name> --tail=100 -f

# Backend server logs
kubectl logs -l app.kubernetes.io/name=<backend-name> --tail=100 -f

# Operator logs (for reconciliation issues)
kubectl logs -n toolhive-system -l app.kubernetes.io/name=toolhive-operator --tail=100 -f
```

#### Checking Events

```bash
# VirtualMCPServer events
kubectl describe virtualmcpserver <name>

# All events in namespace sorted by time
kubectl get events --sort-by='.lastTimestamp' | tail -20
```

#### Status Inspection

```bash
# Full status YAML
kubectl get virtualmcpserver <name> -o yaml

# Just conditions
kubectl get virtualmcpserver <name> -o jsonpath='{.status.conditions}' | jq

# Backend health
kubectl get virtualmcpserver <name> -o jsonpath='{.status.discoveredBackends}' | jq

# Capabilities summary
kubectl get virtualmcpserver <name> -o jsonpath='{.status.capabilities}' | jq
```

#### Testing Connectivity

```bash
# Port-forward to VirtualMCPServer
kubectl port-forward service/<vmcp-name> 8080:8080

# Test health endpoint
curl http://localhost:8080/health

# Port-forward to backend
kubectl port-forward service/<backend-name> 8080:8080
curl http://localhost:8080/health
```

#### Enable Debug Logging

```yaml
spec:
  podTemplateSpec:
    spec:
      containers:
        - name: vmcp
          env:
            - name: LOG_LEVEL
              value: "debug"
```

Apply changes and check logs for detailed information.

### Getting Help

If you continue to experience issues:

1. **Check Examples**: Review working examples in [`examples/operator/virtual-mcps/`](../../examples/operator/virtual-mcps/)
2. **GitHub Issues**: Search or create issues at [ToolHive GitHub](https://github.com/stacklok/toolhive/issues)
3. **Operator Logs**: Check operator logs for reconciliation errors
4. **Documentation**: Review:
   - [VirtualMCPServer API Reference](virtualmcpserver-api.md)
   - [Operator Architecture](../arch/09-operator-architecture.md)
   - [Deployment Modes](../arch/01-deployment-modes.md)

## Related Resources

- **API Reference**: [VirtualMCPServer API Reference](virtualmcpserver-api.md) - Complete field definitions
- **Composite Workflows**: [VirtualMCPCompositeToolDefinition Guide](virtualmcpcompositetooldefinition-guide.md)
- **Operator Setup**: [Deploying ToolHive Operator](../kind/deploying-toolhive-operator.md)
- **Architecture**: [Operator Architecture](../arch/09-operator-architecture.md)
- **Migration**: [Deployment Modes](../arch/01-deployment-modes.md#migration-paths) - CLI to Kubernetes migration
- **Examples**: [Virtual MCP Examples](../../examples/operator/virtual-mcps/) - Working configurations

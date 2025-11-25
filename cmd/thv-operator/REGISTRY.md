# MCPRegistry Reference

## Overview

MCPRegistry is a Kubernetes Custom Resource that manages MCP (Model Context Protocol) server registries. It provides centralized server discovery, automated synchronization, and image validation for MCP servers in your cluster.

## Quick Start

Create a basic registry from a ConfigMap:

```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: my-registry-data
  namespace: toolhive-system
data:
  registry.json: |
    {
      "$schema": "https://raw.githubusercontent.com/stacklok/toolhive/main/pkg/registry/data/schema.json",
      "version": "1.0.0",
      "last_updated": "2025-01-14T00:00:00Z",
      "servers": {
        "github": {
          "description": "GitHub API integration",
          "tier": "Official",
          "status": "Active",
          "transport": "stdio",
          "tools": ["create_issue", "search_repositories"],
          "image": "ghcr.io/github/github-mcp-server:latest",
          "tags": ["github", "api", "production"]
        }
      }
    }
---
apiVersion: toolhive.stacklok.dev/v1alpha1
kind: MCPRegistry
metadata:
  name: my-registry
  namespace: toolhive-system
spec:
  displayName: "My MCP Registry"
  registries:
    - name: configmap-registry
      configMapRef:
        name: my-registry-data
        key: registry.json
```

Apply with:
```bash
kubectl apply -f my-registry.yaml
```

## Sync Operations

### Automatic Sync

Configure automatic synchronization with interval-based policies per registry:

```yaml
spec:
  registries:
    - name: default
      format: toolhive
      configMapRef:
        name: registry-data
        key: registry.json
      syncPolicy:
        interval: "1h"  # Sync every hour
```

Supported intervals:
- `30s`, `5m`, `1h`, `24h`
- Any valid Go duration format

### Manual Sync

Trigger manual sync using annotations:

```bash
kubectl annotate mcpregistry my-registry toolhive.stacklok.dev/manual-sync="$(date +%s)"
```

Or in YAML:
```yaml
metadata:
  annotations:
    toolhive.stacklok.dev/manual-sync: "1704110400"
```

### Sync Status

Check sync status:
```bash
kubectl get mcpregistry my-registry -o jsonpath='{.status.syncStatus}'
```

Status phases:
- `Idle`: No sync needed
- `Syncing`: Sync in progress
- `Complete`: Sync completed successfully
- `Failed`: Sync failed (check `.status.syncStatus.message`)

## Data Sources

### ConfigMap Source

Store registry data in Kubernetes ConfigMaps:

```yaml
spec:
  registries:
    - name: default
      format: toolhive  # or "upstream"
      configMapRef:
        name: registry-data
        key: registry.json  # required
```

### Git Source

Synchronize from Git repositories:

```yaml
spec:
  registries:
    - name: default
      format: toolhive
      git:
        repository: "https://github.com/org/mcp-registry"
        branch: "main"
        path: "registry.json"  # optional, defaults to "registry.json"
```

Supported repository URL formats:
- `https://github.com/org/repo` - HTTPS (recommended)
- `git@github.com:org/repo.git` - SSH
- `ssh://git@example.com/repo.git` - SSH with explicit protocol
- `git://example.com/repo.git` - Git protocol
- `file:///path/to/local/repo` - Local filesystem (for testing)

### API Source

Synchronize from HTTP/HTTPS API endpoints compatible with 
[Model Context Protocol Registry API](https://github.com/modelcontextprotocol/registry/blob/main/docs/reference/api/generic-registry-api.md):

```yaml
spec:
  registries:
    - name: default
      format: toolhive
      api:
        endpoint: "https://registry.example.com"
```

The API source automatically detects the registry format by probing the endpoint:

**ToolHive Registry API** (Supported):
- Endpoint responds to `/v0/info` with registry metadata
- Fetches servers from `/v0/servers`
- Fetches server details from `/v0/servers/{name}`
- No pagination - returns all servers in single response
- Data already in ToolHive format (no conversion needed)

**Upstream MCP Registry API** (Future, once the data format will stabilize):
- Endpoint responds to `/openapi.yaml` with OpenAPI specification
- Will support cursor-based pagination via `/v0/servers?cursor=...`
- Will convert upstream format to ToolHive format automatically

Example configurations:

**Internal ToolHive Registry API:**
```yaml
spec:
  registries:
    - name: default
      format: toolhive
      api:
        endpoint: "http://my-registry-api.default.svc.cluster.local:8080"
      syncPolicy:
        interval: "30m"
```

**External Registry API:**
```yaml
spec:
  registries:
    - name: default
      format: toolhive
      api:
        endpoint: "https://registry.modelcontextprotocol.io/"
      syncPolicy:
        interval: "1h"
```

**Notes:**
- API endpoints are validated at sync time
- Format detection is automatic (ToolHive vs Upstream)
- HTTPS is recommended for production use
- Authentication support planned for future release

### Registry Formats

**ToolHive Format** (default):
- Native ToolHive registry schema
- Supports all ToolHive features
- See [registry schema](../../pkg/registry/data/schema.json)

**Upstream Format**:
- Standard MCP registry format
- Compatible with community registries
- Automatically converted to ToolHive format
- **Note**: Not supported until the upstream schema is more stable

## Filtering

Each registry configuration can define its own filtering rules:

```yaml
spec:
  registries:
    - name: production
      format: toolhive
      configMapRef:
        name: registry-data
        key: registry.json
      filter:
        names:
          include:
            - "prod-*"
          exclude:
            - "*-legacy"
        tags:
          include:
            - "production"
          exclude:
            - "experimental"
            - "deprecated"
```

Filtering is applied per-registry, allowing different filtering rules for different registry sources in the same MCPRegistry.

## Image Validation

### Registry-Based Enforcement

Enforce that MCPServer images must be present in at least one registry:

```yaml
spec:
  enforceServers: true
```

When enabled:
- MCPServers in the namespace are validated against registry content
- Only images present in any registry with `enforceServers: true` are allowed
- MCPServers are matched to registry entries by the `server-registry-name` label
- Invalid images cause MCPServer creation to fail

### MCPServer Matching

MCPServers are matched to registry entries using the `server-registry-name` label:

```yaml
apiVersion: toolhive.stacklok.dev/v1alpha1
kind: MCPServer
metadata:
  name: github-server
  labels:
    server-registry-name: "github"  # Must match registry entry name
spec:
  image: ghcr.io/github/github-mcp-server:latest
```

### Validation Workflow

1. MCPServer is created/updated in namespace
2. Operator checks if any registry in namespace has `enforceServers: true`
3. If yes, validates that the MCPServer's image matches a registry entry
4. Registry matching is done by `server-registry-name` label
5. Allows or rejects based on validation result

### Error Handling

**Note**: Current implementation does not emit Kubernetes events for validation failures. Error details are available in operator logs:

```bash
# Check operator logs for validation errors
kubectl logs -n toolhive-system deployment/toolhive-operator | grep validation
```

## Registry API Service

Each MCPRegistry automatically deploys an API service for registry access:

### API Endpoints

**Registry Data APIs:**
- `GET /api/v1/registry/servers` - List all servers from registry
- `GET /api/v1/registry/servers/{name}` - Get specific server from registry
- `GET /api/v1/registry/info` - Get registry metadata

**Deployed Server APIs** (ToolHive proprietary):
- `GET /api/v1/registry/servers/deployed` - List all deployed MCPServer instances
- `GET /api/v1/registry/servers/deployed/{name}` - Get deployed servers matching registry name

**System APIs:**
- `GET /health` - Health check
- `GET /readiness` - Readiness check
- `GET /version` - Version information
- `GET /api/v1/registry/openapi.yaml` - OpenAPI specification

**Note**: For compatibility with upstream MCP registry APIs, see [MCP Registry Protocol](https://modelcontextprotocol.io/registry) specification.

### Service Access

Internal cluster access:
```
http://{registry-name}-api.{namespace}.svc.cluster.local:8080
```

Port forward for external access:
```bash
kubectl port-forward svc/my-registry-api 8080:8080
curl http://localhost:8080/servers
```

### API Status

Check API deployment status:
```bash
kubectl get mcpregistry my-registry -o jsonpath='{.status.apiStatus}'
```

API phases:
- `Deploying`: API deployment in progress
- `Ready`: API service is available
- `Error`: API deployment failed

## Status Management

### Overall Status

MCPRegistry phase indicates overall state:

```bash
kubectl get mcpregistry
NAME          PHASE     MESSAGE
my-registry   Ready     Registry is ready and API is serving requests
```

Phases:
- `Pending`: Initialization in progress
- `Syncing`: Data synchronization active
- `Ready`: Fully operational
- `Failed`: Operation failed
- `Terminating`: Being deleted

### Detailed Status

```yaml
status:
  phase: Ready
  message: "Registry is ready and API is serving requests"
  syncStatus:
    phase: Complete
    message: "Registry data synchronized successfully"
    serverCount: 5
    lastSyncTime: "2025-01-14T10:30:00Z"
    lastSyncHash: "abc123"
  apiStatus:
    phase: Ready
    endpoint: "http://my-registry-api.toolhive-system.svc.cluster.local:8080"
    readySince: "2025-01-14T10:25:00Z"
  storageRef:
    type: configmap
    configMapRef:
      name: "my-registry-registry-storage"
    configMapRef:
      name: "my-registry-registry-storage"
  lastManualSyncTrigger: "1704110400"
  conditions:
    - type: SyncSuccessful
      status: "True"
      reason: SyncComplete
    - type: APIReady
      status: "True"
      reason: DeploymentReady
```

## Security Best Practices

### Access Control

1. **Namespace Isolation**: Deploy registries in dedicated namespaces
2. **RBAC**: Limit registry modification permissions
3. **Service Accounts**: Use dedicated service accounts for registry operations

### Secret Management

**Note**: Secret management for Git authentication is planned but not yet implemented. Currently, only public repositories are supported for Git sources.

### Image Security

1. **Enable enforcement**: Use `enforceServers: true` to validate images
2. **Registry trust**: Only include trusted registries
3. **Regular updates**: Keep registry data current with security patches

## Troubleshooting

### Common Issues

**Sync Failures**:
```bash
# Check sync status
kubectl get mcpregistry my-registry -o jsonpath='{.status.syncStatus.message}'

# Common causes:
# - Invalid ConfigMap/Git source
# - Network connectivity issues
# - Malformed registry data
```

**API Not Ready**:
```bash
# Check API status
kubectl get mcpregistry my-registry -o jsonpath='{.status.apiStatus}'

# Check deployment
kubectl get deployment my-registry-api

# Common causes:
# - Resource constraints
# - Image pull failures
# - Configuration errors
```

**Image Validation Errors**:
```bash
# Check MCPServer events
kubectl describe mcpserver problematic-server

# Common causes:
# - Image not in registry
# - Registry not synced
# - Typo in image name
```

### Debug Commands

```bash
# View registry events
kubectl get events --field-selector involvedObject.kind=MCPRegistry

# Check operator logs
kubectl logs -n toolhive-system deployment/toolhive-operator

# Describe registry for detailed status
kubectl describe mcpregistry my-registry

# Manual sync trigger
kubectl annotate mcpregistry my-registry toolhive.stacklok.dev/manual-sync="$(date +%s)"
```

### Log Analysis

Operator logs show:
- Sync operations and results
- API deployment status
- Image validation attempts
- Error details with context

Filter for specific registry:
```bash
kubectl logs -n toolhive-system deployment/toolhive-operator | grep "my-registry"
```

## Examples

### Production Registry
```yaml
apiVersion: toolhive.stacklok.dev/v1alpha1
kind: MCPRegistry
metadata:
  name: production-registry
spec:
  displayName: "Production MCP Servers"
  registries:
    - name: default
      format: toolhive
      configMapRef:
        name: prod-registry-data
        key: registry.json
      syncPolicy:
        interval: "1h"
  enforceServers: true
```

### Development Registry
```yaml
apiVersion: toolhive.stacklok.dev/v1alpha1
kind: MCPRegistry
metadata:
  name: dev-registry
spec:
  displayName: "Development MCP Servers"
  registries:
    - name: default
      format: toolhive
      git:
        repository: "https://github.com/org/dev-mcp-registry"
        branch: "develop"
        path: "registry.json"
  # No sync policy = manual sync only
```

### Multiple Registries

You can configure multiple registry sources in a single MCPRegistry:

```yaml
apiVersion: toolhive.stacklok.dev/v1alpha1
kind: MCPRegistry
metadata:
  name: multi-source-registry
spec:
  displayName: "Multi-Source Registry"
  registries:
    - name: production
      format: toolhive
      git:
        repository: "https://github.com/org/prod-registry"
        branch: "main"
        path: "registry.json"
      syncPolicy:
        interval: "1h"
      filter:
        tags:
          include:
            - "production"
    - name: development
      format: toolhive
      configMapRef:
        name: dev-registry-data
        key: registry.json
      filter:
        tags:
          include:
            - "development"
```

Each registry configuration must have a unique `name` within the MCPRegistry.

## See Also

- [MCPServer Documentation](README.md#usage)
- [Operator Installation](../../docs/kind/deploying-toolhive-operator.md)
- [Registry Examples](../../examples/operator/mcp-registries/)
- [Registry Schema](../../pkg/registry/data/schema.json)
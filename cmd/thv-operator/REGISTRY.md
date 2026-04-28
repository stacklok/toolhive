# MCPRegistry Reference

## Overview

MCPRegistry is a Kubernetes Custom Resource that manages MCP (Model Context Protocol) server registries. It provides centralized server discovery and automated synchronization for MCP servers in your cluster.

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
      "$schema": "https://raw.githubusercontent.com/stacklok/toolhive-core/main/registry/types/data/upstream-registry.schema.json",
      "version": "1.0.0",
      "meta": {
        "last_updated": "2025-01-14T00:00:00Z"
      },
      "data": {
        "servers": [
          {
            "name": "io.github.github/github",
            "description": "GitHub API integration",
            "version": "1.0.0",
            "packages": [
              {
                "registryType": "oci",
                "identifier": "ghcr.io/github/github-mcp-server:latest",
                "transport": { "type": "stdio" }
              }
            ],
            "_meta": {
              "io.modelcontextprotocol.registry/publisher-provided": {
                "io.github.github": {
                  "ghcr.io/github/github-mcp-server:latest": {
                    "tier": "Official",
                    "tags": ["github", "api", "production"]
                  }
                }
              }
            }
          }
        ]
      }
    }
---
apiVersion: toolhive.stacklok.dev/v1beta1
kind: MCPRegistry
metadata:
  name: my-registry
  namespace: toolhive-system
spec:
  displayName: "My MCP Registry"
  sources:
    - name: configmap-source
      configMapRef:
        name: my-registry-data
        key: registry.json
  registries:
    - name: default
      sources:
        - configmap-source
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
  sources:
    - name: default
      configMapRef:
        name: registry-data
        key: registry.json
      syncPolicy:
        interval: "1h"  # Sync every hour
  registries:
    - name: default
      sources:
        - default
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

Check registry status:
```bash
kubectl get mcpregistry my-registry -o jsonpath='{.status.phase}'
```

Status phases:
- `Pending`: Registry API deployment is not ready yet
- `Ready`: Registry API is ready and serving requests
- `Failed`: Operation failed (check `.status.message`)
- `Terminating`: Being deleted

## Data Sources

### ConfigMap Source

Store registry data in Kubernetes ConfigMaps:

```yaml
spec:
  sources:
    - name: default
      configMapRef:
        name: registry-data
        key: registry.json  # required
  registries:
    - name: default
      sources:
        - default
```

### Git Source

Synchronize from Git repositories:

```yaml
spec:
  sources:
    - name: default
      git:
        repository: "https://github.com/org/mcp-registry"
        branch: "main"
        path: "registry.json"  # optional, defaults to "registry.json"
  registries:
    - name: default
      sources:
        - default
```

Supported repository URL formats:
- `https://github.com/org/repo` - HTTPS (recommended)
- `git@github.com:org/repo.git` - SSH
- `ssh://git@example.com/repo.git` - SSH with explicit protocol
- `git://example.com/repo.git` - Git protocol
- `file:///path/to/local/repo` - Local filesystem (for testing)

#### Private Repository Authentication

For private Git repositories, you can configure authentication using HTTP Basic Auth:

```yaml
# First, create a Secret containing your Git credentials
apiVersion: v1
kind: Secret
metadata:
  name: git-credentials
  namespace: toolhive-system
type: Opaque
stringData:
  password: "ghp_your_github_token_here"  # GitHub PAT, GitLab token, etc.
---
apiVersion: toolhive.stacklok.dev/v1beta1
kind: MCPRegistry
metadata:
  name: private-registry
  namespace: toolhive-system
spec:
  displayName: "Private MCP Registry"
  sources:
    - name: default
      git:
        repository: "https://github.com/org/private-mcp-registry"
        branch: "main"
        path: "registry.json"
        auth:
          username: "git"  # Use "git" for GitHub PATs
          passwordSecretRef:
            name: git-credentials
            key: password
      syncPolicy:
        interval: "1h"
  registries:
    - name: default
      sources:
        - default
```

**Authentication notes:**
- For **GitHub Personal Access Tokens (PATs)**: Use `username: "git"` with the PAT as the password
- For **GitLab tokens**: Use `username: "oauth2"` with the token as the password
- For **Bitbucket app passwords**: Use your Bitbucket username with the app password
- The Secret must exist in the same namespace as the MCPRegistry
- The password file is securely mounted at `/secrets/{secretName}/{key}` in the registry-api pod, where `{key}` is the value specified in `passwordSecretRef.key`

### API Source

Synchronize from HTTP/HTTPS API endpoints compatible with 
[Model Context Protocol Registry API](https://github.com/modelcontextprotocol/registry/blob/main/docs/reference/api/generic-registry-api.md):

```yaml
spec:
  sources:
    - name: default
      api:
        endpoint: "https://registry.example.com"
  registries:
    - name: default
      sources:
        - default
```

The API source fetches servers from an endpoint speaking the upstream MCP registry API:

- Endpoint responds to `/v0/info` with registry metadata
- Fetches servers from `/v0/servers`
- Fetches server details from `/v0/servers/{name}`
- Server entries use the upstream MCP server schema, with ToolHive-specific metadata carried through publisher-provided extensions

Example configurations:

**Internal ToolHive Registry API:**
```yaml
spec:
  sources:
    - name: internal-api
      api:
        endpoint: "http://my-registry-api.default.svc.cluster.local:8080"
      syncPolicy:
        interval: "30m"
  registries:
    - name: default
      sources:
        - internal-api
```

**External Registry API:**
```yaml
spec:
  sources:
    - name: upstream
      api:
        endpoint: "https://registry.modelcontextprotocol.io/"
      syncPolicy:
        interval: "1h"
  registries:
    - name: default
      sources:
        - upstream
```

**Notes:**
- API endpoints are validated at sync time
- HTTPS is recommended for production use
- Authentication support planned for future release

### Registry Format

ToolHive registries use the upstream MCP server format published in
[`stacklok/toolhive-core`](https://github.com/stacklok/toolhive-core)
under `registry/types/data/`:

- `upstream-registry.schema.json` validates the registry envelope and
  references the official MCP server schema.
- `publisher-provided.schema.json` defines the ToolHive-specific metadata
  carried under `_meta["io.modelcontextprotocol.registry/publisher-provided"]`
  (tier, tools, permissions, OAuth/OIDC config, etc.).

The legacy ToolHive-native format is no longer accepted. Existing files
can be migrated with `thv registry convert --in <file> --in-place`.

## Filtering

Each registry configuration can define its own filtering rules:

```yaml
spec:
  sources:
    - name: production
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
  registries:
    - name: default
      sources:
        - production
```

Filtering is applied per-source, allowing different filtering rules for different data sources in the same MCPRegistry.

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

Check API endpoint:
```bash
kubectl get mcpregistry my-registry -o jsonpath='{.status.url}'
```

Check ready replicas:
```bash
kubectl get mcpregistry my-registry -o jsonpath='{.status.readyReplicas}'
```

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
  message: "Registry API is ready and serving requests"
  url: "http://my-registry-api.toolhive-system:8080"
  readyReplicas: 1
  observedGeneration: 1
  conditions:
    - type: Ready
      status: "True"
      reason: RegistryReady
      message: "Registry API is ready and serving requests"
```

## Security Best Practices

### Access Control

1. **Namespace Isolation**: Deploy registries in dedicated namespaces
2. **RBAC**: Limit registry modification permissions
3. **Service Accounts**: Use dedicated service accounts for registry operations

### Secret Management

Git authentication credentials are stored securely in Kubernetes Secrets:

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: git-credentials
  namespace: toolhive-system
type: Opaque
stringData:
  password: "ghp_your_token_here"
```

**Best practices for Git credentials:**
1. **Use tokens, not passwords**: Prefer GitHub PATs, GitLab tokens, or app passwords over account passwords
2. **Scope tokens minimally**: Grant only `repo:read` or equivalent read-only permissions
3. **Rotate regularly**: Set up token rotation policies
4. **Use separate tokens per registry**: Don't share tokens across registries
5. **Consider RBAC**: Limit which service accounts can read the credentials Secret

### Image Security

1. **Registry trust**: Only include trusted registries
2. **Regular updates**: Keep registry data current with security patches

## Troubleshooting

### Common Issues

**Sync Failures**:
```bash
# Check registry status message
kubectl get mcpregistry my-registry -o jsonpath='{.status.message}'

# Common causes:
# - Invalid ConfigMap/Git source
# - Network connectivity issues
# - Malformed registry data
```

**API Not Ready**:
```bash
# Check phase and message
kubectl get mcpregistry my-registry -o jsonpath='{.status.phase}: {.status.message}'

# Check deployment
kubectl get deployment my-registry-api

# Common causes:
# - Resource constraints
# - Image pull failures
# - Configuration errors
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
- Error details with context

Filter for specific registry:
```bash
kubectl logs -n toolhive-system deployment/toolhive-operator | grep "my-registry"
```

## Examples

### Production Registry
```yaml
apiVersion: toolhive.stacklok.dev/v1beta1
kind: MCPRegistry
metadata:
  name: production-registry
spec:
  displayName: "Production MCP Servers"
  sources:
    - name: prod-source
      configMapRef:
        name: prod-registry-data
        key: registry.json
      syncPolicy:
        interval: "1h"
  registries:
    - name: default
      sources:
        - prod-source
```

### Development Registry
```yaml
apiVersion: toolhive.stacklok.dev/v1beta1
kind: MCPRegistry
metadata:
  name: dev-registry
spec:
  displayName: "Development MCP Servers"
  sources:
    - name: dev-source
      git:
        repository: "https://github.com/org/dev-mcp-registry"
        branch: "develop"
        path: "registry.json"
  # No sync policy = manual sync only
  registries:
    - name: default
      sources:
        - dev-source
```

### Private Git Repository Registry
```yaml
apiVersion: v1
kind: Secret
metadata:
  name: private-repo-token
  namespace: toolhive-system
type: Opaque
stringData:
  token: "<github token>"
---
apiVersion: toolhive.stacklok.dev/v1beta1
kind: MCPRegistry
metadata:
  name: private-registry
  namespace: toolhive-system
spec:
  displayName: "Private Organization Registry"
  sources:
    - name: private-source
      git:
        repository: "https://github.com/myorg/private-mcp-servers"
        branch: "main"
        path: "registry.json"
        auth:
          username: "git"
          passwordSecretRef:
            name: private-repo-token
            key: token
      syncPolicy:
        interval: "30m"
  registries:
    - name: default
      sources:
        - private-source
```

### Multiple Sources

You can configure multiple data sources in a single MCPRegistry and aggregate them into registry views:

```yaml
apiVersion: toolhive.stacklok.dev/v1beta1
kind: MCPRegistry
metadata:
  name: multi-source-registry
spec:
  displayName: "Multi-Source Registry"
  sources:
    - name: production
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
      configMapRef:
        name: dev-registry-data
        key: registry.json
      filter:
        tags:
          include:
            - "development"
  registries:
    - name: default
      sources:
        - production
        - development
```

Each source must have a unique `name` within the MCPRegistry. Registry views reference sources by name.

## See Also

- [MCPServer Documentation](README.md#usage)
- [Operator Installation](../../docs/kind/deploying-toolhive-operator.md)
- [Registry Examples](../../examples/operator/mcp-registries/)
- [Private Git Registry Example](../../examples/operator/mcp-registries/mcpregistry-git-private.yaml)
- [Registry Schema](../../docs/registry/schema.md)

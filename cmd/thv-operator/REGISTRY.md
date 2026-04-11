# MCPRegistry Reference

## Overview

MCPRegistry is a Kubernetes Custom Resource that manages MCP (Model Context Protocol) server registries. It provides centralized server discovery, automated synchronization, and image validation for MCP servers in your cluster.

The MCPRegistry CRD uses a **pass-through configuration model**: you provide the complete registry server `config.yaml` content in `spec.configYAML`, and the operator passes it through verbatim as a ConfigMap -- no parsing or transformation. This gives you full control over the registry server configuration while the operator handles only the Kubernetes deployment plumbing.

## Quick Start

Create a minimal registry with a Kubernetes source (watches MCPServer resources in the namespace):

```yaml
apiVersion: toolhive.stacklok.dev/v1alpha1
kind: MCPRegistry
metadata:
  name: my-registry
  namespace: toolhive-system
spec:
  displayName: "My MCP Registry"
  configYAML: |
    sources:
      - name: k8s
        format: upstream
        kubernetes: {}
    registries:
      - name: default
        sources: ["k8s"]
    database:
      host: registry-db-rw
      port: 5432
      user: db_app
      database: registry
    auth:
      mode: anonymous
```

Apply with:
```bash
kubectl apply -f my-registry.yaml
```

### CRD Fields

The MCPRegistry spec has four fields for configuring the registry server:

| Field | Required | Purpose |
|---|---|---|
| `configYAML` | Yes | Complete registry server `config.yaml` content (passed through verbatim) |
| `volumes` | No | Standard Kubernetes volumes to add to the pod (for Secrets, ConfigMaps, etc.) |
| `volumeMounts` | No | Standard Kubernetes volume mounts for the registry-api container |
| `pgpassSecretRef` | No | Reference to a Secret containing a pgpass file (operator handles chmod 0600 plumbing) |

Additional fields (`displayName`, `enforceServers`, `podTemplateSpec`) are documented in later sections.

### Config YAML Structure

The `configYAML` field contains a complete registry server configuration. Top-level sections:

| Section | Required | Purpose |
|---|---|---|
| `sources` | Yes | Data source definitions (file, git, api, kubernetes) |
| `registries` | Yes | Registry views that aggregate sources |
| `database` | Yes | PostgreSQL connection settings |
| `auth` | Yes | Authentication mode (`anonymous` or `oauth`) |
| `telemetry` | No | OpenTelemetry configuration |

## Sync Operations

### Automatic Sync

Configure automatic synchronization with interval-based policies per source inside `configYAML`:

```yaml
spec:
  configYAML: |
    sources:
      - name: default
        format: toolhive
        file:
          path: /config/registry/default/registry.json
        syncPolicy:
          interval: 1h
    registries:
      - name: default
        sources: ["default"]
    database:
      host: registry-db-rw
      port: 5432
      user: db_app
      database: registry
    auth:
      mode: anonymous
  volumes:
    - name: registry-data
      configMap:
        name: registry-data
        items:
          - key: registry.json
            path: registry.json
  volumeMounts:
    - name: registry-data
      mountPath: /config/registry/default
      readOnly: true
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

All data sources are configured inside `configYAML`. When a source references data on the filesystem (ConfigMap files, git auth tokens), you must also define `spec.volumes` and `spec.volumeMounts` to wire those files into the registry-api container. The file paths in `configYAML` must match the mount paths in `volumeMounts`.

### ConfigMap Source

Store registry data in Kubernetes ConfigMaps. In `configYAML`, use the `file:` source type with a `path` that matches the `volumeMount` mount path. Define the ConfigMap volume and mount explicitly in the MCPRegistry spec.

```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: registry-data
  namespace: toolhive-system
data:
  registry.json: |
    {
      "$schema": "https://raw.githubusercontent.com/stacklok/toolhive/main/pkg/registry/data/toolhive-legacy-registry.schema.json",
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
  configYAML: |
    sources:
      - name: configmap-source
        format: toolhive
        file:
          path: /config/registry/default/registry.json
    registries:
      - name: default
        sources: ["configmap-source"]
    database:
      host: registry-db-rw
      port: 5432
      user: db_app
      database: registry
    auth:
      mode: anonymous
  volumes:
    - name: registry-data
      configMap:
        name: registry-data
        items:
          - key: registry.json
            path: registry.json
  volumeMounts:
    - name: registry-data
      mountPath: /config/registry/default
      readOnly: true
```

### Git Source

Synchronize from Git repositories. The git configuration goes inside `configYAML`:

```yaml
spec:
  configYAML: |
    sources:
      - name: default
        format: toolhive
        git:
          repository: https://github.com/org/mcp-registry
          branch: main
          path: registry.json
    registries:
      - name: default
        sources: ["default"]
    database:
      host: registry-db-rw
      port: 5432
      user: db_app
      database: registry
    auth:
      mode: anonymous
```

Supported repository URL formats:
- `https://github.com/org/repo` - HTTPS (recommended)
- `git@github.com:org/repo.git` - SSH
- `ssh://git@example.com/repo.git` - SSH with explicit protocol
- `git://example.com/repo.git` - Git protocol
- `file:///path/to/local/repo` - Local filesystem (for testing)

#### Private Repository Authentication

For private Git repositories, configure authentication using `passwordFile` in `configYAML` and mount the Secret containing the token via `volumes`/`volumeMounts`:

```yaml
# First, create a Secret containing your Git credentials
apiVersion: v1
kind: Secret
metadata:
  name: git-credentials
  namespace: toolhive-system
type: Opaque
stringData:
  token: "ghp_your_github_token_here"
---
apiVersion: toolhive.stacklok.dev/v1alpha1
kind: MCPRegistry
metadata:
  name: private-registry
  namespace: toolhive-system
spec:
  displayName: "Private MCP Registry"
  configYAML: |
    sources:
      - name: default
        format: toolhive
        git:
          repository: https://github.com/org/private-mcp-registry
          branch: main
          path: registry.json
          auth:
            username: git
            # File path must match the volumeMount below
            passwordFile: /secrets/git-credentials/token
        syncPolicy:
          interval: 1h
    registries:
      - name: default
        sources: ["default"]
    database:
      host: registry-db-rw
      port: 5432
      user: db_app
      database: registry
    auth:
      mode: anonymous
  # Volume to project the git credentials secret into the container filesystem
  volumes:
    - name: git-auth-credentials
      secret:
        secretName: git-credentials
        items:
          - key: token
            path: token
  # Mount the secret at the path referenced by passwordFile in configYAML
  volumeMounts:
    - name: git-auth-credentials
      mountPath: /secrets/git-credentials
      readOnly: true
```

**Authentication notes:**
- For **GitHub Personal Access Tokens (PATs)**: Use `username: "git"` with the PAT as the password
- For **GitLab tokens**: Use `username: "oauth2"` with the token as the password
- For **Bitbucket app passwords**: Use your Bitbucket username with the app password
- The Secret must exist in the same namespace as the MCPRegistry
- The `passwordFile` path in `configYAML` must match the `mountPath` + file name in `volumeMounts`/`volumes`

### API Source

Synchronize from HTTP/HTTPS API endpoints compatible with
[Model Context Protocol Registry API](https://github.com/modelcontextprotocol/registry/blob/main/docs/reference/api/generic-registry-api.md).

API sources require no volumes or volume mounts -- the registry server handles the network call internally. API sources must use `format: upstream`.

```yaml
spec:
  configYAML: |
    sources:
      - name: default
        format: upstream
        api:
          endpoint: https://registry.example.com
    registries:
      - name: default
        sources: ["default"]
    database:
      host: registry-db-rw
      port: 5432
      user: db_app
      database: registry
    auth:
      mode: anonymous
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
  configYAML: |
    sources:
      - name: internal-api
        format: upstream
        api:
          endpoint: http://my-registry-api.default.svc.cluster.local:8080
        syncPolicy:
          interval: 30m
    registries:
      - name: default
        sources: ["internal-api"]
    database:
      host: registry-db-rw
      port: 5432
      user: db_app
      database: registry
    auth:
      mode: anonymous
```

**External Registry API:**
```yaml
spec:
  configYAML: |
    sources:
      - name: upstream
        format: upstream
        api:
          endpoint: https://registry.modelcontextprotocol.io/
        syncPolicy:
          interval: 1h
    registries:
      - name: default
        sources: ["upstream"]
    database:
      host: registry-db-rw
      port: 5432
      user: db_app
      database: registry
    auth:
      mode: anonymous
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
- See [registry schema](../../pkg/registry/data/toolhive-legacy-registry.schema.json)

**Upstream Format**:
- Standard MCP registry format
- Compatible with community registries
- Automatically converted to ToolHive format
- Required for API sources (`format: upstream`)
- **Note**: Not supported until the upstream schema is more stable

## Filtering

Each source inside `configYAML` can define its own filtering rules:

```yaml
spec:
  configYAML: |
    sources:
      - name: production
        format: toolhive
        file:
          path: /config/registry/production/registry.json
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
        sources: ["production"]
    database:
      host: registry-db-rw
      port: 5432
      user: db_app
      database: registry
    auth:
      mode: anonymous
  volumes:
    - name: registry-data-production
      configMap:
        name: registry-data
        items:
          - key: registry.json
            path: registry.json
  volumeMounts:
    - name: registry-data-production
      mountPath: /config/registry/production
      readOnly: true
```

Filtering is applied per-source, allowing different filtering rules for different data sources in the same MCPRegistry.

## Database Configuration

### PostgreSQL with pgpass

Configure database settings inside `configYAML` and provide credentials via `spec.pgpassSecretRef`. The operator handles the Kubernetes plumbing for pgpass automatically: it creates an init container that copies the pgpass file from the Secret to an emptyDir volume and runs `chmod 0600` (required by libpq), then mounts the file at `/home/appuser/.pgpass` and sets the `PGPASSFILE` environment variable.

This is necessary because Kubernetes secret volumes mount files as root-owned, and the registry container runs as non-root (UID 65532). A root-owned 0600 file is unreadable by UID 65532, and using `fsGroup` changes permissions to 0640 which libpq also rejects.

```yaml
# Secret containing the pgpass file
# Format: hostname:port:database:username:password (one entry per line)
# See https://www.postgresql.org/docs/current/libpq-pgpass.html
apiVersion: v1
kind: Secret
metadata:
  name: my-registry-pgpass
  namespace: toolhive-system
type: Opaque
stringData:
  .pgpass: |
    registry-db-rw:5432:registry:db_app:myapppassword
    registry-db-rw:5432:registry:db_migrator:mymigrationpassword
---
apiVersion: toolhive.stacklok.dev/v1alpha1
kind: MCPRegistry
metadata:
  name: my-registry
  namespace: toolhive-system
spec:
  displayName: "Registry with Database Auth"
  configYAML: |
    sources:
      - name: production
        format: toolhive
        file:
          path: /config/registry/production/registry.json
    registries:
      - name: default
        sources: ["production"]
    database:
      host: registry-db-rw
      port: 5432
      user: db_app
      migrationUser: db_migrator
      database: registry
      sslMode: require
      maxOpenConns: 20
    auth:
      mode: anonymous
  pgpassSecretRef:
    name: my-registry-pgpass
    key: .pgpass
  volumes:
    - name: registry-data-production
      configMap:
        name: prod-registry
        items:
          - key: registry.json
            path: registry.json
  volumeMounts:
    - name: registry-data-production
      mountPath: /config/registry/production
      readOnly: true
```

## Authentication

### OAuth Configuration

Configure OAuth authentication inside `configYAML`. OAuth secrets (client secrets) and CA certificates are mounted via `volumes`/`volumeMounts`, and their file paths must match the `clientSecretFile` and `caCertPath` values in `configYAML`.

Auth defaults to `oauth` mode. Use `mode: anonymous` for development and testing.

```yaml
apiVersion: toolhive.stacklok.dev/v1alpha1
kind: MCPRegistry
metadata:
  name: secure-registry
  namespace: toolhive-system
spec:
  displayName: "Secure Registry with OAuth"
  configYAML: |
    sources:
      - name: production
        format: toolhive
        file:
          path: /config/registry/production/registry.json
    registries:
      - name: default
        sources: ["production"]
    database:
      host: registry-db-rw
      port: 5432
      user: db_app
      database: registry
    auth:
      mode: oauth
      oauth:
        resourceUrl: https://registry.example.com
        realm: mcp-registry
        scopesSupported:
          - mcp-registry:read
          - mcp-registry:write
        providers:
          - name: keycloak
            issuerUrl: https://keycloak.example.com/realms/mcp
            audience: mcp-registry
            clientId: mcp-registry
            # File path must match the volumeMount for the OAuth client secret
            clientSecretFile: /secrets/oauth-client-secret/secret
            # File path must match the volumeMount for the CA certificate
            caCertPath: /config/certs/keycloak-ca/ca.crt
  volumes:
    # Registry data from a ConfigMap
    - name: registry-data-production
      configMap:
        name: prod-registry
        items:
          - key: registry.json
            path: registry.json
    # OAuth client secret from a Kubernetes Secret
    - name: oauth-client-secret
      secret:
        secretName: oauth-client-secret
        items:
          - key: secret
            path: secret
    # CA certificate for the OAuth provider from a ConfigMap
    - name: keycloak-ca
      configMap:
        name: keycloak-ca
        items:
          - key: ca.crt
            path: ca.crt
  volumeMounts:
    # Mount registry data at the path referenced in configYAML sources
    - name: registry-data-production
      mountPath: /config/registry/production
      readOnly: true
    # Mount OAuth client secret at the path referenced by clientSecretFile
    - name: oauth-client-secret
      mountPath: /secrets/oauth-client-secret
      readOnly: true
    # Mount CA certificate at the path referenced by caCertPath
    - name: keycloak-ca
      mountPath: /config/certs/keycloak-ca
      readOnly: true
```

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

Do not inline credentials (passwords, tokens, client secrets) in `configYAML` -- it is stored in a ConfigMap, not a Secret. Instead, mount credentials as Kubernetes Secrets via `volumes`/`volumeMounts` and reference them by file path in `configYAML`.

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: git-credentials
  namespace: toolhive-system
type: Opaque
stringData:
  token: "ghp_your_token_here"
```

**Best practices for credentials:**
1. **Use tokens, not passwords**: Prefer GitHub PATs, GitLab tokens, or app passwords over account passwords
2. **Scope tokens minimally**: Grant only `repo:read` or equivalent read-only permissions
3. **Rotate regularly**: Set up token rotation policies
4. **Use separate tokens per registry**: Don't share tokens across registries
5. **Consider RBAC**: Limit which service accounts can read the credentials Secret
6. **Use pgpassSecretRef for database credentials**: The operator handles the chmod 0600 plumbing automatically

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
# - Invalid configYAML content
# - Volume/volumeMount path mismatch with configYAML file paths
# - ConfigMap or Secret referenced in volumes does not exist
# - Network connectivity issues for git/api sources
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
# - Configuration errors in configYAML
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
  namespace: toolhive-system
spec:
  displayName: "Production MCP Servers"
  configYAML: |
    sources:
      - name: prod-source
        format: toolhive
        file:
          path: /config/registry/production/registry.json
        syncPolicy:
          interval: 1h
    registries:
      - name: default
        sources: ["prod-source"]
    database:
      host: registry-db-rw
      port: 5432
      user: db_app
      database: registry
    auth:
      mode: oauth
      oauth:
        resourceUrl: https://registry.example.com
        realm: mcp-registry
        scopesSupported:
          - mcp-registry:read
        providers:
          - name: keycloak
            issuerUrl: https://keycloak.example.com/realms/mcp
            audience: mcp-registry
            clientId: mcp-registry
            clientSecretFile: /secrets/oauth-client-secret/secret
  enforceServers: true
  pgpassSecretRef:
    name: production-pgpass
    key: .pgpass
  volumes:
    - name: registry-data-production
      configMap:
        name: prod-registry-data
        items:
          - key: registry.json
            path: registry.json
    - name: oauth-client-secret
      secret:
        secretName: oauth-client-secret
        items:
          - key: secret
            path: secret
  volumeMounts:
    - name: registry-data-production
      mountPath: /config/registry/production
      readOnly: true
    - name: oauth-client-secret
      mountPath: /secrets/oauth-client-secret
      readOnly: true
```

### Development Registry
```yaml
apiVersion: toolhive.stacklok.dev/v1alpha1
kind: MCPRegistry
metadata:
  name: dev-registry
  namespace: toolhive-system
spec:
  displayName: "Development MCP Servers"
  configYAML: |
    sources:
      - name: dev-source
        format: toolhive
        git:
          repository: https://github.com/org/dev-mcp-registry
          branch: develop
          path: registry.json
    registries:
      - name: default
        sources: ["dev-source"]
    database:
      host: registry-db-rw
      port: 5432
      user: db_app
      database: registry
    auth:
      mode: anonymous
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
apiVersion: toolhive.stacklok.dev/v1alpha1
kind: MCPRegistry
metadata:
  name: private-registry
  namespace: toolhive-system
spec:
  displayName: "Private Organization Registry"
  configYAML: |
    sources:
      - name: private-source
        format: toolhive
        git:
          repository: https://github.com/myorg/private-mcp-servers
          branch: main
          path: registry.json
          auth:
            username: git
            passwordFile: /secrets/private-repo-token/token
        syncPolicy:
          interval: 30m
    registries:
      - name: default
        sources: ["private-source"]
    database:
      host: registry-db-rw
      port: 5432
      user: db_app
      database: registry
    auth:
      mode: anonymous
  volumes:
    - name: git-auth-credentials
      secret:
        secretName: private-repo-token
        items:
          - key: token
            path: token
  volumeMounts:
    - name: git-auth-credentials
      mountPath: /secrets/private-repo-token
      readOnly: true
```

### Multiple Sources

You can configure multiple data sources in a single MCPRegistry and aggregate them into registry views. All source and registry configuration lives inside `configYAML`:

```yaml
apiVersion: toolhive.stacklok.dev/v1alpha1
kind: MCPRegistry
metadata:
  name: multi-source-registry
  namespace: toolhive-system
spec:
  displayName: "Multi-Source Registry"
  configYAML: |
    sources:
      - name: production
        format: toolhive
        git:
          repository: https://github.com/org/prod-registry
          branch: main
          path: registry.json
        syncPolicy:
          interval: 1h
        filter:
          tags:
            include: ["production"]
      - name: development
        format: toolhive
        file:
          path: /config/registry/development/registry.json
        filter:
          tags:
            include: ["development"]
    registries:
      - name: default
        sources:
          - production
          - development
    database:
      host: registry-db-rw
      port: 5432
      user: db_app
      database: registry
    auth:
      mode: anonymous
  volumes:
    - name: dev-registry-data
      configMap:
        name: dev-registry-data
        items:
          - key: registry.json
            path: registry.json
  volumeMounts:
    - name: dev-registry-data
      mountPath: /config/registry/development
      readOnly: true
```

Each source must have a unique `name` within the `configYAML`. Registry views reference sources by name.

## See Also

- [MCPServer Documentation](README.md#usage)
- [Operator Installation](../../docs/kind/deploying-toolhive-operator.md)
- [Registry Examples](../../examples/operator/mcp-registries/)
- [Private Git Registry Example](../../examples/operator/mcp-registries/mcpregistry-configyaml-git-auth.yaml)
- [Registry Schema](../../pkg/registry/data/toolhive-legacy-registry.schema.json)

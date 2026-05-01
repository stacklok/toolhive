# MCPRegistry Reference

## Overview

MCPRegistry is a Kubernetes Custom Resource that manages MCP (Model Context Protocol) server registries. It provides centralized server discovery and automated synchronization for MCP servers in your cluster.

## Quick Start

The simplest MCPRegistry uses a Kubernetes source, which discovers servers
directly from `MCPServer` resources in the namespace and needs no extra
volumes:

```yaml
apiVersion: toolhive.stacklok.dev/v1beta1
kind: MCPRegistry
metadata:
  name: my-registry
  namespace: toolhive-system
spec:
  displayName: "My MCP Registry"
  configYAML: |
    sources:
      - name: k8s
        kubernetes: {}
    registries:
      - name: default
        sources: ["k8s"]
    database:
      host: postgres
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

For ConfigMap, Git, and API source variants, see [Data Sources](#data-sources)
and the [examples directory](../../examples/operator/mcp-registries/).

## Spec Reference

The `MCPRegistry` CRD exposes a small, decoupled spec — most configuration
lives inside `configYAML`:

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `configYAML` | string | yes | Complete registry server `config.yaml` content. Passed through verbatim; the operator does not parse, validate, or transform it. |
| `volumes` | array of `Volume` | no | Standard Kubernetes volumes appended to the registry-api pod. Use these to project ConfigMaps and Secrets that `configYAML` references by file path. |
| `volumeMounts` | array of `VolumeMount` | no | Standard volume mounts on the registry-api container. Mount paths must match the file paths referenced in `configYAML`. |
| `pgpassSecretRef` | `SecretKeySelector` | no | Reference to a Secret containing a pgpass file. The operator wires up the init container, emptyDir, and `chmod 0600` automatically. See [PostgreSQL Authentication](#postgresql-authentication). |
| `displayName` | string | no | Human-readable name. |
| `podTemplateSpec` | object | no | Pod template overrides for the registry-api pod (resources, affinity, etc.). |

**Security note**: `configYAML` is stored in a ConfigMap, not a Secret. Do
not inline credentials (passwords, tokens, client secrets). Reference
credentials via file paths and mount the actual Secrets through `volumes`
and `volumeMounts`.

### configYAML structure

The registry server's `config.yaml` is documented in the
[ToolHive Registry Server](https://github.com/stacklok/toolhive-registry-server)
project. The four top-level keys ToolHive uses are:

- `sources` — where registry data comes from (Kubernetes, file, Git, API).
- `registries` — named views that aggregate one or more sources.
- `database` — PostgreSQL connection settings.
- `auth` — authentication mode for the registry API.

Files referenced from `sources` (registry data, Git credentials, TLS
material) must be made available through the CRD's `volumes` and
`volumeMounts` fields.

## Sync Operations

### Automatic Sync

Configure automatic synchronization with `syncPolicy` on each source inside `configYAML`:

```yaml
spec:
  configYAML: |
    sources:
      - name: default
        kubernetes: {}
        syncPolicy:
          interval: 1h  # Sync every hour
    registries:
      - name: default
        sources: ["default"]
    database: { host: postgres, port: 5432, user: db_app, database: registry }
    auth: { mode: anonymous }
```

Supported intervals:
- `30s`, `5m`, `1h`, `24h`
- Any valid Go duration format

Omit `syncPolicy` on a source to disable automatic sync (manual-only).

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

All sources are declared inside `configYAML.sources`. Each source has a
unique `name` and exactly one of: `kubernetes`, `file`, `git`, or `api`.

### Kubernetes Source

Discovers servers from `MCPServer` resources in the namespace. No volumes
required — the registry server reads from the Kubernetes API directly.

```yaml
spec:
  configYAML: |
    sources:
      - name: k8s
        kubernetes: {}
    registries:
      - name: default
        sources: ["k8s"]
    database: { host: postgres, port: 5432, user: db_app, database: registry }
    auth: { mode: anonymous }
```

### ConfigMap (File) Source

Project a ConfigMap into the registry-api pod with `volumes`/`volumeMounts`
and reference it as a `file:` source. The path in `configYAML` must match
the `mountPath`.

```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: prod-registry
  namespace: toolhive-system
data:
  registry.json: |
    { "$schema": "https://raw.githubusercontent.com/stacklok/toolhive-core/main/registry/types/data/upstream-registry.schema.json",
      "version": "1.0.0",
      "meta": { "last_updated": "2025-01-14T00:00:00Z" },
      "data": { "servers": [ /* upstream server entries */ ] } }
---
apiVersion: toolhive.stacklok.dev/v1beta1
kind: MCPRegistry
metadata:
  name: configmap-registry
  namespace: toolhive-system
spec:
  configYAML: |
    sources:
      - name: production
        file:
          path: /config/registry/production/registry.json
        syncPolicy:
          interval: 1h
    registries:
      - name: default
        sources: ["production"]
    database: { host: postgres, port: 5432, user: db_app, database: registry }
    auth: { mode: anonymous }
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

For a complete working example, see
[`mcpregistry-configyaml-configmap.yaml`](../../examples/operator/mcp-registries/mcpregistry-configyaml-configmap.yaml).

### Git Source

Sync registry data from a Git repository. The repository URL, branch, and
path live inside `configYAML`:

```yaml
spec:
  configYAML: |
    sources:
      - name: company-repo
        git:
          repository: https://github.com/company/mcp-registry
          branch: main
          path: registry.json   # optional, defaults to "registry.json"
        syncPolicy:
          interval: 1h
    registries:
      - name: default
        sources: ["company-repo"]
    database: { host: postgres, port: 5432, user: db_app, database: registry }
    auth: { mode: anonymous }
```

Supported repository URL formats:
- `https://github.com/org/repo` — HTTPS (recommended)
- `git@github.com:org/repo.git` — SSH
- `ssh://git@example.com/repo.git` — SSH with explicit protocol
- `git://example.com/repo.git` — Git protocol
- `file:///path/to/local/repo` — Local filesystem (for testing)

#### Private Repository Authentication

For private repositories, mount the credential as a file via
`volumes`/`volumeMounts` and reference it with `auth.passwordFile` in
`configYAML`:

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: git-credentials
  namespace: toolhive-system
type: Opaque
stringData:
  token: "ghp_your_personal_access_token_here"
---
apiVersion: toolhive.stacklok.dev/v1beta1
kind: MCPRegistry
metadata:
  name: private-registry
  namespace: toolhive-system
spec:
  configYAML: |
    sources:
      - name: private-repo
        git:
          repository: https://github.com/org/private-mcp-registry
          branch: main
          path: registry.json
          auth:
            username: git   # see notes below
            passwordFile: /secrets/git-credentials/token
        syncPolicy:
          interval: 1h
    registries:
      - name: default
        sources: ["private-repo"]
    database: { host: postgres, port: 5432, user: db_app, database: registry }
    auth: { mode: anonymous }
  volumes:
    - name: git-auth-credentials
      secret:
        secretName: git-credentials
        items:
          - key: token
            path: token
  volumeMounts:
    - name: git-auth-credentials
      mountPath: /secrets/git-credentials
      readOnly: true
```

**Authentication notes:**
- **GitHub Personal Access Tokens (PATs)**: use `username: "git"` and put the PAT in the credential file
- **GitLab tokens**: use `username: "oauth2"`
- **Bitbucket app passwords**: use your Bitbucket username
- The Secret must exist in the same namespace as the MCPRegistry
- The `passwordFile` path in `configYAML` must match `volumeMounts[].mountPath` plus the projected file name

For a complete working example, see
[`mcpregistry-configyaml-git-auth.yaml`](../../examples/operator/mcp-registries/mcpregistry-configyaml-git-auth.yaml).

### API Source

Sync from another registry server speaking the upstream
[MCP registry API](https://github.com/modelcontextprotocol/registry/blob/main/docs/reference/api/generic-registry-api.md):

```yaml
spec:
  configYAML: |
    sources:
      - name: upstream
        api:
          endpoint: http://upstream-registry.default.svc.cluster.local:8080
        syncPolicy:
          interval: 30m
    registries:
      - name: default
        sources: ["upstream"]
    database: { host: postgres, port: 5432, user: db_app, database: registry }
    auth: { mode: anonymous }
```

The API source:
- Probes `/v0/info` for registry metadata
- Fetches servers from `/v0/servers`
- Fetches server details from `/v0/servers/{name}`
- Expects entries using the upstream MCP server schema, with ToolHive-specific metadata carried through publisher-provided extensions

**Notes:**
- API endpoints are validated at sync time
- HTTPS is recommended for production use
- Authentication support is planned for a future release

For a complete working example, see
[`mcpregistry-configyaml-api.yaml`](../../examples/operator/mcp-registries/mcpregistry-configyaml-api.yaml).

### PostgreSQL Authentication

The registry server connects to PostgreSQL using a pgpass file. Because
libpq requires `chmod 0600` and Kubernetes Secret volumes mount files as
root-owned (unreadable by the non-root registry container), the operator
exposes a dedicated `pgpassSecretRef` field that wires up an init
container, emptyDir, and `chmod` automatically:

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: my-registry-pgpass
  namespace: toolhive-system
type: Opaque
stringData:
  .pgpass: |
    postgres:5432:registry:db_app:myapppassword
    postgres:5432:registry:db_migrator:mymigrationpassword
---
apiVersion: toolhive.stacklok.dev/v1beta1
kind: MCPRegistry
metadata:
  name: pgpass-registry
  namespace: toolhive-system
spec:
  configYAML: |
    sources:
      - name: k8s
        kubernetes: {}
    registries:
      - name: default
        sources: ["k8s"]
    database:
      host: postgres
      port: 5432
      user: db_app
      migrationUser: db_migrator
      database: registry
      sslMode: require
    auth: { mode: anonymous }
  pgpassSecretRef:
    name: my-registry-pgpass
    key: .pgpass
```

The operator handles the init container, emptyDir, `chmod 0600`, and the
`PGPASSFILE` environment variable invisibly. See
[`mcpregistry-configyaml-pgpass.yaml`](../../examples/operator/mcp-registries/mcpregistry-configyaml-pgpass.yaml).

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

Each source can define its own `filter` block inside `configYAML`. Filters
are applied per-source, so different sources in the same MCPRegistry can
have different rules:

```yaml
spec:
  configYAML: |
    sources:
      - name: production
        file:
          path: /config/registry/production/registry.json
        filter:
          names:
            include: ["prod-*"]
            exclude: ["*-legacy"]
          tags:
            include: ["production"]
            exclude: ["experimental", "deprecated"]
    registries:
      - name: default
        sources: ["production"]
    database: { host: postgres, port: 5432, user: db_app, database: registry }
    auth: { mode: anonymous }
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

Credentials referenced from `configYAML` (Git tokens, OAuth client
secrets, TLS keys, pgpass files) must come from Kubernetes Secrets that
you mount via the CRD's `volumes`/`volumeMounts` fields. Do **not** inline
credentials in `configYAML` itself — the operator stores `configYAML` in
a ConfigMap, not a Secret.

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

Complete, runnable examples live in
[`examples/operator/mcp-registries/`](../../examples/operator/mcp-registries/):

| File | What it demonstrates |
|------|----------------------|
| [`mcpregistry-configyaml-minimal.yaml`](../../examples/operator/mcp-registries/mcpregistry-configyaml-minimal.yaml) | Smallest possible MCPRegistry, using a Kubernetes source |
| [`mcpregistry-configyaml-configmap.yaml`](../../examples/operator/mcp-registries/mcpregistry-configyaml-configmap.yaml) | ConfigMap-backed registry data via a `file:` source plus volume/volumeMount |
| [`mcpregistry-configyaml-git-auth.yaml`](../../examples/operator/mcp-registries/mcpregistry-configyaml-git-auth.yaml) | Private Git repository with credentials mounted from a Secret |
| [`mcpregistry-configyaml-api.yaml`](../../examples/operator/mcp-registries/mcpregistry-configyaml-api.yaml) | API source pulling from another upstream registry server |
| [`mcpregistry-configyaml-oauth.yaml`](../../examples/operator/mcp-registries/mcpregistry-configyaml-oauth.yaml) | OAuth-protected registry API |
| [`mcpregistry-configyaml-pgpass.yaml`](../../examples/operator/mcp-registries/mcpregistry-configyaml-pgpass.yaml) | PostgreSQL `.pgpass` plumbing via `pgpassSecretRef` |

### Multiple sources

Aggregate several sources into a single registry view by listing them in
`configYAML.registries[].sources`:

```yaml
spec:
  configYAML: |
    sources:
      - name: production
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
        file:
          path: /config/registry/development/registry.json
        filter:
          tags:
            include: ["development"]
    registries:
      - name: default
        sources: ["production", "development"]
    database: { host: postgres, port: 5432, user: db_app, database: registry }
    auth: { mode: anonymous }
  # ... volumes/volumeMounts for the development ConfigMap omitted for brevity
```

Each source must have a unique `name` within the MCPRegistry. Registry
views reference sources by name.

## See Also

- [MCPServer Documentation](README.md#usage)
- [Operator Installation](../../docs/kind/deploying-toolhive-operator.md)
- [Registry Examples](../../examples/operator/mcp-registries/)
- [Private Git Registry Example](../../examples/operator/mcp-registries/mcpregistry-configyaml-git-auth.yaml)
- [Registry Schema](../../docs/registry/schema.md)

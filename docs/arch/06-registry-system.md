# Registry System

The registry system is one of ToolHive's key innovations - providing a curated catalog of trusted MCP servers with metadata, configuration, and provenance information. This document explains how registries work, how to use them, and how to host your own.

## Overview

ToolHive was early to adopt the concept of an MCP server registry. The registry provides:

- **Curated catalog** of trusted MCP servers
- **Metadata** including tools, permissions, and configuration
- **Provenance** information for supply chain security
- **Easy deployment** - just reference by name
- **Custom registries** for organizations

## Registry Architecture

```mermaid
graph TB
    subgraph "Registry Sources"
        Builtin[Built-in Registry<br/>Embedded JSON]
        Git[Git Repository]
        CM[ConfigMap]
    end

    subgraph "ToolHive CLI"
        CLI[thv CLI]
        Provider[Provider Interface<br/>Local/Remote]
    end

    subgraph "Kubernetes"
        MCPReg[MCPRegistry CRD]
        Operator[thv-operator]
        API[Registry API Service<br/>Optional]
    end

    Builtin --> Provider
    Git --> MCPReg
    CM --> MCPReg
    Provider --> CLI

    MCPReg --> Operator
    Operator --> API

    style Builtin fill:#81c784
    style Git fill:#90caf9
    style CM fill:#90caf9
```

## Built-in Registry

ToolHive ships with a curated registry from [toolhive-registry](https://github.com/stacklok/toolhive-registry).

**Features:**
- Maintained by Stacklok
- Trusted and verified servers
- Provenance information
- Regular updates

**Browse registry:**
```bash
thv registry list
thv search <query>
```

**Run from registry:**
```bash
thv run server-name
```

**Implementation:**
- Embedded: `pkg/registry/data/registry.json`
- Manager: `pkg/registry/provider.go`, `pkg/registry/provider_local.go`, `pkg/registry/provider_remote.go`
- Update: `cmd/regup/` (registry updater tool)

## Registry Format

### Top-Level Structure

**Implementation**: `pkg/registry/types.go`

```json
{
  "version": "1.0.0",
  "last_updated": "2025-10-13T12:00:00Z",
  "servers": {
    "server-name": { /* ImageMetadata */ }
  },
  "remote_servers": {
    "remote-name": { /* RemoteServerMetadata */ }
  },
  "groups": [
    { /* Group */ }
  ]
}
```

### Server Entry (Container-based)

**Implementation**: `pkg/registry/types.go`

```json
{
  "name": "weather-server",
  "description": "Provides weather information for locations",
  "tier": "Official",
  "status": "active",
  "image": "ghcr.io/stacklok/mcp-weather:v1.0.0",
  "transport": "sse",
  "target_port": 3000,
  "tools": ["get-weather", "get-forecast"],
  "permissions": {
    "network": {
      "outbound": {
        "allow_host": ["api.weather.gov"],
        "allow_port": [443]
      }
    }
  },
  "env_vars": [
    {
      "name": "API_KEY",
      "description": "Weather API key",
      "required": true,
      "secret": true
    }
  ],
  "args": ["--port", "3000"],
  "docker_tags": ["v1.0.0", "latest"],
  "metadata": {
    "stars": 150,
    "pulls": 5000,
    "last_updated": "2025-10-01T10:00:00Z"
  },
  "repository_url": "https://github.com/example/weather-mcp",
  "tags": ["weather", "api", "official"],
  "provenance": {
    "sigstore_url": "https://rekor.sigstore.dev",
    "repository_uri": "https://github.com/example/weather-mcp",
    "signer_identity": "build@example.com"
  }
}
```

### Remote Server Entry

**Implementation**: `pkg/registry/types.go`

```json
{
  "name": "cloud-mcp-server",
  "description": "Cloud-hosted MCP server",
  "tier": "Partner",
  "status": "active",
  "url": "https://mcp.example.com/sse",
  "transport": "sse",
  "tools": ["data-analysis", "ml-inference"],
  "headers": [
    {
      "name": "X-API-Key",
      "description": "API key for authentication",
      "required": true,
      "secret": true
    }
  ],
  "env_vars": [
    {
      "name": "REGION",
      "description": "Cloud region",
      "required": false,
      "default": "us-east-1"
    }
  ],
  "metadata": {
    "stars": 200,
    "last_updated": "2025-10-10T15:00:00Z"
  },
  "repository_url": "https://github.com/example/cloud-mcp",
  "tags": ["cloud", "ml", "partner"]
}
```

### Group Entry

**Implementation**: `pkg/registry/types.go`

```json
{
  "name": "data-pipeline",
  "description": "Data processing pipeline tools",
  "servers": {
    "data-ingestion": { /* ImageMetadata */ },
    "data-transform": { /* ImageMetadata */ }
  },
  "remote_servers": {
    "data-storage": { /* RemoteServerMetadata */ }
  }
}
```

## Using the Registry

### Discovery

**List all servers:**
```bash
thv registry list
```

**Search by keyword:**
```bash
thv search weather
```

**Show server details:**
```bash
thv registry info weather-server
```

**Implementation**: `cmd/thv/app/registry.go`, `cmd/thv/app/search.go`

### Running from Registry

**Simple run:**
```bash
thv run weather-server
```

**What happens:**
1. Look up `weather-server` in registry
2. Get image, transport, permissions from metadata
3. Prompt for required env vars
4. Create RunConfig with registry defaults
5. Deploy workload

**With overrides:**
```bash
thv run weather-server \
  --env API_KEY=xyz \
  --proxy-port 9000 \
  --permission-profile custom.json
```

User overrides take precedence over registry defaults.

**Implementation**: `cmd/thv/app/run.go`

### Environment Variables from Registry

**Registry defines requirements:**
```json
{
  "env_vars": [
    {
      "name": "API_KEY",
      "description": "Weather API key from weather.gov",
      "required": true,
      "secret": true
    },
    {
      "name": "CACHE_TTL",
      "description": "Cache TTL in seconds",
      "required": false,
      "default": "3600"
    }
  ]
}
```

**ToolHive handles:**
- Prompts for required variables if not provided
- Uses defaults for optional variables
- Stores secrets securely
- Adds to RunConfig

**Implementation**: `pkg/registry/types.go`

## Custom Registries

Organizations can provide their own registries.

### File-Based Registry

**Create registry JSON:**
```json
{
  "version": "1.0.0",
  "servers": {
    "internal-tool": {
      "name": "internal-tool",
      "image": "registry.company.com/mcp/internal-tool:latest",
      "transport": "stdio",
      "permissions": { "network": { "outbound": { "insecure_allow_all": true }}}
    }
  }
}
```

**Add to ToolHive:**

Custom registries can be configured in the ToolHive configuration file.

**Configuration location:**
- Linux: `~/.config/toolhive/config.yaml`
- macOS: `~/Library/Application Support/toolhive/config.yaml`

**Implementation**: `pkg/config/`

### Remote Registry

Remote registries can be configured in the ToolHive configuration file to fetch registry data from external sources.

**ToolHive fetches:**
- On startup
- Caches locally

**Authentication:**
- Basic auth: `https://user:pass@registry.company.com/registry.json`
- Bearer token: via environment variable

**Implementation**: `pkg/registry/registry.go`

### Registry Priority

When multiple registries configured:

1. **Custom registries** (in order added)
2. **Built-in registry**

First match wins. Use registry namespacing to avoid conflicts:
```bash
thv run custom-registry/server-name
```

**Implementation**: `pkg/registry/registry.go`

## Registry API Server

> **Note**: The registry API server is being moved to a separate project and will be maintained independently.

ToolHive includes a registry API server (`thv-registry-api`) for hosting custom MCP server registries.

**Key capabilities:**
- HTTP API for serving registry data
- File or Kubernetes ConfigMap storage
- Used by `MCPRegistry` CRD in Kubernetes deployments

The registry API server provides HTTP endpoints for serving registry data to other components.

## MCPRegistry CRD (Kubernetes)

For Kubernetes deployments, registries managed via `MCPRegistry` CRD.

**Implementation**: `cmd/thv-operator/api/v1alpha1/mcpregistry_types.go`

### Example CRD

```yaml
apiVersion: toolhive.stacklok.dev/v1alpha1
kind: MCPRegistry
metadata:
  name: company-registry
spec:
  source:
    type: git
    git:
      repository: https://github.com/company/mcp-registry
      branch: main
      path: registry.json
  syncPolicy:
    interval: 1h
```

### Source Types

#### Git Source

```yaml
source:
  type: git
  git:
    repository: https://github.com/example/registry
    branch: main
    path: registry.json
```

**Features:**
- Automatic sync from Git repository
- Branch or tag tracking
- Shallow clones for efficiency

**Implementation**: `cmd/thv-operator/pkg/sources/git.go`

#### ConfigMap Source

```yaml
source:
  type: configmap
  configmap:
    name: mcp-registry-data
    namespace: default
    key: registry.json
```

**Features:**
- Native Kubernetes resource
- Direct updates via kubectl
- No external dependencies

**Implementation**: `cmd/thv-operator/pkg/sources/configmap.go`

### Sync Policy

**Automatic sync:**
```yaml
syncPolicy:
  interval: 1h
```

**Manual sync only:**

Omit the `syncPolicy` field entirely. Manual sync can be triggered:

```bash
kubectl annotate mcpregistry company-registry \
  toolhive.stacklok.dev/sync-trigger=true
```

**Implementation**: `cmd/thv-operator/controllers/mcpregistry_controller.go`

### API Service

When `apiService.enabled: true`, operator creates:

1. **Deployment**: Running `thv-registry-api`
2. **Service**: Exposing API endpoints
3. **ConfigMap**: Containing registry data

**Access:**
```bash
# Within cluster
curl http://company-registry-api.default.svc.cluster.local:8080/api/v1/registry

# Via port-forward
kubectl port-forward svc/company-registry-api 8080:8080
curl http://localhost:8080/api/v1/registry
```

**Implementation**: `cmd/thv-operator/pkg/registryapi/service.go`

### Status Management

**Status fields:**
```yaml
status:
  phase: Ready
  lastSyncTime: "2025-10-13T12:00:00Z"
  syncStatus:
    phase: Synced
    message: "Successfully synced registry"
    lastSyncHash: "abc123def456"
  apiServiceStatus:
    ready: true
    endpoint: "http://company-registry-api.default.svc.cluster.local:8080"
```

**Phases:**
- `Pending` - Initial state
- `Syncing` - Fetching registry data
- `Ready` - Registry available
- `Failed` - Sync failed
- `Terminating` - Registry being deleted

**Implementation**: `cmd/thv-operator/pkg/mcpregistrystatus/`

### Storage

Registry data stored in ConfigMap:

**Format:**
```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: company-registry-storage
  ownerReferences:
  - apiVersion: mcp.stacklok.com/v1alpha1
    kind: MCPRegistry
    name: company-registry
data:
  registry.json: |
    { ... }
  sync_metadata.json: |
    {
      "lastSyncTime": "2025-10-13T12:00:00Z",
      "hash": "abc123"
    }
```

**Owner references** ensure automatic cleanup when MCPRegistry deleted.

**Implementation**: `cmd/thv-operator/pkg/sources/storage_manager.go`

## Registry Schema

### ImageMetadata (Container Servers)

**Required fields:**
- `image` - Container image reference
- `description` - What the server does
- `transport` - Communication protocol
- `tier` - Classification (Official, Partner, Community)

**Optional fields:**
- `target_port` - Port for SSE/Streamable HTTP
- `permissions` - Permission profile
- `env_vars` - Environment variable definitions
- `args` - Default command arguments
- `docker_tags` - Available tags
- `provenance` - Supply chain metadata
- `tools` - List of tool names
- `metadata` - Stars, pulls, last updated
- `repository_url` - Source code URL
- `tags` - Categorization labels

**Implementation**: `pkg/registry/types.go`

### RemoteServerMetadata (Remote Servers)

**Required fields:**
- `url` - Remote server endpoint
- `description` - What the server does
- `transport` - Must be `sse` or `streamable-http`
- `tier` - Classification

**Optional fields:**
- `headers` - HTTP headers for authentication
- `oauth_config` - OAuth/OIDC configuration
- `env_vars` - Client environment variables
- `tools` - List of tool names
- `metadata` - Popularity metrics
- `repository_url` - Documentation URL
- `tags` - Categorization labels

**Implementation**: `pkg/registry/types.go`

### Group

**Structure:**
```json
{
  "name": "data-pipeline",
  "description": "Complete data processing pipeline",
  "servers": {
    "data-reader": { /* ImageMetadata */ },
    "data-processor": { /* ImageMetadata */ }
  },
  "remote_servers": {
    "data-warehouse": { /* RemoteServerMetadata */ }
  }
}
```

**Use cases:**
- Deploy related servers together
- Virtual MCP aggregation
- Organizational structure

**Run all servers in group:**
```bash
thv group run data-pipeline  # assuming 'data-pipeline' is defined in your registry
```

**Implementation**: `pkg/registry/types.go`

## Provenance and Security

### Image Provenance

ToolHive supports Sigstore verification:

**Provenance fields:**
- `sigstore_url` - Sigstore/Rekor instance
- `repository_uri` - Source repository
- `repository_ref` - Git ref (tag, commit)
- `signer_identity` - Who built the image
- `runner_environment` - Build environment
- `cert_issuer` - Certificate authority
- `attestation` - SLSA attestation data

**Verification:**
```bash
thv run weather-server --image-verification enabled
```

**Implementation**: `pkg/registry/types.go`, cosign integration planned

### Supply Chain Security

**Best practices:**
1. **Pin image tags**: Use specific versions, not `latest`
2. **Verify provenance**: Check signer identity
3. **Review permissions**: Audit network/file access
4. **Check repository**: Review source code
5. **Monitor updates**: Track registry updates

## Future: Upstream MCP Registry Format

ToolHive will support the upstream [MCP registry format](https://github.com/modelcontextprotocol/registry):

**Migration plan:**
1. **Read both formats**: ToolHive format + upstream format
2. **Deprecation period**: Support both formats

**Timeline**: Planned for future release

**Implementation**: `pkg/registry/` (converter to be added)

## Registry Operations

### CLI Operations

**List servers:**
```bash
thv registry list
```

**Show server info:**
```bash
thv registry info <server-name>
```

**Implementation**: `cmd/thv/app/registry.go`

### Kubernetes Operations

**Create registry:**
```bash
kubectl apply -f mcpregistry.yaml
```

**Check status:**
```bash
kubectl get mcpregistry company-registry -o yaml
```

**Trigger manual sync:**
```bash
kubectl annotate mcpregistry company-registry toolhive.stacklok.dev/sync-trigger=true
```

**View registry data:**
```bash
kubectl get configmap company-registry-storage -o jsonpath='{.data.registry\.json}' | jq
```

**Implementation**: `cmd/thv-operator/controllers/mcpregistry_controller.go`

## Related Documentation

- [Core Concepts](02-core-concepts.md) - Registry concept
- [Architecture Overview](00-overview.md) - Registry in platform
- [Deployment Modes](01-deployment-modes.md) - Registry usage per mode
- [Groups](07-groups.md) - Groups in registry
- [Operator Architecture](09-operator-architecture.md) - MCPRegistry CRD

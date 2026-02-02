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
        ExtAPI[External Registry API<br/>ToolHive Registry Server<br/>or MCP Registry]
    end

    subgraph "ToolHive CLI"
        CLI[thv CLI]
        Provider[Provider Interface<br/>Local/Remote/API]
    end

    subgraph "Kubernetes"
        MCPReg[MCPRegistry CRD]
        Operator[thv-operator]
        IntAPI[Internal Registry API<br/>Optional per-CRD]
    end

    Builtin --> Provider
    ExtAPI --> Provider
    Git --> MCPReg
    CM --> MCPReg
    Provider --> CLI

    MCPReg --> Operator
    Operator --> IntAPI

    style Builtin fill:#81c784
    style Git fill:#90caf9
    style CM fill:#90caf9
    style ExtAPI fill:#ce93d8
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
    "signer_identity": "build@example.com",
    "runner_environment": "github-actions",
    "cert_issuer": "https://token.actions.githubusercontent.com"
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

**Implementation**: `pkg/registry/provider.go`, `pkg/registry/provider_local.go`, `pkg/registry/provider_remote.go`, `pkg/registry/factory.go`

### API Registry Provider

ToolHive supports live MCP Registry API endpoints that implement the official [MCP Registry API v0.1 specification](https://registry.modelcontextprotocol.io/docs). This enables on-demand querying of servers from dynamic registry APIs.

**Key differences from Remote Registry:**
- **On-demand queries**: Fetches servers as needed, not bulk download
- **Live data**: Always queries the latest data from the API
- **Standard protocol**: Uses official MCP Registry API specification
- **Pagination support**: Handles large registries via cursor-based pagination
- **Search capabilities**: Supports server search via API queries

**Set API registry:**
```bash
# URLs without .json extension are probed - if they implement /v0.1/servers, they're treated as API endpoints
thv config set-registry https://registry.example.com
```

**With private IP support:**
```bash
thv config set-registry https://registry.internal.company.com --allow-private-ip
```

**Check current registry:**
```bash
thv config get-registry
# Output: Current registry: https://registry.example.com (API endpoint)
```

**Unset API registry:**
```bash
thv config unset-registry
```

**API Requirements:**

The API endpoint must implement:
- `GET /v0.1/servers` - List all servers with pagination
- `GET /v0.1/servers/:name` - Get specific server by reverse-DNS name
- `GET /v0.1/servers?search=<query>` - Search servers
- `GET /openapi.yaml` - OpenAPI specification (version 1.0.0)

**Response format:**

Servers are returned in the upstream [MCP Registry format](https://github.com/modelcontextprotocol/registry):

```json
{
  "server": {
    "name": "io.github.example/weather",
    "description": "Weather information MCP server",
    "packages": [
      {
        "registry_type": "oci",
        "identifier": "ghcr.io/example/weather-mcp:v1.0.0",
        "version": "v1.0.0"
      }
    ],
    "remotes": [],
    "repository": {
      "type": "git",
      "url": "https://github.com/example/weather-mcp"
    }
  }
}
```

**Type conversion:**

ToolHive automatically converts upstream MCP Registry types to internal format:

- **Container servers**: `packages` with `registry_type: "oci"` → `ImageMetadata`
- **Remote servers**: `remotes` with SSE/HTTP transport → `RemoteServerMetadata`
- **Package formats**:
  - `oci`/`docker` → Docker image reference
  - `npm` → `npx://<package>@<version>`
  - `pypi` → `uvx://<package>@<version>`

**Implementation**:
- `pkg/registry/api/client.go` - MCP Registry API client
- `pkg/registry/provider_api.go` - API provider implementation with type conversion
- `pkg/config/registry.go` - Configuration methods (`setRegistryAPI`)
- `pkg/registry/factory.go` - Provider factory with API support
- `cmd/thv/app/config.go` - CLI commands

**Use cases:**
- Connect to official MCP Registry at https://registry.modelcontextprotocol.io
- Point to organization's private MCP Registry API
- Use third-party registry services
- Dynamic server catalogs that update frequently

**Stacklok's Registry Server Implementation:**

For organizations needing a full-featured registry server, [ToolHive Registry Server](https://github.com/stacklok/toolhive-registry-server) provides enterprise features:

- Multiple data sources (Git, API, File, Managed, Kubernetes)
- PostgreSQL backend for scalable storage
- Enterprise OAuth 2.0/OIDC authentication (Okta, Auth0, Azure AD)
- Background synchronization with automatic updates
- Docker Compose and Kubernetes/Helm deployment options

For detailed setup and configuration, see the [Registry Server documentation](https://docs.stacklok.com/toolhive/guides-registry/).

### Registry Priority

When multiple registries configured, ToolHive uses this priority order:

1. **API Registry** (if configured) - Highest priority for live data
2. **Remote Registry** (if configured) - Static remote registry URL
3. **Local Registry** (if configured) - Custom local file
4. **Built-in Registry** - Default embedded registry

The factory selects the first configured registry type in this order. The `thv config set-registry` command auto-detects the registry type:

```bash
# API registry - URLs without .json are probed for /v0.1/servers endpoint
thv config set-registry https://registry.modelcontextprotocol.io

# Remote static registry - URLs ending in .json are treated as static files
thv config set-registry https://example.com/registry.json

# Local file registry
thv config set-registry /path/to/registry.json

# Check current registry configuration
thv config get-registry

# Remove custom registry (fall back to built-in)
thv config unset-registry
```

**Implementation**: `pkg/registry/factory.go`, `pkg/registry/provider.go`, `pkg/registry/provider_local.go`, `pkg/registry/provider_remote.go`, `pkg/registry/provider_api.go`

## Enterprise Registry Deployment

For organizations requiring a centralized, scalable registry server, [ToolHive Registry Server](https://github.com/stacklok/toolhive-registry-server) provides enterprise-grade capabilities.

### When to Use ToolHive Registry Server

| Scenario | Recommended Solution |
|----------|---------------------|
| Single user, local development | Built-in embedded registry (default) |
| Team sharing curated servers | Static JSON file via `thv config set-registry https://example.com/registry.json` |
| Dynamic organization-wide registry | Standalone ToolHive Registry Server with `thv config set-registry https://registry.company.com` |
| Kubernetes cluster with shared registry | MCPRegistry CRD (deploys ToolHive Registry Server in-cluster) |
| Multi-cluster enterprise | Standalone ToolHive Registry Server as central API, connect via `thv config set-registry` |

### Architecture Overview

ToolHive Registry Server implements a 4-layer architecture:

1. **API Layer**: Chi router with OAuth/OIDC middleware
2. **Service Layer**: PostgreSQL or in-memory backends
3. **Registry Layer**: Git, API, File, Managed, Kubernetes registry handlers
4. **Sync Layer**: Background coordinator for automatic updates

### Registry Types

| Type | Sync Mode | Description |
|------|-----------|-------------|
| API | Automatic | Upstream MCP Registry API endpoints |
| Git | Automatic | Git repositories containing registry JSON |
| File | Automatic | Local filesystem (ToolHive or upstream format) |
| Managed | On-demand | API-managed registries with publish/delete |
| Kubernetes | On-demand | K8s deployment discovery |

### Connecting ToolHive to Registry Server

**CLI configuration:**
```bash
# Point CLI to your registry server
thv config set-registry https://registry.company.com

# For internal deployments
thv config set-registry https://registry.internal.company.com --allow-private-ip
```

### Documentation Resources

For complete registry server documentation, see:

- [Registry Server Guides](https://docs.stacklok.com/toolhive/guides-registry/) - Configuration, authentication, deployment
- [Registry API Reference](https://docs.stacklok.com/toolhive/reference/registry-api) - API endpoint documentation
- [ToolHive Registry Schema](https://docs.stacklok.com/toolhive/reference/registry-schema-toolhive) - Registry format reference


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
  configMapRef:
    name: mcp-registry-data
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

1. **Deployment**: Running [ToolHive Registry Server](https://github.com/stacklok/toolhive-registry-server) (image: `ghcr.io/stacklok/thv-registry-api`)
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
  syncStatus:
    phase: Complete
    message: "Successfully synced registry"
    lastSyncTime: "2025-10-13T12:00:00Z"
    lastSyncHash: "abc123def456"
  apiStatus:
    phase: Ready
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

**Implementation**:
- `pkg/registry/types.go` - Provenance type definitions
- `pkg/container/verifier/` - Sigstore/cosign verification using sigstore-go library
- `pkg/runner/retriever/retriever.go` - Image verification orchestration

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

### Internal Documentation
- [Core Concepts](02-core-concepts.md) - Registry concept
- [Architecture Overview](00-overview.md) - Registry in platform
- [Deployment Modes](01-deployment-modes.md) - Registry usage per mode
- [Groups](07-groups.md) - Groups in registry
- [Operator Architecture](09-operator-architecture.md) - MCPRegistry CRD

### External Documentation
- [ToolHive User Documentation](https://docs.stacklok.com/toolhive/) - User-facing guides
- [Registry Server Documentation](https://docs.stacklok.com/toolhive/guides-registry/) - Enterprise registry server
- [ToolHive Registry Schema](https://docs.stacklok.com/toolhive/reference/registry-schema-toolhive) - Registry format reference
- [Upstream Registry Schema](https://docs.stacklok.com/toolhive/reference/registry-schema-upstream) - MCP standard format
- [Registry API Reference](https://docs.stacklok.com/toolhive/reference/registry-api) - API specification

### Related Repositories
- [ToolHive Registry Server](https://github.com/stacklok/toolhive-registry-server) - Registry server component
- [toolhive-registry](https://github.com/stacklok/toolhive-registry) - Curated server catalog
- [MCP Registry](https://github.com/modelcontextprotocol/registry) - Upstream MCP registry specification

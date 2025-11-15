# CRD Specifications for Kubernetes Registry

This document provides detailed field reference documentation for the Kubernetes Registry CRDs.

## MCPRegistry CRD

The `MCPRegistry` CRD represents a registry source and its synchronization configuration.

### MCPRegistry Spec

| Field | Description | Required | Default | Values |
|-------|-------------|----------|---------|---------|
| `displayName` | Human-readable name for the registry | No | - | Any string |
| `description` | Additional context about the registry's purpose | No | - | Any string |
| `format` | Format of the registry data | No | - | `toolhive`, `upstream` |
| `source` | Defines where to fetch registry data from | Yes | - | See Source Configuration |
| `syncPolicy` | How and when to synchronize with the source | No | - | See Sync Policy |
| `filter` | Which servers to include/exclude from the registry | No | - | See Filter Configuration |

### Source Configuration

| Field | Description | Required | Default | Values |
|-------|-------------|----------|---------|---------|
| `type` | Source type | Yes | - | `configmap`, `url`, `git`, `registry` |
| `configMap` | ConfigMap source configuration | No | - | See ConfigMap Source |
| `url` | URL source configuration | No | - | See URL Source |
| `git` | Git source configuration | No | - | See Git Source |
| `registry` | Registry source configuration | No | - | See Registry Source |

### ConfigMap Source

| Field | Description | Required | Default | Values |
|-------|-------------|----------|---------|---------|
| `name` | Name of the ConfigMap | Yes | - | Any valid ConfigMap name |
| `key` | Key containing registry data | No | `registry.json` | Any key in ConfigMap |

### URL Source

| Field | Description | Required | Default | Values |
|-------|-------------|----------|---------|---------|
| `url` | HTTP(S) URL to fetch registry data | Yes | - | Valid HTTP/HTTPS URL |
| `headers` | Optional HTTP headers | No | - | Key-value map |
| `authSecret` | Secret containing auth credentials | No | - | See Auth Secret Reference |

### Git Source

| Field | Description | Required | Default | Values |
|-------|-------------|----------|---------|---------|
| `repository` | Git repository URL | Yes | - | Valid Git repository URL |
| `branch` | Git branch to use | No | `main` | Any valid branch name |
| `path` | Path to registry file in repo | No | `registry.json` | Any file path |
| `authSecret` | Secret containing Git auth credentials | No | - | See Auth Secret Reference |

### Registry Source

| Field | Description | Required | Default | Values |
|-------|-------------|----------|---------|---------|
| `url` | Registry API endpoint URL | Yes | - | Valid HTTP/HTTPS URL to registry API |
| `headers` | Optional HTTP headers | No | - | Key-value map |
| `authSecret` | Secret containing auth credentials | No | - | See Auth Secret Reference |

### Sync Policy

| Field | Description | Required | Default | Values |
|-------|-------------|----------|---------|---------|
| `enabled` | Enable automatic synchronization | No | `true` | `true`, `false` |
| `interval` | How often to synchronize | No | `1h` | Duration (e.g., `30m`, `2h`) |
| `onUpdate` | Behavior when registry content changes | No | `update` | `create`, `update`, `recreate` |
| `retry` | Retry behavior for failed syncs | No | - | See Retry Policy |

### Retry Policy

| Field | Description | Required | Default | Values |
|-------|-------------|----------|---------|---------|
| `maxRetries` | Maximum number of retry attempts | No | 3 | 0-∞ |
| `backoffInterval` | Initial backoff interval | No | `30s` | Duration (e.g., `10s`, `1m`) |

### Filter Configuration

| Field | Description | Required | Default | Values |
|-------|-------------|----------|---------|---------|
| `include` | Patterns for servers to include | No | - | See Filter Criteria |
| `exclude` | Patterns for servers to exclude | No | - | See Filter Criteria |

### Filter Criteria

| Field | Description | Required | Default | Values |
|-------|-------------|----------|---------|---------|
| `names` | Server name patterns (supports wildcards) | No | - | String array |
| `tags` | Tag patterns to match | No | - | String array |
| `tiers` | Tier levels to match | No | - | String array (e.g., `Official`, `Community`) |
| `transports` | Transport types to match | No | - | String array (e.g., `stdio`, `sse`) |

## Job CRDs

### MCPRegistryImportJob

Declarative import operations for registry data.

**Status Field**: Includes job lifecycle status (`idle`, `scheduled`, `running`, `completed`, `failed`) and operation results. For large result sets (e.g., per-server import status), detailed output can be stored in a dedicated ConfigMap referenced from the status.

### MCPRegistryExportJob  

Declarative export operations for registry data.

**Status Field**: Includes job lifecycle status and export results. Large exported data or detailed operation logs can be stored in a ConfigMap for efficient access and storage management.

### MCPRegistrySyncJob

Declarative synchronization operations for registry sources.

**Status Field**: Includes job lifecycle status and sync results. Per-registry or per-server sync details can be stored in ConfigMaps when result data exceeds reasonable status field limits.

*Note: Detailed specifications for job CRDs will be defined in Phase 3 implementation.*
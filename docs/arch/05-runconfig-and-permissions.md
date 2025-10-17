# RunConfig and Permission Profiles

This document describes ToolHive's configuration format (RunConfig) and security model (Permission Profiles). These are fundamental to understanding how workloads are configured and secured.

## RunConfig Overview

**RunConfig** is ToolHive's standard, portable configuration format for MCP servers. It is:

- **Serializable**: JSON and YAML formats
- **Versioned**: Schema evolution with migration support
- **Portable**: Export from one system, import to another
- **Complete**: Contains everything needed to run a workload
- **Part of API contract**: Format stability guaranteed

**Implementation**: `pkg/runner/config.go`

**Current schema version**: `v0.1.0` (`pkg/runner/config.go`)

## RunConfig Structure

### Core Fields

The complete `RunConfig` struct is defined in `pkg/runner/config.go`.

**Key field categories:**
- **Identity**: `name`, `containerName`, `baseName` - Workload identifiers
- **What to run**: `image` or `remoteURL` - Container image or remote endpoint
- **Transport**: `transport`, `host`, `port`, `targetPort`, `proxyMode` - Communication configuration
- **Execution**: `cmdArgs`, `envVars` - Runtime parameters
- **Security**: `permissionProfile`, `isolateNetwork` - Permission boundaries
- **Middleware**: `oidcConfig`, `authzConfig`, `auditConfig`, `middlewareConfigs` - Request processing
- **Tool filtering**: `toolsFilter`, `toolsOverride` - Tool control
- **Storage**: `volumes`, `secrets` - Data and credentials
- **Grouping**: `group` - Logical organization
- **Platform-specific**: `k8sPodTemplatePatch`, `containerLabels` - Runtime-specific options

### Field Category Details

#### Identity Fields

| Field | Purpose | Example |
|-------|---------|---------|
| `Name` | User-facing workload name | `"my-weather-server"` |
| `ContainerName` | Container/workload identifier | `"thv-my-weather-server-abc123"` |
| `BaseName` | Sanitized base name | `"my-weather-server"` |

**Name sanitization**: Special characters replaced with `-`, reserved words handled

**Implementation**: `pkg/workloads/types/validate.go`

#### What to Run

**Container-based workload:**
```json
{
  "image": "ghcr.io/example/mcp-server:latest",
  "cmd_args": ["--verbose"]
}
```

**Remote workload:**
```json
{
  "remote_url": "https://mcp.example.com/sse",
  "remote_auth_config": {
    "client_id": "...",
    "issuer": "https://auth.example.com"
  }
}
```

**Implementation**: `pkg/runner/config.go-49`

#### Transport Configuration

**Stdio transport:**
```json
{
  "transport": "stdio",
  "host": "127.0.0.1",
  "port": 8080,
  "proxy_mode": "streamable-http"
}
```

**SSE/Streamable HTTP transport:**
```json
{
  "transport": "sse",
  "host": "127.0.0.1",
  "port": 8080,
  "target_port": 3000,
  "target_host": "127.0.0.1"
}
```

**Fields:**
- `transport`: `stdio`, `sse`, or `streamable-http`
- `host`: Proxy listen address (default: `127.0.0.1`)
- `port`: Proxy listen port (host side)
- `target_port`: Container port (SSE/Streamable only)
- `target_host`: Container host (default: `127.0.0.1`)
- `proxy_mode`: For stdio: `sse` or `streamable-http`

**Implementation**: `pkg/runner/config.go-76`, `139`

#### Environment Variables

**Sources:**
1. Direct specification via configuration
2. Environment files
3. Environment directories
4. Secret references

**Merge order:**
1. User-provided environment variables
2. Environment file variables
3. Environment directory variables
4. Transport-specific variables (overwrites existing) - `MCP_TRANSPORT`, `MCP_PORT`, etc.
5. Secret-derived variables (overwrites existing at runtime)

**Architecture reasoning**: User inputs form the base, transport variables overwrite to ensure correct MCP protocol configuration, and secrets overwrite last to guarantee sensitive values take final precedence.

**Format:**
```json
{
  "env_vars": {
    "API_KEY": "value",
    "LOG_LEVEL": "debug",
    "MCP_TRANSPORT": "sse",
    "MCP_PORT": "3000"
  }
}
```

**Implementation**: `pkg/runner/config.go-88`, `290-303`

#### Volumes

**Format**: `"host-path:container-path[:ro]"`

**Example:**
```json
{
  "volumes": [
    "/home/user/data:/data:ro",
    "/tmp:/tmp"
  ]
}
```

**Relative paths**: Resolved relative to current directory

**Implementation**: `pkg/runner/config.go-95`

#### Secrets

**Format**: `"<secret-name>,target=<ENV_VAR>"`

**Example:**
```json
{
  "secrets": [
    "api-key,target=API_KEY",
    "db-password,target=DB_PASSWORD"
  ]
}
```

**Secret providers:**
- `encrypted`: Encrypted local storage (default)
- `1password`: 1Password SDK integration
- `environment`: Environment variable provider
- `none`: No-op provider (for testing)

**Implementation**: `pkg/runner/config.go`, `307-341`

### Middleware Configuration

**Structure:**
```json
{
  "middleware_configs": [
    {
      "type": "auth",
      "parameters": {
        "oidcConfig": {
          "issuer": "https://accounts.google.com",
          "audience": "my-app"
        }
      }
    },
    {
      "type": "authz",
      "parameters": {
        "policies": "permit(...);"
      }
    }
  ]
}
```

**Middleware types:**
- `auth` - JWT authentication
- `tokenexchange` - OAuth token exchange
- `tool-filter` - Filter tool lists
- `tool-call-filter` - Filter tool calls
- `mcp-parser` - Parse JSON-RPC (always present)
- `telemetry` - OpenTelemetry
- `authz` - Cedar authorization
- `audit` - Request logging

**Implementation**: `pkg/runner/config.go-161`, `pkg/transport/types/transport.go-39`

### Tool Filtering

**Filter specific tools:**
```json
{
  "tools_filter": ["web-search", "calculator"]
}
```

**Override tool names/descriptions:**
```json
{
  "tools_override": {
    "web-search": {
      "name": "google-search",
      "description": "Search Google for information"
    },
    "calculator": {
      "description": "Perform mathematical calculations"
    }
  }
}
```

**Implementation**: `pkg/runner/config.go-154`, `464-472`

## RunConfig Lifecycle

### Creation

**From command line:**
```bash
thv run ghcr.io/example/mcp-server:latest \
  --transport sse \
  --port 8080 \
  --permission-profile network \
  --env API_KEY=value
```

ToolHive constructs RunConfig internally:

**Implementation**: `cmd/thv/app/run.go`, `pkg/runner/config.go`

### Serialization

**Write to file:**
```go
config.WriteJSON(writer)
```

**Read from file:**
```go
config, err := runner.ReadJSON(reader)
```

**Schema validation:**
- Version field checked
- Unknown fields ignored (forward compatibility)
- Required fields validated

**Implementation**: `pkg/runner/config.go-206`

### State Storage

**Location:**
- Linux: `~/.local/state/toolhive/state/<workload-name>.json`
- macOS: `~/Library/Application Support/toolhive/state/<workload-name>.json`

**Saved automatically:**
- On workload creation
- On configuration update
- Used for restart

**Implementation**: `pkg/runner/config.go`, `pkg/state/`

### Export/Import

RunConfig serialization enables portability across systems and deployment contexts.

**Export architecture:**
- Serializes complete workload configuration to JSON
- Includes all runtime parameters, permissions, middleware
- Excludes secret values (only secret references included)

**Import architecture:**
- Deserializes JSON to RunConfig struct
- Validates schema version compatibility
- Resolves secrets at import time from configured provider

**Use cases:**
- Configuration sharing between environments
- Workload backup and restore
- System migration
- CI/CD automation

**Implementation**: `cmd/thv/app/export.go`, `pkg/runner/config.go`

## Permission Profiles

Permission profiles define security boundaries for MCP servers using a defense-in-depth approach:

1. **Filesystem isolation** - Control read/write access
2. **Network isolation** - Control inbound/outbound connections
3. **Privilege isolation** - Avoid privileged mode

**Implementation**: `pkg/permissions/profile.go`

### Profile Structure

```go
type Profile struct {
    Name       string              `json:"name,omitempty"`
    Read       []MountDeclaration  `json:"read,omitempty"`
    Write      []MountDeclaration  `json:"write,omitempty"`
    Network    *NetworkPermissions `json:"network,omitempty"`
    Privileged bool                `json:"privileged,omitempty"`
}
```

### Filesystem Permissions

#### Mount Declarations

Three formats supported:

1. **Single path**: Same path on host and container
   ```json
   {"read": ["/home/user/data"]}
   ```
   Mounts `/home/user/data` → `/home/user/data` (read-only)

2. **Host:Container**: Different paths
   ```json
   {"read": ["/home/user/data:/data"]}
   ```
   Mounts `/home/user/data` → `/data` (read-only)

3. **Resource URI**: Named resources
   ```json
   {"read": ["volume://my-data:/data"]}
   ```
   Mounts volume `my-data` → `/data` (read-only)

**Windows path handling:**
- Windows paths allowed as host paths (left side of colon)
- Windows paths rejected as container paths (right side of colon)
- Architectural reason: Containers run Linux internally, requiring Linux-style paths
- Example: `C:\Users\name\data:/data` (Windows host → Linux container path)

**Implementation**: `pkg/permissions/profile.go`

#### Read vs Write

**Read mounts:**
- Mounted as read-only
- Container cannot modify files
- Use for configuration, input data

**Write mounts:**
- Mounted as read-write
- Container can create/modify/delete files
- Use for output data, logs, caches

**Example:**
```json
{
  "read": ["/home/user/config:/config"],
  "write": ["/home/user/output:/output"]
}
```

#### Security Considerations

**Path traversal prevention:**
- Mount declarations validated for `..` and null bytes
- Command injection patterns rejected
- Windows paths handled specially

**Implementation**: `pkg/permissions/profile.go-182`

### Network Permissions

#### Outbound Connections

**Allow all (insecure):**
```json
{
  "network": {
    "outbound": {
      "insecure_allow_all": true
    }
  }
}
```

**Whitelist hosts:**
```json
{
  "network": {
    "outbound": {
      "allow_host": ["api.example.com", "*.google.com"]
    }
  }
}
```

**Whitelist ports:**
```json
{
  "network": {
    "outbound": {
      "allow_port": [80, 443, 8080]
    }
  }
}
```

**Combined:**
```json
{
  "network": {
    "outbound": {
      "allow_host": ["api.example.com"],
      "allow_port": [443]
    }
  }
}
```

**Implementation**: `pkg/permissions/profile.go-66`

#### Inbound Connections

**Whitelist sources:**
```json
{
  "network": {
    "inbound": {
      "allow_host": ["192.168.1.0/24", "10.0.0.100"]
    }
  }
}
```

**Note**: Inbound restrictions currently have limited implementation.

**Implementation**: `pkg/permissions/profile.go-72`

#### Network Isolation

When `isolate_network: true` in RunConfig:

1. Container runs in isolated network
2. No internet access by default
3. Egress proxy enforces whitelist
4. Only allowed hosts/ports reachable

**Egress proxy implementation:**
- Transparent CONNECT proxy
- DNS resolution controlled
- TLS connections inspected (optional)

**Implementation**: `pkg/networking/`

### Privileged Mode

**⚠️ Warning**: Privileged mode removes most security isolation!

**When set to `true`:**
- Container has access to all host devices
- Security namespaces disabled
- Equivalent to root on host

**Use cases:**
- Docker-in-Docker scenarios
- Hardware device access
- System-level debugging

**Recommendation**: Avoid unless absolutely necessary!

**Example:**
```json
{
  "privileged": true
}
```

**Implementation**: `pkg/permissions/profile.go-44`

### Built-in Profiles

#### `none` Profile

**Default profile** - No permissions:

```json
{
  "name": "none",
  "read": [],
  "write": [],
  "network": {
    "outbound": {
      "insecure_allow_all": false,
      "allow_host": [],
      "allow_port": []
    }
  },
  "privileged": false
}
```

**Use for**: Maximum security, no external access needed

**Implementation**: `pkg/permissions/profile.go`

#### `network` Profile

**Full network access**:

```json
{
  "name": "network",
  "read": [],
  "write": [],
  "network": {
    "outbound": {
      "insecure_allow_all": true
    }
  },
  "privileged": false
}
```

**Use for**: API calls, web scraping, external services

**Implementation**: `pkg/permissions/profile.go`

### Custom Profiles

Custom permission profiles can be defined in JSON files for reusable security policies.

**Profile structure example:**

```json
{
  "name": "data-processor",
  "read": [
    "/home/user/input:/input"
  ],
  "write": [
    "/home/user/output:/output"
  ],
  "network": {
    "outbound": {
      "allow_host": ["api.example.com"],
      "allow_port": [443]
    }
  },
  "privileged": false
}
```

**Profile resolution**: Profiles can be referenced by name (built-in), file path (custom), or from registry metadata (server-specific defaults).

**Implementation**: `pkg/permissions/profile.go`

### Profile Selection

**Priority order:**
1. Command-line path: `--permission-profile /path/to/profile.json`
2. Built-in name: `--permission-profile network`
3. Registry default: From server metadata
4. Global default: `none`

**Implementation**: `pkg/permissions/`, registry metadata

## Security Best Practices

### Principle of Least Privilege

1. **Start with `none` profile**
2. **Add only required permissions**
3. **Use read-only mounts when possible**
4. **Whitelist specific hosts, not wildcards**
5. **Never use `privileged: true` without careful consideration**

### Permission Auditing

**Architecture approach:**
- RunConfig files provide declarative permission specifications
- Exported configurations can be reviewed before deployment
- Container runtime APIs expose actual applied permissions
- Gap between declared and applied permissions indicates security issues

**Verification points:**
- Permission profile contents in RunConfig
- Actual mount points in running containers
- Network policy enforcement
- Privilege escalation prevention

**Implementation**: `cmd/thv/app/export.go`, container runtime inspection APIs

### Network Isolation

**Architecture pattern:**
1. RunConfig `isolate_network` flag triggers isolated network creation
2. Container placed in custom network with no default egress
3. Egress proxy deployed to enforce permission profile rules
4. DNS resolution controlled by proxy
5. Only whitelisted hosts/ports reachable

**Network policy enforcement:**
```json
{
  "network": {
    "outbound": {
      "allow_host": ["api.example.com"],
      "allow_port": [443]
    }
  }
}
```

**Implementation**: `pkg/networking/`, `pkg/permissions/profile.go`

### Secrets Management

**Architecture principle**: Secrets referenced by name, never embedded in configuration.

**Secret reference pattern:**
- RunConfig contains secret name and target environment variable
- Secret values resolved at runtime from provider
- No plaintext secrets in serialized RunConfig files
- Secret changes don't require RunConfig updates

**Provider architecture:**
- **encrypted**: Password-protected local storage (default)
- **1password**: 1Password SDK integration for enterprise vaults
- **environment**: CI/CD environment variables
- **none**: Testing/development no-op provider

**Implementation**: `pkg/secrets/`, `pkg/runner/config.go`

## Platform-Specific Considerations

### Kubernetes

**Pod security context:**
- RunConfig permission profile → Security context
- Network policies generated from profile
- Volume mounts → PersistentVolumeClaims or HostPath

**Pod template patches:**
```json
{
  "k8s_pod_template_patch": "{\"spec\":{\"nodeSelector\":{\"disktype\":\"ssd\"}}}"
}
```

**Implementation**: Operator converts profiles to K8s resources

### Docker/Podman

**Container security:**
- `--cap-drop ALL` by default
- Specific capabilities added per profile
- `--security-opt no-new-privileges`
- Network isolation via custom networks

**Implementation**: `pkg/container/runtime/docker/`

## Related Documentation

- [Core Concepts](02-core-concepts.md) - RunConfig and Permission Profile concepts
- [Architecture Overview](00-overview.md) - RunConfig as API contract
- [Deployment Modes](01-deployment-modes.md) - RunConfig portability
- [Transport Architecture](03-transport-architecture.md) - Transport configuration
- [Operator Architecture](09-operator-architecture.md) - K8s-specific configuration

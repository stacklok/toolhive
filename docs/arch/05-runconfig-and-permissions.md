# RunConfig and Permission Profiles

This document describes ToolHive's configuration format (RunConfig) and security model (Permission Profiles). These are fundamental to understanding how workloads are configured and secured.

## RunConfig Overview

**RunConfig** is ToolHive's standard, portable configuration format for MCP servers. It is:

- **Serializable**: JSON and YAML formats
- **Versioned**: Schema evolution with migration support
- **Portable**: Export from one system, import to another
- **Complete**: Contains everything needed to run a workload
- **Part of API contract**: Format stability guaranteed

**Implementation**: `pkg/runner/config.go:34`

**Current schema version**: `v0.1.0` (`pkg/runner/config.go:30`)

## RunConfig Structure

### Core Fields

```go
type RunConfig struct {
    // Schema version for format evolution
    SchemaVersion string `json:"schema_version"`

    // What to run (container or remote)
    Image     string `json:"image,omitempty"`
    RemoteURL string `json:"remote_url,omitempty"`

    // Identity
    Name          string `json:"name"`
    ContainerName string `json:"container_name,omitempty"`
    BaseName      string `json:"base_name,omitempty"`

    // Transport configuration
    Transport  types.TransportType `json:"transport"`
    Host       string              `json:"host"`
    Port       int                 `json:"port"`
    TargetPort int                 `json:"target_port,omitempty"`
    TargetHost string              `json:"target_host,omitempty"`
    ProxyMode  types.ProxyMode     `json:"proxy_mode,omitempty"`

    // Execution
    CmdArgs []string          `json:"cmd_args,omitempty"`
    EnvVars map[string]string `json:"env_vars,omitempty"`

    // Security
    PermissionProfile           *permissions.Profile `json:"permission_profile"`
    PermissionProfileNameOrPath string               `json:"permission_profile_name_or_path,omitempty"`
    IsolateNetwork              bool                 `json:"isolate_network,omitempty"`

    // Middleware and policies
    OIDCConfig      *auth.TokenValidatorConfig `json:"oidc_config,omitempty"`
    AuthzConfig     *authz.Config              `json:"authz_config,omitempty"`
    AuditConfig     *audit.Config              `json:"audit_config,omitempty"`
    TelemetryConfig *telemetry.Config          `json:"telemetry_config,omitempty"`
    MiddlewareConfigs []types.MiddlewareConfig `json:"middleware_configs,omitempty"`

    // Tool filtering
    ToolsFilter   []string                 `json:"tools_filter,omitempty"`
    ToolsOverride map[string]ToolOverride  `json:"tools_override,omitempty"`

    // Storage
    Volumes      []string `json:"volumes,omitempty"`
    EnvFileDir   string   `json:"env_file_dir,omitempty"`
    IgnoreConfig *ignore.Config `json:"ignore_config,omitempty"`

    // Secrets
    Secrets []string `json:"secrets,omitempty"`

    // Grouping
    Group string `json:"group,omitempty"`

    // Platform-specific
    K8sPodTemplatePatch string            `json:"k8s_pod_template_patch,omitempty"`
    ContainerLabels     map[string]string `json:"container_labels,omitempty"`

    // Other
    Debug             bool   `json:"debug,omitempty"`
    TrustProxyHeaders bool   `json:"trust_proxy_headers,omitempty"`
    ThvCABundle       string `json:"thv_ca_bundle,omitempty"`
    JWKSAuthTokenFile string `json:"jwks_auth_token_file,omitempty"`
}
```

### Field Categories

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

**Implementation**: `pkg/runner/config.go:42-49`

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

**Implementation**: `pkg/runner/config.go:64-76`, `139`

#### Environment Variables

**Sources:**
1. Direct specification: `--env KEY=value`
2. Environment files: `--env-file path/to/.env`
3. Environment directory: `--env-file-dir path/to/dir`
4. Secrets: `--secret name,target=ENV_VAR`

**Merge order:**
1. Base environment from config
2. Transport-specific vars (`MCP_TRANSPORT`, `MCP_PORT`, etc.)
3. User-provided environment variables
4. Secret-derived environment variables

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

**Implementation**: `pkg/runner/config.go:84-88`, `290-303`

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

**Implementation**: `pkg/runner/config.go:93-95`

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
- `1password`: 1Password CLI integration

**Implementation**: `pkg/runner/config.go:119`, `307-341`

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

**Implementation**: `pkg/runner/config.go:159-161`, `pkg/transport/types/transport.go:31-39`

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

**Implementation**: `pkg/runner/config.go:150-154`, `464-472`

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

**Implementation**: `cmd/thv/app/run.go`, `pkg/runner/config.go:209`

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

**Implementation**: `pkg/runner/config.go:164-206`

### State Storage

**Location:**
- Linux: `~/.local/state/toolhive/state/<workload-name>.json`
- macOS: `~/Library/Application Support/toolhive/state/<workload-name>.json`

**Saved automatically:**
- On workload creation
- On configuration update
- Used for restart

**Implementation**: `pkg/runner/config.go:429`, `pkg/state/`

### Export/Import

**Export:**
```bash
thv export my-server > config.json
```

**Import:**
```bash
thv run --from-config config.json
```

**Use cases:**
- Share configurations
- Backup workloads
- Migration between systems
- CI/CD pipelines

**Implementation**: `cmd/thv/app/export.go`

## Permission Profiles

Permission profiles define security boundaries for MCP servers using a defense-in-depth approach:

1. **Filesystem isolation** - Control read/write access
2. **Network isolation** - Control inbound/outbound connections
3. **Privilege isolation** - Avoid privileged mode

**Implementation**: `pkg/permissions/profile.go:22`

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

**Windows paths supported:**
```json
{"read": ["C:\\Users\\name\\data:C:\\data"]}
```

**Implementation**: `pkg/permissions/profile.go:152`, `404`

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

**Implementation**: `pkg/permissions/profile.go:170-182`

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

**Implementation**: `pkg/permissions/profile.go:56-66`

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

**Implementation**: `pkg/permissions/profile.go:68-72`

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

**Implementation**: `pkg/permissions/profile.go:42-44`

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

**Implementation**: `pkg/permissions/profile.go:112`

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

**Implementation**: `pkg/permissions/profile.go:133`

### Custom Profiles

**Define in JSON file:**

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

**Use profile:**
```bash
thv run my-server --permission-profile /path/to/profile.json
```

**Implementation**: `pkg/permissions/profile.go:94`

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

**Review RunConfig before running:**
```bash
thv export my-server | jq '.permission_profile'
```

**Check actual mounts:**
```bash
docker inspect thv-my-server | jq '.[0].Mounts'
```

### Network Isolation

**Enable network isolation:**
```bash
thv run my-server \
  --isolate-network \
  --permission-profile custom-profile.json
```

**Custom profile with whitelist:**
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

### Secrets Management

**Never hardcode secrets in RunConfig:**
```bash
# ❌ Bad
thv run my-server --env API_KEY=secret123

# ✅ Good
thv run my-server --secret api-key,target=API_KEY
```

**Secret providers:**
- Encrypted storage (password-protected)
- 1Password integration

**Implementation**: `pkg/secrets/`

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

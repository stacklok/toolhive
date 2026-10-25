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
- **Runtime configuration**: `runtimeConfig` - Base image and package customization for protocol schemes
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

**Implementation**: `pkg/runner/config.go`

#### Runtime Configuration

**Purpose**: Customize base images and build packages for protocol scheme workloads (`uvx://`, `npx://`, `go://`)

**When used**: Only applies when using protocol schemes that auto-generate container images

**Structure:**
```json
{
  "runtime_config": {
    "builder_image": "golang:1.24-alpine",
    "additional_packages": ["gcc", "musl-dev"]
  }
}
```

**Fields:**
- `builder_image`: Override the default base image for the builder stage
  - Go: Default `golang:1.27-alpine`
  - Node: Default `node:24-alpine`
  - Python: Default `python:3.14-slim`
- `additional_packages`: Extra packages to install during the build and runtime stages (e.g., build tools, libraries)

**CLI usage:**
```bash
# Override Go version
thv run go://github.com/example/server --runtime-image golang:1.23-alpine

# Add build dependencies
thv run uvx://mcp-server \
  --runtime-image python:3.11-slim \
  --runtime-add-package gcc \
  --runtime-add-package musl-dev
```

**Configuration priority** (highest to lowest):
1. Per-workload override in `RunConfig.RuntimeConfig`
2. User config file (`$XDG_CONFIG_HOME/toolhive/config.yaml` `runtimeConfigs` map)
3. Built-in defaults

**Note**: For Go workloads, only the builder image is configurable. The runtime stage always uses `alpine:3.23` for simplicity and security.

**Implementation**: `pkg/runner/config.go`, `pkg/container/templates/runtime_config.go`

#### Transport Configuration

**Stdio transport:**
```json
{
  "transport": "stdio",
  "host": "127.0.0.1",
  "port": 8080,
  "proxy_mode": "streamable-http",
  "max_request_body_size": 8388608,
  "proxy_read_timeout": "45s"
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
- `max_request_body_size`: Maximum inbound MCP request body size in bytes; omitted or zero uses the 8 MiB default
- `proxy_read_timeout`: Maximum time to read a complete client request, as a Go duration string; omitted or zero uses the secure 30-second default

**Implementation**: `pkg/runner/config.go`

#### Environment Variables

**Sources:**
1. Direct specification via configuration
2. Environment files
3. Environment directories
4. Secret references

**Merge order:**
1. Environment file variables
2. Environment directory variables
3. User-provided environment variables
4. Transport-specific variables (overwrites existing) - `MCP_TRANSPORT`, `MCP_PORT`, etc.
5. Secret-derived variables (overwrites existing at runtime)

**Architecture reasoning**: Environment files and directories form the base layer, user-provided variables overwrite them for explicit control, transport variables overwrite to ensure correct MCP protocol configuration, and secrets overwrite last to guarantee sensitive values take final precedence.

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

**Implementation**: `pkg/runner/config.go`

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

**Implementation**: `pkg/runner/config.go`

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
- `encrypted`: Encrypted local storage
- `1password`: 1Password SDK integration
- `environment`: Environment variable provider

**Note**: There is no automatic default provider. Users must run `thv secret setup` to configure a provider before using secrets functionality.

**Implementation**: `pkg/runner/config.go`

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
      "type": "authorization",
      "parameters": {
        "policies": "permit(...);"
      }
    }
  ]
}
```

**Middleware types** (registered in `GetSupportedMiddlewareFactories` in `pkg/runner/middleware.go`):
- `auth` - JWT authentication
- `tokenexchange` - OAuth 2.0 token exchange (RFC 8693)
- `upstreamswap` - Swap ToolHive JWTs for upstream IdP tokens (embedded auth server)
- `awssts` - AWS STS credential exchange
- `obo` - On-Behalf-Of token flow
- `mcp-parser` - Parse JSON-RPC (always present)
- `tool-filter` - Filter tool lists
- `tool-call-filter` - Filter tool calls
- `ratelimit` - Request rate limiting
- `usagemetrics` - Usage telemetry
- `telemetry` - OpenTelemetry
- `authorization` - Cedar authorization
- `audit` - Request logging
- `recovery` - Panic recovery
- `header-forward` - Forward request headers to upstream (constant: `HeaderForwardMiddlewareName`)
- `validating-webhook` - Validating admission webhook
- `mutating-webhook` - Mutating admission webhook

**Implementation**: `pkg/runner/config.go`, `pkg/transport/types/transport.go`

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

**Implementation**: `pkg/runner/config.go`

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

**Implementation**: `pkg/runner/config.go`

### State Storage

**Location:**
- Linux: `~/.local/state/toolhive/runconfigs/<workload-name>.json`
- macOS: `~/Library/Application Support/toolhive/runconfigs/<workload-name>.json`

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

**Implementation**: `github.com/stacklok/toolhive-core/permissions`

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

`NetworkPermissions` also carries a `Mode` field (`"host"`, `"bridge"`, `"none"`) that selects the container runtime network mode; when empty, the runtime default is used. Network isolation is only enforceable in bridge mode; see [Network isolation requires bridge networking](#network-isolation-requires-bridge-networking).

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

**Implementation**: `github.com/stacklok/toolhive-core/permissions`

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

**Implementation**: `github.com/stacklok/toolhive-core/permissions`

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

**Implementation**: `github.com/stacklok/toolhive-core/permissions`

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

**Implementation**: `github.com/stacklok/toolhive-core/permissions`

#### Network Isolation

When `isolate_network: true` in RunConfig:

1. Container runs in isolated network
2. No internet access by default
3. Egress proxy enforces whitelist
4. Only allowed hosts/ports reachable

**Egress proxy implementation:**
- Standard HTTP/HTTPS forward proxy (Squid)
- Configured via HTTP_PROXY/HTTPS_PROXY environment variables
- DNS resolution controlled via custom DNS container
- ACL-based filtering of hosts and ports

**Implementation**: `pkg/container/docker/squid.go`, `pkg/networking/`

##### Host gateway protection (Docker and Podman)

For isolated bridge workloads, both Squid and Envoy block host access by default,
including when outbound permissions allow all traffic. The shared protected set
contains all daemon bridge gateway IPs and the names `host.docker.internal`,
`gateway.docker.internal`, and `host.containers.internal`. On Podman it also
contains every address mapped to those names in the running DNS sidecar's
`/etc/hosts`; these addresses need not be bridge gateways and are not a fixed
link-local IP or subnet. Squid checks destination IPs before outbound allows;
Envoy applies both authority matching and resolved-address filtering.

Podman discovery reads the running DNS sidecar's `/etc/hosts` through the runtime
archive API before proxy or MCP setup. ToolHive uses `dockurr/dnsmasq:latest` in
bridge mode with the same endpoint map as its proxies, without mounts or extra
hosts entries. Workload mounts do not propagate to sidecars. Runtime-generated
hosts are a prerequisite: ToolHive does not configure image-owned hosts or
disable runtime hosts management. Rootless sidecars use the same runtime host
endpoint; all bridge gateways cover rootful Podman's gateway selection.
Proxy image selection and image entrypoints are unchanged.

`host.containers.internal` must be present with a valid address;
`host.docker.internal` and `gateway.docker.internal` are protected when present. Missing/malformed/oversized hosts data or runtime read failures
fail deployment with an error, **even with `--allow-docker-gateway`**. The archive
read is bounded in size and time, with no host DNS, process-host filesystem, or
hardcoded-address fallback. No extra discovery container is created.

`--allow-docker-gateway` explicitly allows the same hostnames and exact IPs on
**any port**, for Podman as well as Docker. Merely adding a host alias to an
outbound allowlist does not override the default host deny. Previously, Podman
host aliases resolving to non-bridge IPs could escape that protection; they now
require this explicit opt-in.

This policy is a **deployment-time snapshot**, taken after DNS startup (including
any startup network recreation), not continuous runtime discovery. Redeploy
isolated workloads after changing runtime host mappings, rootless network
configuration, or bridge networks. Restarting an existing container alone does
not refresh policy. Docker retains its bridge-discovery path without the hosts
archive read; Kubernetes uses its separate network policy path.

##### Network isolation requires bridge networking

Network isolation is a bridge-topology construct: it builds a per-workload
`internal=true` network plus DNS (dnsmasq) and egress/ingress (Squid) sidecars,
attaching the MCP container to the internal network only. This only works when
the network mode is bridge (`""`, `bridge`, or `default`). For non-bridge modes
the sidecars have no route out and the workload bypasses them entirely, so
isolation is dropped:

| Network mode | `--isolate-network` | Behavior |
| --- | --- | --- |
| bridge/`""`/`default` | any | Isolation enforced as designed |
| `host` (or other non-bridge) | defaulted | Isolation dropped, `WARN` logged (dropping isolation reduces confinement) |
| `host` (or other non-bridge) | explicitly `true` | Build fails with an error naming both ways out |
| `none` | any | Isolation dropped silently (`DEBUG`), since `none` is already maximally confined |

`host` and `none` are intentionally asymmetric: dropping isolation for a
host-networked workload is a real reduction in confinement (warn, or fail fast
when explicitly requested), whereas `none` already blocks all networking, so
isolation is merely redundant. This is enforced at three layers: config build
(`pkg/runner/config_builder.go`), config load (`pkg/runner/config.go` `ReadJSON`
self-heals legacy on-disk configs), and deploy (`pkg/container/docker/client.go`
as a safety net for restart paths). See issue #5775.

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

**Implementation**: `github.com/stacklok/toolhive-core/permissions`

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

**Implementation**: `github.com/stacklok/toolhive-core/permissions`

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

**Implementation**: `github.com/stacklok/toolhive-core/permissions`

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

**Implementation**: `github.com/stacklok/toolhive-core/permissions`

### Profile Selection

**Priority order:**
1. Direct profile object: `WithPermissionProfile(profile)` (programmatic use)
2. Command-line flag: `--permission-profile <name|path>` (supports "none", "network", or file path)
3. Registry default: From server metadata
4. Global default: `network`

**Implementation**: `github.com/stacklok/toolhive-core/permissions`, registry metadata

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

**Implementation**: `pkg/networking/`, `github.com/stacklok/toolhive-core/permissions`

### Secrets Management

**Architecture principle**: Secrets referenced by name, never embedded in configuration.

**Secret reference pattern:**
- RunConfig contains secret name and target environment variable
- Secret values resolved at runtime from provider
- No plaintext secrets in serialized RunConfig files
- Secret changes don't require RunConfig updates

**Provider architecture:**
- **encrypted**: Password-protected local storage
- **1password**: 1Password SDK integration for enterprise vaults
- **environment**: CI/CD environment variables

**Implementation**: `pkg/secrets/`, `pkg/runner/config.go`

## Platform-Specific Considerations

### Kubernetes

**Security configuration:**
- `spec.permissionProfile` is retained for API compatibility but is deprecated and ignored by the operator
- Configure pod security through `spec.podTemplateSpec` using Kubernetes `securityContext` fields
- Define Kubernetes `NetworkPolicy` resources separately for network enforcement

**Pod template patches:**
```json
{
  "k8s_pod_template_patch": "{\"spec\":{\"nodeSelector\":{\"disktype\":\"ssd\"}}}"
}
```

**Implementation**: `spec.podTemplateSpec` configures pod-level controls; Kubernetes `NetworkPolicy` resources are managed separately

### Docker/Podman

**Container security:**
- `--cap-drop ALL` by default
- Specific capabilities added per profile
- `--security-opt no-new-privileges`
- Network isolation via custom networks

**Implementation**: `pkg/container/docker/`

## Related Documentation

- [Core Concepts](02-core-concepts.md) - RunConfig and Permission Profile concepts
- [Architecture Overview](00-overview.md) - RunConfig as API contract
- [Deployment Modes](01-deployment-modes.md) - RunConfig portability
- [Transport Architecture](03-transport-architecture.md) - Transport configuration
- [Operator Architecture](09-operator-architecture.md) - K8s-specific configuration
- [Registry System](06-registry-system.md) - Registry metadata that seeds RunConfig for a server

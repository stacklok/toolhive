# Registry JSON Schema

This document describes the [JSON Schema](https://json-schema.org/) for the
ToolHive MCP server registry and how to use it for validation and development.

> **⚠️ Registry Migration Notice**
>
> The ToolHive registry has been migrated to a separate repository for better management and maintenance.
>
> **To contribute MCP servers, please visit: https://github.com/stacklok/toolhive-catalog**
>
> The registry data in this repository is now automatically synchronized from the external registry.

## Overview

The [`toolhive-legacy-registry.schema.json`](../../pkg/registry/data/toolhive-legacy-registry.schema.json) file provides comprehensive validation for the
[`registry.json`](../../pkg/registry/data/registry.json) file structure. It
ensures consistency, catches common errors, and serves as living documentation
for contributors.

This can also be used to validate a custom registry file to be used with the
[`thv config set-registry-url`](../cli/thv_config_set-registry-url.md) command.

## Schema files

ToolHive uses multiple JSON schemas for different purposes:

### Legacy Registry Schema

- **File**: [`pkg/registry/data/toolhive-legacy-registry.schema.json`](../../pkg/registry/data/toolhive-legacy-registry.schema.json)
- **Schema ID**: `https://raw.githubusercontent.com/stacklok/toolhive/main/pkg/registry/data/toolhive-legacy-registry.schema.json`
- **Purpose**: Validates the internal ToolHive registry format (`registry.json`)

### Upstream Registry Schema

- **File**: [`pkg/registry/data/upstream-registry.schema.json`](../../pkg/registry/data/upstream-registry.schema.json)
- **Schema ID**: `https://raw.githubusercontent.com/stacklok/toolhive/main/pkg/registry/data/upstream-registry.schema.json`
- **Purpose**: Validates registries using the upstream MCP server format. References the official MCP server schema.

### Publisher-Provided Extensions Schema

- **File**: [`pkg/registry/data/publisher-provided.schema.json`](../../pkg/registry/data/publisher-provided.schema.json)
- **Schema ID**: `https://raw.githubusercontent.com/stacklok/toolhive/main/pkg/registry/data/publisher-provided.schema.json`
- **Purpose**: Defines the structure of ToolHive-specific metadata placed under `_meta["io.modelcontextprotocol.registry/publisher-provided"]` in MCP server definitions

The publisher-provided extensions schema allows ToolHive and other publishers to add custom metadata to MCP server definitions in the upstream registry format. This metadata is stored in the `_meta` object under the key `io.modelcontextprotocol.registry/publisher-provided`.

#### Schema Structure

The extensions are organized by publisher namespace. ToolHive uses the `io.github.stacklok` namespace, with server-specific extensions keyed by their identifier:

- **Container servers**: Keyed by OCI image reference (e.g., `ghcr.io/stacklok/mcp-server-example:latest`)
- **Remote servers**: Keyed by URL (e.g., `https://api.example.com/mcp`)

```json
{
  "_meta": {
    "io.modelcontextprotocol.registry/publisher-provided": {
      "io.github.stacklok": {
        "ghcr.io/stacklok/mcp-server-example:latest": {
          "status": "active",
          "tier": "Official",
          "tools": ["example-tool"],
          "tags": ["example", "demo"],
          "permissions": {
            "network": {
              "outbound": {
                "allow_host": ["api.example.com"],
                "allow_port": [443]
              }
            }
          }
        }
      }
    }
  }
}
```

#### Common Fields

These fields are available for all MCP servers (both container-based and remote):

- **`status`** (required): Current status of the server
  - Values: `"active"`, `"deprecated"`, `"Active"`, `"Deprecated"`
  - Default: `"active"`

- **`tier`**: Tier classification of the server
  - Values: `"Official"`, `"Community"`

- **`tools`**: Array of tool names provided by this MCP server
  - Example: `["filesystem_read", "filesystem_write"]`

- **`tags`**: Categorization tags for search and filtering
  - Pattern: `^[a-z0-9][a-z0-9_-]*[a-z0-9]$`
  - Example: `["filesystem", "productivity", "development"]`

- **`metadata`**: Popularity, activity metrics, and Kubernetes-specific metadata
  - `stars`: Number of repository stars
  - `pulls`: Number of container image pulls or usage count
  - `last_updated`: Timestamp in RFC3339 format
  - `kubernetes`: Kubernetes-specific metadata (nested object) - **optional**, only populated when:
    - The server is served from ToolHive Registry Server
    - The server was auto-discovered from a Kubernetes deployment
    - The Kubernetes resource has the required registry annotations (e.g., `toolhive.stacklok.com/registry-description`, `toolhive.stacklok.com/registry-url`)
    - Fields:
      - `kind`: Kubernetes resource kind (e.g., "MCPServer", "VirtualMCPServer", "MCPRemoteProxy")
      - `namespace`: Kubernetes namespace where the resource is deployed
      - `name`: Kubernetes resource name
      - `uid`: Kubernetes resource UID
      - `image`: Container image used by the Kubernetes workload (applicable to MCPServer)
      - `transport`: Transport type configured for the Kubernetes workload (applicable to MCPServer)

- **`custom_metadata`**: Custom user-defined metadata (arbitrary key-value pairs)

#### Container Server Fields

These fields are specific to container-based MCP servers (keyed by OCI image reference):

- **`permissions`**: Security permissions for the container
  - `name`: Permission profile name
  - `network.outbound`: Outbound network access
    - `allow_host`: Array of allowed hostnames or domain patterns
    - `allow_port`: Array of allowed port numbers
    - `insecure_allow_all`: Allow all outbound connections (use with caution)
  - `read`: Array of host filesystem paths for read-only access
  - `write`: Array of host filesystem paths for write access
  - `privileged`: Whether to run in privileged mode

- **`args`**: Default command-line arguments for the container

- **`provenance`**: Software supply chain provenance information
  - `sigstore_url`: Sigstore TUF repository host
  - `repository_uri`: Repository URI for verification
  - `repository_ref`: Repository reference for verification
  - `signer_identity`: Identity of the signer
  - `runner_environment`: Build environment (e.g., `"github-hosted"`)
  - `cert_issuer`: Certificate issuer URI
  - `attestation`: Verified attestation with predicate type and data

- **`docker_tags`**: Available Docker tags for the container image

- **`proxy_port`**: HTTP proxy port for the container (1-65535)

#### Remote Server Fields

These fields are specific to remote MCP servers (keyed by URL):

- **`oauth_config`**: OAuth/OIDC configuration for authentication
  - `issuer`: OAuth/OIDC issuer URL (for OIDC discovery)
  - `authorize_url`: OAuth authorization endpoint (for non-OIDC OAuth)
  - `token_url`: OAuth token endpoint (for non-OIDC OAuth)
  - `client_id`: OAuth client ID
  - `scopes`: Array of OAuth scopes to request
  - `use_pkce`: Whether to use PKCE (default: `true`)
  - `oauth_params`: Additional OAuth parameters
  - `callback_port`: Specific port for OAuth callback server
  - `resource`: OAuth 2.0 resource indicator (RFC 8707)

- **`env_vars`**: Environment variable definitions for client configuration
  - `name`: Environment variable name (pattern: `^[A-Za-z_][A-Za-z0-9_]*$`)
  - `description`: Human-readable explanation
  - `required`: Whether the variable is required
  - `secret`: Whether the variable contains sensitive information
  - `default`: Default value if not provided

#### Example: Container Server

```json
{
  "ghcr.io/stacklok/mcp-filesystem:v1.0.0": {
    "status": "active",
    "tier": "Official",
    "tools": ["read_file", "write_file", "list_directory"],
    "tags": ["filesystem", "productivity"],
    "permissions": {
      "name": "filesystem-access",
      "read": ["/home/user/documents"],
      "write": ["/home/user/documents/output"]
    },
    "args": ["--log-level", "info"],
    "docker_tags": ["v1.0.0", "v1.0", "v1", "latest"],
    "metadata": {
      "stars": 150,
      "pulls": 5000,
      "last_updated": "2025-02-04T10:00:00Z"
    }
  }
}
```

#### Example: Container Server with Kubernetes Metadata

When an MCP server is deployed in Kubernetes and served via the ToolHive Registry Server's auto-discovery feature, additional Kubernetes-specific metadata is included. This requires the Kubernetes resource to have the required registry annotations:

```json
{
  "https://mcp-server.example.com": {
    "status": "active",
    "tier": "Official",
    "tools": ["read_file", "write_file", "list_directory"],
    "tags": ["filesystem", "productivity"],
    "metadata": {
      "stars": 150,
      "pulls": 5000,
      "last_updated": "2025-02-04T10:00:00Z",
      "kubernetes": {
        "kind": "MCPServer",
        "namespace": "mcp-servers",
        "name": "filesystem-server",
        "uid": "a1b2c3d4-e5f6-4a5b-8c9d-0e1f2a3b4c5d",
        "image": "ghcr.io/stacklok/mcp-filesystem:v1.0.0",
        "transport": "streamable-http"
      }
    }
  }
}
```

#### Example: Remote Server

```json
{
  "https://api.example.com/mcp": {
    "status": "active",
    "tier": "Community",
    "tools": ["query_api", "update_resource"],
    "tags": ["api", "integration"],
    "oauth_config": {
      "issuer": "https://auth.example.com",
      "client_id": "mcp-client",
      "scopes": ["read", "write"],
      "use_pkce": true
    },
    "env_vars": [
      {
        "name": "API_KEY",
        "description": "API authentication key",
        "required": true,
        "secret": true
      },
      {
        "name": "API_ENDPOINT",
        "description": "API endpoint URL",
        "required": false,
        "default": "https://api.example.com"
      }
    ]
  }
}
```

## Usage

### Automated validation (Go tests)

The registry is automatically validated against the schema during development
and CI/CD through Go tests. This ensures that any changes to the registry data
are immediately validated.

The validation is implemented in
[`pkg/registry/schema_validation.go`](../../pkg/registry/schema_validation.go)
and tested in
[`pkg/registry/schema_validation_test.go`](../../pkg/registry/schema_validation_test.go).

**Key tests:**

- `TestEmbeddedRegistrySchemaValidation` - Validates the embedded
  `registry.json` against the legacy schema
- `TestRegistrySchemaValidation` - Comprehensive test suite with valid and
  invalid legacy registry examples
- `TestValidateUpstreamRegistry` - Validates upstream registry format
- `TestValidatePublisherProvidedExtensions` - Validates publisher-provided
  extensions structure

**Running the validation:**

```bash
# Run all schema validation tests
go test -v ./pkg/registry -run ".*Schema.*"

# Run just the embedded registry validation
go test -v ./pkg/registry -run TestEmbeddedRegistrySchemaValidation

# Run all registry tests (includes schema validation)
go test -v ./pkg/registry
```

This validation runs automatically as part of:

- Local development (`go test`)
- CI/CD pipeline (GitHub Actions)
- Pre-commit hooks (if configured)

### Manual validation

#### Using check-jsonschema

Install check-jsonschema via Homebrew (macOS):

```bash
brew install check-jsonschema
```

Or via pipx (cross-platform):

```bash
pipx install check-jsonschema
```

Validate the registry with full format validation:

```bash
# Run from the root of the repository
check-jsonschema --schemafile pkg/registry/data/toolhive-legacy-registry.schema.json pkg/registry/data/registry.json
```

#### Using ajv-cli

Install ajv-cli and ajv-formats globally:

```bash
npm install -g ajv-cli ajv-formats
```

Validate the registry with format validation:

```bash
# Run from the root of the repository
ajv validate -c ajv-formats -s pkg/registry/data/toolhive-legacy-registry.schema.json -d pkg/registry/data/registry.json
```

#### Using VS Code

VS Code automatically validates JSON files when a schema is specified. Add this
to the top of any registry JSON file:

```json
{
  "$schema": "https://raw.githubusercontent.com/stacklok/toolhive/main/pkg/registry/data/toolhive-legacy-registry.schema.json",
  ...
}
```

## Methodology

The `draft-07` version of JSON Schema is used to ensure the widest compatibility
with commonly used tools and libraries.

The schema is currently maintained manually, due to differences in how required
vs. optional sections are defined in the Go codebase (`omitempty` vs. nil/empty
conditional checks).

At some point, we may automate this process by generating the schema from the Go
code using something like
[invopop/jsonschema](https://github.com/invopop/jsonschema), but for now, manual
updates are necessary to ensure accuracy and completeness.

## Contributing

**For adding new MCP servers:**

Please visit the [toolhive-registry repository](https://github.com/stacklok/toolhive-registry) which now manages all MCP server definitions.

**For schema improvements:**

When modifying the registry schema in this repository:

1. **Validate locally** before submitting PRs
2. **Follow naming conventions** for consistency
3. **Include comprehensive descriptions** for clarity
4. **Test with existing registry data** to ensure compatibility
5. **Update documentation** to reflect schema changes

**Legacy server addition process (deprecated):**

~~When adding new server entries:~~
1. ~~**Validate locally** before submitting PRs~~
2. ~~**Follow naming conventions** for consistency~~
3. ~~**Include comprehensive descriptions** for clarity~~
4. ~~**Specify minimal permissions** for security~~
5. ~~**Use appropriate tags** for discoverability~~

## Related documentation

- [Registry Management Process](management.md)
- [Registry Inclusion Heuristics](heuristics.md)
- [JSON Schema Specification](https://json-schema.org/)

# Build Environment Variables for Protocol Builds

## Problem Statement

Many organizations operate in restricted network environments where downloading packages directly from public registries (npm, PyPI, Go module proxies) is not permitted. These organizations require packages to be downloaded through internal, approved mirrors that:

- Scan and verify packages for security vulnerabilities before allowing access
- Maintain curated lists of approved packages
- Provide an audit trail of package usage
- Operate behind corporate firewalls with TLS interception

Currently, when using ToolHive's protocol builds (`npx://`, `uvx://`, `go://`), packages are always downloaded from the default public registries during the Docker image build phase. This prevents adoption in security-conscious organizations that mandate the use of internal package mirrors.

Beyond package mirrors, there are other build-time configurations that organizations may need to customize, such as memory limits, private module paths, or trusted hosts.

## Goals

- Provide a general mechanism to inject environment variables into protocol build Dockerfiles
- Allow users to configure custom package mirror URLs as a primary use case
- Support any environment variable that package managers or build tools recognize
- Ensure configured variables are always applied during protocol builds
- Integrate with the existing CA certificate configuration for environments with TLS interception
- Maintain backward compatibility for users without custom configuration requirements

## Non-Goals

- Authentication to private mirrors via username/password during builds (may be addressed in future work)
- Validation that configured mirrors are reachable or functional
- Runtime environment variable injection (this is build-time only)
- Per-MCP-server build environment configuration (global configuration only)

## Architecture Overview

The solution extends ToolHive's configuration system with a new `build_env` section that stores key-value pairs of environment variables. During protocol builds, these variables are injected into the generated Dockerfile's builder stage, allowing users to configure package managers and build tools.

When a user sets a build environment variable (e.g., `thv config set build-env NPM_CONFIG_REGISTRY https://npm.corp.example.com`), ToolHive stores this in the configuration file. During protocol builds, all configured variables are passed to the template renderer and injected as `ENV` directives in the generated Dockerfile.

This approach provides flexibility for unforeseen use cases while solving the immediate package mirror requirement.

## Detailed Design

### Configuration Model

Add a new `BuildEnv` map to the configuration:

```yaml
# ~/.config/toolhive/config.yaml
secrets:
  provider_type: encrypted
  setup_completed: true
ca_certificate_path: /path/to/ca-cert.pem
build_env:
  # Package mirror configuration
  NPM_CONFIG_REGISTRY: "https://npm.corp.example.com"
  PIP_INDEX_URL: "https://pypi.corp.example.com/simple"
  UV_DEFAULT_INDEX: "https://pypi.corp.example.com/simple"
  GOPROXY: "https://goproxy.corp.example.com"
  # Other build-time configurations
  GOPRIVATE: "github.com/myorg/*"
  NODE_OPTIONS: "--max-old-space-size=4096"
```

### CLI Commands

```bash
# Set a build environment variable
thv config set build-env <KEY> <value>

# View all configured build environment variables
thv config get build-env

# View a specific variable
thv config get build-env <KEY>

# Remove a specific variable
thv config unset build-env <KEY>

# Remove all build environment variables
thv config unset build-env --all
```

### Template Data Extension

Extend `TemplateData` in `pkg/container/templates/templates.go`:

```go
type TemplateData struct {
    MCPPackage      string
    MCPPackageClean string
    CACertContent   string
    IsLocalPath     bool
    BuildArgs       []string
    // New field for build environment variables
    BuildEnv        map[string]string
}
```

### Template Modifications

Add a common block to each template (npx.tmpl, uvx.tmpl, go.tmpl) that iterates over all configured environment variables:

```dockerfile
FROM node:22-alpine AS builder

{{if .BuildEnv}}
# Custom build environment variables
{{range $key, $value := .BuildEnv}}ENV {{$key}}="{{$value}}"
{{end}}
{{end}}

# ... rest of template
```

This approach means:
- All templates use the same mechanism
- New environment variables work without template changes
- Users have full control over build-time configuration

### Integration with Protocol Handler

The `createTemplateData` function in `pkg/runner/protocol.go` will be modified to read `build_env` from the configuration and populate the `TemplateData.BuildEnv` field.

## User Experience

### Package Mirror Setup

For organizations requiring custom package mirrors:

```bash
# Configure custom CA certificate for TLS interception (if needed)
thv config set cacert /path/to/corporate-ca.pem

# Configure package mirrors
thv config set build-env NPM_CONFIG_REGISTRY https://artifactory.corp.example.com/api/npm/npm-remote/
thv config set build-env PIP_INDEX_URL https://artifactory.corp.example.com/api/pypi/pypi-remote/simple
thv config set build-env UV_DEFAULT_INDEX https://artifactory.corp.example.com/api/pypi/pypi-remote/simple
thv config set build-env GOPROXY https://artifactory.corp.example.com/api/go/go-remote

# Verify configuration
thv config get build-env
```

### Other Build Configuration Examples

```bash
# Configure private Go modules
thv config set build-env GOPRIVATE "github.com/myorg/*,gitlab.mycompany.com/*"

# Configure Node.js memory limits for large builds
thv config set build-env NODE_OPTIONS "--max-old-space-size=4096"

# Configure pip trusted host (for self-signed certs)
thv config set build-env PIP_TRUSTED_HOST "pypi.corp.example.com"
```

### Running Protocol Builds

Once configured, protocol builds work exactly as before:

```bash
thv run npx://@modelcontextprotocol/server-github
thv run uvx://mcp-server-fetch
thv run go://github.com/mark3labs/mcp-filesystem-server
```

### Viewing Generated Dockerfile

Users can verify their configuration is being applied:

```bash
thv run --dry-run npx://@modelcontextprotocol/server-github
```

This outputs the generated Dockerfile with all configured environment variables.

## Security Considerations

### Variable Name Validation

Build environment variable names should be validated:
- Must match pattern `^[A-Z][A-Z0-9_]*$` (uppercase letters, numbers, underscores)
- Cannot override critical Docker variables (e.g., `PATH`, `HOME`)

### No Credential Storage

This proposal does not include credential storage. If a mirror requires authentication, users should use URL-embedded credentials (not recommended) or configure credentials through other mechanisms. Future work may address secure credential injection.

### Audit Trail

Configured build environment variables are logged during builds (with values redacted for security).

## Alternatives Considered

**Specific Package Mirror Fields**: Create dedicated configuration fields for each package manager. Rejected in favor of the general mechanism because it's more flexible, handles unforeseen use cases, and reduces code complexity.

**Environment Variable Override**: Users could set environment variables like `TOOLHIVE_BUILD_NPM_REGISTRY` that get passed through. Rejected because environment variables don't persist across sessions and don't provide centralized configuration management.

**Per-Server Configuration**: Allow specifying build environment per MCP server. Rejected because it adds complexity and doesn't align with the organizational use case where all builds should use consistent configuration.

## Implementation Plan

1. **Configuration Model**: Add `BuildEnv map[string]string` to `pkg/config/config.go` with validation functions
2. **CLI Commands**: Add `build-env` subcommands to `thv config set/get/unset`
3. **Template Changes**: Extend `TemplateData` and add environment variable injection block to all templates
4. **Protocol Handler Integration**: Modify `createTemplateData` to read build environment configuration
5. **Documentation**: Update user documentation with configuration guide and common examples

# Custom Package Mirror Support for Protocol Builds

## Problem Statement

Many organizations operate in restricted network environments where downloading packages directly from public registries (npm, PyPI, Go module proxies) is not permitted. These organizations require packages to be downloaded through internal, approved mirrors that:

- Scan and verify packages for security vulnerabilities before allowing access
- Maintain curated lists of approved packages
- Provide an audit trail of package usage
- Operate behind corporate firewalls with TLS interception

Currently, when using ToolHive's protocol builds (`npx://`, `uvx://`, `go://`), packages are always downloaded from the default public registries during the Docker image build phase. This prevents adoption in security-conscious organizations that mandate the use of internal package mirrors.

While ToolHive already supports custom CA certificates for TLS verification in corporate environments, there is no mechanism to configure custom package mirror URLs for npm, PyPI, or Go modules.

## Goals

- Allow users to configure custom mirror URLs for npm, PyPI, and Go module proxy
- Ensure configured mirrors are always used during protocol builds (mandatory enforcement)
- Support configuring mirrors independently (e.g., custom npm mirror while using default PyPI)
- Integrate with the existing CA certificate configuration for environments with TLS interception
- Maintain backward compatibility for users without custom mirror requirements

## Non-Goals

- Authentication to private mirrors via username/password during builds (may be addressed in future work)
- Validation that the configured mirrors are reachable or functional
- Support for multiple mirrors per protocol (e.g., fallback chains)
- Mirror configuration per MCP server (global configuration only)

## Architecture Overview

The solution extends ToolHive's configuration system with a new `package_mirrors` section and modifies the Dockerfile templates to inject environment variables that configure the respective package managers.

When a user configures a package mirror (e.g., `thv config set package-mirror npm https://npm.corp.example.com`), ToolHive stores this URL in the configuration file. During protocol builds, ToolHive reads these settings and passes them to the template renderer. The generated Dockerfile then includes the appropriate environment variables (`NPM_CONFIG_REGISTRY`, `PIP_INDEX_URL`, `GOPROXY`) that instruct the package managers to use the configured mirrors instead of public defaults.

This approach ensures that once configured, all protocol builds automatically use the approved mirrors without requiring any changes to the `thv run` commands.

## Detailed Design

### Configuration Model

Add a new `PackageMirrors` struct to the configuration:

```yaml
# ~/.config/toolhive/config.yaml
secrets:
  provider_type: encrypted
  setup_completed: true
ca_certificate_path: /path/to/ca-cert.pem
package_mirrors:
  npm: "https://npm.corp.example.com"
  pypi: "https://pypi.corp.example.com/simple"
  goproxy: "https://goproxy.corp.example.com"
```

Each package manager uses specific environment variables:
- **npm**: `NPM_CONFIG_REGISTRY` - standard npm configuration via environment variable
- **PyPI (pip/uv)**: `PIP_INDEX_URL` and `UV_INDEX_URL` - used by pip and uv respectively
- **Go modules**: `GOPROXY` - standard Go module proxy configuration

### CLI Commands

New commands under `thv config`:

```bash
# Set custom mirrors
thv config set package-mirror npm <url>
thv config set package-mirror pypi <url>
thv config set package-mirror goproxy <url>

# View current mirror configuration
thv config get package-mirror

# Remove custom mirror configuration (revert to defaults)
thv config unset package-mirror npm
thv config unset package-mirror pypi
thv config unset package-mirror goproxy
thv config unset package-mirror --all
```

### Template Data Extension

Extend `TemplateData` in `pkg/container/templates/templates.go` to include mirror URLs. The template renderer will populate these fields from the configuration before generating the Dockerfile.

### Template Modifications

Each template (npx.tmpl, uvx.tmpl, go.tmpl) will be modified to conditionally include environment variables when mirrors are configured.

For npm (npx.tmpl), the template will set `NPM_CONFIG_REGISTRY` at the start of the builder stage.

For Python (uvx.tmpl), the template will set both `PIP_INDEX_URL` and `UV_INDEX_URL` to ensure compatibility with both pip and uv package managers.

For Go (go.tmpl), the template will include `GOPROXY` in the existing environment variable block.

### Integration with Protocol Handler

The `createTemplateData` function in `pkg/runner/protocol.go` will be modified to read mirror settings from the configuration and populate the `TemplateData` struct. This ensures mirrors are automatically applied to all protocol builds without requiring changes to the build flow.

## User Experience

### Initial Setup

For organizations requiring custom mirrors, administrators would set up the configuration once:

```bash
# Configure custom CA certificate for TLS interception (if needed)
thv config set cacert /path/to/corporate-ca.pem

# Configure custom package mirrors
thv config set package-mirror npm https://artifactory.corp.example.com/api/npm/npm-remote/
thv config set package-mirror pypi https://artifactory.corp.example.com/api/pypi/pypi-remote/simple
thv config set package-mirror goproxy https://artifactory.corp.example.com/api/go/go-remote

# Verify configuration
thv config get package-mirror
```

### Running Protocol Builds

Once configured, protocol builds work exactly as before, but use the custom mirrors:

```bash
# npm packages download from corporate mirror
thv run npx://@modelcontextprotocol/server-github

# Python packages download from corporate PyPI mirror
thv run uvx://mcp-server-fetch

# Go modules download from corporate Go proxy
thv run go://github.com/mark3labs/mcp-filesystem-server
```

### Viewing Generated Dockerfile

Users can verify their mirror configuration is being applied using dry-run mode:

```bash
thv run --dry-run npx://@modelcontextprotocol/server-github
```

This outputs the generated Dockerfile, which will include the configured mirror environment variables.

## Security Considerations

### URL Validation

Mirror URLs must be validated:
- Must be valid URLs with http:// or https:// scheme
- https:// should be required by default
- An `--allow-insecure` flag could permit http:// URLs for development environments

### No Credential Storage

This proposal does not include credential storage for authenticated mirrors. If a mirror requires authentication, users should configure credentials through their environment or Docker configuration. Future work may address secure credential injection.

### Audit Trail

When custom mirrors are configured, this is logged during builds to provide visibility into which package sources are being used.

## Alternatives Considered

**Environment Variable Override**: Users could set environment variables like `TOOLHIVE_NPM_MIRROR` instead of persistent configuration. Rejected because environment variables don't persist across sessions and don't provide centralized configuration management.

**Per-Server Mirror Configuration**: Allow specifying mirrors per MCP server in registry entries. Rejected because it adds complexity and doesn't align with the organizational use case where all builds should use approved mirrors consistently.

**Docker Configuration Injection**: Mount the user's npm/pip configuration files into the build container. Rejected because it's fragile, platform-specific, and doesn't work well with ToolHive's ephemeral build context approach.

## Implementation Plan

1. **Configuration Model**: Add `PackageMirrors` struct to `pkg/config/config.go` with validation functions
2. **CLI Commands**: Add `package-mirror` subcommands to `thv config set/get/unset`
3. **Template Changes**: Extend `TemplateData` and update npx.tmpl, uvx.tmpl, go.tmpl with conditional environment variables
4. **Protocol Handler Integration**: Modify `createTemplateData` to read mirror configuration
5. **Documentation**: Update user documentation with mirror configuration guide and examples for common setups (Artifactory, Nexus, etc.)

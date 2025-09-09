# Upstream MCP Registry Format Support

## Problem Statement

ToolHive currently uses its own registry format for MCP servers, which is optimized for container execution and ToolHive's specific needs. However, the Model Context Protocol community has developed a standardized `server.json` format for describing MCP servers in a vendor-neutral way. This creates a barrier for:

- **Server developers**: Must maintain separate metadata for different registries
- **Registry interoperability**: Cannot easily share server definitions between registries
- **Community adoption**: ToolHive servers cannot be easily discovered by other MCP clients
- **Ecosystem fragmentation**: Different formats prevent a unified MCP server ecosystem

## Goals

- Add support for ingesting upstream MCP registry format (`server.json`) into ToolHive
- Enable conversion from ToolHive format to upstream format for ecosystem compatibility
- Maintain backward compatibility with existing ToolHive registry format
- Leverage ToolHive-specific extensions for features not covered by upstream format
- Provide seamless conversion between formats with minimal data loss

## Non-Goals

- Replace ToolHive's existing registry format entirely
- Support every possible upstream format variation immediately
- Automatic synchronization with upstream registries (future enhancement)
- Breaking changes to existing ToolHive APIs

## Architecture Overview

The solution adds bidirectional conversion between ToolHive's container-focused format and the upstream community format:

```
Upstream Format (server.json)     ToolHive Format (registry.json)
┌─────────────────────────┐       ┌─────────────────────────┐
│ • Abstract description  │ ←───→ │ • Container execution   │
│ • Package-centric       │       │ • Security profiles     │
│ • Runtime hints         │       │ • Proxy configuration   │
│ • Multi-deployment      │       │ • ToolHive-specific     │
└─────────────────────────┘       └─────────────────────────┘
```

## Detailed Design

### 1. Upstream Format Structs (`pkg/registry/upstream.go`)

Define Go structs that match the upstream `server.json` schema:

```go
// UpstreamServerDetail represents the complete upstream format
type UpstreamServerDetail struct {
    Server     UpstreamServer     `json:"server"`
    XPublisher *UpstreamPublisher `json:"x-publisher,omitempty"`
}

// UpstreamServer contains core server information
type UpstreamServer struct {
    Name          string                `json:"name"`
    Description   string                `json:"description"`
    Status        UpstreamServerStatus  `json:"status,omitempty"`
    Repository    *UpstreamRepository   `json:"repository,omitempty"`
    VersionDetail UpstreamVersionDetail `json:"version_detail"`
    Packages      []UpstreamPackage     `json:"packages,omitempty"`
    Remotes       []UpstreamRemote      `json:"remotes,omitempty"`
}
```

Key features:
- **Package Support**: npm, pypi, docker, nuget registries
- **Remote Support**: SSE and streamable HTTP transports
- **Flexible Arguments**: Named and positional arguments with variable substitution
- **Environment Variables**: Required/optional, secret/plain, with choices

### 2. ToolHive Extension Format

ToolHive-specific metadata is stored in the `x-publisher` extension field:

```go
type ToolhivePublisherExtension struct {
    Tier           string                `json:"tier,omitempty"`
    Transport      string                `json:"transport,omitempty"`
    Tools          []string              `json:"tools,omitempty"`
    Metadata       *Metadata             `json:"metadata,omitempty"`
    Tags           []string              `json:"tags,omitempty"`
    CustomMetadata map[string]any        `json:"custom_metadata,omitempty"`
    Permissions    *permissions.Profile  `json:"permissions,omitempty"`
    TargetPort     int                   `json:"target_port,omitempty"`
    DockerTags     []string              `json:"docker_tags,omitempty"`
    Provenance     *Provenance           `json:"provenance,omitempty"`
}
```

This approach:
- **Preserves ToolHive features** not in upstream format
- **Maintains compatibility** with upstream tooling
- **Enables round-trip conversion** without data loss
- **Follows extension conventions** used by other tools

### 3. Conversion Functions (`pkg/registry/upstream_conversion.go`)

#### Upstream → ToolHive Conversion

```go
func ConvertUpstreamToToolhive(upstream *UpstreamServerDetail) (ServerMetadata, error)
```

**Package Type Handling**:
- `docker` packages → Direct Docker image references (`nginx:latest`)
- `npm` packages → NPX protocol scheme (`npx://@scope/package@1.0.0`)
- `pypi` packages → UVX protocol scheme (`uvx://package@1.0.0`)
- Other packages → Best-effort conversion

**Server Type Detection**:
- Servers with `remotes` → `RemoteServerMetadata`
- Servers with `packages` → `ImageMetadata`

**Metadata Extraction**:
- ToolHive extensions from `x-publisher.x-dev.toolhive`
- Default values for missing ToolHive-specific fields
- Conversion of upstream enums to ToolHive values

#### ToolHive → Upstream Conversion

```go
func ConvertToolhiveToUpstream(server ServerMetadata) (*UpstreamServerDetail, error)
```

**Package Generation**:
- Docker images → `docker` registry packages
- Protocol schemes → Appropriate registry packages
- Environment variables → Package environment variables

**Extension Population**:
- ToolHive-specific metadata → `x-publisher.x-dev.toolhive`
- Standard upstream fields from ToolHive metadata
- Publisher information for provenance

### 4. Protocol Scheme Handling

ToolHive supports protocol schemes that the upstream format describes as packages:

| Upstream Package | ToolHive Image Format | ToolHive Execution |
|------------------|----------------------|-------------------|
| `npm` registry   | `npx://package@version` | Dynamic container build |
| `pypi` registry  | `uvx://package@version` | Dynamic container build |
| `docker` registry| `image:tag` | Direct container run |

This mapping allows ToolHive to:
- **Execute upstream packages** using existing protocol scheme support
- **Preserve package metadata** for round-trip conversion
- **Leverage dynamic builds** for non-Docker packages

## Implementation Examples

### Example 1: NPM Package Conversion

**Upstream Format**:
```json
{
  "server": {
    "name": "io.modelcontextprotocol/brave-search",
    "description": "MCP server for Brave Search API integration",
    "packages": [{
      "registry_name": "npm",
      "name": "@modelcontextprotocol/server-brave-search",
      "version": "1.0.2",
      "environment_variables": [{
        "name": "BRAVE_API_KEY",
        "description": "Brave Search API Key",
        "is_required": true,
        "is_secret": true
      }]
    }]
  }
}
```

**ToolHive Format**:
```json
{
  "name": "io.modelcontextprotocol/brave-search",
  "description": "MCP server for Brave Search API integration",
  "tier": "Community",
  "status": "active",
  "transport": "stdio",
  "image": "npx://@modelcontextprotocol/server-brave-search@1.0.2",
  "env_vars": [{
    "name": "BRAVE_API_KEY",
    "description": "Brave Search API Key",
    "required": true,
    "secret": true
  }]
}
```

### Example 2: Remote Server Conversion

**Upstream Format**:
```json
{
  "server": {
    "name": "Remote Filesystem Server",
    "description": "Cloud-hosted MCP filesystem server",
    "remotes": [{
      "transport_type": "sse",
      "url": "https://mcp-fs.example.com/sse",
      "headers": [{
        "name": "X-API-Key",
        "description": "API key for authentication",
        "is_required": true,
        "is_secret": true
      }]
    }]
  }
}
```

**ToolHive Format**:
```json
{
  "name": "Remote Filesystem Server",
  "description": "Cloud-hosted MCP filesystem server",
  "tier": "Community",
  "status": "active",
  "transport": "sse",
  "url": "https://mcp-fs.example.com/sse",
  "headers": [{
    "name": "X-API-Key",
    "description": "API key for authentication",
    "required": true,
    "secret": true
  }]
}
```

## Integration Points

### 1. Registry Loading

Extend existing registry loading to support upstream format:

```go
// pkg/registry/loader.go
func LoadUpstreamServer(path string) (*UpstreamServerDetail, error)
func LoadAndConvertUpstream(path string) (ServerMetadata, error)
```

### 2. CLI Commands

Add support for upstream format in existing commands:

```bash
# Import upstream server definition
thv registry import server.json

# Export ToolHive server to upstream format
thv registry export --format=upstream server-name

# Validate upstream format
thv registry validate server.json
```

### 3. Registry Management

Enhance registry operations to handle both formats:

```go
// pkg/registry/manager.go
func (m *Manager) ImportUpstream(path string) error
func (m *Manager) ExportUpstream(serverName, outputPath string) error
```

## Testing Strategy

### 1. Unit Tests (`pkg/registry/upstream_conversion_test.go`)

Comprehensive test coverage for:
- **Conversion accuracy**: Verify data preservation in both directions
- **Package type handling**: Test npm, pypi, docker, unknown packages
- **Server type detection**: Remote vs container server classification
- **Error handling**: Invalid inputs, missing fields, malformed data
- **Edge cases**: Empty packages, multiple remotes, complex arguments

### 2. Integration Tests

- **Round-trip conversion**: Upstream → ToolHive → Upstream
- **Real server definitions**: Test with actual community server.json files
- **CLI integration**: Import/export commands with various formats
- **Registry operations**: Loading and managing mixed format registries

### 3. Compatibility Tests

- **Upstream schema validation**: Ensure generated upstream format is valid
- **ToolHive functionality**: Verify converted servers work with existing features
- **Protocol scheme execution**: Test npm/pypi package execution

## Security Considerations

### 1. Input Validation

- **Schema validation**: Validate upstream format against JSON schema
- **Package name validation**: Prevent malicious package references
- **URL validation**: Validate remote server URLs and headers
- **Path sanitization**: Ensure safe file paths in package arguments

### 2. Extension Field Security

- **Trusted sources**: Only process ToolHive extensions from trusted publishers
- **Permission validation**: Validate permission profiles in extensions
- **Metadata sanitization**: Sanitize custom metadata fields

### 3. Conversion Safety

- **Data preservation**: Ensure no sensitive data leaks during conversion
- **Default security**: Apply secure defaults for missing security fields
- **Audit logging**: Log conversion operations for security auditing

## Migration Strategy

### Phase 1: Core Implementation
- Implement upstream format structs and conversion functions
- Add comprehensive test coverage
- Create basic CLI import/export commands

### Phase 2: Integration
- Integrate with existing registry loading
- Add validation and error handling
- Enhance CLI commands with format detection

### Phase 3: Ecosystem Support
- Add support for community registry URLs
- Implement automatic format detection
- Create migration tools for existing servers

### Phase 4: Advanced Features
- Bidirectional synchronization with upstream registries
- Advanced validation and linting
- Community contribution workflows

## Success Metrics

1. **Conversion Accuracy**: 100% round-trip conversion for supported features
2. **Format Coverage**: Support for 95% of upstream format features
3. **Performance**: Conversion operations complete in <100ms
4. **Compatibility**: Converted servers execute successfully in ToolHive
5. **Community Adoption**: Enable contribution to upstream registries

## Alternatives Considered

### 1. Replace ToolHive Format Entirely
**Rejected**: Would break existing deployments and lose ToolHive-specific features like permission profiles and container optimization.

### 2. Separate Registry for Upstream Format
**Rejected**: Would fragment the ecosystem and require users to manage multiple registries.

### 3. Custom Extension Mechanism
**Rejected**: The `x-publisher` field provides a standard way to extend upstream format without breaking compatibility.

### 4. Automatic Format Detection
**Considered**: Could be added in future phases, but explicit conversion provides better control and error handling.

## Future Enhancements

1. **Registry Synchronization**: Automatic sync with upstream community registries
2. **Format Migration Tools**: Bulk conversion utilities for existing registries
3. **Validation Enhancements**: Advanced linting and compatibility checking
4. **Community Integration**: Direct publishing to upstream registries
5. **Schema Evolution**: Support for future upstream format versions

## Conclusion

Adding upstream MCP registry format support to ToolHive enables:

- **Ecosystem Interoperability**: Share server definitions with other MCP tools
- **Community Participation**: Contribute to and benefit from community registries
- **Developer Experience**: Single source of truth for server metadata
- **Future Compatibility**: Prepare for ecosystem standardization

The implementation leverages ToolHive's extension mechanism to preserve advanced features while maintaining compatibility with the community standard. This positions ToolHive as both a powerful container execution platform and a good citizen in the broader MCP ecosystem.
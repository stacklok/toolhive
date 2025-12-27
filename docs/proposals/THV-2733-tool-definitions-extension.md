# Proposal: Add Tool Definitions to Publisher Extensions

Add comprehensive tool metadata to the existing ToolHive publisher extension (`io.github.stacklok`) in the upstream MCP Registry format. This extends beyond tool names to include descriptions, input/output schemas, and annotations.

## Problem Statement

The current registry stores only tool names (`tools: []string`), which limits discoverability and tooling capabilities. Users and AI agents need richer metadata to:
- Understand what each tool does before running a server
- Validate tool inputs/outputs programmatically
- Enable better tool selection and filtering in aggregation scenarios (e.g., Virtual MCP Server)
- Support IDE/editor integrations with type information

## Goals

- Store full MCP tool definitions in the registry without breaking existing consumers
- Reuse the existing `io.github.stacklok` publisher extension namespace
- Align with the MCP specification's tool schema
- Maintain backward compatibility with the existing `tools` field

## Proposed Solution

Add a new `tool_definitions` field to the existing ToolHive publisher extension structure. This field contains an array of tool objects matching the MCP specification.

### Extension Schema

The `tool_definitions` field is added alongside existing fields in the `io.github.stacklok` extension:

```json
{
  "meta": {
    "publisher_provided": {
      "io.github.stacklok": {
        "<image_or_url>": {
          "status": "active",
          "tier": "Official",
          "tools": ["get_weather", "search_location"],
          "tool_definitions": [
            {
              "name": "get_weather",
              "title": "Weather Information",
              "description": "Get current weather for a location",
              "inputSchema": {
                "type": "object",
                "properties": {
                  "location": {
                    "type": "string",
                    "description": "City name or coordinates"
                  }
                },
                "required": ["location"]
              },
              "outputSchema": {
                "type": "object",
                "properties": {
                  "temperature": { "type": "number" },
                  "conditions": { "type": "string" }
                }
              },
              "annotations": {
                "readOnly": true
              }
            }
          ]
        }
      }
    }
  }
}
```

### Tool Definition Fields

Per the [MCP specification](https://modelcontextprotocol.io/specification/2025-06-18/server/tools):

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `name` | `string` | Yes | Unique identifier for the tool |
| `title` | `string` | No | Human-readable display name |
| `description` | `string` | No | Human-readable description of functionality |
| `inputSchema` | `object` | No | JSON Schema defining expected parameters |
| `outputSchema` | `object` | No | JSON Schema defining expected output structure |
| `annotations` | `object` | No | Properties describing tool behavior |

### Annotations

Tool annotations provide hints about tool behavior:

| Annotation | Type | Description |
|------------|------|-------------|
| `readOnly` | `boolean` | Tool only reads data, no side effects |
| `destructive` | `boolean` | Tool may perform destructive operations |
| `idempotent` | `boolean` | Repeated calls with same args have same effect |
| `openWorld` | `boolean` | Tool interacts with external entities |

## Internal Type Changes

Add a new type to `pkg/registry/registry/registry_types.go`:

```go
// ToolDefinition represents the full metadata for an MCP tool
type ToolDefinition struct {
    Name         string         `json:"name"`
    Title        string         `json:"title,omitempty"`
    Description  string         `json:"description,omitempty"`
    InputSchema  map[string]any `json:"inputSchema,omitempty"`
    OutputSchema map[string]any `json:"outputSchema,omitempty"`
    Annotations  map[string]any `json:"annotations,omitempty"`
}
```

Update `BaseServerMetadata` to include the new field:

```go
type BaseServerMetadata struct {
    // ... existing fields ...
    Tools           []string          `json:"tools" yaml:"tools"`
    ToolDefinitions []*ToolDefinition `json:"tool_definitions,omitempty" yaml:"tool_definitions,omitempty"`
    // ... remaining fields ...
}
```

## Converter Updates

### ToolHive to Upstream (`toolhive_to_upstream.go`)

Add `tool_definitions` to the extension creation functions:

```go
if len(metadata.ToolDefinitions) > 0 {
    extensions["tool_definitions"] = metadata.ToolDefinitions
}
```

### Upstream to ToolHive (`upstream_to_toolhive.go`)

Extract `tool_definitions` from the extension data:

```go
if toolDefs, ok := extensions["tool_definitions"].([]interface{}); ok {
    metadata.ToolDefinitions = remarshalToType[[]*ToolDefinition](toolDefs)
}
```

## Backward Compatibility

- The `tools` field remains unchanged and continues to store tool names as strings
- Consumers that only read `tools` are unaffected
- The `tool_definitions` field is optional; servers without it continue to work
- When both fields exist, `tool_definitions` is the authoritative source; `tools` serves as a quick lookup

## Use Cases

### 1. Enhanced `thv list` Output

```
$ thv list --show-tools
NAME           TOOLS
weather-mcp    get_weather (Get current weather for a location)
               search_location (Find location coordinates)
```

### 2. API Contract Breakage Detection

By storing input/output schemas in the registry, tooling can detect breaking changes when MCP servers are updated:

- Compare stored `inputSchema` against the live server's schema to detect removed or renamed required fields
- Detect type changes (e.g., `string` to `number`) that would break existing clients
- Identify when tools are removed or renamed between versions
- Enable CI/CD gates that prevent publishing servers with breaking changes

### 3. Client Tooling Integration

IDEs and MCP clients can display tool signatures and validate inputs before calling servers.

## What We Are Not Doing

- Not changing how tools are discovered at runtime (still via `tools/list`)
- Not requiring `tool_definitions` for registry entries
- Not adding new publisher extension namespaces
- Not modifying the upstream MCP Registry schema

## Implementation Notes

- Tool definitions can be populated by registry maintainers or automated tooling that queries running servers
- The `tools` array should be kept in sync with `tool_definitions[*].name` for consistency
- Schema validation should accept both minimal entries (name only) and complete entries

## Outcome

- Richer tool metadata available without starting MCP servers
- Better discoverability through descriptions and schemas
- Foundation for advanced filtering and aggregation features
- Maintains full backward compatibility with existing registry consumers

# Converter Test Fixtures

This directory contains JSON fixture files for testing the converter functions between ToolHive ImageMetadata/RemoteServerMetadata and official MCP ServerJSON formats.

## Directory Structure

```
testdata/
├── image_to_server/       # ImageMetadata → ServerJSON conversions
├── server_to_image/       # ServerJSON → ImageMetadata conversions
├── remote_to_server/      # RemoteServerMetadata → ServerJSON conversions
└── server_to_remote/      # ServerJSON → RemoteServerMetadata conversions
```

Each directory contains:
- `input_*.json` - Input data for the conversion
- `expected_*.json` - Expected output after conversion

## Test Coverage

### Image-based Servers

**GitHub Server** (`image_to_server/input_github.json`)
- Full production example with 46 tools
- 5 environment variables (including optional ones)
- Permissions and provenance metadata
- Demonstrates complete ToolHive → Official MCP conversion

**Round-trip** (`server_to_image/`)
- Uses the output from `image_to_server` as input
- Validates bidirectional conversion without data loss
- Ensures all fields are preserved through the conversion cycle

### Remote Servers

**Example Remote** (`remote_to_server/input_example.json`)
- SSE transport type
- Multiple headers (required and optional)
- Demonstrates remote server conversion pattern

**Round-trip** (`server_to_remote/`)
- Validates remote server bidirectional conversion
- Ensures headers and metadata are preserved

## Usage in Tests

The fixtures are used by `converters_fixture_test.go`:

```go
func TestConverters_Fixtures(t *testing.T) {
    // Table-driven test that:
    // 1. Loads input fixture
    // 2. Runs conversion
    // 3. Compares with expected output
    // 4. Runs additional validation checks
}
```

## Adding New Test Cases

To add a new test case:

1. **Create input file** in the appropriate directory:
   ```bash
   # For image-based server
   vi testdata/image_to_server/input_myserver.json
   ```

2. **Generate expected output** using the converter:
   ```go
   // Example code to generate expected output
   imageMetadata := loadFromFile("input_myserver.json")
   serverJSON, _ := ImageMetadataToServerJSON("myserver", imageMetadata)
   saveToFile("expected_myserver.json", serverJSON)
   ```

3. **Add test case** to `converters_fixture_test.go`:
   ```go
   {
       name:         "ImageMetadata to ServerJSON - MyServer",
       fixtureDir:   "testdata/image_to_server",
       inputFile:    "input_myserver.json",
       expectedFile: "expected_myserver.json",
       serverName:   "myserver",
       convertFunc:  "ImageToServer",
       validateFunc: validateImageToServerConversion,
   },
   ```

## Fixture Format Examples

### ImageMetadata Format
```json
{
  "description": "Server description",
  "tier": "Official",
  "status": "Active",
  "transport": "stdio",
  "tools": ["tool1", "tool2"],
  "image": "ghcr.io/org/server:v1.0.0",
  "env_vars": [...],
  "permissions": {...},
  "provenance": {...}
}
```

### ServerJSON Format
```json
{
  "$schema": "https://static.modelcontextprotocol.io/schemas/2025-10-17/server.schema.json",
  "name": "io.github.stacklok/server",
  "description": "Server description",
  "version": "1.0.0",
  "packages": [{
    "registryType": "oci",
    "identifier": "ghcr.io/org/server:v1.0.0",
    "transport": {"type": "stdio"},
    "environmentVariables": [...]
  }],
  "_meta": {
    "io.modelcontextprotocol.registry/publisher-provided": {
      "io.github.stacklok": {
        "ghcr.io/org/server:v1.0.0": {
          "status": "Active",
          "tier": "Official",
          "tools": [...],
          ...
        }
      }
    }
  }
}
```

### RemoteServerMetadata Format
```json
{
  "description": "Remote server description",
  "transport": "sse",
  "url": "https://api.example.com/mcp",
  "headers": [
    {
      "name": "Authorization",
      "description": "Auth header",
      "required": true,
      "secret": true
    }
  ],
  "tools": [...],
  "tags": [...]
}
```

## Benefits of Fixture-based Testing

1. **Visual Inspection** - Easy to see exactly what data is being transformed
2. **Maintainability** - Update fixtures without changing test code
3. **Documentation** - Fixtures serve as examples of expected formats
4. **Regression Detection** - Any changes to output format are immediately visible
5. **Multiple Scenarios** - Easy to add edge cases and variants

## Regenerating Fixtures

If the converter logic changes and you need to regenerate expected outputs:

```bash
# Regenerate all expected outputs
go run scripts/regenerate_fixtures.go

# Or regenerate specific ones
go run scripts/regenerate_fixtures.go --type image_to_server --name github
```

(Note: Create the regenerate script if needed)
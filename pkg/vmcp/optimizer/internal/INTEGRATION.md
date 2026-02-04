# Integrating Optimizer with vMCP

## Overview

The optimizer package ingests MCP server and tool metadata into a searchable database with semantic embeddings. This enables intelligent tool discovery and token optimization for LLM consumption.

## Integration Approach

**Event-Driven Ingestion**: The optimizer integrates directly with vMCP's startup process. When vMCP starts and loads its configured servers, it calls the optimizer to ingest each server's metadata and tools.

❌ **NOT** a separate polling service discovering backends
✅ **IS** called directly by vMCP during server initialization

## How It Is Integrated

The optimizer is already integrated into vMCP and works automatically when enabled via configuration. Here's how the integration works:

### Initialization

When vMCP starts with optimizer enabled in the configuration, it:

1. Initializes the optimizer database (chromem-go + SQLite FTS5)
2. Configures the embedding backend (placeholder, Ollama, or vLLM)
3. Sets up the ingestion service

### Automatic Ingestion

The optimizer integrates with vMCP's `OnRegisterSession` hook, which is called whenever:

- vMCP starts and loads configured MCP servers
- A new MCP server is dynamically added
- A session reconnects or refreshes

When this hook is triggered, the optimizer:

1. Retrieves the server's metadata and tools via MCP protocol
2. Generates embeddings for searchable content
3. Stores the data in both the vector database (chromem-go) and FTS5 database
4. Makes the tools immediately available for semantic search

### Exposed Tools

When the optimizer is enabled, vMCP automatically exposes these tools to LLM clients:

- `optim.find_tool`: Semantic search for tools across all registered servers
- `optim.call_tool`: Dynamic tool invocation after discovery

### Implementation Location

The integration code is located in:
- `cmd/vmcp/optimizer.go`: Optimizer initialization and configuration
- `pkg/vmcp/optimizer/optimizer.go`: Session registration hook implementation
- `cmd/thv-operator/pkg/optimizer/ingestion/service.go`: Core ingestion service

## Configuration

Add optimizer configuration to vMCP's config:

```yaml
# vMCP config
optimizer:
  enabled: true
  db_path: /data/optimizer.db
  embedding:
    backend: vllm  # or "ollama" for local dev, "placeholder" for testing
    url: http://vllm-service:8000
    model: sentence-transformers/all-MiniLM-L6-v2
    dimension: 384
```

## Error Handling

**Important**: Optimizer failures should NOT break vMCP functionality:

- ✅ Log warnings if optimizer fails
- ✅ Continue server startup even if ingestion fails
- ✅ Run ingestion in goroutines to avoid blocking
- ❌ Don't fail server startup if optimizer is unavailable

## Benefits

1. **Automatic**: Servers are indexed as they're added to vMCP
2. **Up-to-date**: Database reflects current vMCP state
3. **No polling**: Event-driven, efficient
4. **Semantic search**: Enables intelligent tool discovery
5. **Token optimization**: Tracks token usage for LLM efficiency

## Testing

```go
func TestOptimizerIntegration(t *testing.T) {
    // Initialize optimizer
    optimizerSvc, err := ingestion.NewService(&ingestion.Config{
        DBConfig: &db.Config{Path: "/tmp/test-optimizer.db"},
        EmbeddingConfig: &embeddings.Config{
            BackendType: "ollama",
            BaseURL:     "http://localhost:11434",
            Model:       "all-minilm",
            Dimension:   384,
            Dimension:   384,
        },
    })
    require.NoError(t, err)
    defer optimizerSvc.Close()
    
    // Simulate vMCP starting a server
    ctx := context.Background()
    tools := []mcp.Tool{
        {Name: "get_weather", Description: "Get current weather"},
        {Name: "get_forecast", Description: "Get weather forecast"},
    }
    
    err = optimizerSvc.IngestServer(
        ctx,
        "weather-001",
        "weather-service",
        "http://weather.local",
        models.TransportSSE,
        ptr("Weather information service"),
        tools,
    )
    require.NoError(t, err)
    
    // Verify ingestion
    server, err := optimizerSvc.GetServer(ctx, "weather-001")
    require.NoError(t, err)
    assert.Equal(t, "weather-service", server.Name)
}
```

## See Also

- [Optimizer Package README](./README.md) - Package overview and API


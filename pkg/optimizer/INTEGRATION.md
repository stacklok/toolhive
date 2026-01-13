# Integrating Optimizer with vMCP

## Overview

The optimizer package ingests MCP server and tool metadata into a searchable database with semantic embeddings. This enables intelligent tool discovery and token optimization for LLM consumption.

## Integration Approach

**Event-Driven Ingestion**: The optimizer integrates directly with vMCP's startup process. When vMCP starts and loads its configured servers, it calls the optimizer to ingest each server's metadata and tools.

❌ **NOT** a separate polling service discovering backends
✅ **IS** called directly by vMCP during server initialization

## How to Integrate

### 1. Initialize the Optimizer Service (Once at vMCP Startup)

```go
import (
    "github.com/stacklok/toolhive/pkg/optimizer/db"
    "github.com/stacklok/toolhive/pkg/optimizer/embeddings"
    "github.com/stacklok/toolhive/pkg/optimizer/ingestion"
)

// During vMCP initialization
func initializeOptimizer() (*ingestion.Service, error) {
    // Initialize optimizer service
    optimizerSvc, err := ingestion.NewService(&ingestion.Config{
        DBConfig: &db.Config{
            Path: "/data/optimizer.db",
        },
        EmbeddingConfig: &embeddings.Config{
            BackendType:  "vllm",  // or "ollama" for local dev
            BaseURL:      os.Getenv("VLLM_URL"),
            Model:        "sentence-transformers/all-MiniLM-L6-v2",
            Dimension:    384,
            EnableCache:  true,
            MaxCacheSize: 1000,
        },
    })
    if err != nil {
        return nil, fmt.Errorf("failed to initialize optimizer: %w", err)
    }
    
    return optimizerSvc, nil
}
```

### 2. Ingest Each Server (When Server Starts/Registers)

Call `IngestServer()` whenever a server is added to vMCP:

```go
import (
    "github.com/stacklok/toolhive/pkg/optimizer/models"
)

// When a server is registered/started in vMCP
func onServerStart(serverConfig ServerConfig, optimizerSvc *ingestion.Service) error {
    ctx := context.Background()
    
    // Get tools from the MCP server
    tools, err := mcpClient.ListTools(ctx)
    if err != nil {
        return fmt.Errorf("failed to list tools: %w", err)
    }
    
    // Ingest into optimizer
    err = optimizerSvc.IngestServer(
        ctx,
        serverConfig.ID,          // Unique server ID
        serverConfig.Name,        // Server name (e.g., "weather-service")
        serverConfig.URL,         // Server URL
        models.TransportSSE,      // Transport type
        &serverConfig.Description, // Optional description
        tools,                    // Tools from server
    )
    if err != nil {
        log.Printf("Failed to ingest server %s into optimizer: %v", 
            serverConfig.Name, err)
        // Non-fatal - don't block server startup
    }
    
    return nil
}
```

### 3. Integration Points in vMCP

Add optimizer ingestion calls at these points:

#### A. Group Initialization (Startup)

When vMCP loads startup groups:

```go
func (m *Manager) InitializeGroups(ctx context.Context, groups []Group) error {
    for _, group := range groups {
        for _, server := range group.Servers {
            // Start the server
            if err := m.StartServer(ctx, server); err != nil {
                return err
            }
            
            // Ingest into optimizer
            if m.optimizerSvc != nil {
                tools, _ := server.mcpClient.ListTools(ctx)
                _ = m.optimizerSvc.IngestServer(
                    ctx,
                    server.ID,
                    server.Name,
                    server.URL,
                    models.TransportSSE,
                    &server.Description,
                    tools,
                )
            }
        }
    }
    return nil
}
```

#### B. Dynamic Server Addition

When a server is added via API or CLI:

```go
func (api *API) AddServer(ctx context.Context, req AddServerRequest) error {
    // Add server to vMCP
    server, err := api.manager.AddServer(ctx, req)
    if err != nil {
        return err
    }
    
    // Ingest into optimizer
    if api.optimizerSvc != nil {
        tools, _ := server.mcpClient.ListTools(ctx)
        _ = api.optimizerSvc.IngestServer(
            ctx,
            server.ID,
            server.Name,
            server.URL,
            models.TransportSSE,
            &server.Description,
            tools,
        )
    }
    
    return nil
}
```

#### C. Tool Updates

When tools change (optional - can be done periodically or on-demand):

```go
func (m *Manager) RefreshServerTools(ctx context.Context, serverID string) error {
    server := m.GetServer(serverID)
    
    // Get updated tools
    tools, err := server.mcpClient.ListTools(ctx)
    if err != nil {
        return err
    }
    
    // Re-ingest to update optimizer database
    if m.optimizerSvc != nil {
        _ = m.optimizerSvc.IngestServer(
            ctx,
            server.ID,
            server.Name,
            server.URL,
            models.TransportSSE,
            &server.Description,
            tools,
        )
    }
    
    return nil
}
```

## Example: Complete Integration in vMCP Server

```go
package vmcp

import (
    "context"
    "os"
    
    "github.com/stacklok/toolhive/pkg/optimizer/db"
    "github.com/stacklok/toolhive/pkg/optimizer/embeddings"
    "github.com/stacklok/toolhive/pkg/optimizer/ingestion"
    "github.com/stacklok/toolhive/pkg/optimizer/models"
)

type Server struct {
    manager      *Manager
    optimizerSvc *ingestion.Service
}

func NewServer(config *Config) (*Server, error) {
    // Initialize vMCP manager
    manager := NewManager(config)
    
    // Initialize optimizer (if enabled)
    var optimizerSvc *ingestion.Service
    if config.OptimizerEnabled {
        var err error
        optimizerSvc, err = ingestion.NewService(&ingestion.Config{
            DBConfig: &db.Config{
                Path: config.OptimizerDBPath,
            },
            EmbeddingConfig: &embeddings.Config{
                BackendType:  os.Getenv("EMBEDDING_BACKEND"),  // "vllm" or "ollama"
                BaseURL:      os.Getenv("EMBEDDING_URL"),
                Model:        os.Getenv("EMBEDDING_MODEL"),
                Dimension:    384,
                EnableCache:  true,
                MaxCacheSize: 1000,
            },
        })
        if err != nil {
            log.Printf("Warning: Failed to initialize optimizer: %v", err)
            // Non-fatal - vMCP can run without optimizer
        }
    }
    
    return &Server{
        manager:      manager,
        optimizerSvc: optimizerSvc,
    }, nil
}

func (s *Server) Start(ctx context.Context) error {
    // Load and start all configured groups
    groups := s.manager.LoadGroups()
    
    for _, group := range groups {
        for _, serverConfig := range group.Servers {
            // Start the MCP server
            server, err := s.manager.StartServer(ctx, serverConfig)
            if err != nil {
                log.Printf("Failed to start server %s: %v", serverConfig.Name, err)
                continue
            }
            
            // Ingest into optimizer (non-blocking, best-effort)
            if s.optimizerSvc != nil {
                go s.ingestServer(ctx, server)
            }
        }
    }
    
    // Start vMCP API server
    return s.manager.Serve(ctx)
}

func (s *Server) ingestServer(ctx context.Context, server *MCPServer) {
    // Get tools from server
    tools, err := server.Client.ListTools(ctx)
    if err != nil {
        log.Printf("Warning: Failed to list tools from %s: %v", server.Name, err)
        return
    }
    
    // Ingest into optimizer
    err = s.optimizerSvc.IngestServer(
        ctx,
        server.ID,
        server.Name,
        server.URL,
        models.TransportSSE,
        &server.Description,
        tools,
    )
    if err != nil {
        log.Printf("Warning: Failed to ingest server %s into optimizer: %v", 
            server.Name, err)
    } else {
        log.Printf("Successfully ingested %s into optimizer (%d tools)", 
            server.Name, len(tools))
    }
}
```

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
            BackendType: "placeholder",
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
- [Embeddings README](./embeddings/README.md) - Embedding backend configuration


# VMCPOptimizer Package

This package provides semantic tool discovery for Virtual MCP Server, reducing token usage by allowing LLMs to discover relevant tools on-demand instead of receiving all tool definitions upfront.

## Architecture

The optimizer exposes a clean interface-based architecture:

```
pkg/vmcp/optimizer/
├── optimizer.go        # Public Optimizer interface and EmbeddingOptimizer implementation  
├── config.go           # Configuration types
├── README.md           # This file
└── internal/           # Implementation details (not part of public API)
    ├── embeddings/     # Embedding backends (Ollama, OpenAI-compatible, vLLM)
    ├── db/             # Database operations (chromem-go vectors, SQLite FTS5)
    ├── ingestion/      # Tool ingestion service
    ├── models/         # Internal data models
    └── tokens/         # Token counting utilities
```

## Public API

### Optimizer Interface

```go
type Optimizer interface {
    // FindTool searches for tools matching the description and keywords
    FindTool(ctx context.Context, input FindToolInput) (*FindToolOutput, error)
    
    // CallTool invokes a tool by name with parameters
    CallTool(ctx context.Context, input CallToolInput) (*mcp.CallToolResult, error)
    
    // Close cleans up optimizer resources
    Close() error
    
    // HandleSessionRegistration handles session setup for optimizer mode
    HandleSessionRegistration(...) (bool, error)
    
    // OptimizerHandlerProvider provides tool handlers for MCP integration
    adapter.OptimizerHandlerProvider
}
```

### Factory Pattern

```go
// Factory creates an Optimizer instance
type Factory func(
    ctx context.Context,
    cfg *Config,
    mcpServer *server.MCPServer,
    backendClient vmcp.BackendClient,
    sessionManager *transportsession.Manager,
) (Optimizer, error)

// NewEmbeddingOptimizer is the production implementation
func NewEmbeddingOptimizer(...) (Optimizer, error)
```

## Usage

### In vMCP Server

```go
import "github.com/stacklok/toolhive/pkg/vmcp/optimizer"

// Configure server with optimizer
serverCfg := &vmcpserver.Config{
    OptimizerFactory: optimizer.NewEmbeddingOptimizer,
    OptimizerConfig: &optimizer.Config{
        Enabled: true,
        PersistPath: "/data/optimizer",
        HybridSearchRatio: 70, // 70% semantic, 30% keyword
        EmbeddingConfig: &embeddings.Config{
            BackendType: "ollama",
            BaseURL: "http://localhost:11434",
            Model: "nomic-embed-text",
            Dimension: 768,
        },
    },
}
```

### MCP Tools Exposed

When the optimizer is enabled, vMCP exposes two tools instead of all backend tools:

1. **`optim_find_tool`**: Semantic search for tools
   - Input: `tool_description` (natural language), optional `tool_keywords`, `limit`
   - Output: Ranked tools with similarity scores and token metrics

2. **`optim_call_tool`**: Dynamic tool invocation  
   - Input: `backend_id`, `tool_name`, `parameters`
   - Output: Tool execution result

## Benefits

- **Token Savings**: Only relevant tools are sent to the LLM (typically 80-95% reduction)
- **Hybrid Search**: Combines semantic embeddings (70%) with BM25 keyword matching (30%)
- **Startup Ingestion**: Tools are indexed once at startup, not per-session
- **Clean Architecture**: Interface-based design allows easy testing and alternative implementations

## Implementation Details

The `internal/` directory contains implementation details that are not part of the public API:

- **embeddings/**: Pluggable embedding backends (Ollama, vLLM, OpenAI-compatible)
- **db/**: Hybrid search using chromem-go (vector DB) + SQLite FTS5 (BM25)
- **ingestion/**: Tool ingestion pipeline with background embedding generation
- **models/**: Internal data structures for backend tools and metadata
- **tokens/**: Token counting for metrics calculation

These internal packages use internal import paths and cannot be imported from outside the optimizer package.

## Testing

The interface-based design enables easy testing:

```go
// Mock the interface for unit tests
mockOpt := mocks.NewMockOptimizer(ctrl)
mockOpt.EXPECT().FindTool(...).Return(...)
mockOpt.EXPECT().Close()

// Use in server configuration
cfg.Optimizer = mockOpt
```

## Migration from Integration Pattern

Previous versions used an `Integration` interface. The current `Optimizer` interface provides the same functionality with cleaner separation of concerns:

**Before (Integration):**
- `OptimizerIntegration optimizer.Integration`
- `optimizer.NewIntegration(...)`

**After (Optimizer):**
- `Optimizer optimizer.Optimizer`  
- `OptimizerFactory optimizer.Factory`
- `optimizer.NewEmbeddingOptimizer(...)`

The factory pattern allows the server to create the optimizer at startup with all necessary dependencies.

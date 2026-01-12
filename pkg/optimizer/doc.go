// Package optimizer provides semantic tool discovery and ingestion for MCP servers.
//
// The optimizer package implements an ingestion service that discovers MCP workloads
// from ToolHive, generates semantic embeddings for tools using ONNX Runtime, and stores
// them in a SQLite database with vector search capabilities.
//
// # Architecture
//
// The optimizer follows a similar architecture to mcp-optimizer (Python) but adapted
// for Go idioms and patterns:
//
//	pkg/optimizer/
//	├── doc.go                    // Package documentation
//	├── models/                   // Database models and types
//	│   ├── models.go            // Core domain models (Server, Tool, etc.)
//	│   └── transport.go         // Transport and status enums
//	├── db/                       // Database layer
//	│   ├── db.go                // Database connection and config
//	│   ├── migrations/          // SQL migration files
//	│   │   └── 001_initial.sql
//	│   ├── workload_server.go   // Workload server operations
//	│   ├── workload_tool.go     // Workload tool operations
//	│   ├── registry_server.go   // Registry server operations
//	│   └── registry_tool.go     // Registry tool operations
//	├── embeddings/              // Embedding generation
//	│   ├── manager.go           // Embedding manager with ONNX Runtime
//	│   └── cache.go             // Optional embedding cache
//	├── mcpclient/               // MCP client for tool discovery
//	│   └── client.go            // MCP client wrapper
//	├── ingestion/               // Core ingestion service
//	│   ├── service.go           // Ingestion service implementation
//	│   └── errors.go            // Custom errors
//	└── tokens/                  // Token counting (for LLM consumption)
//	    └── counter.go           // Token counter using tiktoken-go
//
// # Core Concepts
//
// **Ingestion**: Discovers MCP workloads from ToolHive (via Docker or Kubernetes),
// connects to each workload to list tools, generates embeddings, and stores in database.
//
// **Embeddings**: Uses ONNX Runtime to generate semantic embeddings for tools and servers.
// Embeddings enable semantic search to find relevant tools based on natural language queries.
//
// **Database**: SQLite with sqlite-vec extension for vector similarity search. Separates
// registry servers (from catalog) and workload servers (running instances).
//
// **Token Counting**: Tracks token counts for tools to measure LLM consumption and
// calculate token savings from semantic filtering.
//
// # Usage
//
// The optimizer can be used in two ways:
//
//  1. **Standalone Command**: Run as a separate `thv optimizer` command for testing
//     and development.
//
//  2. **Integrated Service**: Run as a goroutine within the vMCP process for production
//     use, enabling semantic tool discovery and routing.
//
// Example standalone usage:
//
//	// Start ingestion
//	thv optimizer ingest --runtime docker
//
//	// Query tools
//	thv optimizer query "get current time"
//
// Example integrated usage:
//
//	import "github.com/stacklok/toolhive/pkg/optimizer/ingestion"
//
//	// Create ingestion service
//	svc, err := ingestion.NewService(config)
//	if err != nil {
//	    log.Fatal(err)
//	}
//
//	// Run as goroutine with periodic polling
//	go svc.StartPolling(ctx, 30*time.Second)
package optimizer

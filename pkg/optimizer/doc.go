// Package optimizer provides semantic tool discovery and ingestion for MCP servers.
//
// The optimizer package implements an ingestion service that discovers MCP backends
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
//	│   ├── fts.go               // FTS5 database for BM25 search
//	│   ├── schema_fts.sql       // Embedded FTS5 schema (executed directly)
//	│   ├── hybrid.go            // Hybrid search (semantic + BM25)
//	│   ├── backend_server.go    // Backend server operations
//	│   └── backend_tool.go      // Backend tool operations
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
// **Ingestion**: Discovers MCP backends from ToolHive (via Docker or Kubernetes),
// connects to each backend to list tools, generates embeddings, and stores in database.
//
// **Embeddings**: Uses ONNX Runtime to generate semantic embeddings for tools and servers.
// Embeddings enable semantic search to find relevant tools based on natural language queries.
//
// **Database**: Hybrid approach using chromem-go for vector search and SQLite FTS5 for
// keyword search. The database is ephemeral (in-memory by default, optional persistence)
// and schema is initialized directly on startup without migrations.
//
// **Terminology**: Uses "BackendServer" and "BackendTool" to explicitly refer to MCP server
// metadata, distinguishing from vMCP's broader "Backend" concept which represents workloads.
//
// **Token Counting**: Tracks token counts for tools to measure LLM consumption and
// calculate token savings from semantic filtering.
//
// # Usage
//
// The optimizer is integrated into vMCP as native tools:
//
//  1. **vMCP Integration**: The optimizer runs as part of vMCP, exposing
//     optim.find_tool and optim.call_tool to clients.
//
//  2. **Event-Driven Ingestion**: Tools are ingested when vMCP sessions
//     are registered, not via polling.
//
// Example vMCP integration (see pkg/vmcp/optimizer):
//
//	import (
//	    "github.com/stacklok/toolhive/pkg/optimizer/ingestion"
//	    "github.com/stacklok/toolhive/pkg/optimizer/embeddings"
//	)
//
//	// Create embedding manager
//	embMgr, err := embeddings.NewManager(embeddings.Config{
//	    BackendType: "ollama", // or "openai-compatible" or "vllm"
//	    BaseURL:     "http://localhost:11434",
//	    Model:       "all-minilm",
//	    Dimension:   384,
//	})
//
//	// Create ingestion service
//	svc, err := ingestion.NewService(ctx, ingestion.Config{
//	    DBConfig:    dbConfig,
//	}, embMgr)
//
//	// Ingest a server (called by vMCP's OnRegisterSession hook)
//	err = svc.IngestServer(ctx, "weather-service", tools, target)
package optimizer

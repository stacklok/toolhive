// Package vmcp provides the Virtual MCP Server implementation.
//
// Virtual MCP Server aggregates multiple MCP servers from a ToolHive group into a
// single unified interface. This package contains the core domain models and interfaces
// that are platform-agnostic (work for both CLI and Kubernetes deployments).
//
// # Architecture
//
// The vmcp package follows Domain-Driven Design (DDD) principles with clear
// separation of concerns into bounded contexts:
//
//	pkg/vmcp/
//	├── types.go              // Shared domain types (BackendTarget, Tool, etc.)
//	├── errors.go             // Domain errors
//	├── router/               // Request routing
//	│   └── router.go         // Router interface + routing strategies
//	├── aggregator/           // Capability aggregation
//	│   └── aggregator.go     // Aggregator interface + conflict resolution
//	├── auth/                 // Authentication (incoming & outgoing)
//	│   └── auth.go           // Auth interfaces + strategies
//	├── composer/             // Composite tool workflows
//	│   └── composer.go       // Composer interface + workflow engine
//	├── config/               // Configuration model
//	│   └── config.go         // Config types + loaders
//	└── cache/                // Token caching
//	    └── cache.go          // Cache interface + implementations
//
// # Core Concepts
//
// **Routing**: Forward MCP protocol requests (tools, resources, prompts) to
// appropriate backend workloads. Supports session affinity and load balancing.
//
// **Aggregation**: Discover backend capabilities, resolve naming conflicts,
// and merge into a unified view. Three-stage process: discovery, conflict
// resolution, merging.
//
// **Authentication**: Two-boundary model:
//   - Incoming: Clients authenticate to virtual MCP (OIDC, local, anonymous)
//   - Outgoing: Virtual MCP authenticates to backends (extensible strategies)
//
// **Composition**: Execute multi-step workflows across multiple backends.
// Supports sequential and parallel execution, elicitation, error handling.
//
// **Configuration**: Platform-agnostic config model with adapters for CLI
// (YAML) and Kubernetes (CRDs).
//
// **Caching**: Token caching to reduce auth overhead. Pluggable backends
// (memory, Redis).
//
// # Key Interfaces
//
// Router (pkg/vmcp/router):
//
//	type Router interface {
//		RouteTool(ctx context.Context, toolName string) (*vmcp.BackendTarget, error)
//		RouteResource(ctx context.Context, uri string) (*vmcp.BackendTarget, error)
//		RoutePrompt(ctx context.Context, name string) (*vmcp.BackendTarget, error)
//		UpdateRoutingTable(ctx context.Context, table *vmcp.RoutingTable) error
//	}
//
// Aggregator (pkg/vmcp/aggregator):
//
//	type Aggregator interface {
//		DiscoverBackends(ctx context.Context) ([]vmcp.Backend, error)
//		QueryCapabilities(ctx context.Context, backend vmcp.Backend) (*BackendCapabilities, error)
//		ResolveConflicts(ctx context.Context, capabilities map[string]*BackendCapabilities) (*ResolvedCapabilities, error)
//		MergeCapabilities(ctx context.Context, resolved *ResolvedCapabilities) (*AggregatedCapabilities, error)
//	}
//
// Composer (pkg/vmcp/composer):
//
//	type Composer interface {
//		ExecuteWorkflow(ctx context.Context, def *WorkflowDefinition, params map[string]any) (*WorkflowResult, error)
//		ValidateWorkflow(ctx context.Context, def *WorkflowDefinition) error
//		GetWorkflowStatus(ctx context.Context, workflowID string) (*WorkflowStatus, error)
//		CancelWorkflow(ctx context.Context, workflowID string) error
//	}
//
// IncomingAuthenticator (pkg/vmcp/auth):
//
//	type IncomingAuthenticator interface {
//		Authenticate(ctx context.Context, r *http.Request) (*Identity, error)
//		Middleware() func(http.Handler) http.Handler
//	}
//
// OutgoingAuthenticator (pkg/vmcp/auth):
//
//	type OutgoingAuthenticator interface {
//		AuthenticateRequest(ctx context.Context, req *http.Request, strategy string, metadata map[string]any) error
//		GetStrategy(name string) (AuthStrategy, error)
//		RegisterStrategy(name string, strategy AuthStrategy) error
//	}
//
// # Design Principles
//
//  1. Platform Independence: Core domain logic works for both CLI and Kubernetes
//  2. Interface Segregation: Small, focused interfaces for better testability
//  3. Dependency Inversion: Depend on abstractions, not concrete implementations
//  4. Modularity: Each bounded context can be developed and tested independently
//  5. Extensibility: Plugin architecture for auth strategies, routing strategies, etc.
//  6. Type Safety: Shared types at package root avoid circular dependencies
//
// # Usage Example
//
//	import (
//		"github.com/stacklok/toolhive/pkg/vmcp"
//		"github.com/stacklok/toolhive/pkg/vmcp/router"
//		"github.com/stacklok/toolhive/pkg/vmcp/aggregator"
//		"github.com/stacklok/toolhive/pkg/vmcp/auth"
//	)
//
//	// Load configuration
//	cfg, err := loadConfig("vmcp-config.yaml")
//	if err != nil {
//		return err
//	}
//
//	// Create components
//	agg := createAggregator(cfg)
//	rtr := createRouter(cfg)
//	inAuth := createIncomingAuth(cfg)
//	outAuth := createOutgoingAuth(cfg)
//
//	// Discover and aggregate backends
//	backends, err := agg.DiscoverBackends(ctx)
//	capabilities, err := agg.AggregateCapabilities(ctx, backends)
//
//	// Update router with aggregated capabilities
//	err = rtr.UpdateRoutingTable(ctx, capabilities.RoutingTable)
//
//	// Handle incoming requests
//	http.Handle("/tools/call", inAuth.Middleware()(
//		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
//			// Authenticate request
//			identity, err := inAuth.Authenticate(ctx, r)
//
//			// Route to backend
//			target, err := rtr.RouteTool(ctx, toolName)
//
//			// Authenticate to backend
//			backendReq := createBackendRequest(...)
//			err = outAuth.AuthenticateRequest(ctx, backendReq, target.AuthStrategy, target.AuthMetadata)
//
//			// Forward request and return response
//			// ...
//		}),
//	))
//
// # Related Documentation
//
// - Proposal: docs/proposals/THV-2106-virtual-mcp-server.md
// - GitHub Issues: #146-159 in stacklok/stacklok-epics
// - MCP Specification: https://modelcontextprotocol.io/specification
//
// See individual subpackage documentation for detailed usage and examples.
package vmcp

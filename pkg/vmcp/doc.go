// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

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
//	├── core/                 // Identity-explicit vMCP domain object
//	│   ├── core.go           // VMCP interface + Config
//	│   └── core_vmcp.go      // core.New composition
//	├── server/               // HTTP + MCP protocol transport
//	│   ├── serve.go          // server.Serve transport entry point
//	│   └── server.go         // Stable server.New wrapper + Handler/Start/Stop
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
// **Core/Transport Split**: pkg/vmcp/core owns the identity-explicit VMCP
// domain object. pkg/vmcp/server owns the MCP/HTTP transport that serves that
// domain object to clients.
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
// VMCP (pkg/vmcp/core):
//
//	type VMCP interface {
//		ListTools(ctx context.Context, identity *auth.Identity) ([]vmcp.Tool, error)
//		CallTool(ctx context.Context, identity *auth.Identity, name string, args, meta map[string]any) (*vmcp.ToolCallResult, error)
//		ListResources(ctx context.Context, identity *auth.Identity) ([]vmcp.Resource, error)
//		ReadResource(ctx context.Context, identity *auth.Identity, uri string) (*vmcp.ResourceReadResult, error)
//		ListPrompts(ctx context.Context, identity *auth.Identity) ([]vmcp.Prompt, error)
//		GetPrompt(ctx context.Context, identity *auth.Identity, name string, args map[string]any) (*vmcp.PromptGetResult, error)
//		// Lookup* helpers, BackendHealth, and Close omitted.
//	}
//
// The VMCP contract is the domain boundary: identity is always an explicit
// parameter and is never read from context, and the interface uses root
// pkg/vmcp domain types rather than mcp-go protocol types.
//
// Router (pkg/vmcp/router):
//
//	type Router interface {
//		RouteTool(ctx context.Context, toolName string) (*vmcp.BackendTarget, error)
//		RouteResource(ctx context.Context, uri string) (*vmcp.BackendTarget, error)
//		RoutePrompt(ctx context.Context, name string) (*vmcp.BackendTarget, error)
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
// OutgoingAuthRegistry (pkg/vmcp/auth):
//
//	type OutgoingAuthRegistry interface {
//		GetStrategy(name string) (Strategy, error)
//		RegisterStrategy(name string, strategy Strategy) error
//	}
//
// # Extension Model
//
// Domain-layer extension is done with decorators around an inner core.VMCP. A
// decorator filters List* output and refuses matching CallTool, ReadResource, or
// GetPrompt operations before delegating to inner. Because it only holds the
// inner VMCP, it can subtract reachability but cannot widen access to backends.
//
// Transport-layer extension is done by calling (*server.Server).Handler(ctx),
// mounting the fully composed handler in an embedder-owned mux, and applying
// outer middleware there. This preserves ToolHive's internal middleware order.
//
// # Design Principles
//
//  1. Platform Independence: Core domain logic works for both CLI and Kubernetes
//  2. Interface Segregation: Small, focused interfaces for better testability
//  3. Dependency Inversion: Depend on abstractions, not concrete implementations
//  4. Modularity: Each bounded context can be developed and tested independently
//  5. Extensibility: Decorators for domain behavior; outer handler wrapping for transport behavior.
//  6. Type Safety: Shared types at package root avoid circular dependencies
//
// # Usage Example
//
//	import (
//		vmcpcore "github.com/stacklok/toolhive/pkg/vmcp/core"
//		"github.com/stacklok/toolhive/pkg/vmcp/server"
//	)
//
//	// Build the domain object from already-created collaborators.
//	coreVMCP, err := vmcpcore.New(coreCfg)
//	if err != nil {
//		return err
//	}
//
//	// Wrap the domain object in the transport.
//	srv, err := server.Serve(ctx, coreVMCP, serverCfg)
//	if err != nil {
//		return err
//	}
//
//	// Let ToolHive own the listener.
//	if err := srv.Start(ctx); err != nil {
//		return err
//	}
//	defer srv.Stop(ctx)
//
//	// Or, instead of Start, mount the fully composed handler in an
//	// embedder-owned mux and serve sibling routes around it.
//	handler, err := srv.Handler(ctx)
//	if err != nil {
//		return err
//	}
//	rootMux.Handle("/", handler)
//
// # Related Documentation
//
// - Proposal: docs/proposals/THV-2106-virtual-mcp-server.md
// - GitHub Issues: #146-159 in stacklok/stacklok-epics
// - MCP Specification: https://modelcontextprotocol.io/specification
//
// See individual subpackage documentation for detailed usage and examples.
package vmcp

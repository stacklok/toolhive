// Package server implements the Virtual MCP Server that aggregates
// multiple backend MCP servers into a unified interface.
//
// The server exposes aggregated capabilities (tools, resources, prompts)
// and routes incoming MCP protocol requests to appropriate backend workloads.
package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/mark3labs/mcp-go/server"

	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/logger"
	transportsession "github.com/stacklok/toolhive/pkg/transport/session"
	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/aggregator"
	"github.com/stacklok/toolhive/pkg/vmcp/composer"
	"github.com/stacklok/toolhive/pkg/vmcp/discovery"
	"github.com/stacklok/toolhive/pkg/vmcp/router"
	"github.com/stacklok/toolhive/pkg/vmcp/server/adapter"
	vmcpsession "github.com/stacklok/toolhive/pkg/vmcp/session"
)

const (
	// defaultReadHeaderTimeout prevents slowloris attacks by limiting time to read request headers.
	defaultReadHeaderTimeout = 10 * time.Second

	// defaultReadTimeout is the maximum duration for reading the entire request, including body.
	defaultReadTimeout = 30 * time.Second

	// defaultWriteTimeout is the maximum duration before timing out writes of the response.
	defaultWriteTimeout = 30 * time.Second

	// defaultIdleTimeout is the maximum amount of time to wait for the next request when keep-alive's are enabled.
	defaultIdleTimeout = 120 * time.Second

	// defaultMaxHeaderBytes is the maximum size of request headers in bytes (1 MB).
	defaultMaxHeaderBytes = 1 << 20

	// defaultShutdownTimeout is the maximum time to wait for graceful shutdown.
	defaultShutdownTimeout = 10 * time.Second

	// defaultSessionTTL is the default session time-to-live duration.
	// Sessions that are inactive for this duration will be automatically cleaned up.
	defaultSessionTTL = 30 * time.Minute
)

// Config holds the Virtual MCP Server configuration.
type Config struct {
	// Name is the server name exposed in MCP protocol
	Name string

	// Version is the server version
	Version string

	// Host is the bind address (default: "127.0.0.1")
	Host string

	// Port is the bind port (default: 4483)
	Port int

	// EndpointPath is the MCP endpoint path (default: "/mcp")
	EndpointPath string

	// SessionTTL is the session time-to-live duration (default: 30 minutes)
	// Sessions inactive for this duration will be automatically cleaned up
	SessionTTL time.Duration

	// AuthMiddleware is the optional authentication middleware to apply to MCP routes.
	// If nil, no authentication is required.
	// This should be a composed middleware chain (e.g., TokenValidator → IdentityMiddleware).
	AuthMiddleware func(http.Handler) http.Handler

	// AuthInfoHandler is the optional handler for /.well-known/oauth-protected-resource endpoint.
	// Exposes OIDC discovery information about the protected resource.
	AuthInfoHandler http.Handler
}

// Server is the Virtual MCP Server that aggregates multiple backends.
type Server struct {
	config *Config

	// MCP protocol server (mark3labs/mcp-go)
	mcpServer *server.MCPServer

	// HTTP server for Streamable HTTP transport
	httpServer *http.Server

	// Network listener (tracks actual bound port when using port 0)
	listener   net.Listener
	listenerMu sync.RWMutex

	// Router for forwarding requests to backends
	router router.Router

	// Backend client for making requests to backends
	backendClient vmcp.BackendClient

	// Handler factory for creating MCP request handlers
	handlerFactory *adapter.DefaultHandlerFactory

	// Discovery manager for lazy per-user capability discovery
	discoveryMgr discovery.Manager

	// Backends for capability discovery
	backends []vmcp.Backend

	// Session manager for tracking MCP protocol sessions
	// This is ToolHive's session.Manager (pkg/transport/session) - the same component
	// used by streamable proxy for MCP session tracking. It handles:
	//   - Session storage and retrieval
	//   - TTL-based cleanup of inactive sessions
	//   - Session lifecycle management
	// The mark3labs SDK calls our sessionIDAdapter, which delegates to this manager.
	// The SDK does NOT manage sessions itself - it only provides the interface.
	sessionManager *transportsession.Manager

	// Capability adapter for converting aggregator types to SDK types
	capabilityAdapter *adapter.CapabilityAdapter

	// Composite tool workflow definitions keyed by tool name.
	// Initialized during construction and read-only thereafter.
	// Thread-safety: Safe for concurrent reads (no writes after initialization).
	workflowDefs map[string]*composer.WorkflowDefinition

	// Workflow executors for composite tools (adapters around composer + definition).
	// Used by capability adapter to create composite tool handlers.
	// Initialized during construction and read-only thereafter.
	// Thread-safety: Safe for concurrent reads (no writes after initialization).
	workflowExecutors map[string]adapter.WorkflowExecutor

	// Ready channel signals when the server is ready to accept connections.
	// Closed once the listener is created and serving.
	ready     chan struct{}
	readyOnce sync.Once
}

// New creates a new Virtual MCP Server instance.
//
//nolint:gocyclo // Complexity from hook logic is acceptable
func New(
	cfg *Config,
	rt router.Router,
	backendClient vmcp.BackendClient,
	discoveryMgr discovery.Manager,
	backends []vmcp.Backend,
	workflowDefs map[string]*composer.WorkflowDefinition,
) *Server {
	// Apply defaults
	if cfg.Host == "" {
		cfg.Host = "127.0.0.1"
	}
	if cfg.Port == 0 {
		cfg.Port = 4483
	}
	if cfg.EndpointPath == "" {
		cfg.EndpointPath = "/mcp"
	}
	if cfg.Name == "" {
		cfg.Name = "toolhive-vmcp"
	}
	if cfg.Version == "" {
		cfg.Version = "0.1.0"
	}
	if cfg.SessionTTL == 0 {
		cfg.SessionTTL = defaultSessionTTL
	}

	// Create hooks for SDK integration
	hooks := &server.Hooks{}

	// Create mark3labs MCP server
	mcpServer := server.NewMCPServer(
		cfg.Name,
		cfg.Version,
		server.WithToolCapabilities(false), // We'll register tools dynamically
		server.WithLogging(),
		server.WithHooks(hooks),
	)

	// Create SDK elicitation adapter for workflow engine
	// This wraps the mark3labs SDK to provide elicitation functionality to the composer
	sdkElicitationRequester := newSDKElicitationAdapter(mcpServer)

	// Create elicitation handler for workflow engine
	// This provides SDK-agnostic elicitation with security validation
	elicitationHandler := composer.NewDefaultElicitationHandler(sdkElicitationRequester)

	// Create workflow engine (composer) for executing composite tools
	// The composer orchestrates multi-step workflows across backends
	workflowComposer := composer.NewWorkflowEngine(rt, backendClient, elicitationHandler)

	// Validate workflows and create executors (filters out invalid workflows at startup)
	workflowDefs, workflowExecutors := validateAndCreateExecutors(workflowComposer, workflowDefs)

	// Create session manager with VMCPSession factory
	// This enables type-safe access to routing tables while maintaining session lifecycle management
	sessionManager := transportsession.NewManager(cfg.SessionTTL, vmcpsession.VMCPSessionFactory())

	// Create handler factory (used by adapter and for future dynamic registration)
	handlerFactory := adapter.NewDefaultHandlerFactory(rt, backendClient)

	// Create capability adapter (single source of truth for converting aggregator types to SDK types)
	capabilityAdapter := adapter.NewCapabilityAdapter(handlerFactory)

	// Create Server instance
	srv := &Server{
		config:            cfg,
		mcpServer:         mcpServer,
		router:            rt,
		backendClient:     backendClient,
		handlerFactory:    handlerFactory,
		discoveryMgr:      discoveryMgr,
		backends:          backends,
		sessionManager:    sessionManager,
		capabilityAdapter: capabilityAdapter,
		workflowDefs:      workflowDefs,
		workflowExecutors: workflowExecutors,
		ready:             make(chan struct{}),
	}

	// Register OnRegisterSession hook to inject capabilities after SDK registers session.
	// This hook fires AFTER the session is registered in the SDK (unlike AfterInitialize which
	// fires BEFORE session registration), allowing us to safely call AddSessionTools/AddSessionResources.
	//
	// The discovery middleware populates capabilities in the context, which is available here.
	// We inject them into the SDK session and store the routing table for subsequent requests.
	//
	// IMPORTANT: Session capabilities are immutable after injection.
	// - Capabilities discovered during initialize are fixed for the session lifetime
	// - Backend changes (new tools, removed resources) won't be reflected in existing sessions
	// - Clients must create new sessions to see updated capabilities
	// TODO(dynamic-capabilities): Consider implementing capability refresh mechanism when SDK supports it
	hooks.AddOnRegisterSession(func(ctx context.Context, session server.ClientSession) {
		sessionID := session.SessionID()
		logger.Debugw("OnRegisterSession hook called", "session_id", sessionID)

		// Get capabilities from context (discovered by middleware)
		caps, ok := discovery.DiscoveredCapabilitiesFromContext(ctx)
		if !ok || caps == nil {
			logger.Warnw("no discovered capabilities in context for OnRegisterSession hook",
				"session_id", sessionID)
			return
		}

		// Add composite tools to capabilities
		// Composite tools are static (from configuration) and not discovered from backends
		// They are added here to be exposed alongside backend tools in the session
		if len(srv.workflowDefs) > 0 {
			compositeTools := convertWorkflowDefsToTools(srv.workflowDefs)

			// Validate no conflicts between composite tool names and backend tool names
			if err := validateNoToolConflicts(caps.Tools, compositeTools); err != nil {
				logger.Errorw("composite tool name conflict detected",
					"session_id", sessionID,
					"error", err)
				// Don't add composite tools if there are conflicts
				// This prevents ambiguity in routing/execution
				return
			}

			caps.CompositeTools = compositeTools
			logger.Debugw("added composite tools to session capabilities",
				"session_id", sessionID,
				"composite_tool_count", len(compositeTools))
		}

		// Store routing table in VMCPSession for subsequent requests
		// This enables the middleware to reconstruct capabilities from session
		// without re-running discovery for every request
		vmcpSess, err := vmcpsession.GetVMCPSession(sessionID, sessionManager)
		if err != nil {
			logger.Errorw("failed to get VMCPSession for routing table storage",
				"error", err,
				"session_id", sessionID)
			return
		}

		vmcpSess.SetRoutingTable(caps.RoutingTable)
		logger.Debugw("routing table stored in VMCPSession",
			"session_id", sessionID,
			"tool_count", len(caps.RoutingTable.Tools),
			"resource_count", len(caps.RoutingTable.Resources),
			"prompt_count", len(caps.RoutingTable.Prompts))

		// Inject capabilities into SDK session
		if err := srv.injectCapabilities(sessionID, caps); err != nil {
			logger.Errorw("failed to inject session capabilities",
				"error", err,
				"session_id", sessionID)
			return
		}

		logger.Infow("session capabilities injected",
			"session_id", sessionID,
			"tool_count", len(caps.Tools),
			"resource_count", len(caps.Resources))
	})

	return srv
}

// Start starts the Virtual MCP Server and begins serving requests.
func (s *Server) Start(ctx context.Context) error {
	// Create session adapter to expose ToolHive's session.Manager via SDK interface
	// Sessions are ENTIRELY managed by ToolHive's session.Manager (storage, TTL, cleanup).
	// The SDK only calls our Generate/Validate/Terminate methods during MCP protocol flows.
	sessionAdapter := newSessionIDAdapter(s.sessionManager)

	// Create Streamable HTTP server with ToolHive session management
	streamableServer := server.NewStreamableHTTPServer(
		s.mcpServer,
		server.WithEndpointPath(s.config.EndpointPath),
		server.WithSessionIdManager(sessionAdapter),
	)

	// Create HTTP mux with separated authenticated and unauthenticated routes
	mux := http.NewServeMux()

	// Unauthenticated health endpoints
	mux.HandleFunc("/health", s.handleHealth)
	mux.HandleFunc("/ping", s.handleHealth)

	// Optional .well-known discovery endpoints (unauthenticated, RFC 9728 compliant)
	// Handles /.well-known/oauth-protected-resource and subpaths (e.g., /mcp)
	if wellKnownHandler := auth.NewWellKnownHandler(s.config.AuthInfoHandler); wellKnownHandler != nil {
		mux.Handle("/.well-known/", wellKnownHandler)
		logger.Info("RFC 9728 OAuth discovery endpoints enabled at /.well-known/")
	}

	// MCP endpoint - apply middleware chain: auth → discovery
	var mcpHandler http.Handler = streamableServer

	// Apply discovery middleware (runs after auth middleware)
	// Discovery middleware performs per-request capability aggregation with user context
	// Pass sessionManager to enable session-based capability retrieval for subsequent requests
	mcpHandler = discovery.Middleware(s.discoveryMgr, s.backends, s.sessionManager)(mcpHandler)
	logger.Info("Discovery middleware enabled for lazy per-user capability discovery")

	// Apply authentication middleware if configured (runs first in chain)
	if s.config.AuthMiddleware != nil {
		mcpHandler = s.config.AuthMiddleware(mcpHandler)
		logger.Info("Authentication middleware enabled for MCP endpoints")
	}

	mux.Handle("/", mcpHandler)

	// Create HTTP server
	addr := fmt.Sprintf("%s:%d", s.config.Host, s.config.Port)
	s.httpServer = &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: defaultReadHeaderTimeout,
		ReadTimeout:       defaultReadTimeout,
		WriteTimeout:      defaultWriteTimeout,
		IdleTimeout:       defaultIdleTimeout,
		MaxHeaderBytes:    defaultMaxHeaderBytes,
	}

	// Create listener (allows port 0 to bind to random available port)
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("failed to create listener: %w", err)
	}

	s.listenerMu.Lock()
	s.listener = listener
	s.listenerMu.Unlock()

	actualAddr := listener.Addr().String()
	logger.Infof("Starting Virtual MCP Server at %s%s", actualAddr, s.config.EndpointPath)
	logger.Infof("Health endpoints available at %s/health and %s/ping", actualAddr, actualAddr)

	// Start server in background
	errCh := make(chan error, 1)
	go func() {
		if err := s.httpServer.Serve(listener); err != nil && err != http.ErrServerClosed {
			errCh <- fmt.Errorf("HTTP server error: %w", err)
		}
	}()

	// Signal that the server is ready (listener created and serving started)
	s.readyOnce.Do(func() {
		close(s.ready)
	})

	// Wait for either context cancellation or server error
	select {
	case <-ctx.Done():
		logger.Info("Context cancelled, shutting down server")
		return s.Stop(context.Background())
	case err := <-errCh:
		return err
	}
}

// Stop gracefully stops the Virtual MCP Server.
func (s *Server) Stop(ctx context.Context) error {
	logger.Info("Stopping Virtual MCP Server")

	var errs []error

	// Stop HTTP server (this internally closes the listener)
	if s.httpServer != nil {
		// Create shutdown context with timeout
		shutdownCtx, cancel := context.WithTimeout(ctx, defaultShutdownTimeout)
		defer cancel()

		if err := s.httpServer.Shutdown(shutdownCtx); err != nil {
			errs = append(errs, fmt.Errorf("failed to shutdown HTTP server: %w", err))
		}
	}

	// Clear listener reference (already closed by httpServer.Shutdown)
	s.listenerMu.Lock()
	s.listener = nil
	s.listenerMu.Unlock()

	// Stop session manager after HTTP server shutdown
	if s.sessionManager != nil {
		if err := s.sessionManager.Stop(); err != nil {
			errs = append(errs, fmt.Errorf("failed to stop session manager: %w", err))
		}
	}

	if len(errs) > 0 {
		logger.Errorf("Errors during shutdown: %v", errs)
		return errors.Join(errs...)
	}

	logger.Info("Virtual MCP Server stopped")
	return nil
}

// Address returns the server's actual listen address.
// If the server is started with port 0, this returns the actual bound port.
func (s *Server) Address() string {
	s.listenerMu.RLock()
	defer s.listenerMu.RUnlock()

	if s.listener != nil {
		return s.listener.Addr().String()
	}
	return fmt.Sprintf("%s:%d", s.config.Host, s.config.Port)
}

// handleHealth handles /health and /ping HTTP requests.
// Returns 200 OK if the server is running and able to respond.
//
// Security Note: This endpoint is unauthenticated and intentionally minimal.
// It only confirms the HTTP server is responding. No version information,
// session counts, or operational metrics are exposed to prevent information
// disclosure in multi-tenant scenarios.
//
// For operational monitoring, implement an authenticated /metrics endpoint
// that requires proper authorization.
func (*Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	response := map[string]string{
		"status": "ok",
	}

	w.Header().Set("Content-Type", "application/json")
	// Always send 200 OK - even if JSON encoding fails below, the server is responding
	w.WriteHeader(http.StatusOK)

	// Encode response. If this fails (extremely unlikely for simple map[string]string),
	// the 200 OK status has already been sent above.
	if err := json.NewEncoder(w).Encode(response); err != nil {
		logger.Errorf("Failed to encode health response: %v", err)
	}
}

// SessionManager returns the session manager instance.
// This is useful for testing and monitoring.
func (s *Server) SessionManager() *transportsession.Manager {
	return s.sessionManager
}

// Ready returns a channel that is closed when the server is ready to accept connections.
// This is useful for testing and synchronization.
func (s *Server) Ready() <-chan struct{} {
	return s.ready
}

// injectCapabilities injects capabilities into a newly created SDK session.
//
// This method is called ONCE per session during the OnRegisterSession hook, when the
// session has just been created by the SDK and is empty. It:
//  1. Converts aggregator types to SDK types using adapter
//  2. Adds discovered backend capabilities via SDK APIs (AddSessionTools, AddSessionResources)
//  3. Adds composite tool capabilities with workflow handlers
//
// Important constraints:
//   - Called only during session creation (session state is empty)
//   - No previous capabilities exist, so no deletion needed
//   - Capabilities are IMMUTABLE for the session lifetime (see limitation below)
//   - Discovery middleware does not re-run for subsequent requests
//
// LIMITATION: Session capabilities are fixed at creation time.
// If backends change (new tools added, resources removed), existing sessions won't see updates.
// Clients must create new sessions to discover updated capabilities.
// This is a deliberate design choice to avoid notification spam and maintain simplicity.
// Future enhancement: Implement capability refresh mechanism when SDK provides support.
//
// Note: SDK v0.43.0 does not support per-session prompts yet.
func (s *Server) injectCapabilities(
	sessionID string,
	caps *aggregator.AggregatedCapabilities,
) error {
	// Convert and add backend tools
	if len(caps.Tools) > 0 {
		sdkTools, err := s.capabilityAdapter.ToSDKTools(caps.Tools)
		if err != nil {
			return fmt.Errorf("failed to convert tools to SDK format: %w", err)
		}

		if err := s.mcpServer.AddSessionTools(sessionID, sdkTools...); err != nil {
			return fmt.Errorf("failed to add session tools: %w", err)
		}
		logger.Debugw("added session backend tools", "session_id", sessionID, "count", len(sdkTools))
	}

	// Convert and add composite tools
	if len(caps.CompositeTools) > 0 {
		compositeSDKTools, err := s.capabilityAdapter.ToCompositeToolSDKTools(caps.CompositeTools, s.workflowExecutors)
		if err != nil {
			return fmt.Errorf("failed to convert composite tools to SDK format: %w", err)
		}

		if err := s.mcpServer.AddSessionTools(sessionID, compositeSDKTools...); err != nil {
			return fmt.Errorf("failed to add session composite tools: %w", err)
		}
		logger.Debugw("added session composite tools", "session_id", sessionID, "count", len(compositeSDKTools))
	}

	// Convert and add resources
	if len(caps.Resources) > 0 {
		sdkResources := s.capabilityAdapter.ToSDKResources(caps.Resources)

		if err := s.mcpServer.AddSessionResources(sessionID, sdkResources...); err != nil {
			return fmt.Errorf("failed to add session resources: %w", err)
		}
		logger.Debugw("added session resources", "session_id", sessionID, "count", len(sdkResources))
	}

	// Note: SDK v0.43.0 does not support per-session prompts yet.
	// Per-session prompts are required to maintain multi-tenant security isolation.
	// Prompts cannot be registered globally via mcpServer.AddPrompt() without leaking
	// capability information across session boundaries (e.g., admin prompts visible to regular users).
	if len(caps.Prompts) > 0 {
		logger.Warnw("prompts discovered but not exposed - awaiting SDK support for per-session prompts",
			"session_id", sessionID,
			"prompt_count", len(caps.Prompts),
			"sdk_version", "v0.43.0",
			"required_api", "AddSessionPrompts()",
			"reason", "multi-tenant security requires per-session capability isolation")
		// TODO(prompts): Implement when mark3labs/mcp-go adds AddSessionPrompts() API
		// Conversion logic already exists in capability_adapter.ToSDKPrompts()
		// Implementation will be: s.mcpServer.AddSessionPrompts(sessionID, sdkPrompts...)
	}

	logger.Infow("session capabilities injected during initialization",
		"session_id", sessionID,
		"backend_tools", len(caps.Tools),
		"composite_tools", len(caps.CompositeTools),
		"resources", len(caps.Resources))

	return nil
}

// validateAndCreateExecutors validates workflow definitions and creates executors for valid workflows.
//
// This function:
//  1. Validates each workflow definition (cycle detection, tool references, etc.)
//  2. Filters out invalid workflows (logs errors but continues)
//  3. Creates workflow executors only for valid workflows
//  4. Returns filtered maps containing only valid workflows
//
// Invalid workflows are excluded from registration to prevent runtime failures and
// security issues (resource exhaustion from cycles, information disclosure from errors).
func validateAndCreateExecutors(
	validator composer.Composer,
	workflowDefs map[string]*composer.WorkflowDefinition,
) (map[string]*composer.WorkflowDefinition, map[string]adapter.WorkflowExecutor) {
	if len(workflowDefs) == 0 {
		return make(map[string]*composer.WorkflowDefinition), make(map[string]adapter.WorkflowExecutor)
	}

	validDefs := make(map[string]*composer.WorkflowDefinition, len(workflowDefs))
	validExecutors := make(map[string]adapter.WorkflowExecutor, len(workflowDefs))

	for name, def := range workflowDefs {
		if err := validator.ValidateWorkflow(context.Background(), def); err != nil {
			logger.Errorf("Invalid workflow definition '%s' - excluding from capabilities: %v", name, err)
			continue
		}

		validDefs[name] = def
		validExecutors[name] = newComposerWorkflowExecutor(validator, def)
		logger.Debugf("Validated workflow definition: %s", name)
	}

	if len(validDefs) > 0 {
		logger.Infof("Loaded %d valid composite tool workflows", len(validDefs))
	}

	return validDefs, validExecutors
}

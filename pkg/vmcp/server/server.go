// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

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
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/mark3labs/mcp-go/server"

	"github.com/stacklok/toolhive/pkg/audit"
	"github.com/stacklok/toolhive/pkg/auth"
	mcpparser "github.com/stacklok/toolhive/pkg/mcp"
	"github.com/stacklok/toolhive/pkg/recovery"
	"github.com/stacklok/toolhive/pkg/telemetry"
	transportsession "github.com/stacklok/toolhive/pkg/transport/session"
	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/aggregator"
	"github.com/stacklok/toolhive/pkg/vmcp/composer"
	vmcpconfig "github.com/stacklok/toolhive/pkg/vmcp/config"
	"github.com/stacklok/toolhive/pkg/vmcp/discovery"
	"github.com/stacklok/toolhive/pkg/vmcp/health"
	"github.com/stacklok/toolhive/pkg/vmcp/optimizer"
	"github.com/stacklok/toolhive/pkg/vmcp/router"
	"github.com/stacklok/toolhive/pkg/vmcp/server/adapter"
	vmcpsession "github.com/stacklok/toolhive/pkg/vmcp/session"
	vmcpstatus "github.com/stacklok/toolhive/pkg/vmcp/status"
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

	// defaultHeartbeatInterval sends SSE heartbeat pings on GET connections.
	// Prevents proxies/load balancers from closing idle SSE connections.
	defaultHeartbeatInterval = 30 * time.Second

	// defaultSessionTTL is the default session time-to-live duration.
	// Sessions that are inactive for this duration will be automatically cleaned up.
	defaultSessionTTL = 30 * time.Minute
)

//go:generate mockgen -destination=mocks/mock_watcher.go -package=mocks -source=server.go Watcher

// Watcher is the interface for Kubernetes backend watcher integration.
// Used in dynamic mode (outgoingAuth.source: discovered) to gate readiness
// on controller-runtime cache sync before serving requests.
type Watcher interface {
	// WaitForCacheSync waits for the Kubernetes informer caches to sync.
	// Returns true if caches synced successfully, false on timeout or error.
	WaitForCacheSync(ctx context.Context) bool
}

// Config holds the Virtual MCP Server configuration.
type Config struct {
	// Name is the server name exposed in MCP protocol
	Name string

	// Version is the server version
	Version string

	// GroupRef is the name of the MCPGroup containing backend workloads.
	// Used for operational visibility in status endpoint and logging.
	GroupRef string

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

	// TelemetryProvider is the optional telemetry provider.
	// If nil, no telemetry is recorded.
	TelemetryProvider *telemetry.Provider

	// AuditConfig is the optional audit configuration.
	// If nil, no audit logging is performed.
	// Component should be set to "vmcp-server" to distinguish vMCP audit logs.
	AuditConfig *audit.Config

	// HealthMonitorConfig is the optional health monitoring configuration.
	// If nil, health monitoring is disabled.
	HealthMonitorConfig *health.MonitorConfig

	// StatusReportingInterval is the interval for reporting status updates.
	// If zero, defaults to 30 seconds.
	// Lower values provide faster status updates but increase API server load.
	StatusReportingInterval time.Duration

	// Watcher is the optional Kubernetes backend watcher for dynamic mode.
	// Only set when running in K8s with outgoingAuth.source: discovered.
	// Used for /readyz endpoint to gate readiness on cache sync.
	Watcher Watcher

	// OptimizerFactory builds an optimizer from a list of tools.
	// If not set, the optimizer is disabled.
	OptimizerFactory func(context.Context, []server.ServerTool) (optimizer.Optimizer, error)

	// OptimizerConfig holds the optimizer configuration from the vMCP config.
	// When non-nil, Start() creates the appropriate store and embedding client,
	// wires the OptimizerFactory, and registers cleanup in shutdownFuncs.
	OptimizerConfig *vmcpconfig.OptimizerConfig

	// StatusReporter enables vMCP runtime to report operational status.
	// In Kubernetes mode: Updates VirtualMCPServer.Status (requires RBAC)
	// In CLI mode: NoOpReporter (no persistent status)
	// If nil, status reporting is disabled.
	StatusReporter vmcpstatus.Reporter

	// SessionManagementV2 enables session-scoped backend client lifecycle.
	// When true, a SessionManager is used instead of sessionIDAdapter. Defaults to false.
	SessionManagementV2 bool

	// SessionFactory creates MultiSessions for Phase 2 session management.
	// Required when SessionManagementV2 is true; ignored otherwise.
	SessionFactory vmcpsession.MultiSessionFactory
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

	// Backend registry for capability discovery
	// For static mode (CLI), this is an immutable registry created from initial backends.
	// For dynamic mode (K8s), this is a DynamicRegistry updated by the operator.
	backendRegistry vmcp.BackendRegistry

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

	// vmcpSessionMgr is the Phase 2 session manager. Non-nil only when
	// Config.SessionManagementV2 is true and Config.SessionFactory is non-nil.
	vmcpSessionMgr *vmcpSessionManager

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

	// healthMonitor performs periodic health checks on backends.
	// Nil if health monitoring is disabled.
	// Protected by healthMonitorMu: RLock for reads (getter methods, HTTP handlers),
	// Lock for writes (initialization, disabling on start failure).
	healthMonitor   *health.Monitor
	healthMonitorMu sync.RWMutex

	// statusReporter enables vMCP to report operational status to control plane.
	// Nil if status reporting is disabled.
	statusReporter vmcpstatus.Reporter

	// shutdownFuncs contains cleanup functions to run during Stop().
	// Populated during Start() initialization before blocking; no mutex needed
	// since Stop() is only called after Start()'s select returns.
	shutdownFuncs []func(context.Context) error
}

// New creates a new Virtual MCP Server instance.
//
// The backendRegistry parameter provides the list of available backends:
// - For static mode (CLI), pass an immutable registry created from initial backends
// - For dynamic mode (K8s), pass a DynamicRegistry that will be updated by the operator
//
//nolint:gocyclo // Complexity from hook logic is acceptable
func New(
	ctx context.Context,
	cfg *Config,
	rt router.Router,
	backendClient vmcp.BackendClient,
	discoveryMgr discovery.Manager,
	backendRegistry vmcp.BackendRegistry,
	workflowDefs map[string]*composer.WorkflowDefinition,
) (*Server, error) {
	// Apply defaults
	if cfg.Host == "" {
		cfg.Host = "127.0.0.1"
	}
	// Note: Port 0 means "let OS assign random port" - intentionally no default applied here.
	// CLI provides default via flag (4483), so Port is only 0 in tests for dynamic port assignment.
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

	// Decorate backend client with telemetry if provider is configured
	// This must happen BEFORE creating the workflow engine so that workflow
	// backend calls are instrumented when they occur during workflow execution.
	if cfg.TelemetryProvider != nil {
		var err error
		// Get initial backends list from registry for telemetry setup
		initialBackends := backendRegistry.List(ctx)
		backendClient, err = monitorBackends(
			ctx,
			cfg.TelemetryProvider.MeterProvider(),
			cfg.TelemetryProvider.TracerProvider(),
			initialBackends,
			backendClient,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to monitor backends: %w", err)
		}
	}

	// Create workflow auditor if audit config is provided
	var workflowAuditor *audit.WorkflowAuditor
	if cfg.AuditConfig != nil {
		if err := cfg.AuditConfig.Validate(); err != nil {
			return nil, fmt.Errorf("invalid audit configuration: %w", err)
		}
		var err error
		workflowAuditor, err = audit.NewWorkflowAuditor(cfg.AuditConfig)
		if err != nil {
			return nil, fmt.Errorf("failed to create workflow auditor: %w", err)
		}
		slog.Info("workflow audit logging enabled")
	}

	// Create workflow engine (composer) for executing composite tools
	// The composer orchestrates multi-step workflows across backends
	// Use in-memory state store with 5-minute cleanup interval and 1-hour max age for completed workflows
	stateStore := composer.NewInMemoryStateStore(5*time.Minute, 1*time.Hour)
	workflowComposer := composer.NewWorkflowEngine(rt, backendClient, elicitationHandler, stateStore, workflowAuditor)

	// Validate workflows and create executors (fail fast on invalid workflows)
	workflowDefs, workflowExecutors, err := validateAndCreateExecutors(workflowComposer, workflowDefs)
	if err != nil {
		return nil, fmt.Errorf("workflow validation failed: %w", err)
	}

	// Decorate workflow executors with telemetry if provider is configured
	if cfg.TelemetryProvider != nil {
		workflowExecutors, err = monitorWorkflowExecutors(
			cfg.TelemetryProvider.MeterProvider(),
			cfg.TelemetryProvider.TracerProvider(),
			workflowExecutors,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to monitor workflow executors: %w", err)
		}
	}

	// Create session manager with VMCPSession factory
	// This enables type-safe access to routing tables while maintaining session lifecycle management
	sessionManager := transportsession.NewManager(cfg.SessionTTL, vmcpsession.VMCPSessionFactory())

	// Create handler factory (used by adapter and for future dynamic registration)
	handlerFactory := adapter.NewDefaultHandlerFactory(rt, backendClient)

	// Create capability adapter (single source of truth for converting aggregator types to SDK types)
	capabilityAdapter := adapter.NewCapabilityAdapter(handlerFactory)

	// Create health monitor if configured
	var healthMon *health.Monitor
	if cfg.HealthMonitorConfig != nil {
		// Get initial backends list from registry for health monitoring setup
		initialBackends := backendRegistry.List(ctx)
		healthMon, err = health.NewMonitor(backendClient, initialBackends, *cfg.HealthMonitorConfig)
		if err != nil {
			return nil, fmt.Errorf("failed to create health monitor: %w", err)
		}
		slog.Info("health monitoring enabled",
			"check_interval", cfg.HealthMonitorConfig.CheckInterval,
			"unhealthy_threshold", cfg.HealthMonitorConfig.UnhealthyThreshold,
			"timeout", cfg.HealthMonitorConfig.Timeout,
			"degraded_threshold", cfg.HealthMonitorConfig.DegradedThreshold)
	} else {
		slog.Info("health monitoring disabled")
	}

	// Create session manager if the feature flag is enabled.
	var vmcpSessMgr *vmcpSessionManager
	if cfg.SessionManagementV2 {
		if cfg.SessionFactory == nil {
			return nil, fmt.Errorf("SessionManagementV2 is enabled but no SessionFactory was provided")
		}
		vmcpSessMgr = newVMCPSessionManager(sessionManager, cfg.SessionFactory, backendRegistry)
		slog.Info("session-scoped backend lifecycle enabled")
	}

	// Create Server instance
	srv := &Server{
		config:            cfg,
		mcpServer:         mcpServer,
		router:            rt,
		backendClient:     backendClient,
		handlerFactory:    handlerFactory,
		discoveryMgr:      discoveryMgr,
		backendRegistry:   backendRegistry,
		sessionManager:    sessionManager,
		capabilityAdapter: capabilityAdapter,
		workflowDefs:      workflowDefs,
		workflowExecutors: workflowExecutors,
		ready:             make(chan struct{}),
		healthMonitor:     healthMon,
		statusReporter:    cfg.StatusReporter,
		vmcpSessionMgr:    vmcpSessMgr,
	}

	// Register OnRegisterSession hook to inject capabilities after SDK registers session.
	// See handleSessionRegistration for implementation details.
	hooks.AddOnRegisterSession(func(ctx context.Context, session server.ClientSession) {
		srv.handleSessionRegistration(ctx, session, sessionManager)
	})

	return srv, nil
}

// Handler builds and returns the MCP HTTP handler without starting a listener.
// This enables embedding the vmcp server inside another HTTP server or framework.
//
// The returned handler includes all routes (health, metrics, well-known, MCP)
// and the full middleware chain (recovery, header validation, auth, audit,
// discovery, backend enrichment, MCP parsing, telemetry).
//
// Each call builds a fresh handler. The method is safe to call multiple times.
// All returned handlers share the same underlying MCPServer and SessionManager,
// so callers should not serve concurrent traffic through multiple handlers.
func (s *Server) Handler(_ context.Context) (http.Handler, error) {
	// Use vmcpSessionMgr (session-scoped backend lifecycle) when available;
	// otherwise fall back to sessionIDAdapter.
	sdkSessionMgr := func() server.SessionIdManager {
		if s.vmcpSessionMgr != nil {
			return s.vmcpSessionMgr
		}
		return newSessionIDAdapter(s.sessionManager)
	}()

	// Create Streamable HTTP server with ToolHive session management
	streamableServer := server.NewStreamableHTTPServer(
		s.mcpServer,
		server.WithEndpointPath(s.config.EndpointPath),
		server.WithSessionIdManager(sdkSessionMgr),
		server.WithHeartbeatInterval(defaultHeartbeatInterval),
	)

	// Create HTTP mux with separated authenticated and unauthenticated routes
	mux := http.NewServeMux()

	// Unauthenticated health endpoints
	mux.HandleFunc("/health", s.handleHealth)
	mux.HandleFunc("/ping", s.handleHealth)
	mux.HandleFunc("/readyz", s.handleReadiness)
	mux.HandleFunc("/status", s.handleStatus)
	mux.HandleFunc("/api/backends/health", s.handleBackendHealth)

	// Optional Prometheus metrics endpoint (unauthenticated)
	if s.config.TelemetryProvider != nil {
		if prometheusHandler := s.config.TelemetryProvider.PrometheusHandler(); prometheusHandler != nil {
			mux.Handle("/metrics", prometheusHandler)
			slog.Info("prometheus metrics endpoint enabled at /metrics")
		} else {
			slog.Warn("prometheus metrics endpoint is not enabled, but telemetry provider is configured")
		}
	}

	// Optional .well-known discovery endpoints (unauthenticated, RFC 9728 compliant)
	// Handles /.well-known/oauth-protected-resource and subpaths (e.g., /mcp)
	if wellKnownHandler := auth.NewWellKnownHandler(s.config.AuthInfoHandler); wellKnownHandler != nil {
		mux.Handle("/.well-known/", wellKnownHandler)
		slog.Info("rFC 9728 OAuth discovery endpoints enabled at /.well-known/")
	}

	// MCP endpoint - apply middleware chain (wrapping order, execution happens in reverse):
	// Code wraps: auth → audit → discovery → backend enrichment → MCP parsing → telemetry
	// Execution order: telemetry → MCP parsing → backend enrichment → discovery → audit → auth → handler

	var mcpHandler http.Handler = streamableServer

	if s.config.TelemetryProvider != nil {
		mcpHandler = s.config.TelemetryProvider.Middleware(s.config.Name, "streamable-http")(mcpHandler)
		slog.Info("telemetry middleware enabled for MCP endpoints")
	}

	// Apply MCP parsing middleware to extract JSON-RPC method from request body.
	// This runs before telemetry so that recordMetrics can label metrics with the
	// actual mcp_method (e.g. "tools/call", "initialize") instead of "unknown".
	mcpHandler = mcpparser.ParsingMiddleware(mcpHandler)

	// Apply backend enrichment middleware if audit is configured
	// This runs after discovery populates the routing table, so it can extract backend names
	if s.config.AuditConfig != nil {
		mcpHandler = s.backendEnrichmentMiddleware(mcpHandler)
		slog.Info("backend enrichment middleware enabled for audit events")
	}

	// Apply discovery middleware (runs after audit/auth middleware)
	// Discovery middleware performs per-request capability aggregation with user context
	// Pass sessionManager to enable session-based capability retrieval for subsequent requests
	// The backend registry provides dynamic backend list (supports DynamicRegistry for K8s)
	// Pass health monitor to enable filtering based on current health status (respects circuit breaker)
	s.healthMonitorMu.RLock()
	healthMon := s.healthMonitor
	s.healthMonitorMu.RUnlock()

	var healthStatusProvider health.StatusProvider
	if healthMon != nil {
		healthStatusProvider = healthMon
	}
	discoveryOpts := func() []discovery.MiddlewareOption {
		if s.vmcpSessionMgr != nil {
			return []discovery.MiddlewareOption{discovery.WithSessionScopedRouting()}
		}
		return nil
	}()
	mcpHandler = discovery.Middleware(
		s.discoveryMgr, s.backendRegistry, s.sessionManager, healthStatusProvider, discoveryOpts...,
	)(mcpHandler)
	slog.Info("discovery middleware enabled for lazy per-user capability discovery")

	// Apply audit middleware if configured (runs after auth, before discovery)
	if s.config.AuditConfig != nil {
		if err := s.config.AuditConfig.Validate(); err != nil {
			return nil, fmt.Errorf("invalid audit configuration: %w", err)
		}
		auditor, err := audit.NewAuditorWithTransport(
			s.config.AuditConfig,
			"streamable-http", // vMCP uses streamable HTTP transport
		)
		if err != nil {
			return nil, fmt.Errorf("failed to create auditor: %w", err)
		}
		mcpHandler = auditor.Middleware(mcpHandler)
		slog.Info("audit middleware enabled for MCP endpoints")
	}

	// Apply authentication middleware if configured (runs first in chain)
	if s.config.AuthMiddleware != nil {
		mcpHandler = s.config.AuthMiddleware(mcpHandler)
		slog.Info("authentication middleware enabled for MCP endpoints")
	}

	// Apply Accept header validation (rejects GET requests without Accept: text/event-stream)
	mcpHandler = headerValidatingMiddleware(mcpHandler)

	// Apply recovery middleware as outermost (catches panics from all inner middleware)
	mcpHandler = recovery.Middleware(mcpHandler)
	slog.Info("recovery middleware enabled for MCP endpoints")

	mux.Handle("/", mcpHandler)

	return mux, nil
}

// Start starts the Virtual MCP Server and begins serving requests.
//
//nolint:gocyclo // Complexity from health monitoring and startup orchestration is acceptable
func (s *Server) Start(ctx context.Context) error {
	// Create optimizer store and wire factory if optimizer is configured
	if s.config.OptimizerConfig != nil {
		if err := s.initOptimizer(); err != nil {
			return err
		}
	}

	// Build the HTTP handler (middleware chain, routes, mux)
	handler, err := s.Handler(ctx)
	if err != nil {
		return fmt.Errorf("failed to build handler: %w", err)
	}

	// Create HTTP server
	addr := fmt.Sprintf("%s:%d", s.config.Host, s.config.Port)
	s.httpServer = &http.Server{
		Addr:              addr,
		Handler:           handler,
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
	slog.Info("starting Virtual MCP Server", "address", actualAddr, "endpoint", s.config.EndpointPath)
	slog.Info("health endpoints available",
		"health", actualAddr+"/health",
		"ping", actualAddr+"/ping",
		"status", actualAddr+"/status",
		"backends_health", actualAddr+"/api/backends/health")

	// Start server in background
	errCh := make(chan error, 1)
	go func() {
		if err := s.httpServer.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- fmt.Errorf("HTTP server error: %w", err)
		}
	}()

	// Signal that the server is ready (listener created and serving started)
	s.readyOnce.Do(func() {
		close(s.ready)
	})

	// Start health monitor if configured
	s.healthMonitorMu.RLock()
	healthMon := s.healthMonitor
	s.healthMonitorMu.RUnlock()

	if healthMon != nil {
		if err := healthMon.Start(ctx); err != nil {
			// Log error and disable health monitoring - treat as if it wasn't configured
			// This ensures getter methods correctly report monitoring as disabled
			slog.Warn("failed to start health monitor, disabling health monitoring", "error", err)
			s.healthMonitorMu.Lock()
			s.healthMonitor = nil
			s.healthMonitorMu.Unlock()
		} else {
			slog.Info("health monitor started")
		}
	}

	// Start status reporter if configured
	if s.statusReporter != nil {
		shutdown, err := s.statusReporter.Start(ctx)
		if err != nil {
			return fmt.Errorf("failed to start status reporter: %w", err)
		}
		s.shutdownFuncs = append(s.shutdownFuncs, shutdown)

		// Create internal context for status reporting goroutine lifecycle
		// This ensures the goroutine is cleaned up on all exit paths
		statusReportingCtx, statusReportingCancel := context.WithCancel(ctx)

		// Prepare status reporting config
		statusConfig := DefaultStatusReportingConfig()
		statusConfig.Reporter = s.statusReporter
		if s.config.StatusReportingInterval > 0 {
			statusConfig.Interval = s.config.StatusReportingInterval
		}

		// Start periodic status reporting in background
		go s.periodicStatusReporting(statusReportingCtx, statusConfig)

		// Append cancel function to shutdownFuncs for cleanup
		// Done after starting goroutine to avoid race if Stop() is called immediately
		s.shutdownFuncs = append(s.shutdownFuncs, func(context.Context) error {
			statusReportingCancel()
			return nil
		})
	}

	// Wait for either context cancellation or server error
	select {
	case <-ctx.Done():
		slog.Info("context cancelled, shutting down server")
		return s.Stop(context.Background())
	case err := <-errCh:
		// HTTP server error - log and tear down cleanly
		slog.Error("hTTP server error", "error", err)
		if stopErr := s.Stop(context.Background()); stopErr != nil {
			// Combine errors if Stop() also fails
			return fmt.Errorf("server error: %w; stop error: %v", err, stopErr)
		}
		return err
	}
}

// Stop gracefully stops the Virtual MCP Server.
func (s *Server) Stop(ctx context.Context) error {
	slog.Info("stopping Virtual MCP Server")

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

	// Stop health monitor to clean up health check goroutines
	s.healthMonitorMu.RLock()
	healthMon := s.healthMonitor
	s.healthMonitorMu.RUnlock()

	if healthMon != nil {
		if err := healthMon.Stop(); err != nil {
			errs = append(errs, fmt.Errorf("failed to stop health monitor: %w", err))
		}
	}

	// Run shutdown functions (e.g., status reporter cleanup, future components)
	for _, shutdown := range s.shutdownFuncs {
		if err := shutdown(ctx); err != nil {
			errs = append(errs, fmt.Errorf("failed to execute shutdown function: %w", err))
		}
	}

	// Stop discovery manager to clean up background goroutines
	if s.discoveryMgr != nil {
		s.discoveryMgr.Stop()
	}

	if len(errs) > 0 {
		slog.Error("errors during shutdown", "errors", errs)
		return errors.Join(errs...)
	}

	slog.Info("virtual MCP Server stopped")
	return nil
}

// initOptimizer creates the optimizer tool store and wires the OptimizerFactory.
// It registers the store's Close method as a shutdown function.
func (s *Server) initOptimizer() error {
	embClient, err := optimizer.NewEmbeddingClient(s.config.OptimizerConfig)
	if err != nil {
		return fmt.Errorf("failed to create embedding client: %w", err)
	}

	store, err := optimizer.NewSQLiteToolStore(embClient)
	if err != nil {
		return fmt.Errorf("failed to create optimizer store: %w", err)
	}
	s.shutdownFuncs = append(s.shutdownFuncs, func(_ context.Context) error {
		return store.Close()
	})
	s.config.OptimizerFactory = optimizer.NewDummyOptimizerFactoryWithStore(store, optimizer.DefaultTokenCounter())
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
		slog.Error("failed to encode health response", "error", err)
	}
}

// handleReadiness handles /readyz HTTP requests for Kubernetes readiness probes.
//
// In dynamic mode (K8s with outgoingAuth.source: discovered), this endpoint gates
// readiness on the controller-runtime manager's cache sync status. The pod will
// not be marked ready until the manager has populated its cache with current
// backend information from the MCPGroup.
//
// In static mode (CLI or K8s with inline backends), this always returns 200 OK
// since there's no cache to sync.
//
// Design Pattern:
// This follows the same readiness gating pattern used by cert-manager and ArgoCD:
// - /health: Always returns 200 if server is responding (liveness probe)
// - /readyz: Returns 503 until caches synced, then 200 (readiness probe)
//
// K8s Configuration:
//
//	readinessProbe:
//	  httpGet:
//	    path: /readyz
//	    port: 4483
//	  initialDelaySeconds: 5
//	  periodSeconds: 5
//	  timeoutSeconds: 5
func (s *Server) handleReadiness(w http.ResponseWriter, r *http.Request) {
	// Static mode: always ready (no watcher, no cache to sync)
	if s.config.Watcher == nil {
		response := map[string]string{
			"status": "ready",
			"mode":   "static",
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		if err := json.NewEncoder(w).Encode(response); err != nil {
			slog.Error("failed to encode readiness response", "error", err)
		}
		return
	}

	// Dynamic mode: gate readiness on cache sync
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	if !s.config.Watcher.WaitForCacheSync(ctx) {
		// Cache not synced yet - return 503 Service Unavailable
		response := map[string]string{
			"status": "not_ready",
			"mode":   "dynamic",
			"reason": "cache_sync_pending",
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		if err := json.NewEncoder(w).Encode(response); err != nil {
			slog.Error("failed to encode readiness response", "error", err)
		}
		return
	}

	// Cache synced - ready to serve requests
	response := map[string]string{
		"status": "ready",
		"mode":   "dynamic",
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	if err := json.NewEncoder(w).Encode(response); err != nil {
		slog.Error("failed to encode readiness response", "error", err)
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
//   - If injectOptimizerCapabilities is called, this should not be called again.
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
		slog.Debug("added session backend tools", "session_id", sessionID, "count", len(sdkTools))
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
		slog.Debug("added session composite tools", "session_id", sessionID, "count", len(compositeSDKTools))
	}

	// Convert and add resources
	if len(caps.Resources) > 0 {
		sdkResources := s.capabilityAdapter.ToSDKResources(caps.Resources)

		if err := s.mcpServer.AddSessionResources(sessionID, sdkResources...); err != nil {
			return fmt.Errorf("failed to add session resources: %w", err)
		}
		slog.Debug("added session resources", "session_id", sessionID, "count", len(sdkResources))
	}

	// Note: SDK v0.43.0 does not support per-session prompts yet.
	// Per-session prompts are required to maintain multi-tenant security isolation.
	// Prompts cannot be registered globally via mcpServer.AddPrompt() without leaking
	// capability information across session boundaries (e.g., admin prompts visible to regular users).
	if len(caps.Prompts) > 0 {
		slog.Warn("prompts discovered but not exposed - awaiting SDK support for per-session prompts",
			"session_id", sessionID,
			"prompt_count", len(caps.Prompts),
			"sdk_version", "v0.43.0",
			"required_api", "AddSessionPrompts()",
			"reason", "multi-tenant security requires per-session capability isolation")
		// TODO(prompts): Implement when mark3labs/mcp-go adds AddSessionPrompts() API
		// Conversion logic already exists in capability_adapter.ToSDKPrompts()
		// Implementation will be: s.mcpServer.AddSessionPrompts(sessionID, sdkPrompts...)
	}

	slog.Info("session capabilities injected during initialization",
		"session_id", sessionID,
		"backend_tools", len(caps.Tools),
		"composite_tools", len(caps.CompositeTools),
		"resources", len(caps.Resources))

	return nil
}

// injectOptimizerCapabilities injects all capabilities into the session, including optimizer tools.
// It should not be called if not in optimizer mode and replaces injectCapabilities.
//
// When optimizer mode is enabled, instead of exposing all backend tools directly,
// vMCP exposes only two meta-tools:
//   - find_tool: Search for tools by description
//   - call_tool: Invoke a tool by name with parameters
//
// This method:
// 1. Converts all tools (backend + composite) to SDK format with handlers
// 2. Injects the optimizer capabilities into the session
func (s *Server) injectOptimizerCapabilities(
	ctx context.Context,
	sessionID string,
	caps *aggregator.AggregatedCapabilities,
) error {

	tools := append([]vmcp.Tool{}, caps.Tools...)
	tools = append(tools, caps.CompositeTools...)

	sdkTools, err := s.capabilityAdapter.ToSDKTools(tools)
	if err != nil {
		return fmt.Errorf("failed to convert tools to SDK format: %w", err)
	}

	// Create optimizer instance from factory
	opt, err := s.config.OptimizerFactory(ctx, sdkTools)
	if err != nil {
		return fmt.Errorf("failed to create optimizer: %w", err)
	}

	// Create optimizer tools (find_tool, call_tool)
	optimizerTools := adapter.CreateOptimizerTools(opt)

	slog.Debug("created optimizer tools for session",
		"session_id", sessionID,
		"backend_tool_count", len(caps.Tools),
		"composite_tool_count", len(caps.CompositeTools),
		"total_tools_indexed", len(sdkTools))

	// Clear tools from caps - they're now wrapped by optimizer
	// Resources and prompts are preserved and handled normally
	capsCopy := *caps
	capsCopy.Tools = nil
	capsCopy.CompositeTools = nil

	// Manually add the optimizer tools, since we don't want to bother converting
	// optimizer tools into `vmcp.Tool`s as well.
	if err := s.mcpServer.AddSessionTools(sessionID, optimizerTools...); err != nil {
		return fmt.Errorf("failed to add session tools: %w", err)
	}

	return s.injectCapabilities(sessionID, &capsCopy)
}

// handleSessionRegistration processes a new MCP session registration.
//
// This hook fires AFTER the session is registered in the SDK (unlike AfterInitialize which
// fires BEFORE session registration), allowing us to safely call AddSessionTools/AddSessionResources.
//
// The discovery middleware populates capabilities in the context, which is available here.
// We inject them into the SDK session and store the routing table for subsequent requests.
//
// This method performs the following steps:
//  1. Retrieves discovered capabilities from context
//  2. Adds composite tools from configuration
//  3. Stores routing table in VMCPSession for request routing
//  4. Injects capabilities into the SDK session
//
// IMPORTANT: Session capabilities are immutable after injection.
//   - Capabilities discovered during initialize are fixed for the session lifetime
//   - Backend changes (new tools, removed resources) won't be reflected in existing sessions
//   - Clients must create new sessions to see updated capabilities
//
// TODO(dynamic-capabilities): Consider implementing capability refresh mechanism when SDK supports it
//
// The sessionManager parameter is passed explicitly because this method is called
// from a closure registered before the Server is fully constructed.
func (s *Server) handleSessionRegistration(
	ctx context.Context,
	session server.ClientSession,
	sessionManager *transportsession.Manager,
) {
	sessionID := session.SessionID()
	slog.Debug("onRegisterSession hook called", "session_id", sessionID)

	// Delegate to session-scoped backend lifecycle when vmcpSessionMgr is active.
	if s.vmcpSessionMgr != nil {
		s.handleSessionRegistrationV2(ctx, session)
		return
	}

	// Get capabilities from context (discovered by middleware)
	caps, ok := discovery.DiscoveredCapabilitiesFromContext(ctx)
	if !ok || caps == nil {
		slog.Warn("no discovered capabilities in context for OnRegisterSession hook",
			"session_id", sessionID)
		return
	}

	// Validate that routing table exists
	if caps.RoutingTable == nil {
		slog.Warn("routing table is nil in discovered capabilities",
			"session_id", sessionID)
		return
	}

	// Add composite tools to capabilities
	// Composite tools are static (from configuration) and not discovered from backends
	// They are added here to be exposed alongside backend tools in the session
	if len(s.workflowDefs) > 0 {
		compositeTools := convertWorkflowDefsToTools(s.workflowDefs)

		// Validate no conflicts between composite tool names and backend tool names
		if err := validateNoToolConflicts(caps.Tools, compositeTools); err != nil {
			slog.Error("composite tool name conflict detected",
				"session_id", sessionID,
				"error", err)
			// Don't add composite tools if there are conflicts
			// This prevents ambiguity in routing/execution
			return
		}

		caps.CompositeTools = compositeTools
		slog.Debug("added composite tools to session capabilities",
			"session_id", sessionID,
			"composite_tool_count", len(compositeTools))
	}

	// Store routing table in VMCPSession for subsequent requests
	// This enables the middleware to reconstruct capabilities from session
	// without re-running discovery for every request
	vmcpSess, err := vmcpsession.GetVMCPSession(sessionID, sessionManager)
	if err != nil {
		slog.Error("failed to get VMCPSession for routing table storage",
			"error", err,
			"session_id", sessionID)
		return
	}

	vmcpSess.SetRoutingTable(caps.RoutingTable)
	vmcpSess.SetTools(caps.Tools)
	slog.Debug("routing table and tools stored in VMCPSession",
		"session_id", sessionID,
		"tool_count", len(caps.RoutingTable.Tools),
		"resource_count", len(caps.RoutingTable.Resources),
		"prompt_count", len(caps.RoutingTable.Prompts))

	if s.config.OptimizerFactory != nil {
		err = s.injectOptimizerCapabilities(ctx, sessionID, caps)
		if err != nil {
			slog.Error("failed to create optimizer tools",
				"error", err,
				"session_id", sessionID)
		} else {
			slog.Info("optimizer capabilities injected")
		}
		return
	}

	// Inject capabilities into SDK session
	if err := s.injectCapabilities(sessionID, caps); err != nil {
		slog.Error("failed to inject session capabilities",
			"error", err,
			"session_id", sessionID)
		return
	}

	slog.Info("session capabilities injected",
		"session_id", sessionID,
		"tool_count", len(caps.Tools),
		"resource_count", len(caps.Resources))
}

// handleSessionRegistrationV2 handles session registration when SessionManagementV2 is enabled.
//
// It is invoked from handleSessionRegistration and:
//  1. Creates a MultiSession with real backend HTTP connections via CreateSession().
//  2. Retrieves SDK-format tools with session-scoped routing handlers.
//  3. Registers the tools with the SDK MCPServer for the session.
//
// Tool calls are routed directly through the session's backend connections
// rather than through the global router and discovery middleware.
//
// # Current capability surface
//
// Only backend tools are registered in this path. The following capabilities
// are not yet supported and require future work:
//
//   - Resources: session-scoped resource handlers need to be added to
//     vmcpSessionManager (analogous to GetAdaptedTools) before resources
//     from MultiSession.Resources() can be registered via AddSessionResources.
//
//   - Composite tools: workflow-backed tools defined in s.workflowDefs are
//     not wired into the V2 path. They need session-scoped executor adapters.
//
//   - Optimizer mode: the optimizer wraps all tools into find_tool/call_tool
//     meta-tools. The optimizer path (injectOptimizerCapabilities) is not
//     called here and is unsupported when SessionManagementV2 is enabled.
//
//   - Prompts: not supported in either path until the SDK adds
//     AddSessionPrompts.
func (s *Server) handleSessionRegistrationV2(ctx context.Context, session server.ClientSession) {
	sessionID := session.SessionID()
	slog.Debug("creating session-scoped backends", "session_id", sessionID)

	// NOTE: the initialize response (including the Mcp-Session-Id header) has
	// already been sent to the client before this hook fires. Any error below
	// terminates the session internally, but the client holds a session ID that
	// will appear to have no tools (tool calls will return "not found" from the
	// SDK). This is an architectural constraint of the two-phase pattern — there
	// is no way to retract the session ID after it has been sent.
	//
	// NOTE: there is a brief race window between the client receiving the session
	// ID and this hook completing. A client that pipelines a tools/call immediately
	// after initialize may receive a "tool not found" error before AddSessionTools
	// completes. Conforming MCP clients call tools/list before tools/call, so this
	// window is expected to be harmless in practice.
	if _, err := s.vmcpSessionMgr.CreateSession(ctx, sessionID); err != nil {
		slog.Error("failed to create session-scoped backends",
			"session_id", sessionID,
			"error", err)
		if _, termErr := s.vmcpSessionMgr.Terminate(sessionID); termErr != nil {
			slog.Warn("failed to clean up placeholder after session creation failure",
				"session_id", sessionID,
				"error", termErr)
		}
		return
	}

	adaptedTools, err := s.vmcpSessionMgr.GetAdaptedTools(sessionID)
	if err != nil {
		slog.Error("failed to get session-scoped tools",
			"session_id", sessionID,
			"error", err)
		if _, termErr := s.vmcpSessionMgr.Terminate(sessionID); termErr != nil {
			slog.Warn("failed to clean up session after tool retrieval failure",
				"session_id", sessionID,
				"error", termErr)
		}
		return
	}

	if len(adaptedTools) > 0 {
		if err := s.mcpServer.AddSessionTools(sessionID, adaptedTools...); err != nil {
			slog.Error("failed to add session tools",
				"session_id", sessionID,
				"error", err)
			if _, termErr := s.vmcpSessionMgr.Terminate(sessionID); termErr != nil {
				slog.Warn("failed to clean up session after AddSessionTools failure",
					"session_id", sessionID,
					"error", termErr)
			}
			return
		}
	}

	slog.Info("session capabilities injected",
		"session_id", sessionID,
		"tool_count", len(adaptedTools))
}

// validateAndCreateExecutors validates workflow definitions and creates executors.
//
// This function:
//  1. Validates each workflow definition (cycle detection, tool references, etc.)
//  2. Returns error on first validation failure (fail-fast)
//  3. Creates workflow executors for all valid workflows
//
// Failing fast on invalid workflows provides immediate user feedback and prevents
// security issues (resource exhaustion from cycles, information disclosure from errors).
func validateAndCreateExecutors(
	validator composer.Composer,
	workflowDefs map[string]*composer.WorkflowDefinition,
) (map[string]*composer.WorkflowDefinition, map[string]adapter.WorkflowExecutor, error) {
	if len(workflowDefs) == 0 {
		return nil, nil, nil
	}

	validDefs := make(map[string]*composer.WorkflowDefinition, len(workflowDefs))
	validExecutors := make(map[string]adapter.WorkflowExecutor, len(workflowDefs))

	for name, def := range workflowDefs {
		if err := validator.ValidateWorkflow(context.Background(), def); err != nil {
			return nil, nil, fmt.Errorf("invalid workflow definition '%s': %w", name, err)
		}

		validDefs[name] = def
		validExecutors[name] = newComposerWorkflowExecutor(validator, def)
		slog.Debug("validated workflow definition", "name", name)
	}

	if len(validDefs) > 0 {
		slog.Info("loaded valid composite tool workflows", "count", len(validDefs))
	}

	return validDefs, validExecutors, nil
}

// GetBackendHealthStatus returns the health status of a specific backend.
// Returns error if health monitoring is disabled or backend not found.
func (s *Server) GetBackendHealthStatus(backendID string) (vmcp.BackendHealthStatus, error) {
	s.healthMonitorMu.RLock()
	healthMon := s.healthMonitor
	s.healthMonitorMu.RUnlock()

	if healthMon == nil {
		return vmcp.BackendUnknown, fmt.Errorf("health monitoring is disabled")
	}
	return healthMon.GetBackendStatus(backendID)
}

// GetBackendHealthState returns the full health state of a specific backend.
// Returns error if health monitoring is disabled or backend not found.
func (s *Server) GetBackendHealthState(backendID string) (*health.State, error) {
	s.healthMonitorMu.RLock()
	healthMon := s.healthMonitor
	s.healthMonitorMu.RUnlock()

	if healthMon == nil {
		return nil, fmt.Errorf("health monitoring is disabled")
	}
	return healthMon.GetBackendState(backendID)
}

// GetAllBackendHealthStates returns the health states of all backends.
// Returns empty map if health monitoring is disabled.
func (s *Server) GetAllBackendHealthStates() map[string]*health.State {
	s.healthMonitorMu.RLock()
	healthMon := s.healthMonitor
	s.healthMonitorMu.RUnlock()

	if healthMon == nil {
		return make(map[string]*health.State)
	}
	return healthMon.GetAllBackendStates()
}

// GetHealthSummary returns a summary of backend health across all backends.
// Returns zero-valued summary if health monitoring is disabled.
func (s *Server) GetHealthSummary() health.Summary {
	s.healthMonitorMu.RLock()
	healthMon := s.healthMonitor
	s.healthMonitorMu.RUnlock()

	if healthMon == nil {
		return health.Summary{}
	}
	return healthMon.GetHealthSummary()
}

// BackendHealthResponse represents the health status response for all backends.
type BackendHealthResponse struct {
	// MonitoringEnabled indicates if health monitoring is active.
	MonitoringEnabled bool `json:"monitoring_enabled"`

	// Summary provides aggregate health statistics.
	// Only populated if MonitoringEnabled is true.
	Summary *health.Summary `json:"summary,omitempty"`

	// Backends contains the detailed health state of each backend.
	// Only populated if MonitoringEnabled is true.
	Backends map[string]*health.State `json:"backends,omitempty"`
}

// handleBackendHealth handles /api/backends/health HTTP requests.
// Returns 200 OK with backend health information.
//
// Security Note: This endpoint is unauthenticated and may expose backend topology.
// Consider applying authentication middleware if operating in multi-tenant mode.
func (s *Server) handleBackendHealth(w http.ResponseWriter, _ *http.Request) {
	s.healthMonitorMu.RLock()
	healthMon := s.healthMonitor
	s.healthMonitorMu.RUnlock()

	response := BackendHealthResponse{
		MonitoringEnabled: healthMon != nil,
	}

	if healthMon != nil {
		summary := s.GetHealthSummary()
		response.Summary = &summary
		response.Backends = s.GetAllBackendHealthStates()
	}

	// Encode response before writing headers to ensure encoding succeeds
	data, err := json.Marshal(response)
	if err != nil {
		slog.Error("failed to encode backend health response", "error", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write(data); err != nil {
		slog.Error("failed to write backend health response", "error", err)
	}
}

// notAcceptableBody is the JSON-RPC error returned when a GET request is missing
// the Accept: text/event-stream header required by the Streamable HTTP transport.
var notAcceptableBody = []byte(
	`{"jsonrpc":"2.0","id":"server-error","error":` +
		`{"code":-32600,"message":"Not Acceptable: Client must accept text/event-stream"}}`,
)

// headerValidatingMiddleware rejects GET requests that do not include
// Accept: text/event-stream, as required by the MCP Streamable HTTP transport spec.
func headerValidatingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet &&
			!strings.Contains(r.Header.Get("Accept"), "text/event-stream") {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotAcceptable)
			if _, err := w.Write(notAcceptableBody); err != nil {
				slog.Error("failed to write not-acceptable response", "error", err)
			}
			return
		}
		next.ServeHTTP(w, r)
	})
}

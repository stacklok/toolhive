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
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/mark3labs/mcp-go/server"

	"github.com/stacklok/toolhive/pkg/audit"
	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/logger"
	"github.com/stacklok/toolhive/pkg/recovery"
	"github.com/stacklok/toolhive/pkg/telemetry"
	transportsession "github.com/stacklok/toolhive/pkg/transport/session"
	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/aggregator"
	"github.com/stacklok/toolhive/pkg/vmcp/composer"
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

	// Watcher is the optional Kubernetes backend watcher for dynamic mode.
	// Only set when running in K8s with outgoingAuth.source: discovered.
	// Used for /readyz endpoint to gate readiness on cache sync.
	Watcher Watcher

	// OptimizerIntegration is the optional optimizer integration.
	// If nil, optimizer is disabled and backend tools are exposed directly.
	// If set, this takes precedence over OptimizerConfig.
	OptimizerIntegration optimizer.Integration

	// OptimizerConfig is the optional optimizer configuration (for backward compatibility).
	// If OptimizerIntegration is set, this is ignored.
	// If both are nil, optimizer is disabled.
	OptimizerConfig *optimizer.Config

	// StatusReporter enables vMCP runtime to report operational status.
	// In Kubernetes mode: Updates VirtualMCPServer.Status (requires RBAC)
	// In CLI mode: NoOpReporter (no persistent status)
	// If nil, status reporting is disabled.
	StatusReporter vmcpstatus.Reporter
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

	// optimizerIntegration provides semantic tool discovery via optim_find_tool and optim_call_tool.
	// Nil if optimizer is disabled.
	optimizerIntegration OptimizerIntegration

	// statusReporter enables vMCP to report operational status to control plane.
	// Nil if status reporting is disabled.
	statusReporter vmcpstatus.Reporter

	// statusReportingCtx controls the lifecycle of the periodic status reporting goroutine.
	// Created in Start(), cancelled in Stop() or on Start() error paths.
	statusReportingCtx    context.Context
	statusReportingCancel context.CancelFunc

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
		logger.Info("Workflow audit logging enabled")
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
		// Construct selfURL to prevent health checker from checking itself
		selfURL := fmt.Sprintf("http://%s:%d%s", cfg.Host, cfg.Port, cfg.EndpointPath)
		healthMon, err = health.NewMonitor(backendClient, initialBackends, *cfg.HealthMonitorConfig, selfURL)
		if err != nil {
			return nil, fmt.Errorf("failed to create health monitor: %w", err)
		}
		logger.Infow("Health monitoring enabled",
			"check_interval", cfg.HealthMonitorConfig.CheckInterval,
			"unhealthy_threshold", cfg.HealthMonitorConfig.UnhealthyThreshold,
			"timeout", cfg.HealthMonitorConfig.Timeout,
			"degraded_threshold", cfg.HealthMonitorConfig.DegradedThreshold)
	} else {
		logger.Info("Health monitoring disabled")
	}

	// Initialize optimizer integration if configured
	var optimizerInteg optimizer.Integration
	if cfg.OptimizerIntegration != nil {
		optimizerInteg = cfg.OptimizerIntegration
	} else if cfg.OptimizerConfig != nil && cfg.OptimizerConfig.Enabled {
		// Create optimizer integration from config (for backward compatibility)
		var err error
		optimizerInteg, err = optimizer.NewIntegration(ctx, cfg.OptimizerConfig, mcpServer, backendClient, sessionManager)
		if err != nil {
			return nil, fmt.Errorf("failed to create optimizer integration: %w", err)
		}
	}

	// Initialize optimizer if configured (registers tools and ingests backends)
	if optimizerInteg != nil {
		if err := optimizerInteg.Initialize(ctx, mcpServer, backendRegistry); err != nil {
			return nil, fmt.Errorf("failed to initialize optimizer: %w", err)
		}
		// Store optimizer integration in config for cleanup during Stop()
		cfg.OptimizerIntegration = optimizerInteg
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
	}

	// Register OnRegisterSession hook to inject capabilities after SDK registers session.
	// See handleSessionRegistration for implementation details.
	hooks.AddOnRegisterSession(func(ctx context.Context, session server.ClientSession) {
		srv.handleSessionRegistration(ctx, session, sessionManager)
	})

	return srv, nil
}

// Start starts the Virtual MCP Server and begins serving requests.
//
//nolint:gocyclo // Complexity from health monitoring and middleware setup is acceptable
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
	mux.HandleFunc("/readyz", s.handleReadiness)
	mux.HandleFunc("/status", s.handleStatus)
	mux.HandleFunc("/api/backends/health", s.handleBackendHealth)

	// Optional Prometheus metrics endpoint (unauthenticated)
	if s.config.TelemetryProvider != nil {
		if prometheusHandler := s.config.TelemetryProvider.PrometheusHandler(); prometheusHandler != nil {
			mux.Handle("/metrics", prometheusHandler)
			logger.Info("Prometheus metrics endpoint enabled at /metrics")
		} else {
			logger.Warn("Prometheus metrics endpoint is not enabled, but telemetry provider is configured")
		}
	}

	// Optional .well-known discovery endpoints (unauthenticated, RFC 9728 compliant)
	// Handles /.well-known/oauth-protected-resource and subpaths (e.g., /mcp)
	if wellKnownHandler := auth.NewWellKnownHandler(s.config.AuthInfoHandler); wellKnownHandler != nil {
		mux.Handle("/.well-known/", wellKnownHandler)
		logger.Info("RFC 9728 OAuth discovery endpoints enabled at /.well-known/")
	}

	// MCP endpoint - apply middleware chain (wrapping order, execution happens in reverse):
	// Code wraps: auth → audit → discovery → backend enrichment → telemetry
	// Execution order: telemetry → backend enrichment → discovery → audit → auth → handler
	var mcpHandler http.Handler = streamableServer

	if s.config.TelemetryProvider != nil {
		mcpHandler = s.config.TelemetryProvider.Middleware(s.config.Name, "streamable-http")(mcpHandler)
		logger.Info("Telemetry middleware enabled for MCP endpoints")
	}

	// Apply backend enrichment middleware if audit is configured
	// This runs after discovery populates the routing table, so it can extract backend names
	if s.config.AuditConfig != nil {
		mcpHandler = s.backendEnrichmentMiddleware(mcpHandler)
		logger.Info("Backend enrichment middleware enabled for audit events")
	}

	// Apply discovery middleware (runs after audit/auth middleware)
	// Discovery middleware performs per-request capability aggregation with user context
	// Pass sessionManager to enable session-based capability retrieval for subsequent requests
	// The backend registry provides dynamic backend list (supports DynamicRegistry for K8s)
	mcpHandler = discovery.Middleware(s.discoveryMgr, s.backendRegistry, s.sessionManager)(mcpHandler)
	logger.Info("Discovery middleware enabled for lazy per-user capability discovery")

	// Apply audit middleware if configured (runs after auth, before discovery)
	if s.config.AuditConfig != nil {
		if err := s.config.AuditConfig.Validate(); err != nil {
			return fmt.Errorf("invalid audit configuration: %w", err)
		}
		auditor, err := audit.NewAuditorWithTransport(
			s.config.AuditConfig,
			"streamable-http", // vMCP uses streamable HTTP transport
		)
		if err != nil {
			return fmt.Errorf("failed to create auditor: %w", err)
		}
		mcpHandler = auditor.Middleware(mcpHandler)
		logger.Info("Audit middleware enabled for MCP endpoints")
	}

	// Apply authentication middleware if configured (runs first in chain)
	if s.config.AuthMiddleware != nil {
		mcpHandler = s.config.AuthMiddleware(mcpHandler)
		logger.Info("Authentication middleware enabled for MCP endpoints")
	}

	// Apply recovery middleware last (so it executes first and catches panics from all other middleware)
	mcpHandler = recovery.Middleware(mcpHandler)
	logger.Info("Recovery middleware enabled for MCP endpoints")

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
	logger.Infof("Health endpoints available at %s/health, %s/ping, %s/status, and %s/api/backends/health",
		actualAddr, actualAddr, actualAddr, actualAddr)

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
			logger.Warnf("Failed to start health monitor, disabling health monitoring: %v", err)
			s.healthMonitorMu.Lock()
			s.healthMonitor = nil
			s.healthMonitorMu.Unlock()
		} else {
			logger.Info("Health monitor started")
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
		s.statusReportingCtx, s.statusReportingCancel = context.WithCancel(ctx)

		// Start periodic status reporting in background with internal context
		statusConfig := DefaultStatusReportingConfig()
		statusConfig.Reporter = s.statusReporter
		go s.periodicStatusReporting(s.statusReportingCtx, statusConfig)
	}

	// Wait for either context cancellation or server error
	select {
	case <-ctx.Done():
		logger.Info("Context cancelled, shutting down server")
		return s.Stop(context.Background())
	case err := <-errCh:
		// HTTP server error - log and tear down cleanly
		logger.Errorf("HTTP server error: %v", err)
		if stopErr := s.Stop(context.Background()); stopErr != nil {
			// Combine errors if Stop() also fails
			return fmt.Errorf("server error: %w; stop error: %v", err, stopErr)
		}
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

	// Stop health monitor to clean up health check goroutines
	s.healthMonitorMu.RLock()
	healthMon := s.healthMonitor
	s.healthMonitorMu.RUnlock()

	if healthMon != nil {
		if err := healthMon.Stop(); err != nil {
			errs = append(errs, fmt.Errorf("failed to stop health monitor: %w", err))
		}
	}

	// Stop optimizer integration if configured
	if s.optimizerIntegration != nil {
		if err := s.optimizerIntegration.Close(); err != nil {
			errs = append(errs, fmt.Errorf("failed to close optimizer integration: %w", err))
		}
	}

	// Cancel status reporting goroutine if running
	if s.statusReportingCancel != nil {
		s.statusReportingCancel()
	}

	// Run shutdown functions (e.g., status reporter, future components)
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
			logger.Errorf("Failed to encode readiness response: %v", err)
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
			logger.Errorf("Failed to encode readiness response: %v", err)
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
		logger.Errorf("Failed to encode readiness response: %v", err)
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
//  4. Injects capabilities into the SDK session (or delegates to optimizer if enabled)
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
	logger.Debugw("OnRegisterSession hook called", "session_id", sessionID)

	// Get capabilities from context (discovered by middleware)
	caps, ok := discovery.DiscoveredCapabilitiesFromContext(ctx)
	if !ok || caps == nil {
		logger.Warnw("no discovered capabilities in context for OnRegisterSession hook",
			"session_id", sessionID)
		return
	}

	// Validate that routing table exists
	if caps.RoutingTable == nil {
		logger.Warnw("routing table is nil in discovered capabilities",
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
	vmcpSess.SetTools(caps.Tools)
	logger.Debugw("routing table and tools stored in VMCPSession",
		"session_id", sessionID,
		"tool_count", len(caps.RoutingTable.Tools),
		"resource_count", len(caps.RoutingTable.Resources),
		"prompt_count", len(caps.RoutingTable.Prompts))

	// Delegate to optimizer integration if enabled
	if s.config.OptimizerIntegration != nil {
		handled, err := s.config.OptimizerIntegration.HandleSessionRegistration(
			ctx,
			sessionID,
			caps,
			s.mcpServer,
			s.capabilityAdapter.ToSDKResources,
		)
		if err != nil {
			logger.Errorw("failed to handle session registration with optimizer",
				"error", err,
				"session_id", sessionID)
			return
		}
		if handled {
			// Optimizer handled the registration, we're done
			return
		}
		// If optimizer didn't handle it, fall through to normal registration
	}

	// Inject capabilities into SDK session
	if err := s.injectCapabilities(sessionID, caps); err != nil {
		logger.Errorw("failed to inject session capabilities",
			"error", err,
			"session_id", sessionID)
		return
	}

	logger.Infow("session capabilities injected",
		"session_id", sessionID,
		"tool_count", len(caps.Tools),
		"resource_count", len(caps.Resources))
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
		logger.Debugf("Validated workflow definition: %s", name)
	}

	if len(validDefs) > 0 {
		logger.Infof("Loaded %d valid composite tool workflows", len(validDefs))
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
		logger.Errorf("Failed to encode backend health response: %v", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write(data); err != nil {
		logger.Errorf("Failed to write backend health response: %v", err)
	}
}

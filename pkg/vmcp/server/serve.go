// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"cmp"
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/stacklok/toolhive/pkg/audit"
	asrunner "github.com/stacklok/toolhive/pkg/authserver/runner"
	"github.com/stacklok/toolhive/pkg/telemetry"
	transportsession "github.com/stacklok/toolhive/pkg/transport/session"
	"github.com/stacklok/toolhive/pkg/vmcp"
	vmcpconfig "github.com/stacklok/toolhive/pkg/vmcp/config"
	"github.com/stacklok/toolhive/pkg/vmcp/core"
	"github.com/stacklok/toolhive/pkg/vmcp/health"
	"github.com/stacklok/toolhive/pkg/vmcp/server/sessionmanager"
	vmcpstatus "github.com/stacklok/toolhive/pkg/vmcp/status"
)

// ServerConfig holds the transport-only runtime configuration for Serve.
//
// It carries the subset of the legacy server.Config that configures the HTTP/SDK
// runtime, plus the cross-cutting TelemetryProvider/AuditConfig that are consumed
// by both the core (core.New) and the transport (Serve) — not a clean partition.
// The fields the core exclusively owns (Aggregator, Router, BackendClient, Authz)
// live on core.Config instead and are absent here. Two collaborators are shared
// rather than core-owned, so they appear on both configs: BackendRegistry (the
// Serve session layer also enumerates backends when creating a session) and the
// composite-tool WorkflowDefs (carried indirectly via SessionManagerConfig, whose
// ComposerFactory closes over the core-owned router/backend client). The
// composition root passes the same BackendRegistry instance to both core.New and
// Serve and assembles SessionManagerConfig.
//
// The name intentionally stutters as server.ServerConfig: it is mandated by the
// New/Serve split, and the package's existing Config is the legacy transport
// config being decomposed, so it cannot be reused for this transport-only type.
//
//nolint:revive // exported-stutter is intentional; see the doc comment above.
type ServerConfig struct {
	// Name and Version are exposed in the MCP protocol via the mcp-go server.
	Name    string
	Version string

	// GroupRef is the name of the MCPGroup containing backend workloads, used for
	// operational visibility in the status endpoint and logging.
	GroupRef string

	// Host is the bind address (default: "127.0.0.1").
	Host string

	// Port is the bind port. A zero value means "let the OS assign a random port".
	Port int

	// EndpointPath is the MCP endpoint path (default: "/mcp").
	EndpointPath string

	// SessionTTL is the session time-to-live duration (default: 30 minutes).
	SessionTTL time.Duration

	// AuthMiddleware is the optional authentication middleware applied to MCP routes.
	// If nil, no authentication is required.
	AuthMiddleware func(http.Handler) http.Handler

	// AuthInfoHandler is the optional handler for the
	// /.well-known/oauth-protected-resource endpoint.
	AuthInfoHandler http.Handler

	// AuthServer is the optional embedded authorization server. When non-nil, its
	// routes are registered on the mux alongside the protected resource metadata.
	AuthServer *asrunner.EmbeddedAuthServer

	// HealthMonitor is the already-built backend health monitor (constructed at the
	// composition root so the same instance's StatusProvider can be injected into the
	// core). Serve owns only its Start/Stop lifecycle. Nil disables health monitoring.
	HealthMonitor *health.Monitor

	// StatusReportingInterval is the interval for reporting status updates.
	// If zero, the default reporting interval is used.
	StatusReportingInterval time.Duration

	// StatusReporter enables the vMCP runtime to report operational status.
	// If nil, status reporting is disabled.
	StatusReporter vmcpstatus.Reporter

	// Watcher is the optional Kubernetes backend watcher for dynamic mode, used by
	// the /readyz endpoint to gate readiness on cache sync.
	Watcher Watcher

	// BackendRegistry enumerates the configured backends. It is a shared
	// collaborator: the core (core.Config.BackendRegistry) consumes it for
	// capability aggregation, and the Serve session layer consumes it here — when
	// the OnRegisterSession hook fires, sessionmanager.CreateSession lists the
	// registry's backends to open the session's backend connections. The
	// composition root passes the same instance to both core.New and Serve.
	BackendRegistry vmcp.BackendRegistry

	// SessionStorage configures the session storage backend. When nil or provider is
	// "memory", local in-process storage is used.
	SessionStorage *vmcpconfig.SessionStorageConfig

	// SessionManagerConfig is the pre-built construction config for the vMCP session
	// manager (sessionmanager.New). FactoryConfig.Base is the required underlying
	// MultiSessionFactory; the config also carries the composite-tool WorkflowDefs and
	// ComposerFactory, the optimizer factory/config, and the telemetry provider.
	// Required; Serve returns an error when it is nil. The composition root assembles
	// it because the ComposerFactory closes over the core-owned router and backend
	// client.
	//
	// Caller responsibility: unlike server.New, Serve does NOT run validateWorkflows on
	// FactoryConfig.WorkflowDefs — the composition root must validate composite-tool
	// definitions before assembling this config (sessionmanager.New only checks the
	// WorkflowDefs/ComposerFactory pairing). This responsibility moves here with the
	// relocation and matters when server.New is routed through Serve in Phase 3.
	SessionManagerConfig *sessionmanager.FactoryConfig

	// TelemetryProvider is the cross-cutting telemetry provider (also consumed by
	// core.New). If nil, no telemetry is recorded.
	TelemetryProvider *telemetry.Provider

	// AuditConfig is the cross-cutting audit configuration (also consumed by
	// core.New). If nil, no audit logging is performed.
	AuditConfig *audit.Config
}

// Serve is the transport-side entry point of the New/Serve split: it wraps an
// already-constructed core [core.VMCP] and returns the existing *Server.
//
// At this phase Serve constructs the self-contained transport pieces — it applies
// the transport defaults (mirroring server.New), builds the mcp-go server, and wires
// the core's Close into the server's shutdown sequence — plus the SDK session layer:
// the three session hooks and the two-phase session-creation wiring (the transport
// session manager, the session data storage backend, and the vMCP session manager).
// It does NOT build the route mux or the HTTP lifecycle here — those remain in the
// carried-forward (*Server).Handler/Start/Stop, which register the unauthenticated
// routes (/health, /ping, /readyz, /status, /api/backends/health, the metrics and
// .well-known endpoints, and any embedded auth-server routes) when Serve's *Server
// is served.
//
// The remaining transport concerns — the authenticated middleware chain (#5441), the
// direct VMCP request path (#5442), and the AS runner / status reporter / optimizer /
// health monitor lifecycle (#5443) — are relocated under Serve by the subsequent
// Phase 2 tasks. server.New is not yet routed through Serve and keeps its own copy of
// the session wiring until Phase 3, so this is purely additive and observable behavior
// is unchanged.
//
// Serve returns a vmcp.ErrInvalidConfig-wrapped error for a nil cfg, a nil core, or a
// nil required collaborator (SessionManagerConfig or BackendRegistry). The session
// hooks close over the constructed *Server, so they are registered after it is
// assembled.
//
// Contract: the returned *Server now has a live session manager and backend registry,
// but a nil discovery manager, router, and backend client at this phase. It must NOT
// be Start()ed or served on the "/" MCP route until #5441/#5442 wire those fields —
// the carried-forward Handler passes them to discovery.Middleware, which would
// nil-deref. The unauthenticated routes (registered as direct mux entries) are safe
// to serve.
func Serve(ctx context.Context, v core.VMCP, cfg *ServerConfig) (*Server, error) {
	if cfg == nil {
		return nil, fmt.Errorf("%w: nil server config", vmcp.ErrInvalidConfig)
	}
	if v == nil {
		return nil, fmt.Errorf("%w: VMCP is required", vmcp.ErrInvalidConfig)
	}
	if cfg.SessionManagerConfig == nil {
		return nil, fmt.Errorf("%w: SessionManagerConfig is required", vmcp.ErrInvalidConfig)
	}
	if cfg.BackendRegistry == nil {
		// Required: the OnRegisterSession hook enumerates the registry to open the
		// session's backend connections, so a nil registry would nil-panic inside the
		// hook goroutine (where the error is swallowed) rather than here. Fail loudly.
		return nil, fmt.Errorf("%w: BackendRegistry is required", vmcp.ErrInvalidConfig)
	}

	serveCfg := buildServeConfig(cfg)

	// Build the SDK hooks up front so they can be passed to NewMCPServer, but
	// register their callbacks after srv is assembled below — the callbacks close
	// over srv (relocated from server.New, where the hooks object is co-located with
	// the mcp-go server for the same reason).
	hooks := &server.Hooks{}

	// Build the mcp-go server (mirrors server.New): tools and resources are
	// registered dynamically, so capabilities start disabled.
	mcpServer := server.NewMCPServer(
		serveCfg.Name,
		serveCfg.Version,
		server.WithToolCapabilities(false),
		server.WithResourceCapabilities(false, false),
		server.WithLogging(),
		server.WithHooks(hooks),
	)

	// Two-phase session-creation wiring (relocated from server.New). The transport
	// session manager holds the lightweight Streamable HTTP placeholders; the
	// pluggable data storage holds serialisable session metadata (memory or Redis,
	// reading THV_SESSION_REDIS_PASSWORD); the vMCP session manager owns the live,
	// node-local MultiSessions.
	sessionManager := transportsession.NewManager(serveCfg.SessionTTL, transportsession.NewStreamableSession)

	sessionDataStorage, err := buildSessionDataStorage(ctx, serveCfg)
	if err != nil {
		return nil, fmt.Errorf("failed to create session data storage: %w", err)
	}
	// Close sessionDataStorage if Serve returns an error after this point so the
	// background cleanup goroutine does not leak (go-style: pair acquisition with
	// release).
	closeStorageOnErr := true
	defer func() {
		if closeStorageOnErr {
			_ = sessionDataStorage.Close()
		}
	}()

	vmcpSessMgr, optimizerCleanup, err := sessionmanager.New(sessionDataStorage, cfg.SessionManagerConfig, cfg.BackendRegistry)
	if err != nil {
		return nil, err
	}

	srv := &Server{
		config:             serveCfg,
		mcpServer:          mcpServer,
		backendRegistry:    cfg.BackendRegistry,
		sessionManager:     sessionManager,
		sessionDataStorage: sessionDataStorage,
		vmcpSessionMgr:     vmcpSessMgr,
		ready:              make(chan struct{}),
		healthMonitor:      cfg.HealthMonitor,
		statusReporter:     cfg.StatusReporter,
	}

	if optimizerCleanup != nil {
		srv.shutdownFuncs = append(srv.shutdownFuncs, optimizerCleanup)
	}

	// Serve owns the injected core's lifecycle: release its backend connections
	// when the server stops. core.VMCP.Close is idempotent, so this is safe even
	// if Stop runs before Start.
	srv.shutdownFuncs = append(srv.shutdownFuncs, func(context.Context) error {
		return v.Close()
	})

	// Register the three SDK hooks against the assembled *Server (relocated from
	// server.New). They drive phase-2 of two-phase creation (OnRegisterSession) and
	// the cross-pod Redis re-hydration of per-session tools (OnBeforeListTools /
	// OnBeforeCallTool); the *Server receiver methods are unchanged.
	hooks.AddOnRegisterSession(func(hookCtx context.Context, session server.ClientSession) {
		srv.handleSessionRegistration(hookCtx, session)
	})
	hooks.AddBeforeListTools(func(hookCtx context.Context, _ any, _ *mcp.ListToolsRequest) {
		srv.lazyInjectSessionTools(hookCtx)
	})
	hooks.AddBeforeCallTool(func(hookCtx context.Context, _ any, _ *mcp.CallToolRequest) {
		srv.lazyInjectSessionTools(hookCtx)
	})

	// Disarm the close-on-error guard: the Server is fully constructed.
	closeStorageOnErr = false
	return srv, nil
}

// buildServeConfig maps the transport-only ServerConfig onto the existing *Config
// the carried-forward (*Server) methods read from, applying the same transport
// defaults server.New applies today. Defaults are applied here rather than by
// mutating the caller's ServerConfig (go-style: copy before mutating caller input).
// Port 0 is left untouched to mean "OS-assigned".
//
// Several Config fields are deliberately NOT mapped at this phase (see
// TestBuildServeConfigMapsSharedFields, which guards this list against drift):
//   - AuthzMiddleware: the authenticated/authz middleware chain is relocated under
//     Serve by #5441, after which authorization moves to the core admission seam.
//   - HealthMonitorConfig: Serve receives the already-built *health.Monitor via
//     ServerConfig.HealthMonitor (A2) and assigns it to the Server directly, so it
//     never needs the monitor's construction config.
//   - StatusReporter: Serve assigns ServerConfig.StatusReporter to the Server field
//     directly (like HealthMonitor); nothing reads Config.StatusReporter on the Serve
//     path, so mapping it here would be dead.
//   - SessionFactory, OptimizerFactory, OptimizerConfig: the vMCP session manager is
//     built directly in Serve from ServerConfig.SessionManagerConfig (a pre-built
//     *sessionmanager.FactoryConfig that carries the session factory and optimizer
//     wiring), not via Config→New, so these Config fields are unused on the Serve path.
func buildServeConfig(cfg *ServerConfig) *Config {
	return &Config{
		Name:                    cmp.Or(cfg.Name, defaultServerName),
		Version:                 cmp.Or(cfg.Version, defaultServerVersion),
		GroupRef:                cfg.GroupRef,
		Host:                    cmp.Or(cfg.Host, defaultHost),
		Port:                    cfg.Port,
		EndpointPath:            cmp.Or(cfg.EndpointPath, defaultEndpointPath),
		SessionTTL:              cmp.Or(cfg.SessionTTL, defaultSessionTTL),
		AuthMiddleware:          cfg.AuthMiddleware,
		AuthInfoHandler:         cfg.AuthInfoHandler,
		AuthServer:              cfg.AuthServer,
		TelemetryProvider:       cfg.TelemetryProvider,
		AuditConfig:             cfg.AuditConfig,
		StatusReportingInterval: cfg.StatusReportingInterval,
		Watcher:                 cfg.Watcher,
		SessionStorage:          cfg.SessionStorage,
	}
}

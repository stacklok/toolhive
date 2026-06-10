// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"cmp"
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/mark3labs/mcp-go/server"

	"github.com/stacklok/toolhive/pkg/audit"
	asrunner "github.com/stacklok/toolhive/pkg/authserver/runner"
	"github.com/stacklok/toolhive/pkg/telemetry"
	"github.com/stacklok/toolhive/pkg/vmcp"
	vmcpconfig "github.com/stacklok/toolhive/pkg/vmcp/config"
	"github.com/stacklok/toolhive/pkg/vmcp/core"
	"github.com/stacklok/toolhive/pkg/vmcp/health"
	"github.com/stacklok/toolhive/pkg/vmcp/optimizer"
	vmcpsession "github.com/stacklok/toolhive/pkg/vmcp/session"
	vmcpstatus "github.com/stacklok/toolhive/pkg/vmcp/status"
)

// ServerConfig holds the transport-only runtime configuration for Serve.
//
// It carries the subset of the legacy server.Config that configures the HTTP/SDK
// runtime, plus the cross-cutting TelemetryProvider/AuditConfig that are consumed
// by both the core (core.New) and the transport (Serve) — not a clean partition.
// The fields the core owns (Aggregator, Router, BackendRegistry, BackendClient,
// WorkflowDefs, Authz) live on core.Config instead and are absent here.
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

	// PassthroughHeaders is an allowlist of incoming client header names the vMCP
	// forwards, unchanged, to every backend it calls (see headerforward.CaptureMiddleware).
	// Empty disables capture.
	PassthroughHeaders []string

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

	// SessionFactory creates MultiSessions for session management. Required; Serve
	// returns an error when it is nil.
	SessionFactory vmcpsession.MultiSessionFactory

	// SessionStorage configures the session storage backend. When nil or provider is
	// "memory", local in-process storage is used.
	SessionStorage *vmcpconfig.SessionStorageConfig

	// OptimizerFactory builds an optimizer from a list of tools. If nil, the
	// optimizer is disabled.
	OptimizerFactory func(context.Context, []server.ServerTool) (optimizer.Optimizer, error)

	// OptimizerConfig holds the parsed optimizer search parameters. A nil value
	// disables the optimizer.
	OptimizerConfig *optimizer.Config

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
// At this phase Serve constructs only the self-contained transport pieces: it
// applies the transport defaults (mirroring server.New), builds the mcp-go server,
// and wires the core's Close into the server's shutdown sequence. It does NOT build
// the route mux or the HTTP lifecycle here — those remain in the carried-forward
// (*Server).Handler/Start/Stop, which register the unauthenticated routes (/health,
// /ping, /readyz, /status, /api/backends/health, the metrics and .well-known
// endpoints, and any embedded auth-server routes) when Serve's *Server is served.
//
// The remaining transport concerns — the SDK session hooks and two-phase session
// creation, the authenticated middleware chain, the direct VMCP request path, and
// the AS runner / status reporter / optimizer / health monitor lifecycle — are
// relocated under Serve by the subsequent Phase 2 tasks. server.New is not yet
// routed through Serve, so this is purely additive and observable behavior is
// unchanged.
//
// Serve returns a vmcp.ErrInvalidConfig-wrapped error for a nil cfg, a nil core, or
// a nil required collaborator (SessionFactory). The ctx parameter is reserved for
// the transport wiring relocated by later phases.
//
// Contract: the returned *Server has nil session manager, discovery manager, and
// backend registry at this phase. It must NOT be Start()ed or served on the "/" MCP
// route until #5440/#5441/#5442 wire those fields — the carried-forward Handler
// passes them to WithSessionIdManager and discovery.Middleware, which would nil-deref.
// The unauthenticated routes (registered as direct mux entries) are safe to serve.
func Serve(_ context.Context, v core.VMCP, cfg *ServerConfig) (*Server, error) {
	if cfg == nil {
		return nil, fmt.Errorf("%w: nil server config", vmcp.ErrInvalidConfig)
	}
	if v == nil {
		return nil, fmt.Errorf("%w: VMCP is required", vmcp.ErrInvalidConfig)
	}
	if cfg.SessionFactory == nil {
		return nil, fmt.Errorf("%w: SessionFactory is required", vmcp.ErrInvalidConfig)
	}

	serveCfg := buildServeConfig(cfg)

	// Build the mcp-go server (mirrors server.New): tools and resources are
	// registered dynamically, so capabilities start disabled. Hooks are empty for
	// now; the session hooks are relocated here by a later Phase 2 task.
	mcpServer := server.NewMCPServer(
		serveCfg.Name,
		serveCfg.Version,
		server.WithToolCapabilities(false),
		server.WithResourceCapabilities(false, false),
		server.WithLogging(),
		server.WithHooks(&server.Hooks{}),
	)

	srv := &Server{
		config:         serveCfg,
		mcpServer:      mcpServer,
		ready:          make(chan struct{}),
		healthMonitor:  cfg.HealthMonitor,
		statusReporter: cfg.StatusReporter,
	}

	// Serve owns the injected core's lifecycle: release its backend connections
	// when the server stops. core.VMCP.Close is idempotent, so this is safe even
	// if Stop runs before Start.
	srv.shutdownFuncs = append(srv.shutdownFuncs, func(context.Context) error {
		return v.Close()
	})

	return srv, nil
}

// buildServeConfig maps the transport-only ServerConfig onto the existing *Config
// the carried-forward (*Server) methods read from, applying the same transport
// defaults server.New applies today. Defaults are applied here rather than by
// mutating the caller's ServerConfig (go-style: copy before mutating caller input).
// Port 0 is left untouched to mean "OS-assigned".
//
// Three Config fields are deliberately NOT mapped at this phase (see
// TestBuildServeConfigMapsSharedFields, which guards this list against drift):
//   - AuthzMiddleware: the authenticated/authz middleware chain is relocated under
//     Serve by #5441, after which authorization moves to the core admission seam.
//   - HealthMonitorConfig: Serve receives the already-built *health.Monitor via
//     ServerConfig.HealthMonitor (A2) and assigns it to the Server directly, so it
//     never needs the monitor's construction config.
//   - StatusReporter: Serve assigns ServerConfig.StatusReporter to the Server field
//     directly (like HealthMonitor); nothing reads Config.StatusReporter on the Serve
//     path, so mapping it here would be dead.
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
		PassthroughHeaders:      cfg.PassthroughHeaders,
		AuthServer:              cfg.AuthServer,
		TelemetryProvider:       cfg.TelemetryProvider,
		AuditConfig:             cfg.AuditConfig,
		StatusReportingInterval: cfg.StatusReportingInterval,
		Watcher:                 cfg.Watcher,
		OptimizerFactory:        cfg.OptimizerFactory,
		OptimizerConfig:         cfg.OptimizerConfig,
		SessionFactory:          cfg.SessionFactory,
		SessionStorage:          cfg.SessionStorage,
	}
}

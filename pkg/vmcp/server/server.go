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
	"maps"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/stacklok/toolhive-core/mcpcompat/server"
	tcredis "github.com/stacklok/toolhive-core/redis"
	"github.com/stacklok/toolhive/pkg/audit"
	"github.com/stacklok/toolhive/pkg/auth"
	asrunner "github.com/stacklok/toolhive/pkg/authserver/runner"
	"github.com/stacklok/toolhive/pkg/authz"
	"github.com/stacklok/toolhive/pkg/bodylimit"
	mcpparser "github.com/stacklok/toolhive/pkg/mcp"
	baseratelimit "github.com/stacklok/toolhive/pkg/ratelimit"
	"github.com/stacklok/toolhive/pkg/recovery"
	"github.com/stacklok/toolhive/pkg/telemetry"
	transportmiddleware "github.com/stacklok/toolhive/pkg/transport/middleware"
	transportsession "github.com/stacklok/toolhive/pkg/transport/session"
	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/aggregator"
	"github.com/stacklok/toolhive/pkg/vmcp/codemode"
	"github.com/stacklok/toolhive/pkg/vmcp/composer"
	vmcpconfig "github.com/stacklok/toolhive/pkg/vmcp/config"
	"github.com/stacklok/toolhive/pkg/vmcp/core"
	"github.com/stacklok/toolhive/pkg/vmcp/headerforward"
	"github.com/stacklok/toolhive/pkg/vmcp/health"
	"github.com/stacklok/toolhive/pkg/vmcp/optimizer"
	vmcpratelimit "github.com/stacklok/toolhive/pkg/vmcp/ratelimit"
	"github.com/stacklok/toolhive/pkg/vmcp/router"
	"github.com/stacklok/toolhive/pkg/vmcp/server/sessionmanager"
	vmcpsession "github.com/stacklok/toolhive/pkg/vmcp/session"
	vmcpstatus "github.com/stacklok/toolhive/pkg/vmcp/status"
)

const (
	// defaultReadHeaderTimeout prevents slowloris attacks by limiting time to read request headers.
	defaultReadHeaderTimeout = 10 * time.Second

	// defaultReadTimeout is the maximum duration for reading the entire request, including body.
	defaultReadTimeout = 30 * time.Second

	// defaultWriteTimeout is the server-level write deadline set on http.Server.WriteTimeout.
	// It protects all routes (health, metrics, well-known, etc.) from slow-write clients.
	// For qualifying SSE (GET) connections, transportmiddleware.WriteTimeout clears this
	// per-request via http.ResponseController.SetWriteDeadline(time.Time{}) (golang/go#16100).
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

	// Transport defaults shared by New and Serve (buildServeConfig) so the two
	// entry points cannot drift while New is not yet routed through Serve.
	defaultServerName    = "toolhive-vmcp"
	defaultServerVersion = "0.1.0"
	defaultHost          = "127.0.0.1"
	defaultEndpointPath  = "/mcp"

	// capabilityCacheTTL bounds how long the per-identity aggregated capability view is
	// reused before re-sweeping backends. The core re-aggregates on every call (it holds no
	// per-session cache), so without this the Serve path does a full backend tools/list sweep
	// per tool call; a short TTL collapses bursts of calls into ~one sweep while keeping
	// staleness tighter than the legacy once-per-session refresh.
	capabilityCacheTTL = 30 * time.Second
)

// heartbeatInterval returns the configured heartbeat interval, or the default
// when the configured value is zero or negative (unset or invalid).
func heartbeatInterval(d time.Duration) time.Duration {
	if d <= 0 {
		return defaultHeartbeatInterval
	}
	return d
}

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

	// HeartbeatInterval configures the SSE keep-alive ping interval on GET
	// connections. When zero, the Handler defaults to defaultHeartbeatInterval (30s).
	// Prevents proxies/load balancers from closing idle SSE connections.
	//
	// This is intentionally plumbed end-to-end (ServerConfig → Config → Handler →
	// WithHeartbeatInterval) ahead of any CLI-flag or CRD wiring. No external entry
	// point sets it yet, so in practice it is always the default unless an embedder
	// assigns it programmatically; the plumbing exists so surfacing a flag/field
	// later is a one-line change rather than a re-thread through the server.
	HeartbeatInterval time.Duration

	// AuthMiddleware is the optional authentication middleware to apply to MCP routes.
	// If nil, no authentication is required.
	// This should be a composed middleware chain (e.g., TokenValidator + MCP parser).
	AuthMiddleware func(http.Handler) http.Handler

	// AuthzMiddleware is the optional authorization middleware to apply AFTER discovery.
	// Split from AuthMiddleware so authz can access discovered tool annotations
	// injected by the annotation enrichment middleware.
	// If nil, no authorization is performed.
	AuthzMiddleware func(http.Handler) http.Handler

	// AuthInfoHandler is the optional handler for /.well-known/oauth-protected-resource endpoint.
	// Exposes OIDC discovery information about the protected resource.
	AuthInfoHandler http.Handler

	// PassthroughHeaders is an allowlist of incoming client header names that the
	// vMCP forwards, unchanged, to every backend it calls. They are captured
	// per-request at the incoming edge (headerforward.CaptureMiddleware) and merged
	// into the per-session backend client's header-forward config. Empty disables
	// capture (no middleware is installed).
	PassthroughHeaders []string

	// RateLimiter is the optional core-layer limiter applied at CallTool.
	// It runs below the session optimizer, so tool names are already resolved
	// to the backend tool name before bucket selection.
	RateLimiter baseratelimit.Limiter

	// AuthServer is the optional embedded authorization server.
	// When non-nil, the routes returned by Routes() are registered on the mux
	// alongside the protected resource metadata endpoint.
	AuthServer *asrunner.EmbeddedAuthServer

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

	// OptimizerConfig holds the parsed optimizer search parameters (typed values).
	// When non-nil, Start() creates the search store, wires the OptimizerFactory,
	// and registers the store cleanup in shutdownFuncs.
	// A nil value disables the optimizer.
	OptimizerConfig *optimizer.Config

	// CodeModeConfig enables vMCP code mode. When non-nil, New wraps the core with a
	// codemode decorator that advertises the execute_tool_script virtual tool and runs
	// submitted Starlark scripts, whose inner tool calls route back through core.CallTool
	// (so the core admission seam authorizes each one). The decorator sits below any
	// optimizer: execute_tool_script appears in the core's tool set, so an enabled
	// optimizer indexes and routes to it like any other tool. A nil value (the default)
	// leaves the core undecorated and execute_tool_script absent from tools/list.
	//
	// Unlike the optimizer (which is mutually exclusive with Authz, see the guard in New),
	// code mode may be combined with Authz: a script's inner tool calls are re-authorized
	// by real name through the core admission seam, so Cedar remains the gate on everything
	// a script can do. See the codemode.decorator doc for the full rationale.
	CodeModeConfig *codemode.Config

	// StatusReporter enables vMCP runtime to report operational status.
	// In Kubernetes mode: Updates VirtualMCPServer.Status (requires RBAC)
	// In CLI mode: NoOpReporter (no persistent status)
	// If nil, status reporting is disabled.
	StatusReporter vmcpstatus.Reporter

	// SessionFactory creates MultiSessions for session management.
	// Required; must not be nil.
	SessionFactory vmcpsession.MultiSessionFactory

	// SessionStorage configures the session storage backend.
	// When nil or provider is "memory", local in-process storage is used.
	// When provider is "redis", a Redis-backed store is created for cross-pod
	// session persistence; the Redis password is read from the
	// THV_SESSION_REDIS_PASSWORD environment variable.
	SessionStorage *vmcpconfig.SessionStorageConfig

	// Aggregator merges backend capabilities into the advertised set. It is consumed
	// by the domain core (core.Config.Aggregator) that New builds via deriveCoreConfig:
	// the core is the single aggregator on the New/Serve path, so the session factory
	// must NOT also aggregate. Required; New fails (via core.New) when nil. The
	// composition root supplies it (the same aggregator that backs discovery).
	Aggregator aggregator.Aggregator

	// Authz is the authorizer-agnostic authorization config fed to the core admission
	// seam (core.Config.Authz). A nil value means authorization is unconfigured
	// (allow-all), matching the legacy `AuthzMiddleware != nil` guard: the composition
	// root populates it only when Cedar policies exist. It is distinct from the
	// (vestigial) AuthzMiddleware field — the core enforces authz from this config, not
	// from the HTTP middleware. When non-nil, Name must be non-empty (the Cedar resource
	// entity name).
	Authz *authz.Config
}

// Server is the Virtual MCP Server that aggregates multiple backends.
type Server struct {
	config *Config

	// core is the domain VMCP and the single source of truth for the advertised
	// capability set and call routing: session registration sources
	// tools/resources from core.ListTools/ListResources, request handlers
	// delegate to core.CallTool/ReadResource, and authorization is enforced by
	// the core admission seam. Serve always sets it, so it is non-nil for every
	// server (BackendHealth keeps a defensive nil guard regardless).
	core core.VMCP

	// authzGateEnabled installs the pre-dispatch authorization CallGate in Handler.
	// Authz is core-owned and stripped from the transport ServerConfig (see
	// deriveServerConfig / buildServeConfig), so it never reaches s.config; New —
	// the only composition path that knows the authz config — sets this flag from
	// cfg.Authz != nil after Serve builds the Server. It stays false for direct-Serve
	// callers, which have no way to configure authorization and run allow-all cores.
	authzGateEnabled bool

	// optimizerFactory builds a per-session optimizer over the core's advertised
	// tools. Set only when the optimizer is enabled (nil otherwise). When non-nil,
	// session registration advertises find_tool/
	// call_tool in place of the raw core tools and dispatches call_tool's inner
	// invocation through core.CallTool. The shared store and cleanup are owned by the
	// session manager; this is the resolved factory surfaced via Manager.OptimizerFactory.
	optimizerFactory func(context.Context, []server.ServerTool) (optimizer.Optimizer, error)

	// MCP protocol server (stacklok/toolhive-core/mcpcompat)
	mcpServer *server.MCPServer

	// HTTP server for Streamable HTTP transport
	httpServer *http.Server

	// Network listener (tracks actual bound port when using port 0)
	listener   net.Listener
	listenerMu sync.RWMutex

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
	sessionManager *transportsession.Manager

	// sessionDataStorage is the pluggable key-value backend for session metadata.
	// Currently always LocalSessionDataStorage (in-memory, single-process).
	// Redis-backed storage for multi-pod deployments is not yet wired.
	sessionDataStorage transportsession.DataStorage

	// vmcpSessionMgr manages session-scoped backend client lifecycle.
	vmcpSessionMgr SessionManager

	// listChanged consumes backend list_changed notifications and propagates
	// them to affected, still-live sessions (see list_changed.go). Serve always
	// constructs and starts it; server.New sets its cache invalidator once the
	// caching aggregator is known. Nil only for a *Server built by a test that
	// bypasses Serve entirely.
	listChanged *listChangedCoordinator

	// Ready channel signals when the server is ready to accept connections.
	// Closed once the listener is created and serving.
	ready     chan struct{}
	readyOnce sync.Once

	// statusReporter enables vMCP to report operational status to control plane.
	// Nil if status reporting is disabled.
	statusReporter vmcpstatus.Reporter

	// shutdownFuncs contains cleanup functions to run during Stop().
	// Populated during Start() initialization before blocking; no mutex needed
	// since Stop() is only called after Start()'s select returns.
	shutdownFuncs []func(context.Context) error
}

// buildSessionDataStorage constructs the DataStorage backend from cfg.
// When cfg.SessionStorage is nil or provider is "memory" (or empty), local in-process
// storage is used. When provider is "redis", a Redis-backed store is created
// using the address, DB, and key prefix from cfg.SessionStorage; the password
// is read from the THV_SESSION_REDIS_PASSWORD environment variable.
// Any other provider value is a misconfiguration and returns an error.
func buildSessionDataStorage(ctx context.Context, cfg *Config) (transportsession.DataStorage, error) {
	// Default to in-process storage when session storage is not configured,
	// or when the provider is explicitly "memory" or left empty.
	if cfg.SessionStorage == nil ||
		cfg.SessionStorage.Provider == "" ||
		strings.EqualFold(cfg.SessionStorage.Provider, "memory") {
		return transportsession.NewLocalSessionDataStorage(cfg.SessionTTL)
	}
	if cfg.SessionStorage.Provider != "redis" {
		return nil, fmt.Errorf("unsupported session storage provider %q (supported: \"memory\", \"redis\")",
			cfg.SessionStorage.Provider)
	}
	keyPrefix := cfg.SessionStorage.KeyPrefix
	if keyPrefix == "" {
		keyPrefix = "thv:vmcp:session:"
	}
	redisCfg := tcredis.Config{
		Addr:     cfg.SessionStorage.Address,
		Password: os.Getenv(vmcpconfig.RedisPasswordEnvVar),
		DB:       int(cfg.SessionStorage.DB),
	}
	slog.Info("using Redis session storage",
		"address", cfg.SessionStorage.Address,
		"db", cfg.SessionStorage.DB,
		"key_prefix", keyPrefix,
	)
	return transportsession.NewRedisSessionDataStorage(ctx, redisCfg, keyPrefix, cfg.SessionTTL)
}

// New creates a Virtual MCP Server by composing the domain core (core.New) with the
// transport (Serve).
//
// It is the composition root for the in-memory Config form: it builds the backend
// health monitor (A2), assembles the core via core.New using the config projected by
// deriveCoreConfig, and hands that core to Serve with the transport config projected by
// deriveServerConfig. The transport/core wiring it once performed inline now lives
// behind core.New + Serve.
//
// The backendRegistry parameter provides the list of available backends:
// - For static mode (CLI), pass an immutable registry created from initial backends
// - For dynamic mode (K8s), pass a DynamicRegistry that will be updated by the operator
//
// Signature/contract changes for embedders (the body now routes through core.New + Serve):
//   - The discovery.Manager parameter was dropped when the discovery middleware was
//     removed; capability discovery is the core's responsibility. Existing callers must
//     drop that argument.
//   - Config.Aggregator is now REQUIRED (core.New rejects a nil aggregator); the core is
//     the single source of the advertised capability set.
//   - Config.AuthzMiddleware is vestigial: the Serve path never applies it. Authorization
//     is enforced by the core admission seam from Config.Authz. An embedder that enforced
//     Cedar authz only via AuthzMiddleware must set Config.Authz instead — New returns an
//     error (ErrInvalidConfig) if AuthzMiddleware is set without Authz, so a silent
//     allow-all fails fast at construction.
func New(
	ctx context.Context,
	cfg *Config,
	rt router.Router,
	backendClient vmcp.BackendClient,
	backendRegistry vmcp.BackendRegistry,
	workflowDefs map[string]*composer.WorkflowDefinition,
) (*Server, error) {
	// Resolve transport defaults for the transport side on a COPY (WithDefaults does not
	// mutate cfg). The core receives the RAW cfg so its ServerName is the un-defaulted
	// cfg.Name: Cedar authz keys on the real VirtualMCPServer name, not the synthetic
	// "toolhive-vmcp" transport fallback.
	resolved := WithDefaults(cfg)

	// AuthzMiddleware is vestigial on the New/Serve path (Serve never applies it; authz
	// moved to the core admission seam). Setting it without a corresponding Config.Authz
	// would silently degrade to allow-all — a security regression for an embedder that
	// relied on it for Cedar authz — so fail fast instead of logging a warning the embedder
	// is unlikely to notice. cli/serve.go sets Authz whenever it sets AuthzMiddleware, so
	// this never fires in-tree.
	if cfg.AuthzMiddleware != nil && cfg.Authz == nil {
		return nil, fmt.Errorf("%w: Config.AuthzMiddleware is set but has no effect on the "+
			"New/Serve path; set Config.Authz to enforce authorization, or clear AuthzMiddleware",
			vmcp.ErrInvalidConfig)
	}

	// The core admission seam has no representation for the optimizer's meta-tools
	// (find_tool/call_tool), so combining Authz with the optimizer would let those calls
	// bypass Cedar evaluation entirely. Fail fast per the admission seam contract (see the
	// core.Admission doc); the optimizer keeps its own HTTP-path authz until a focused PR.
	if cfg.Authz != nil && cfg.OptimizerConfig != nil {
		return nil, fmt.Errorf("%w: Config.Authz and Config.OptimizerConfig are mutually "+
			"exclusive; the optimizer meta-tools (find_tool, call_tool) are not represented "+
			"in the core admission seam", vmcp.ErrInvalidConfig)
	}

	// Cedar resource entities are scoped to MCP::"<Name>" (see Config.Authz), so an empty
	// Name with Authz set would key policies on MCP::"" and silently fail to match. core.New
	// also rejects this (its admission seam), but fail at the construction root too for a
	// clearer error consistent with the guards above.
	if cfg.Authz != nil && cfg.Name == "" {
		return nil, fmt.Errorf("%w: Config.Name is required when Config.Authz is set "+
			"(it is the Cedar resource entity name)", vmcp.ErrInvalidConfig)
	}

	// The SDK elicitation adapter wraps the mcp-go server Serve builds below, so it
	// cannot exist before core.New. Give the core a late-bound requester now and bind the
	// real adapter to Serve's server before serving begins (RequestElicitation is only
	// invoked at request time, after bind).
	elicitation := newLateBoundElicitationRequester()

	// Wrap the aggregator in a per-identity caching decorator: the core re-derives the
	// advertised view on every call, so without this the Serve path re-sweeps every backend's
	// tools/list per tool call. The cache is keyed on identity + forwarded credentials, so it
	// never serves one caller's capability view to another.
	cachedAgg := aggregator.NewCachingAggregator(cfg.Aggregator, capabilityCacheTTL)

	coreVMCP, err := core.New(deriveCoreConfig(
		cfg, cachedAgg, rt, backendClient, backendRegistry, workflowDefs,
		cfg.Authz, elicitation,
	))
	if err != nil {
		return nil, err
	}

	if cfg.RateLimiter != nil {
		coreVMCP = vmcpratelimit.NewDecorator(coreVMCP, cfg.RateLimiter)
	}

	// Wrap the core with the code mode decorator when enabled. It sits BELOW any optimizer:
	// ListTools now advertises execute_tool_script alongside the backend tools, so the
	// Serve-layer optimizer (if enabled) indexes it like any other tool, and the script's
	// inner calls route back through core.CallTool for admission. The decorator delegates
	// Close to the inner core, so the closeCoreOnErr guard below still releases it.
	if cfg.CodeModeConfig != nil {
		coreVMCP = codemode.NewDecorator(coreVMCP, cfg.CodeModeConfig)
	}

	// core.New started the workflow state store's cleanup goroutine and the backend health
	// monitor (both owned by the core now). If Serve fails after this point, close the core so
	// neither leaks (mirrors Serve's closeStorageOnErr guard); on success the core's lifecycle
	// is owned by srv.Stop, so the guard is disarmed before returning.
	closeCoreOnErr := true
	defer func() {
		if closeCoreOnErr {
			_ = coreVMCP.Close()
		}
	}()

	// On the New/Serve path the core is the single aggregator and the source of the
	// advertised set; the session factory only opens per-session backend connections and
	// binds identity. AdvertiseFromCore makes the session manager source tools from the
	// core and (when the optimizer is enabled) build the Serve-layer optimizer over the
	// core's tools instead of decorating the factory. Composite-tool workflows are owned
	// by the core, so no WorkflowDefs/ComposerFactory are wired here.
	sessMgrCfg := &sessionmanager.FactoryConfig{
		Base:              cfg.SessionFactory,
		OptimizerConfig:   cfg.OptimizerConfig,
		OptimizerFactory:  cfg.OptimizerFactory,
		TelemetryProvider: cfg.TelemetryProvider,
		AdvertiseFromCore: true,
	}

	srv, err := Serve(ctx, coreVMCP, deriveServerConfig(resolved, backendRegistry, sessMgrCfg))
	if err != nil {
		return nil, err
	}

	// Enable the pre-dispatch authorization gate when Cedar policies are configured.
	// Authz is core-owned and not carried on the transport ServerConfig Serve builds
	// from, so New — which holds cfg.Authz — sets the flag here (see Server.authzGateEnabled).
	srv.authzGateEnabled = cfg.Authz != nil

	// Bind the elicitation adapter to the SDK server Serve built so composite-workflow
	// elicitation reaches the same mcp-go server that serves client traffic.
	elicitAdapter := NewSDKElicitationAdapter(srv.MCPServer())
	elicitation.bind(elicitAdapter)

	// Give the list_changed coordinator its cache invalidator now that the
	// caching aggregator is known. cachedAgg is the exact instance the core
	// calls into; type-asserting it here (rather than threading a second
	// collaborator through core.New/Serve) keeps CacheInvalidator scoped to
	// this one wiring point. cachedAgg implements it whenever caching is
	// enabled (ttl > 0); when disabled, NewCachingAggregator returns cfg.Aggregator
	// unwrapped, which does not implement it, and invalidation is skipped —
	// there is no cache to go stale.
	if inv, ok := cachedAgg.(aggregator.CacheInvalidator); ok {
		srv.listChanged.setInvalidator(inv)
	}

	// Bind the server->client forwarders onto the concrete backend client so a
	// backend's mid-call elicitation, sampling, progress/logging, and
	// list_changed traffic is relayed to the downstream client on the same
	// session. This mirrors the elicitation late-binding above (the SDK adapters
	// wrap the mcp-go server Serve just built, so they cannot exist before this
	// point); a backend client that does not implement the binder simply does
	// not forward server->client traffic.
	if binder, ok := backendClient.(vmcp.ClientForwarderBinder); ok {
		binder.BindForwarders(
			elicitAdapter,
			NewSDKSamplingAdapter(srv.MCPServer()),
			NewSDKNotifierAdapter(srv.MCPServer()),
			srv.BackendListChangedNotifier(),
		)
	}

	closeCoreOnErr = false // Serve succeeded; srv.Stop now owns the core's lifecycle.
	return srv, nil
}

// Handler builds and returns the MCP HTTP handler without starting a listener.
// This enables embedding the vmcp server inside another HTTP server or framework.
//
// The returned handler includes all routes (health, metrics, well-known, MCP)
// and the full middleware chain (recovery, body limit, header validation, auth,
// rate limit, audit, MCP parsing, telemetry).
//
// Each call builds a fresh handler. The method is safe to call multiple times.
// All returned handlers share the same underlying MCPServer and SessionManager,
// so callers should not serve concurrent traffic through multiple handlers.
func (s *Server) Handler(_ context.Context) (http.Handler, error) {
	// Create Streamable HTTP server with ToolHive session management.
	streamableOpts := []server.StreamableHTTPOption{
		server.WithEndpointPath(s.config.EndpointPath),
		server.WithSessionIdManager(s.vmcpSessionMgr),
		server.WithHeartbeatInterval(heartbeatInterval(s.config.HeartbeatInterval)),
	}
	// Install the pre-dispatch authorization gate only when authz is configured
	// (allow-all otherwise, matching the core admission seam's guard). The gate
	// re-runs the core admission decision for gated methods so a denied tools/call,
	// resources/read, or prompts/get is rejected as HTTP 403 + JSON-RPC 403 before
	// the SDK dispatches it, instead of the SDK's 200 result.
	if s.authzGateEnabled {
		streamableOpts = append(streamableOpts, server.WithCallGate(s.authzCallGate()))
	}
	streamableServer := server.NewStreamableHTTPServer(s.mcpServer, streamableOpts...)

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

	// RFC 9728 protected resource metadata.
	// Always register a .well-known handler so OAuth discovery requests get a
	// clean response (404 JSON when auth is off) instead of falling through to
	// the MCP handler, which rejects GETs with a 406 JSON-RPC error that
	// breaks Claude Code's OAuth error parsing.
	wellKnownHandler := auth.NewWellKnownHandler(s.config.AuthInfoHandler)
	mux.Handle("/.well-known/", wellKnownHandler)
	if s.config.AuthInfoHandler != nil {
		slog.Debug("RFC 9728 OAuth protected resource metadata enabled")
	}

	// Register embedded auth server routes if configured
	if s.config.AuthServer != nil {
		s.config.AuthServer.RegisterHandlers(mux)
		slog.Debug("embedded authorization server routes registered")
	}

	// MCP endpoint - apply middleware chain (wrapping order, execution happens in reverse):
	// Code wraps: auth → rate-limit → audit → MCP-parsing → telemetry → classification
	// Execution order: recovery → body-limit → header-val → auth →
	//   rate-limit → audit → MCP-parsing → telemetry → classification → handler
	//
	// Upstream token refresh failures are detected inside AuthMiddleware itself:
	// GetAllUpstreamCredentials returns a non-empty failed-provider slice when
	// any upstream refresh fails, and the middleware short-circuits with
	// HTTP 401 + WWW-Authenticate before the request reaches any inner layer.
	//
	// The legacy HTTP authz, annotation-enrichment, and discovery layers have all been
	// removed: every caller now routes through Serve, so authorization is enforced by the
	// core admission seam (#5438) and capability/health filtering by the core, rather than
	// by HTTP middleware.
	//
	// Authorization additionally runs a pre-dispatch CallGate INSIDE the streamable
	// server (installed above only when Authz is configured): it re-runs the core
	// admission decision for tools/call, resources/read, and prompts/get and rejects a
	// denial as HTTP 403 + JSON-RPC 403 before dispatch. The gate sits before session
	// validation (403-before-404) and inside the audit middleware, so a denied call is
	// audited with outcome "denied". See call_gate.go.

	var mcpHandler http.Handler = streamableServer

	// Classify Modern (2026-07-28) vs Legacy at the decode seam and reject
	// malformed Modern requests before dispatch. No routing change: Legacy
	// and well-formed Modern requests both fall through to the same handler
	// (Modern dispatch lands in Phase 2, #5756). Applied before telemetry
	// (i.e. it runs closer to the handler) so a rejection is still recorded
	// by the telemetry middleware instead of bypassing it entirely.
	mcpHandler = classificationMiddleware(mcpHandler)

	if s.config.TelemetryProvider != nil {
		mcpHandler = s.config.TelemetryProvider.Middleware(s.config.Name, "streamable-http")(mcpHandler)
		slog.Info("telemetry middleware enabled for MCP endpoints")
	}

	// Apply MCP parsing middleware to extract JSON-RPC method from request body.
	// This runs before telemetry so that recordMetrics can label metrics with the
	// actual mcp_method (e.g. "tools/call", "initialize") instead of "unknown".
	// Note: ParsingMiddleware is also composed inside the auth middleware (for audit/authz).
	// The second application here is a no-op because the context already holds a
	// ParsedMCPRequest; it exists only so the telemetry layer works correctly even
	// when auth middleware is nil.
	mcpHandler = mcpparser.ParsingMiddleware(mcpHandler)

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

	mcpHandler = s.applyForwardedHeaderCapture(mcpHandler)

	// Apply Accept header validation (rejects GET requests without Accept: text/event-stream)
	mcpHandler = headerValidatingMiddleware(mcpHandler)

	// Clear the write deadline for qualifying SSE connections (GET +
	// Accept: text/event-stream + MCP endpoint path) so the server-level
	// WriteTimeout does not kill long-lived SSE streams (see golang/go#16100).
	// Non-qualifying requests are left untouched; http.Server.WriteTimeout
	// (defaultWriteTimeout) remains in effect for them.
	mcpHandler = transportmiddleware.WriteTimeout(s.config.EndpointPath)(mcpHandler)

	// Cap request body size before the MCP parser (and all inner middleware)
	// buffers it via io.ReadAll, rejecting oversized bodies with 413. This is
	// parity with the proxy transports (see pkg/bodylimit). It only bounds the
	// request body and does not affect long-lived SSE response streams.
	mcpHandler = bodylimit.Middleware(bodylimit.DefaultMaxRequestBodySize)(mcpHandler)

	// Apply recovery middleware as outermost (catches panics from all inner middleware)
	mcpHandler = recovery.Middleware(mcpHandler)
	slog.Info("recovery middleware enabled for MCP endpoints")

	mux.Handle("/", mcpHandler)

	return mux, nil
}

// applyForwardedHeaderCapture wraps next with the forwarded-header capture
// middleware when passthrough headers are configured. It copies the allowlisted
// incoming headers into the request context so the per-session backend client
// forwards them to backends (see pkg/vmcp/headerforward). Identity-independent
// plumbing: its position relative to auth is immaterial; it must only run before
// session creation, which every inner handler satisfies. No-op when the allowlist
// is empty.
func (s *Server) applyForwardedHeaderCapture(next http.Handler) http.Handler {
	if len(s.config.PassthroughHeaders) == 0 {
		return next
	}
	slog.Info("forwarded-header capture enabled for MCP endpoints", "headers", s.config.PassthroughHeaders)
	return headerforward.CaptureMiddleware(s.config.PassthroughHeaders)(next)
}

// Start starts the Virtual MCP Server and begins serving requests.
func (s *Server) Start(ctx context.Context) error {
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

	// The backend health monitor is owned by the core (built and started in core.New, stopped
	// in core.Close), so the server no longer starts or stops it here.

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

	// The backend health monitor is stopped by core.Close (the core owns it); Serve registered
	// a shutdown function that closes the core, run in the loop below.

	// Run shutdown functions (e.g., status reporter cleanup, future components)
	for _, shutdown := range s.shutdownFuncs {
		if err := shutdown(ctx); err != nil {
			errs = append(errs, fmt.Errorf("failed to execute shutdown function: %w", err))
		}
	}

	// Stop session manager after HTTP server shutdown
	if s.sessionManager != nil {
		if err := s.sessionManager.Stop(); err != nil {
			errs = append(errs, fmt.Errorf("failed to stop session manager: %w", err))
		}
	}

	// Close session data storage last: HTTP server is down (no new in-flight requests),
	// all other components have stopped (no further restore or liveness checks).
	if s.sessionDataStorage != nil {
		if err := s.sessionDataStorage.Close(); err != nil {
			errs = append(errs, fmt.Errorf("failed to close session data storage: %w", err))
		}
	}

	if len(errs) > 0 {
		slog.Error("errors during shutdown", "errors", errs)
		return errors.Join(errs...)
	}

	slog.Info("virtual MCP Server stopped")
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

// MCPServer returns the underlying mcpcompat *server.MCPServer instance
// servicing this vMCP server's /mcp endpoint.
//
// Intended for embedders that wrap the vMCP composer in their own pipeline and
// need to drive SDK-level operations (such as RequestElicitation) against the
// same server that handles incoming client traffic. A parallel MCPServer
// constructed by the embedder will not work: ClientSession correlation is
// keyed to the server that received the initialize request.
//
// Trust boundary: this accessor is in-process only; the returned pointer is
// the same instance for the lifetime of the Server and is safe for concurrent
// use per mcpcompat guarantees.
//
// Safe operations include RequestElicitation against an active session,
// registering observability hooks, and reading registered
// tools/resources/prompts. Callers MUST NOT call shutdown/close or alter the
// serving lifecycle; that is owned by (*Server).Start and (*Server).Stop.
// Callers SHOULD prefer per-session tool registration over global
// AddTool/SetTools: globally registered tools are served to every session and
// bypass vMCP's per-session capability scoping (they still pass through the
// HTTP auth/authz chain).
//
// Elicitation callers must:
//   - Propagate the inbound request ctx so ClientSession resolves, and pass a
//     bounded deadline; RequestElicitation blocks on the remote client.
//   - Ensure the connected client advertised the "elicitation" capability
//     during initialize.
//   - Handle accept / decline / cancel responses distinctly per MCP 2025-06-18;
//     keep requestedSchema as a flat object of primitives (string / number /
//     integer / boolean / enum), which is what conforming clients accept.
//   - Never include secrets, credentials, tokens, or internal addressing
//     (backend IDs, pod names, routing-table entries) in the prompt message
//     or schema: elicitation payloads are surfaced to end-user clients.
func (s *Server) MCPServer() *server.MCPServer {
	return s.mcpServer
}

// Ready returns a channel that is closed when the server is ready to accept connections.
// This is useful for testing and synchronization.
func (s *Server) Ready() <-chan struct{} {
	return s.ready
}

// setSessionResourcesDirect sets resources directly on the session via the SessionWithResources
// interface, analogous to setSessionToolsDirect for resources.
func setSessionResourcesDirect(session server.ClientSession, resources []server.ServerResource) error {
	sessionWithResources, ok := session.(server.SessionWithResources)
	if !ok {
		return fmt.Errorf("session does not support per-session resources")
	}

	existing := sessionWithResources.GetSessionResources()
	resourceMap := make(map[string]server.ServerResource, len(existing)+len(resources))
	for k, v := range existing {
		resourceMap[k] = v
	}
	for _, res := range resources {
		resourceMap[res.Resource.URI] = res
	}
	sessionWithResources.SetSessionResources(resourceMap)
	return nil
}

// setSessionResourceTemplatesDirect sets resource templates directly on the session via
// the SessionWithResourceTemplates interface, analogous to setSessionResourcesDirect for
// resource templates. Keyed by URI template string.
func setSessionResourceTemplatesDirect(session server.ClientSession, templates []server.ServerResourceTemplate) error {
	sessionWithTemplates, ok := session.(server.SessionWithResourceTemplates)
	if !ok {
		return fmt.Errorf("session does not support per-session resource templates")
	}

	existing := sessionWithTemplates.GetSessionResourceTemplates()
	templateMap := make(map[string]server.ServerResourceTemplate, len(existing)+len(templates))
	for k, v := range existing {
		templateMap[k] = v
	}
	for _, tmpl := range templates {
		templateMap[tmpl.Template.URITemplate] = tmpl
	}
	sessionWithTemplates.SetSessionResourceTemplates(templateMap)
	return nil
}

// setSessionPromptsDirect sets prompts directly on the session via the SessionWithPrompts
// interface, analogous to setSessionResourcesDirect for prompts.
func setSessionPromptsDirect(session server.ClientSession, prompts []server.ServerPrompt) error {
	sessionWithPrompts, ok := session.(server.SessionWithPrompts)
	if !ok {
		return fmt.Errorf("session does not support per-session prompts")
	}

	existing := sessionWithPrompts.GetSessionPrompts()
	promptMap := make(map[string]server.ServerPrompt, len(existing)+len(prompts))
	for k, v := range existing {
		promptMap[k] = v
	}
	for _, p := range prompts {
		promptMap[p.Prompt.Name] = p
	}
	sessionWithPrompts.SetSessionPrompts(promptMap)
	return nil
}

// setSessionToolsDirect sets tools directly on the session via the SessionWithTools
// interface, bypassing MCPServer.AddSessionTools. This avoids sending notifications
// through the session's notification channel, which would accumulate as stale
// messages during session registration (the notification goroutine from the
// initialize request has already exited at that point).
func setSessionToolsDirect(session server.ClientSession, tools []server.ServerTool) error {
	sessionWithTools, ok := session.(server.SessionWithTools)
	if !ok {
		return fmt.Errorf("session does not support per-session tools")
	}

	// Merge with any existing tools (preserves tools set by earlier calls)
	existing := sessionWithTools.GetSessionTools()
	toolMap := make(map[string]server.ServerTool, len(existing)+len(tools))
	for k, v := range existing {
		toolMap[k] = v
	}
	for _, tool := range tools {
		toolMap[tool.Tool.Name] = tool
	}
	sessionWithTools.SetSessionTools(toolMap)
	return nil
}

// setSessionToolsReplace sets tools directly on the session via the
// SessionWithTools interface, REPLACING any existing session tools rather than
// merging with them (compare setSessionToolsDirect, used at registration, which
// merges to preserve tools set by earlier calls). The list_changed coordinator
// uses this to re-project a session's tool set after a backend's tools changed:
// a tool the backend removed must actually disappear from the session, which a
// merge could never achieve. SetSessionTools itself does the add/remove
// reconciliation against the live go-sdk server (session.go's syncSessionTools),
// including the RemoveTools call for names that dropped out — that reconciliation
// is what emits the downstream notifications/tools/list_changed.
func setSessionToolsReplace(session server.ClientSession, tools []server.ServerTool) error {
	sessionWithTools, ok := session.(server.SessionWithTools)
	if !ok {
		return fmt.Errorf("session does not support per-session tools")
	}

	toolMap := make(map[string]server.ServerTool, len(tools))
	for _, tool := range tools {
		toolMap[tool.Tool.Name] = tool
	}
	sessionWithTools.SetSessionTools(toolMap)
	return nil
}

// lazyInjectSessionTools injects tools into the SDK ephemeral session for sessions
// that were reconstructed from Redis on a different pod (cross-pod session sharing).
//
// When a client connects to pod B with an existing session ID (established on pod A),
// the SDK creates an ephemeral session with no tools because OnRegisterSession only fires
// during Initialize, which the client doesn't re-send to pod B. This method is called
// from OnBeforeListTools and OnBeforeCallTool hooks to lazily inject the tools before
// the SDK handler reads from the per-session tool store.
//
// For sessions initialized on this pod (normal case), tools are already in the store
// (set by setSessionToolsDirect during OnRegisterSession); this method is a no-op.
func (s *Server) lazyInjectSessionTools(ctx context.Context) {
	sess := server.ClientSessionFromContext(ctx)
	if sess == nil {
		return
	}
	sessionWithTools, ok := sess.(server.SessionWithTools)
	if !ok {
		return
	}
	if len(sessionWithTools.GetSessionTools()) > 0 {
		return // tools already registered (normal pod-local case)
	}
	sessionID := sess.SessionID()

	// Re-derive the tool set the same way registration did, so cross-pod re-injection
	// matches the advertised set: a fresh core.ListTools for the request identity (the core
	// is stateless, so re-deriving on pod B is the cache-miss equivalent of the
	// once-per-session registration call), optimizer-wrapped into find_tool/call_tool when
	// the optimizer is enabled — via serveSessionTools, the same helper registration uses.
	//
	// Note: this lists under the CURRENT request identity, not the session's bound identity.
	// For the realistic same-principal load-balanced case they are equal, so the advertised
	// set is identical across pods. A cross-identity re-hydration would advertise the
	// requester's own filtered set, but the call-time binding check (enforceSessionBinding,
	// run before core.CallTool/ReadResource) is the backstop — it rejects a mismatched
	// caller, so no other principal's capabilities can be invoked.
	identity, _ := auth.IdentityFromContext(ctx)
	adaptedTools, err := s.serveSessionTools(ctx, sessionID, identity)
	if err != nil || len(adaptedTools) == 0 {
		slog.Debug("lazyInjectSessionTools: no tools available for session", "session_id", sessionID)
		return
	}
	if err := setSessionToolsDirect(sess, adaptedTools); err != nil {
		slog.Warn("lazyInjectSessionTools: failed to inject tools", "session_id", sessionID, "error", err)
	}
}

// handleSessionRegistration processes a new MCP session registration.
// It fires AFTER the session is registered in the SDK.
func (s *Server) handleSessionRegistration(
	ctx context.Context,
	session server.ClientSession,
) {
	// Error is logged and handled within handleSessionRegistrationImpl.
	// The session is terminated on failure; no further action needed here.
	_ = s.handleSessionRegistrationImpl(ctx, session)
}

// handleSessionRegistrationImpl handles session registration.
//
// It is invoked from handleSessionRegistration and:
//  1. Creates a MultiSession with real backend HTTP connections via CreateSession().
//  2. Retrieves SDK-format tools and resources with session-scoped routing handlers.
//  3. Registers backend tools, composite tools, and resources with the SDK for the session.
//
// Tool and resource calls are routed directly through the session's backend connections
// rather than through a global router.
// Composite tool executors use the shared backend client and router.
//
// # Current capability surface
//
//   - Optimizer mode: when configured, all tools (backend + composite) are
//     indexed into the optimizer and only find_tool/call_tool are exposed,
//     using session-scoped tool handlers.
//
//   - Prompts: injected per session via SessionWithPrompts, mirroring resources;
//     see injectCoreSessionCapabilities.
func (s *Server) handleSessionRegistrationImpl(ctx context.Context, session server.ClientSession) (retErr error) {
	sessionID := session.SessionID()
	slog.Debug("creating session-scoped backends", "session_id", sessionID)

	// Defer cleanup: if any error occurs, terminate the session and log failures.
	defer func() {
		if retErr != nil {
			if _, termErr := s.vmcpSessionMgr.Terminate(sessionID); termErr != nil {
				slog.Warn("failed to clean up session after error",
					"session_id", sessionID,
					"error", termErr,
					"original_error", retErr)
			}
		}
	}()

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
	var multiSess vmcpsession.MultiSession
	if multiSess, retErr = s.vmcpSessionMgr.CreateSession(ctx, sessionID); retErr != nil {
		slog.Error("failed to create session-scoped backends",
			"session_id", sessionID,
			"error", retErr)
		return retErr
	}

	// The core is the single authoritative aggregation: source the advertised tool/resource
	// set from core.ListTools/ListResources (called once per session here) and install
	// handlers that route through the core. CreateSession above still establishes the bound
	// session record (identity binding, TTL, Validate). The returned error becomes retErr
	// (named return), so the defer terminates the session on failure.
	if retErr = s.injectCoreSessionCapabilities(ctx, session); retErr != nil {
		return retErr
	}

	// Register with the list_changed coordinator only once capability injection
	// has succeeded, so a session that never fully registered is never tracked.
	// backendIDs comes from the MultiSession's own metadata (populated by the
	// factory at CreateSession) rather than being re-derived here, so it always
	// matches the backends this session actually connected to. fwdHeaders is
	// cloned: it is retained for the whole session lifetime off the request path,
	// so defense-in-depth against a caller mutating the request-scoped map after
	// this returns (copy-before-mutate). identity is immutable (safe to retain)
	// and backendIDs is a fresh map, so neither needs cloning.
	identity, _ := auth.IdentityFromContext(ctx)
	s.listChanged.track(sessionID, &trackedSession{
		sess:       session,
		identity:   identity,
		fwdHeaders: maps.Clone(headerforward.ForwardedHeadersFromContext(ctx)),
		backendIDs: backendIDSetFromMetadata(multiSess),
	})
	return nil
}

// backendIDSetFromMetadata extracts the session's connected backend ID set from
// its MultiSession metadata (vmcpsession.MetadataKeyBackendIDs, a comma-separated
// sorted list written by the session factory). Returns an empty, non-nil set
// when the key is absent or empty rather than nil, so
// list_changed.affectedKinds's range-over-nil-map degrades to "no backends",
// not a panic.
func backendIDSetFromMetadata(sess vmcpsession.MultiSession) map[string]struct{} {
	ids := make(map[string]struct{})
	if sess == nil {
		return ids
	}
	raw, ok := sess.GetMetadataValue(vmcpsession.MetadataKeyBackendIDs)
	if !ok || raw == "" {
		return ids
	}
	for _, id := range strings.Split(raw, ",") {
		if id = strings.TrimSpace(id); id != "" {
			ids[id] = struct{}{}
		}
	}
	return ids
}

// backendHealth returns the core-owned backend health reporter, or nil when health
// monitoring is disabled (the core owns the monitor's lifecycle; #5443 reversal). The
// s.core nil guard is defensive: a Serve-built server always has a non-nil core.
func (s *Server) backendHealth() health.Reporter {
	if s.core == nil {
		return nil
	}
	return s.core.BackendHealth()
}

// GetBackendHealthStatus returns the health status of a specific backend.
// Returns error if health monitoring is disabled or backend not found.
func (s *Server) GetBackendHealthStatus(backendID string) (vmcp.BackendHealthStatus, error) {
	healthMon := s.backendHealth()

	if healthMon == nil {
		return vmcp.BackendUnknown, fmt.Errorf("health monitoring is disabled")
	}
	return healthMon.GetBackendStatus(backendID)
}

// GetBackendHealthState returns the full health state of a specific backend.
// Returns error if health monitoring is disabled or backend not found.
func (s *Server) GetBackendHealthState(backendID string) (*health.State, error) {
	healthMon := s.backendHealth()

	if healthMon == nil {
		return nil, fmt.Errorf("health monitoring is disabled")
	}
	return healthMon.GetBackendState(backendID)
}

// GetAllBackendHealthStates returns the health states of all backends.
// Returns empty map if health monitoring is disabled.
func (s *Server) GetAllBackendHealthStates() map[string]*health.State {
	healthMon := s.backendHealth()

	if healthMon == nil {
		return make(map[string]*health.State)
	}
	return healthMon.GetAllBackendStates()
}

// GetHealthSummary returns a summary of backend health across all backends.
// Returns zero-valued summary if health monitoring is disabled.
func (s *Server) GetHealthSummary() health.Summary {
	healthMon := s.backendHealth()

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
	healthMon := s.backendHealth()

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

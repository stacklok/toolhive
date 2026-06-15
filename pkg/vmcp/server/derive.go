// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"github.com/stacklok/toolhive/pkg/authz"
	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/aggregator"
	"github.com/stacklok/toolhive/pkg/vmcp/composer"
	"github.com/stacklok/toolhive/pkg/vmcp/core"
	"github.com/stacklok/toolhive/pkg/vmcp/health"
	"github.com/stacklok/toolhive/pkg/vmcp/router"
	"github.com/stacklok/toolhive/pkg/vmcp/server/sessionmanager"
)

// deriveServerConfig projects the transport-only configuration out of the legacy,
// in-memory server.Config onto the ServerConfig that Serve consumes. It is the
// transport half of the Phase 3 config decomposition (#5444); the composition root
// (#5445) will call it when server.New is reduced to a wrapper over
// Serve(ctx, core.New(deriveCoreConfig(cfg, …)), deriveServerConfig(cfg, …)).
//
// cfg is treated as read-only (go-style: copy before mutating caller input); a fresh
// ServerConfig is returned. This is a pure projection: transport defaults are resolved
// once at the composition root via WithDefaults (Option 1), so cfg already holds resolved
// values and deriveServerConfig applies no defaulting of its own. Port 0 still means
// "OS-assigned".
//
// Three collaborators the transport needs but server.Config does not carry directly are
// passed in by the composition root rather than reached out of cfg:
//   - healthMon: the *health.Monitor built at the composition root (A2). Serve owns only
//     its Start/Stop; the same instance's StatusProvider is injected into the core via
//     deriveCoreConfig. Nil disables health monitoring.
//   - backendRegistry: the shared backend registry (also passed to deriveCoreConfig);
//     the Serve session layer enumerates it when opening a session's connections.
//   - sessionManagerConfig: the pre-assembled *sessionmanager.FactoryConfig. The legacy
//     SessionFactory/OptimizerFactory/OptimizerConfig fields fold into it (#5440), and its
//     ComposerFactory closes over the core-owned router/backend client, so only the
//     composition root can build it.
//
// AuthzMiddleware is intentionally NOT projected: ServerConfig has no such field —
// authorization moved to the core admission seam (#5438). The AuthzMiddleware field on
// server.Config is kept as a vestigial input the Serve path ignores (cli/serve.go still
// sets it and must stay unchanged); only the dead HTTP authz/annotation blocks that read
// it are removed later (#5445).
func deriveServerConfig(
	cfg *Config,
	healthMon *health.Monitor,
	backendRegistry vmcp.BackendRegistry,
	sessionManagerConfig *sessionmanager.FactoryConfig,
) *ServerConfig {
	return &ServerConfig{
		Name:                    cfg.Name,
		Version:                 cfg.Version,
		GroupRef:                cfg.GroupRef,
		Host:                    cfg.Host,
		Port:                    cfg.Port, // 0 means "OS-assigned".
		EndpointPath:            cfg.EndpointPath,
		SessionTTL:              cfg.SessionTTL,
		AuthMiddleware:          cfg.AuthMiddleware,
		RateLimitMiddleware:     cfg.RateLimitMiddleware,
		AuthInfoHandler:         cfg.AuthInfoHandler,
		AuthServer:              cfg.AuthServer,
		HealthMonitor:           healthMon,
		StatusReportingInterval: cfg.StatusReportingInterval,
		StatusReporter:          cfg.StatusReporter,
		Watcher:                 cfg.Watcher,
		BackendRegistry:         backendRegistry,
		SessionStorage:          cfg.SessionStorage,
		SessionManagerConfig:    sessionManagerConfig,
		// Cross-cutting (also on core.Config) — R3, not a clean partition:
		TelemetryProvider: cfg.TelemetryProvider,
		AuditConfig:       cfg.AuditConfig,
	}
}

// deriveCoreConfig assembles the core.Config that core.New consumes from the legacy
// server.Config plus the collaborators the composition root builds. It is the core half
// of the Phase 3 config decomposition (#5444); #5445 will call it inside the reduced
// server.New as core.New(deriveCoreConfig(cfg, …)).
//
// cfg is treated as read-only; only its cross-cutting fields are read:
//   - ServerName = cfg.Name — the Cedar resource entity name, matching the serverName the
//     legacy HTTP authz path threads through (cli/serve.go passes vmcpCfg.Name). The raw
//     name is used (not the transport default) so authorization keys on the real
//     VirtualMCPServer name rather than the synthetic "toolhive-vmcp" fallback.
//   - TelemetryProvider / AuditConfig — the cross-cutting fields shared with the transport
//     (R3, not a clean partition); deriveServerConfig copies the same two.
//
// Everything else is a collaborator passed through rather than reached out of cfg: the
// aggregator, router, backend client, backend registry, and workflow definitions that
// arrive as separate server.New parameters today; the authz config that feeds the
// admission seam (server.Config carries only the pre-built AuthzMiddleware, never the
// authz.Config, so it cannot come from cfg); the elicitation requester; and the health
// StatusProvider — the read-only view of the same *health.Monitor deriveServerConfig
// places on ServerConfig.HealthMonitor (A2). A nil healthProvider means no health
// filtering; a nil authzCfg means allow-all (matching today's AuthzMiddleware != nil
// guard).
func deriveCoreConfig(
	cfg *Config,
	agg aggregator.Aggregator,
	rt router.Router,
	backendClient vmcp.BackendClient,
	backendRegistry vmcp.BackendRegistry,
	workflowDefs map[string]*composer.WorkflowDefinition,
	authzCfg *authz.Config,
	elicitation vmcp.ElicitationRequester,
	healthProvider health.StatusProvider,
) *core.Config {
	return &core.Config{
		Aggregator:      agg,
		Router:          rt,
		BackendRegistry: backendRegistry,
		BackendClient:   backendClient,
		WorkflowDefs:    workflowDefs,
		Authz:           authzCfg,
		ServerName:      cfg.Name,
		// Cross-cutting (also on ServerConfig) — R3, not a clean partition:
		TelemetryProvider:    cfg.TelemetryProvider,
		AuditConfig:          cfg.AuditConfig,
		HealthStatusProvider: healthProvider,
		Elicitation:          elicitation,
	}
}

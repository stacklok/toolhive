// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package app

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/stacklok/toolhive-core/env"
	"github.com/stacklok/toolhive/pkg/auth/upstreamtoken"
	authserverrunner "github.com/stacklok/toolhive/pkg/authserver/runner"
	"github.com/stacklok/toolhive/pkg/authserver/server/keys"
	"github.com/stacklok/toolhive/pkg/vmcp"
	authfactory "github.com/stacklok/toolhive/pkg/vmcp/auth/factory"
	vmcpconfig "github.com/stacklok/toolhive/pkg/vmcp/config"
	"github.com/stacklok/toolhive/pkg/vmcp/optimizer"
	vmcpserver "github.com/stacklok/toolhive/pkg/vmcp/server"
	"github.com/stacklok/toolhive/pkg/vmcp/server/sessionmanager"
	vmcpsession "github.com/stacklok/toolhive/pkg/vmcp/session"
	"github.com/stacklok/toolhive/pkg/vmcp/session/optimizerdec"
	vmcpstatus "github.com/stacklok/toolhive/pkg/vmcp/status"
)

// BuildServerConfig derives *server.ServerConfig from vmcpCfg (plus opts) — an
// independent projection of the same input as BuildCore. The two derivations are
// independent: the same opts slice is passed to both, and shared stateful
// collaborators (telemetry provider, backend registry + watcher) are provided via
// options so each Build call receives the same instance.
//
// The cleanup func releases rate-limit middleware (if it returned one) and closes
// the embedded auth server (if BuildServerConfig created one from an injected
// RunConfig). It does NOT shut down the telemetry provider — that lifecycle belongs
// to the caller that provided it via WithTelemetryProvider.
//
// # Backend registry
//
// ServerConfig.BackendRegistry is required by server.Serve. When WithBackendRegistry
// is provided via opts, the same registry is used. When absent, BuildServerConfig
// resolves the registry from vmcpCfg using the same rules as BuildCore: for the
// "discovered" (Kubernetes) outgoingAuth source, callers MUST provide
// WithBackendRegistry — an error is returned otherwise, since two K8s watchers must
// not be started. BuildCore enforces the same requirement, so both entry points
// behave identically.
//
//nolint:gocyclo // Complexity from server transport initialization sequence is acceptable here.
func BuildServerConfig(ctx context.Context, cfg *vmcpconfig.Config, opts ...Option) (*vmcpserver.ServerConfig, func(), error) {
	if cfg == nil {
		return nil, noop, fmt.Errorf("%w: nil vmcp config", vmcp.ErrInvalidConfig)
	}
	// Validate before assembling so a programmatic embedder gets a loud error rather
	// than a silently unauthenticated server (e.g. nil IncomingAuth → anonymous). See
	// the matching guard in BuildCore.
	if err := vmcpconfig.NewValidator().Validate(cfg); err != nil {
		return nil, noop, fmt.Errorf("invalid vmcp config: %w", err)
	}
	o := applyOptions(opts)

	// Work on a copy to avoid mutating the caller's config: InjectSubjectProviderNames
	// (below) mutates OutgoingAuth strategies in place. The shallow struct copy alone
	// would still share the caller's OutgoingAuth/IncomingAuth pointers, so deep-copy
	// OutgoingAuth (the one InjectSubjectProviderNames writes to) via its generated
	// DeepCopy, and shallow-copy IncomingAuth defensively.
	cfgCopy := *cfg
	cfg = &cfgCopy
	if cfg.IncomingAuth != nil {
		incomingCopy := *cfg.IncomingAuth
		cfg.IncomingAuth = &incomingCopy
	}
	if cfg.OutgoingAuth != nil {
		cfg.OutgoingAuth = cfg.OutgoingAuth.DeepCopy()
	}

	var cleanupFuncs []func()

	// Build or use the embedded auth server.
	var embeddedAuthServer *authserverrunner.EmbeddedAuthServer
	if o.authServerRunConfig != nil {
		// Inject SubjectProviderName on token_exchange strategies that omitted it.
		if err := vmcpconfig.InjectSubjectProviderNames(cfg, o.authServerRunConfig); err != nil {
			runCleanup(cleanupFuncs)
			return nil, noop, fmt.Errorf("failed to default outgoing auth subject provider names: %w", err)
		}

		var err error
		embeddedAuthServer, err = authserverrunner.NewEmbeddedAuthServer(ctx, o.authServerRunConfig)
		if err != nil {
			runCleanup(cleanupFuncs)
			return nil, noop, fmt.Errorf("failed to create embedded auth server: %w", err)
		}
		cleanupFuncs = append(cleanupFuncs, func() {
			if closeErr := embeddedAuthServer.Close(); closeErr != nil {
				slog.Error("failed to close embedded auth server", "error", closeErr)
			}
		})
		slog.Info("embedded authorization server initialized")
	}

	// Build outgoing auth registry for session factory.
	outgoingRegistry, err := authfactory.NewOutgoingAuthRegistry(ctx, &env.OSReader{})
	if err != nil {
		runCleanup(cleanupFuncs)
		return nil, noop, fmt.Errorf("failed to create outgoing auth registry: %w", err)
	}

	// Extract upstream token reader and key provider from the embedded auth server,
	// if present, for use by the incoming auth middleware.
	var upstreamReader upstreamtoken.TokenReader
	var keyProvider keys.PublicKeyProvider
	if embeddedAuthServer != nil {
		stor := embeddedAuthServer.IDPTokenStorage()
		refresher := embeddedAuthServer.UpstreamTokenRefresher()
		upstreamReader = upstreamtoken.NewInProcessService(stor, refresher)
		keyProvider = embeddedAuthServer.KeyProvider()
	}

	// Determine optimizer config and pass-through tool names.
	optCfg, passThroughTools, err := resolveOptimizerConfig(cfg)
	if err != nil {
		runCleanup(cleanupFuncs)
		return nil, noop, err
	}

	// Warn on anonymous incoming auth before building middleware.
	incomingAuthCfg := resolveIncomingAuth(cfg)
	if incomingAuthCfg.Type == vmcpconfig.IncomingAuthTypeAnonymous {
		slog.Warn(
			"vMCP is configured with anonymous incoming auth; all anonymous sessions share a "+
				"single sentinel binding, so possession of a session ID is sufficient to act as "+
				"that session from any source. Anonymous mode is intended for development only.",
			"incoming_auth_type", vmcpconfig.IncomingAuthTypeAnonymous,
		)
	}
	slog.Info("setting up incoming authentication", "type", incomingAuthCfg.Type)

	// Build incoming auth middleware (authz middleware is vestigial; authorization
	// moved to the core admission seam and is built in BuildCore).
	authMiddleware, _, authInfoHandler, err := authfactory.NewIncomingAuthMiddleware(
		ctx, incomingAuthCfg, cfg.Name, passThroughTools, upstreamReader, keyProvider,
	)
	if err != nil {
		runCleanup(cleanupFuncs)
		return nil, noop, fmt.Errorf("failed to create authentication middleware: %w", err)
	}

	// Rate limiting is applied as a core-layer decorator in BuildCore (post #5522),
	// keyed on the resolved backend tool name below the optimizer — it is no longer
	// HTTP middleware and is therefore not part of the transport ServerConfig.

	// Resolve status reporter.
	statusReporter := o.statusReporter
	if statusReporter == nil {
		statusReporter, err = vmcpstatus.NewReporter()
		if err != nil {
			runCleanup(cleanupFuncs)
			return nil, noop, fmt.Errorf("failed to create status reporter: %w", err)
		}
	}

	// Build session factory and session manager config.
	sessionFactory := vmcpsession.NewSessionFactory(outgoingRegistry)
	sessMgrCfg := &sessionmanager.FactoryConfig{
		Base:              sessionFactory,
		OptimizerConfig:   optCfg,
		TelemetryProvider: o.telemetryProvider,
		AdvertiseFromCore: true,
	}

	// Resolve backend registry for ServerConfig.BackendRegistry.
	backendReg, watcher, err := resolveBackendRegistryForServerConfig(ctx, cfg, o)
	if err != nil {
		runCleanup(cleanupFuncs)
		return nil, noop, err
	}

	// Apply server transport defaults for any unset fields.
	resolved := vmcpserver.WithDefaults(&vmcpserver.Config{
		Name:       cfg.Name,
		Version:    o.version,
		GroupRef:   cfg.Group,
		Host:       o.host,
		Port:       o.port,
		SessionTTL: o.sessionTTL,
	})

	srvCfg := &vmcpserver.ServerConfig{
		Name:                    resolved.Name,
		Version:                 resolved.Version,
		GroupRef:                resolved.GroupRef,
		Host:                    resolved.Host,
		Port:                    resolved.Port,
		EndpointPath:            resolved.EndpointPath,
		SessionTTL:              resolved.SessionTTL,
		AuthMiddleware:          authMiddleware,
		AuthInfoHandler:         authInfoHandler,
		PassthroughHeaders:      cfg.PassthroughHeaders,
		AuthServer:              embeddedAuthServer,
		TelemetryProvider:       o.telemetryProvider,
		AuditConfig:             cfg.Audit,
		StatusReportingInterval: getStatusReportingInterval(cfg),
		StatusReporter:          statusReporter,
		Watcher:                 watcher,
		BackendRegistry:         backendReg,
		SessionStorage:          cfg.SessionStorage,
		SessionManagerConfig:    sessMgrCfg,
	}

	cleanup := func() { runCleanup(cleanupFuncs) }
	return srvCfg, cleanup, nil
}

// resolveBackendRegistryForServerConfig returns the backend registry to use in
// ServerConfig.BackendRegistry. Returns an error for the Kubernetes discovered mode
// when no pre-built registry is injected (to prevent starting a duplicate watcher).
func resolveBackendRegistryForServerConfig(
	ctx context.Context,
	cfg *vmcpconfig.Config,
	o *options,
) (vmcp.BackendRegistry, vmcpserver.Watcher, error) {
	if o.backendRegistry != nil {
		return o.backendRegistry, o.watcher, nil
	}

	if cfg.OutgoingAuth != nil && cfg.OutgoingAuth.Source == vmcpconfig.OutgoingAuthSourceDiscovered {
		return nil, nil, errDiscoveredModeRequiresRegistry
	}

	if len(cfg.Backends) > 0 {
		reg, err := buildStaticBackendRegistry(ctx, cfg)
		if err != nil {
			return nil, nil, err
		}
		return reg, nil, nil
	}

	reg, err := buildDynamicBackendRegistry(ctx, cfg)
	if err != nil {
		return nil, nil, err
	}
	return reg, nil, nil
}

// resolveOptimizerConfig derives the optimizer config and pass-through tool names.
// Returns nil config when the optimizer is disabled.
func resolveOptimizerConfig(cfg *vmcpconfig.Config) (*optimizer.Config, map[string]struct{}, error) {
	optCfg, err := optimizer.GetAndValidateConfig(cfg.Optimizer)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to validate optimizer config: %w", err)
	}
	if optCfg == nil {
		return nil, nil, nil
	}
	// When the optimizer is enabled its meta-tools must bypass the response filter in
	// tools/list so they are visible to clients.
	passThroughTools := map[string]struct{}{
		optimizerdec.FindToolName: {},
		optimizerdec.CallToolName: {},
	}
	return optCfg, passThroughTools, nil
}

// resolveIncomingAuth returns a non-nil IncomingAuthConfig from vmcpCfg, defaulting
// to anonymous when IncomingAuth is absent.
func resolveIncomingAuth(cfg *vmcpconfig.Config) *vmcpconfig.IncomingAuthConfig {
	if cfg.IncomingAuth != nil {
		return cfg.IncomingAuth
	}
	return &vmcpconfig.IncomingAuthConfig{Type: vmcpconfig.IncomingAuthTypeAnonymous}
}

// vmcpNamespace returns the Kubernetes namespace for rate-limiter scoping from the
// VMCP_NAMESPACE environment variable, falling back to "local".
func vmcpNamespace() string {
	namespace := os.Getenv("VMCP_NAMESPACE")
	if namespace == "" {
		return "local"
	}
	return namespace
}

// getStatusReportingInterval extracts the status reporting interval from vmcpCfg.
// Returns 0 when not configured, which uses the server default.
func getStatusReportingInterval(cfg *vmcpconfig.Config) time.Duration {
	if cfg.Operational != nil &&
		cfg.Operational.FailureHandling != nil &&
		cfg.Operational.FailureHandling.StatusReportingInterval > 0 {
		return time.Duration(cfg.Operational.FailureHandling.StatusReportingInterval)
	}
	return 0
}

// runCleanup runs all cleanup functions in LIFO order.
func runCleanup(fns []func()) {
	for i := len(fns) - 1; i >= 0; i-- {
		fns[i]()
	}
}

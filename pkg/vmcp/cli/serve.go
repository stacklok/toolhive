// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package cli provides the business logic for the vMCP serve and validate
// commands. It is designed to be imported by both the standalone vmcp binary
// (cmd/vmcp/app) and the thv vmcp subcommand (cmd/thv/app), keeping all
// server-initialization logic in one importable place.
package cli

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"time"

	"go.opentelemetry.io/otel/trace"
	"gopkg.in/yaml.v3"
	"k8s.io/client-go/rest"

	"github.com/stacklok/toolhive-core/env"
	"github.com/stacklok/toolhive/pkg/audit"
	"github.com/stacklok/toolhive/pkg/auth/upstreamtoken"
	authserverconfig "github.com/stacklok/toolhive/pkg/authserver"
	authserverrunner "github.com/stacklok/toolhive/pkg/authserver/runner"
	"github.com/stacklok/toolhive/pkg/authserver/server/keys"
	"github.com/stacklok/toolhive/pkg/container/runtime"
	"github.com/stacklok/toolhive/pkg/groups"
	"github.com/stacklok/toolhive/pkg/migration"
	"github.com/stacklok/toolhive/pkg/telemetry"
	"github.com/stacklok/toolhive/pkg/versions"
	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/aggregator"
	vmcpauth "github.com/stacklok/toolhive/pkg/vmcp/auth"
	"github.com/stacklok/toolhive/pkg/vmcp/auth/factory"
	vmcpclient "github.com/stacklok/toolhive/pkg/vmcp/client"
	"github.com/stacklok/toolhive/pkg/vmcp/config"
	"github.com/stacklok/toolhive/pkg/vmcp/discovery"
	"github.com/stacklok/toolhive/pkg/vmcp/health"
	"github.com/stacklok/toolhive/pkg/vmcp/k8s"
	"github.com/stacklok/toolhive/pkg/vmcp/optimizer"
	vmcprouter "github.com/stacklok/toolhive/pkg/vmcp/router"
	vmcpserver "github.com/stacklok/toolhive/pkg/vmcp/server"
	vmcpsession "github.com/stacklok/toolhive/pkg/vmcp/session"
	"github.com/stacklok/toolhive/pkg/vmcp/session/optimizerdec"
	vmcpstatus "github.com/stacklok/toolhive/pkg/vmcp/status"
)

// ServeConfig holds all parameters needed to start the vMCP server.
// Populated by the caller from Cobra flag values or equivalent.
// At least one of ConfigPath or GroupRef must be non-empty; ConfigPath takes
// precedence when both are provided.
type ServeConfig struct {
	// ConfigPath is the path to the vMCP YAML configuration file.
	// When set, takes precedence over GroupRef.
	ConfigPath string
	// GroupRef is a ToolHive group name used for zero-config quick mode when
	// ConfigPath is empty. A minimal in-memory config is generated from this value.
	GroupRef string
	// Host is the address the server binds to (e.g. "127.0.0.1").
	Host string
	// Port is the TCP port the server listens on.
	Port int
	// EnableAudit enables audit logging with default configuration when
	// the loaded config does not already define an audit section.
	EnableAudit bool
}

// validateQuickModeHost returns an error when the config represents quick mode
// (GroupRef set, ConfigPath empty) and Host is not a loopback address. Quick
// mode always uses anonymous auth, so binding to a non-loopback interface would
// expose an unauthenticated server on the network. Empty host is treated as the
// default loopback address; "localhost" is accepted as a known loopback name.
func (c ServeConfig) validateQuickModeHost() error {
	if c.ConfigPath != "" || c.GroupRef == "" {
		return nil
	}
	h := c.Host
	if h == "" {
		h = "127.0.0.1"
	}
	if h == "localhost" {
		return nil
	}
	ip := net.ParseIP(h)
	if ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("quick mode (--group) only supports loopback bind addresses (e.g. 127.0.0.1); got %q", c.Host)
	}
	return nil
}

// Serve loads configuration, initializes all subsystems, and starts the vMCP
// server. It blocks until the context is cancelled or the server stops.
//
//nolint:gocyclo // Complexity from server initialization sequence is acceptable here.
func Serve(ctx context.Context, cfg ServeConfig) error {
	if err := cfg.validateQuickModeHost(); err != nil {
		return err
	}

	// Load and validate configuration — file path takes precedence over group quick mode.
	vmcpCfg, err := func() (*config.Config, error) {
		switch {
		case cfg.ConfigPath != "":
			return loadAndValidateConfig(cfg.ConfigPath)
		case cfg.GroupRef != "":
			return generateQuickModeConfig(cfg.GroupRef)
		default:
			return nil, fmt.Errorf("either --config or --group must be specified")
		}
	}()
	if err != nil {
		return err
	}

	// Apply --enable-audit flag when the config has no audit section.
	if cfg.EnableAudit && vmcpCfg.Audit == nil {
		vmcpCfg.Audit = audit.DefaultConfig()
		vmcpCfg.Audit.Component = "vmcp-server"
		slog.Info("audit logging enabled with default configuration")
	}

	// Load auth server config from sibling file if present.
	// Skip in quick mode (no config file) — there is no sibling directory to search.
	var authServerRC *authserverconfig.RunConfig
	if cfg.ConfigPath != "" {
		authServerRC, err = loadAuthServerConfig(cfg.ConfigPath)
		if err != nil {
			return err
		}
	}

	// Auto-populate SubjectProviderName on any token_exchange strategy that
	// omitted it when an embedded auth server is active.
	config.InjectSubjectProviderNames(vmcpCfg, authServerRC)

	// Construct embedded authorization server if configured.
	var embeddedAuthServer *authserverrunner.EmbeddedAuthServer
	if authServerRC != nil {
		embeddedAuthServer, err = authserverrunner.NewEmbeddedAuthServer(ctx, authServerRC)
		if err != nil {
			return fmt.Errorf("failed to create embedded auth server: %w", err)
		}
		defer func() {
			if closeErr := embeddedAuthServer.Close(); closeErr != nil {
				slog.Error(fmt.Sprintf("failed to close embedded auth server: %v", closeErr))
			}
		}()
		slog.Info("embedded authorization server initialized")
	}

	// Discover backends and create client.
	backends, backendClient, outgoingRegistry, err := discoverBackends(ctx, vmcpCfg)
	if err != nil {
		return err
	}

	// Create conflict resolver based on configuration.
	conflictResolver, err := aggregator.NewConflictResolver(vmcpCfg.Aggregation)
	if err != nil {
		return fmt.Errorf("failed to create conflict resolver: %w", err)
	}

	// If telemetry is configured, create the provider early so aggregator can use it.
	var telemetryProvider *telemetry.Provider
	if vmcpCfg.Telemetry != nil {
		telemetryProvider, err = telemetry.NewProvider(ctx, *vmcpCfg.Telemetry)
		if err != nil {
			return fmt.Errorf("failed to create telemetry provider: %w", err)
		}
		defer func() {
			if shutdownErr := telemetryProvider.Shutdown(ctx); shutdownErr != nil {
				slog.Error(fmt.Sprintf("failed to shutdown telemetry provider: %v", shutdownErr))
			}
		}()
	}

	// Create aggregator with tracer provider (nil if telemetry not configured).
	var tracerProvider trace.TracerProvider
	if telemetryProvider != nil {
		tracerProvider = telemetryProvider.TracerProvider()
	}
	agg := aggregator.NewDefaultAggregator(backendClient, conflictResolver, vmcpCfg.Aggregation, tracerProvider)

	// DynamicRegistry tracks backends for dynamic discovery in Kubernetes mode.
	dynamicRegistry := vmcp.NewDynamicRegistry(backends)
	backendRegistry := vmcp.BackendRegistry(dynamicRegistry)

	discoveryMgr, err := discovery.NewManager(agg)
	if err != nil {
		return fmt.Errorf("failed to create discovery manager: %w", err)
	}
	slog.Info("dynamic backend registry enabled for Kubernetes environment")

	// Backend watcher for dynamic backend discovery.
	var backendWatcher *k8s.BackendWatcher

	// If outgoingAuth.source is "discovered", start K8s backend watcher.
	if vmcpCfg.OutgoingAuth != nil && vmcpCfg.OutgoingAuth.Source == "discovered" {
		slog.Info("detected dynamic backend discovery mode (outgoingAuth.source: discovered)")

		restConfig, err := rest.InClusterConfig()
		if err != nil {
			return fmt.Errorf("failed to get in-cluster config: %w", err)
		}

		namespace := os.Getenv("VMCP_NAMESPACE")
		if namespace == "" {
			return fmt.Errorf("VMCP_NAMESPACE environment variable not set")
		}

		backendWatcher, err = k8s.NewBackendWatcher(restConfig, namespace, vmcpCfg.Group, dynamicRegistry)
		if err != nil {
			return fmt.Errorf("failed to create backend watcher: %w", err)
		}

		go func() {
			slog.Info("starting Kubernetes backend watcher in background")
			if err := backendWatcher.Start(ctx); err != nil {
				slog.Error(fmt.Sprintf("Backend watcher stopped with error: %v", err))
			}
		}()

		slog.Info("kubernetes backend watcher started for dynamic backend discovery")
	}

	// Create router.
	rtr := vmcprouter.NewDefaultRouter()

	slog.Info(fmt.Sprintf("Setting up incoming authentication (type: %s)", vmcpCfg.IncomingAuth.Type))

	// Configure health monitoring if enabled.
	var healthMonitorConfig *health.MonitorConfig
	if vmcpCfg.Operational != nil &&
		vmcpCfg.Operational.FailureHandling != nil &&
		vmcpCfg.Operational.FailureHandling.HealthCheckInterval > 0 {

		checkInterval := time.Duration(vmcpCfg.Operational.FailureHandling.HealthCheckInterval)
		if vmcpCfg.Operational.FailureHandling.UnhealthyThreshold < 1 {
			return fmt.Errorf("invalid health check configuration: unhealthy threshold must be >= 1, got %d",
				vmcpCfg.Operational.FailureHandling.UnhealthyThreshold)
		}

		defaults := health.DefaultConfig()

		healthCheckTimeout := defaults.Timeout
		if vmcpCfg.Operational.FailureHandling.HealthCheckTimeout > 0 {
			healthCheckTimeout = time.Duration(vmcpCfg.Operational.FailureHandling.HealthCheckTimeout)
		}

		healthMonitorConfig = &health.MonitorConfig{
			CheckInterval:      checkInterval,
			UnhealthyThreshold: vmcpCfg.Operational.FailureHandling.UnhealthyThreshold,
			Timeout:            healthCheckTimeout,
			DegradedThreshold:  defaults.DegradedThreshold,
		}

		if vmcpCfg.Operational.FailureHandling.CircuitBreaker != nil {
			cbConfig := vmcpCfg.Operational.FailureHandling.CircuitBreaker
			healthMonitorConfig.CircuitBreaker = &health.CircuitBreakerConfig{
				Enabled:          cbConfig.Enabled,
				FailureThreshold: cbConfig.FailureThreshold,
				Timeout:          time.Duration(cbConfig.Timeout),
			}
			if cbConfig.Enabled {
				slog.Info(fmt.Sprintf("Circuit breaker enabled (threshold: %d failures, timeout: %v)",
					cbConfig.FailureThreshold, time.Duration(cbConfig.Timeout)))
			}
		}

		slog.Info("health monitoring configured from operational settings")
	}

	// Create status reporter.
	statusReporter, err := vmcpstatus.NewReporter()
	if err != nil {
		return fmt.Errorf("failed to create status reporter: %w", err)
	}

	optCfg, err := optimizer.GetAndValidateConfig(vmcpCfg.Optimizer)
	if err != nil {
		return fmt.Errorf("failed to validate optimizer config: %w", err)
	}

	envReader := &env.OSReader{}
	sessionFactory, err := createSessionFactory(
		envReader.Getenv("VMCP_SESSION_HMAC_SECRET"),
		runtime.IsKubernetesRuntimeWithEnv(envReader),
		outgoingRegistry,
		agg,
	)
	if err != nil {
		return err
	}

	// When the optimizer is enabled, its meta-tools must pass through the authz
	// response filter so they appear in tools/list.
	var passThroughTools map[string]struct{}
	if optCfg != nil {
		passThroughTools = map[string]struct{}{
			optimizerdec.FindToolName: {},
			optimizerdec.CallToolName: {},
		}
	}

	// Extract dependencies from the embedded auth server.
	var upstreamReader upstreamtoken.TokenReader
	var keyProvider keys.PublicKeyProvider
	if embeddedAuthServer != nil {
		stor := embeddedAuthServer.IDPTokenStorage()
		refresher := embeddedAuthServer.UpstreamTokenRefresher()
		upstreamReader = upstreamtoken.NewInProcessService(stor, refresher)
		keyProvider = embeddedAuthServer.KeyProvider()
	}

	authMiddleware, authzMiddleware, authInfoHandler, err :=
		factory.NewIncomingAuthMiddleware(ctx, vmcpCfg.IncomingAuth, passThroughTools, upstreamReader, keyProvider)
	if err != nil {
		return fmt.Errorf("failed to create authentication middleware: %w", err)
	}

	slog.Info(fmt.Sprintf("Incoming authentication configured: %s", vmcpCfg.IncomingAuth.Type))

	serverCfg := &vmcpserver.Config{
		Name:                    vmcpCfg.Name,
		Version:                 versions.Version,
		GroupRef:                vmcpCfg.Group,
		Host:                    cfg.Host,
		Port:                    cfg.Port,
		AuthMiddleware:          authMiddleware,
		AuthzMiddleware:         authzMiddleware,
		AuthInfoHandler:         authInfoHandler,
		AuthServer:              embeddedAuthServer,
		TelemetryProvider:       telemetryProvider,
		AuditConfig:             vmcpCfg.Audit,
		HealthMonitorConfig:     healthMonitorConfig,
		StatusReportingInterval: getStatusReportingInterval(vmcpCfg),
		Watcher:                 nil, // set below if backendWatcher is non-nil
		StatusReporter:          statusReporter,
		OptimizerConfig:         optCfg,
		SessionFactory:          sessionFactory,
		SessionStorage:          vmcpCfg.SessionStorage,
	}

	// Assign Watcher only when backendWatcher is non-nil. A typed nil
	// *k8s.BackendWatcher assigned to the Watcher interface produces a
	// non-nil interface value, which panics on the first /readyz probe.
	if backendWatcher != nil {
		serverCfg.Watcher = backendWatcher
	}

	// Convert composite tool configurations to workflow definitions.
	workflowDefs, err := vmcpserver.ConvertConfigToWorkflowDefinitions(vmcpCfg.CompositeTools)
	if err != nil {
		return fmt.Errorf("failed to convert composite tool definitions: %w", err)
	}
	if len(workflowDefs) > 0 {
		slog.Info(fmt.Sprintf("Loaded %d composite tool workflow definitions", len(workflowDefs)))
	}

	// Create server with discovery manager, backend registry, and workflow definitions.
	srv, err := vmcpserver.New(ctx, serverCfg, rtr, backendClient, discoveryMgr, backendRegistry, workflowDefs)
	if err != nil {
		return fmt.Errorf("failed to create Virtual MCP Server: %w", err)
	}

	slog.Info(fmt.Sprintf("Starting Virtual MCP Server at %s", srv.Address()))
	return srv.Start(ctx)
}

// getStatusReportingInterval extracts the status reporting interval from config.
// Returns 0 if not configured, which uses the default interval.
func getStatusReportingInterval(cfg *config.Config) time.Duration {
	if cfg.Operational != nil &&
		cfg.Operational.FailureHandling != nil &&
		cfg.Operational.FailureHandling.StatusReportingInterval > 0 {
		return time.Duration(cfg.Operational.FailureHandling.StatusReportingInterval)
	}
	return 0
}

// loadAndValidateConfig loads and validates the vMCP configuration file.
func loadAndValidateConfig(configPath string) (*config.Config, error) {
	slog.Info(fmt.Sprintf("Loading configuration from: %s", configPath))

	envReader := &env.OSReader{}
	loader := config.NewYAMLLoader(configPath, envReader)
	cfg, err := loader.Load()
	if err != nil {
		slog.Error(fmt.Sprintf("Failed to load configuration: %v", err))
		return nil, fmt.Errorf("configuration loading failed: %w", err)
	}

	validator := config.NewValidator()
	if err := validator.Validate(cfg); err != nil {
		slog.Error(fmt.Sprintf("Configuration validation failed: %v", err))
		return nil, fmt.Errorf("validation failed: %w", err)
	}

	slog.Info("configuration loaded and validated successfully")
	slog.Info(fmt.Sprintf("  Name: %s", cfg.Name))
	slog.Info(fmt.Sprintf("  Group: %s", cfg.Group))
	slog.Info(fmt.Sprintf("  Conflict Resolution: %s", cfg.Aggregation.ConflictResolution))
	if len(cfg.CompositeTools) > 0 {
		slog.Info(fmt.Sprintf("  Composite Tools: %d defined", len(cfg.CompositeTools)))
	}

	return cfg, nil
}

// generateQuickModeConfig constructs a minimal in-memory config for zero-config
// quick mode (thv vmcp serve --group <name>). It sets groupRef from groupRef,
// incomingAuth to anonymous, and outgoingAuth.source to "inline" so no
// Kubernetes API access is required. The generated config is validated before
// being returned; returns an error if groupRef is empty or validation fails.
func generateQuickModeConfig(groupRef string) (*config.Config, error) {
	if groupRef == "" {
		return nil, fmt.Errorf("--group must not be empty")
	}
	cfg := &config.Config{
		Name:  groupRef,
		Group: groupRef,
		IncomingAuth: &config.IncomingAuthConfig{
			Type: config.IncomingAuthTypeAnonymous,
		},
		OutgoingAuth: &config.OutgoingAuthConfig{
			Source: "inline",
		},
		Aggregation: &config.AggregationConfig{
			ConflictResolution: vmcp.ConflictStrategyPrefix,
			ConflictResolutionConfig: &config.ConflictResolutionConfig{
				PrefixFormat: "{workload}_",
			},
		},
	}
	if err := config.NewValidator().Validate(cfg); err != nil {
		return nil, fmt.Errorf("quick-mode config validation failed: %w", err)
	}
	return cfg, nil
}

// loadAuthServerConfig loads the auth server RunConfig from a sibling file
// alongside the main config. The operator serializes authserver.RunConfig as a
// separate ConfigMap key (authserver-config.yaml).
// Returns nil with no error if the file does not exist.
func loadAuthServerConfig(configPath string) (*authserverconfig.RunConfig, error) {
	authServerPath := filepath.Join(filepath.Dir(configPath), "authserver-config.yaml")
	//nolint:gosec // path is user-supplied and intentionally read from the local filesystem
	authServerData, readErr := os.ReadFile(authServerPath)
	if readErr != nil {
		if errors.Is(readErr, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to read auth server config %s: %w", authServerPath, readErr)
	}
	var rc authserverconfig.RunConfig
	if unmarshalErr := yaml.Unmarshal(authServerData, &rc); unmarshalErr != nil {
		return nil, fmt.Errorf("failed to parse auth server config %s: %w", authServerPath, unmarshalErr)
	}
	slog.Info("auth server configuration loaded", "path", authServerPath)
	return &rc, nil
}

// discoverBackends initializes managers, discovers backends, and creates the
// backend client. Returns an empty backends list (with no error) when
// discovery succeeds but finds no backends (static or dynamic mode).
func discoverBackends(
	ctx context.Context,
	cfg *config.Config,
) ([]vmcp.Backend, vmcp.BackendClient, vmcpauth.OutgoingAuthRegistry, error) {
	slog.Info("initializing outgoing authentication")
	envReader := &env.OSReader{}
	outgoingRegistry, err := factory.NewOutgoingAuthRegistry(ctx, envReader)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to create outgoing authentication registry: %w", err)
	}

	backendClient, err := vmcpclient.NewHTTPBackendClient(outgoingRegistry)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to create backend client: %w", err)
	}

	var discoverer aggregator.BackendDiscoverer
	if len(cfg.Backends) > 0 {
		// Static mode: use pre-configured backends from config.
		slog.Info(fmt.Sprintf("Static mode: using %d pre-configured backends", len(cfg.Backends)))
		discoverer = aggregator.NewUnifiedBackendDiscovererWithStaticBackends(
			cfg.Backends,
			cfg.OutgoingAuth,
			cfg.Group,
		)
	} else {
		// Dynamic mode: discover backends at runtime from the active workload manager (K8s or local).
		slog.Info("dynamic mode: initializing group manager for backend discovery")
		// EnsureDefaultGroupExists is a no-op in Kubernetes (service account has no
		// create permission on MCPGroup CRDs). If the group does not exist,
		// Discover returns ErrGroupNotFound which is handled below.
		if err := migration.EnsureDefaultGroupExists(); err != nil {
			return nil, nil, nil, fmt.Errorf("failed to ensure default group exists: %w", err)
		}
		groupsManager, err := groups.NewManager()
		if err != nil {
			return nil, nil, nil, fmt.Errorf("failed to create groups manager: %w", err)
		}

		discoverer, err = aggregator.NewBackendDiscoverer(ctx, groupsManager, cfg.OutgoingAuth)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("failed to create backend discoverer: %w", err)
		}
	}

	return runDiscovery(ctx, cfg.Group, discoverer, backendClient, outgoingRegistry)
}

// runDiscovery calls Discover on the provided discoverer and handles the zero-backends
// case. Extracted so tests can inject a stub discoverer without needing a real
// Kubernetes cluster or Docker daemon.
func runDiscovery(
	ctx context.Context,
	groupRef string,
	discoverer aggregator.BackendDiscoverer,
	backendClient vmcp.BackendClient,
	outgoingRegistry vmcpauth.OutgoingAuthRegistry,
) ([]vmcp.Backend, vmcp.BackendClient, vmcpauth.OutgoingAuthRegistry, error) {
	slog.Info(fmt.Sprintf("Discovering backends in group: %s", groupRef))
	backends, err := discoverer.Discover(ctx, groupRef)
	if err != nil {
		// In Kubernetes mode the MCPGroup CRD is operator/user-managed and may
		// not exist yet. Treat a missing group as zero backends so vMCP can
		// start and serve once backends are registered later.
		if runtime.IsKubernetesRuntime() && errors.Is(err, groups.ErrGroupNotFound) {
			slog.Warn(fmt.Sprintf("Group %s not found - vmcp will start but have no backends to proxy", groupRef))
			return []vmcp.Backend{}, backendClient, outgoingRegistry, nil
		}
		return nil, nil, nil, fmt.Errorf("failed to discover backends: %w", err)
	}

	if len(backends) == 0 {
		slog.Warn(fmt.Sprintf("No backends discovered in group %s - vmcp will start but have no backends to proxy", groupRef))
		return []vmcp.Backend{}, backendClient, outgoingRegistry, nil
	}

	slog.Info(fmt.Sprintf("Discovered %d backends", len(backends)))
	return backends, backendClient, outgoingRegistry, nil
}

// createSessionFactory creates a MultiSessionFactory with HMAC-SHA256 token binding.
// The HMAC secret and Kubernetes detection are passed in as parameters (typically sourced
// from the VMCP_SESSION_HMAC_SECRET environment variable and runtime environment detection
// by the caller).
//
// Behavior:
//   - If hmacSecret is non-empty: validates length and creates factory with the secret.
//   - If running in Kubernetes without secret: returns error (production safety requirement).
//   - Otherwise: logs warning and creates factory with default insecure secret.
func createSessionFactory(
	hmacSecret string,
	isKubernetes bool,
	outgoingRegistry vmcpauth.OutgoingAuthRegistry,
	agg aggregator.Aggregator,
) (vmcpsession.MultiSessionFactory, error) {
	const minRecommendedSecretLen = 32

	opts := []vmcpsession.MultiSessionFactoryOption{}
	if agg != nil {
		opts = append(opts, vmcpsession.WithAggregator(agg))
	}

	if hmacSecret != "" {
		if secretLen := len(hmacSecret); secretLen < minRecommendedSecretLen {
			// G706: Safe - only logging integer length, not the secret itself.
			slog.Warn( //nolint:gosec
				"HMAC secret is shorter than recommended length - consider using a longer secret",
				"actual_length", secretLen,
				"recommended_length", minRecommendedSecretLen,
			)
		}
		slog.Info("using provided HMAC secret for session token binding")
		opts = append(opts, vmcpsession.WithHMACSecret([]byte(hmacSecret)))
		return vmcpsession.NewSessionFactory(outgoingRegistry, opts...), nil
	}

	// No secret provided — fail fast in Kubernetes (production environment).
	if isKubernetes {
		return nil, fmt.Errorf(
			"an HMAC secret is required when running in Kubernetes (set VMCP_SESSION_HMAC_SECRET). " +
				"Generate a secure secret with: openssl rand -base64 32",
		)
	}

	// Development mode: use default insecure secret with warning.
	slog.Warn("no HMAC secret provided - using default insecure secret (NOT recommended for production)")
	return vmcpsession.NewSessionFactory(outgoingRegistry, opts...), nil
}

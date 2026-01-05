// Package app provides the entry point for the vmcp command-line application.
package app

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"github.com/stacklok/toolhive/pkg/audit"
	"github.com/stacklok/toolhive/pkg/env"
	"github.com/stacklok/toolhive/pkg/groups"
	"github.com/stacklok/toolhive/pkg/logger"
	"github.com/stacklok/toolhive/pkg/telemetry"
	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/aggregator"
	"github.com/stacklok/toolhive/pkg/vmcp/auth/factory"
	vmcpclient "github.com/stacklok/toolhive/pkg/vmcp/client"
	"github.com/stacklok/toolhive/pkg/vmcp/config"
	"github.com/stacklok/toolhive/pkg/vmcp/discovery"
	"github.com/stacklok/toolhive/pkg/vmcp/health"
	"github.com/stacklok/toolhive/pkg/vmcp/k8s"
	vmcprouter "github.com/stacklok/toolhive/pkg/vmcp/router"
	vmcpserver "github.com/stacklok/toolhive/pkg/vmcp/server"
)

var rootCmd = &cobra.Command{
	Use:               "vmcp",
	DisableAutoGenTag: true,
	Short:             "Virtual MCP Server - Aggregate and proxy multiple MCP servers",
	Long: `Virtual MCP Server (vmcp) is a proxy that aggregates multiple MCP (Model Context Protocol) servers
into a single unified interface. It provides:

- Tool aggregation from multiple MCP servers
- Resource aggregation from multiple sources
- Prompt aggregation and routing
- Authentication and authorization middleware
- Audit logging and telemetry
- Per-backend middleware configuration

vmcp reuses ToolHive's security and middleware infrastructure to provide a secure,
observable, and controlled way to expose multiple MCP servers through a single endpoint.`,
	Run: func(cmd *cobra.Command, _ []string) {
		// If no subcommand is provided, print help
		if err := cmd.Help(); err != nil {
			logger.Errorf("Error displaying help: %v", err)
		}
	},
	PersistentPreRun: func(_ *cobra.Command, _ []string) {
		logger.Initialize()
	},
}

// NewRootCmd creates a new root command for the vmcp CLI.
func NewRootCmd() *cobra.Command {
	// Add persistent flags
	rootCmd.PersistentFlags().Bool("debug", false, "Enable debug mode")
	err := viper.BindPFlag("debug", rootCmd.PersistentFlags().Lookup("debug"))
	if err != nil {
		logger.Errorf("Error binding debug flag: %v", err)
	}

	rootCmd.PersistentFlags().StringP("config", "c", "", "Path to vMCP configuration file")
	err = viper.BindPFlag("config", rootCmd.PersistentFlags().Lookup("config"))
	if err != nil {
		logger.Errorf("Error binding config flag: %v", err)
	}

	// Add subcommands
	rootCmd.AddCommand(newServeCmd())
	rootCmd.AddCommand(newVersionCmd())
	rootCmd.AddCommand(newValidateCmd())

	// Silence printing the usage on error
	rootCmd.SilenceUsage = true

	return rootCmd
}

// newServeCmd creates the serve command for starting the vMCP server
func newServeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Start the Virtual MCP Server",
		Long: `Start the Virtual MCP Server to aggregate and proxy multiple MCP servers.

The server will read the configuration file specified by --config flag and start
listening for MCP client connections. It will aggregate tools, resources, and prompts
from all configured backend MCP servers.`,
		RunE: runServe,
	}

	// Add serve-specific flags
	cmd.Flags().String("host", "127.0.0.1", "Host address to bind to")
	cmd.Flags().Int("port", 4483, "Port to listen on")
	cmd.Flags().Bool("enable-audit", false, "Enable audit logging with default configuration")

	return cmd
}

// newVersionCmd creates the version command
func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print version information",
		Long:  "Display version information for vmcp",
		Run: func(_ *cobra.Command, _ []string) {
			// Version information will be injected at build time
			logger.Infof("vmcp version: %s", getVersion())
		},
	}
}

// newValidateCmd creates the validate command for checking configuration
func newValidateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "validate",
		Short: "Validate configuration file",
		Long: `Validate the vMCP configuration file for syntax and semantic errors.

This command checks:
- YAML/JSON syntax validity
- Required fields presence
- Middleware configuration correctness
- Backend configuration validity`,
		RunE: func(_ *cobra.Command, _ []string) error {
			configPath := viper.GetString("config")
			if configPath == "" {
				return fmt.Errorf("no configuration file specified, use --config flag")
			}

			logger.Infof("Validating configuration: %s", configPath)

			// Load configuration from YAML
			envReader := &env.OSReader{}
			loader := config.NewYAMLLoader(configPath, envReader)
			cfg, err := loader.Load()
			if err != nil {
				logger.Errorf("Failed to load configuration: %v", err)
				return fmt.Errorf("configuration loading failed: %w", err)
			}

			logger.Debugf("Configuration loaded successfully, performing validation...")

			// Validate configuration
			validator := config.NewValidator()
			if err := validator.Validate(cfg); err != nil {
				logger.Errorf("Configuration validation failed: %v", err)
				return fmt.Errorf("validation failed: %w", err)
			}

			logger.Infof("✓ Configuration is valid")
			logger.Infof("  Name: %s", cfg.Name)
			logger.Infof("  Group: %s", cfg.Group)
			logger.Infof("  Incoming Auth: %s", cfg.IncomingAuth.Type)
			logger.Infof("  Outgoing Auth: %s (source: %s)",
				func() string {
					if len(cfg.OutgoingAuth.Backends) > 0 {
						return fmt.Sprintf("%d backends configured", len(cfg.OutgoingAuth.Backends))
					}
					return "default only"
				}(),
				cfg.OutgoingAuth.Source)
			logger.Infof("  Conflict Resolution: %s", cfg.Aggregation.ConflictResolution)

			if len(cfg.CompositeTools) > 0 {
				logger.Infof("  Composite Tools: %d defined", len(cfg.CompositeTools))
			}

			return nil
		},
	}
}

// getVersion returns the version string (will be set at build time)
func getVersion() string {
	// This will be replaced with actual version info using ldflags
	return "dev"
}

// loadAndValidateConfig loads and validates the vMCP configuration file
func loadAndValidateConfig(configPath string) (*config.Config, error) {
	logger.Infof("Loading configuration from: %s", configPath)

	envReader := &env.OSReader{}
	loader := config.NewYAMLLoader(configPath, envReader)
	cfg, err := loader.Load()
	if err != nil {
		logger.Errorf("Failed to load configuration: %v", err)
		return nil, fmt.Errorf("configuration loading failed: %w", err)
	}

	validator := config.NewValidator()
	if err := validator.Validate(cfg); err != nil {
		logger.Errorf("Configuration validation failed: %v", err)
		return nil, fmt.Errorf("validation failed: %w", err)
	}

	logger.Infof("Configuration loaded and validated successfully")
	logger.Infof("  Name: %s", cfg.Name)
	logger.Infof("  Group: %s", cfg.Group)
	logger.Infof("  Conflict Resolution: %s", cfg.Aggregation.ConflictResolution)
	if len(cfg.CompositeTools) > 0 {
		logger.Infof("  Composite Tools: %d defined", len(cfg.CompositeTools))
	}

	return cfg, nil
}

// discoverBackends initializes managers, discovers backends, and creates backend client
// Returns empty backends list with no error if running in Kubernetes where CLI discovery doesn't work
func discoverBackends(ctx context.Context, cfg *config.Config) ([]vmcp.Backend, vmcp.BackendClient, error) {
	// Create outgoing authentication registry
	logger.Info("Initializing outgoing authentication")
	envReader := &env.OSReader{}
	outgoingRegistry, err := factory.NewOutgoingAuthRegistry(ctx, envReader)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create outgoing authentication registry: %w", err)
	}

	// Create backend client first (needed even with empty backends)
	backendClient, err := vmcpclient.NewHTTPBackendClient(outgoingRegistry)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create backend client: %w", err)
	}

	// Initialize managers for backend discovery
	logger.Info("Initializing group manager")
	groupsManager, err := groups.NewManager()
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create groups manager: %w", err)
	}

	// Create backend discoverer based on runtime environment
	discoverer, err := aggregator.NewBackendDiscoverer(ctx, groupsManager, cfg.OutgoingAuth)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create backend discoverer: %w", err)
	}

	logger.Infof("Discovering backends in group: %s", cfg.Group)
	backends, err := discoverer.Discover(ctx, cfg.Group)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to discover backends: %w", err)
	}

	if len(backends) == 0 {
		logger.Warnf("No backends discovered in group %s - vmcp will start but have no backends to proxy", cfg.Group)
		return []vmcp.Backend{}, backendClient, nil
	}

	logger.Infof("Discovered %d backends", len(backends))
	return backends, backendClient, nil
}

// runServe implements the serve command logic
//
//nolint:gocyclo // Complexity from server initialization and configuration is acceptable
func runServe(cmd *cobra.Command, _ []string) error {
	ctx := cmd.Context()
	configPath := viper.GetString("config")

	if configPath == "" {
		return fmt.Errorf("no configuration file specified, use --config flag")
	}

	// Load and validate configuration
	cfg, err := loadAndValidateConfig(configPath)
	if err != nil {
		return err
	}

	// Check if --enable-audit flag is set
	enableAudit, _ := cmd.Flags().GetBool("enable-audit")
	if enableAudit && cfg.Audit == nil {
		// Create default audit config with reasonable defaults
		cfg.Audit = audit.DefaultConfig()
		cfg.Audit.Component = "vmcp-server"
		logger.Info("Audit logging enabled with default configuration")
	}

	// Discover backends and create client
	backends, backendClient, err := discoverBackends(ctx, cfg)
	if err != nil {
		return err
	}

	// Create conflict resolver based on configuration
	conflictResolver, err := aggregator.NewConflictResolver(cfg.Aggregation)
	if err != nil {
		return fmt.Errorf("failed to create conflict resolver: %w", err)
	}

	// Create aggregator
	agg := aggregator.NewDefaultAggregator(backendClient, conflictResolver, cfg.Aggregation.Tools)

	// Create backend registry for CLI environment
	// CLI always uses immutable registry (backends fixed at startup)
	backendRegistry := vmcp.NewImmutableRegistry(backends)

	// Use standard manager (no version-based invalidation needed)
	discoveryMgr, err := discovery.NewManager(agg)
	if err != nil {
		return fmt.Errorf("failed to create discovery manager: %w", err)
	}
	logger.Info("Immutable backend registry created for CLI environment")

	// Backend watcher is not used in CLI mode (always nil)
	var backendWatcher *k8s.BackendWatcher

	// Create router
	rtr := vmcprouter.NewDefaultRouter()

	logger.Infof("Setting up incoming authentication (type: %s)", cfg.IncomingAuth.Type)

	authMiddleware, authInfoHandler, err := factory.NewIncomingAuthMiddleware(ctx, cfg.IncomingAuth)
	if err != nil {
		return fmt.Errorf("failed to create authentication middleware: %w", err)
	}

	logger.Infof("Incoming authentication configured: %s", cfg.IncomingAuth.Type)

	// Create server configuration with flags
	// Cobra validates flag types at parse time, so these values are safe to use directly
	host, _ := cmd.Flags().GetString("host")
	port, _ := cmd.Flags().GetInt("port")

	// If telemetry is configured, create the provider.
	var telemetryProvider *telemetry.Provider
	if cfg.Telemetry != nil {
		var err error
		telemetryProvider, err = telemetry.NewProvider(ctx, *cfg.Telemetry)
		if err != nil {
			return fmt.Errorf("failed to create telemetry provider: %w", err)
		}
		defer func() {
			err := telemetryProvider.Shutdown(ctx)
			if err != nil {
				logger.Errorf("failed to shutdown telemetry provider: %v", err)
			}
		}()
	}

	// Configure health monitoring if enabled
	var healthMonitorConfig *health.MonitorConfig
	if cfg.Operational != nil && cfg.Operational.FailureHandling != nil && cfg.Operational.FailureHandling.HealthCheckInterval > 0 {
		// Note: HealthCheckInterval is config.Duration (alias for time.Duration), already in nanoseconds
		// from YAML/JSON parsing via time.ParseDuration. This is a simple type cast, not unit conversion.
		checkInterval := time.Duration(cfg.Operational.FailureHandling.HealthCheckInterval)
		if cfg.Operational.FailureHandling.UnhealthyThreshold < 1 {
			return fmt.Errorf("invalid health check configuration: unhealthy threshold must be >= 1, got %d",
				cfg.Operational.FailureHandling.UnhealthyThreshold)
		}

		defaults := health.DefaultConfig()
		healthMonitorConfig = &health.MonitorConfig{
			CheckInterval:      checkInterval,
			UnhealthyThreshold: cfg.Operational.FailureHandling.UnhealthyThreshold,
			Timeout:            defaults.Timeout,
			DegradedThreshold:  defaults.DegradedThreshold,
		}
		logger.Info("Health monitoring configured from operational settings")
	}

	serverCfg := &vmcpserver.Config{
		Name:                cfg.Name,
		Version:             getVersion(),
		GroupRef:            cfg.Group,
		Host:                host,
		Port:                port,
		AuthMiddleware:      authMiddleware,
		AuthInfoHandler:     authInfoHandler,
		TelemetryProvider:   telemetryProvider,
		AuditConfig:         cfg.Audit,
		HealthMonitorConfig: healthMonitorConfig,
		K8sManager:          backendWatcher,
	}

	// Convert composite tool configurations to workflow definitions
	workflowDefs, err := vmcpserver.ConvertConfigToWorkflowDefinitions(cfg.CompositeTools)
	if err != nil {
		return fmt.Errorf("failed to convert composite tool definitions: %w", err)
	}
	if len(workflowDefs) > 0 {
		logger.Infof("Loaded %d composite tool workflow definitions", len(workflowDefs))
	}

	// Create server with discovery manager, backend registry, and workflow definitions
	srv, err := vmcpserver.New(ctx, serverCfg, rtr, backendClient, discoveryMgr, backendRegistry, workflowDefs)
	if err != nil {
		return fmt.Errorf("failed to create Virtual MCP Server: %w", err)
	}

	// Start server (blocks until shutdown signal)
	logger.Infof("Starting Virtual MCP Server at %s", srv.Address())
	return srv.Start(ctx)
}

// aggregateCapabilities aggregates capabilities from backends or creates empty capabilities.
//
// NOTE: This function is currently unused due to lazy discovery implementation (issue #2501).
// It may be removed in a future cleanup or used for startup-time capability caching.
//
//nolint:unused // Unused until we implement startup aggregation or caching
func aggregateCapabilities(
	ctx context.Context,
	agg aggregator.Aggregator,
	backends []vmcp.Backend,
) (*aggregator.AggregatedCapabilities, error) {
	logger.Info("Aggregating capabilities from backends")

	if len(backends) > 0 {
		aggCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()

		capabilities, err := agg.AggregateCapabilities(aggCtx, backends)
		if err != nil {
			return nil, fmt.Errorf("failed to aggregate capabilities: %w", err)
		}

		logger.Infof("Aggregated %d tools, %d resources, %d prompts from %d backends",
			capabilities.Metadata.ToolCount,
			capabilities.Metadata.ResourceCount,
			capabilities.Metadata.PromptCount,
			capabilities.Metadata.BackendCount)

		return capabilities, nil
	}

	// No backends available - create empty capabilities
	logger.Warnf("No backends available - starting with empty capabilities")
	return &aggregator.AggregatedCapabilities{
		Tools:     []vmcp.Tool{},
		Resources: []vmcp.Resource{},
		Prompts:   []vmcp.Prompt{},
		RoutingTable: &vmcp.RoutingTable{
			Tools:     make(map[string]*vmcp.BackendTarget),
			Resources: make(map[string]*vmcp.BackendTarget),
			Prompts:   make(map[string]*vmcp.BackendTarget),
		},
		Metadata: &aggregator.AggregationMetadata{
			BackendCount:  0,
			ToolCount:     0,
			ResourceCount: 0,
			PromptCount:   0,
		},
	}, nil
}

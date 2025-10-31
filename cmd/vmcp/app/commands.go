// Package app provides the entry point for the vmcp command-line application.
package app

import (
	"fmt"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"github.com/stacklok/toolhive/pkg/groups"
	"github.com/stacklok/toolhive/pkg/logger"
	"github.com/stacklok/toolhive/pkg/vmcp/aggregator"
	vmcpclient "github.com/stacklok/toolhive/pkg/vmcp/client"
	"github.com/stacklok/toolhive/pkg/vmcp/config"
	vmcprouter "github.com/stacklok/toolhive/pkg/vmcp/router"
	vmcpserver "github.com/stacklok/toolhive/pkg/vmcp/server"
	"github.com/stacklok/toolhive/pkg/workloads"
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
	return &cobra.Command{
		Use:   "serve",
		Short: "Start the Virtual MCP Server",
		Long: `Start the Virtual MCP Server to aggregate and proxy multiple MCP servers.

The server will read the configuration file specified by --config flag and start
listening for MCP client connections. It will aggregate tools, resources, and prompts
from all configured backend MCP servers.`,
		RunE: runServe,
	}
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
			loader := config.NewYAMLLoader(configPath)
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
			logger.Infof("  Group: %s", cfg.GroupRef)
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

			if cfg.TokenCache != nil {
				logger.Infof("  Token Cache: %s", cfg.TokenCache.Provider)
			}

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

// runServe implements the serve command logic
func runServe(cmd *cobra.Command, _ []string) error {
	ctx := cmd.Context()
	configPath := viper.GetString("config")

	if configPath == "" {
		return fmt.Errorf("no configuration file specified, use --config flag")
	}

	logger.Infof("Loading configuration from: %s", configPath)

	// Load configuration from YAML
	loader := config.NewYAMLLoader(configPath)
	cfg, err := loader.Load()
	if err != nil {
		logger.Errorf("Failed to load configuration: %v", err)
		return fmt.Errorf("configuration loading failed: %w", err)
	}

	// Validate configuration
	validator := config.NewValidator()
	if err := validator.Validate(cfg); err != nil {
		logger.Errorf("Configuration validation failed: %v", err)
		return fmt.Errorf("validation failed: %w", err)
	}

	logger.Infof("Configuration loaded and validated successfully")
	logger.Infof("  Name: %s", cfg.Name)
	logger.Infof("  Group: %s", cfg.GroupRef)
	logger.Infof("  Conflict Resolution: %s", cfg.Aggregation.ConflictResolution)

	// Initialize managers for backend discovery
	logger.Info("Initializing workload and group managers")
	workloadsManager, err := workloads.NewManager(ctx)
	if err != nil {
		return fmt.Errorf("failed to create workloads manager: %w", err)
	}

	groupsManager, err := groups.NewManager()
	if err != nil {
		return fmt.Errorf("failed to create groups manager: %w", err)
	}

	// Create backend discoverer
	discoverer := aggregator.NewCLIBackendDiscoverer(workloadsManager, groupsManager)

	// Discover backends from the configured group
	logger.Infof("Discovering backends in group: %s", cfg.GroupRef)
	backends, err := discoverer.Discover(ctx, cfg.GroupRef)
	if err != nil {
		return fmt.Errorf("failed to discover backends: %w", err)
	}

	if len(backends) == 0 {
		return fmt.Errorf("no backends found in group %s", cfg.GroupRef)
	}

	logger.Infof("Discovered %d backends", len(backends))

	// Create backend client
	backendClient := vmcpclient.NewHTTPBackendClient()

	// Create conflict resolver based on configuration
	// Use the factory method that handles all strategies
	conflictResolver, err := aggregator.NewConflictResolver(cfg.Aggregation)
	if err != nil {
		return fmt.Errorf("failed to create conflict resolver: %w", err)
	}

	// Create aggregator
	agg := aggregator.NewDefaultAggregator(backendClient, conflictResolver, cfg.Aggregation.Tools)

	// Aggregate capabilities from all backends
	logger.Info("Aggregating capabilities from backends")
	capabilities, err := agg.AggregateCapabilities(ctx, backends)
	if err != nil {
		return fmt.Errorf("failed to aggregate capabilities: %w", err)
	}

	logger.Infof("Aggregated %d tools, %d resources, %d prompts from %d backends",
		capabilities.Metadata.ToolCount,
		capabilities.Metadata.ResourceCount,
		capabilities.Metadata.PromptCount,
		capabilities.Metadata.BackendCount)

	// Create router
	rtr := vmcprouter.NewDefaultRouter()

	// Create server configuration
	serverCfg := &vmcpserver.Config{
		Name:    cfg.Name,
		Version: getVersion(),
		Host:    "127.0.0.1", // TODO: Make configurable
		Port:    4483,        // TODO: Make configurable
	}

	// Create server
	srv := vmcpserver.New(serverCfg, rtr, backendClient)

	// Register capabilities
	logger.Info("Registering capabilities with server")
	if err := srv.RegisterCapabilities(ctx, capabilities); err != nil {
		return fmt.Errorf("failed to register capabilities: %w", err)
	}

	// Start server (blocks until shutdown signal)
	logger.Infof("Starting Virtual MCP Server at %s", srv.Address())
	return srv.Start(ctx)
}

package app

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/stacklok/toolhive/pkg/client"
	"github.com/stacklok/toolhive/pkg/config"
)

// OtelCmd is the parent command for OpenTelemetry configuration
var OtelCmd = &cobra.Command{
	Use:   "otel",
	Short: "Manage OpenTelemetry configuration",
	Long:  "Configure OpenTelemetry settings for observability and monitoring of MCP servers.",
}

var setOtelEndpointCmd = &cobra.Command{
	Use:   "set-endpoint <endpoint>",
	Short: "Set the OpenTelemetry endpoint URL",
	Long: `Set the OpenTelemetry OTLP endpoint URL for tracing and metrics.
This endpoint will be used by default when running MCP servers unless overridden by the --otel-endpoint flag.

Example:
  thv config otel set-endpoint https://api.honeycomb.io`,
	Args: cobra.ExactArgs(1),
	RunE: setOtelEndpointCmdFunc,
}

var getOtelEndpointCmd = &cobra.Command{
	Use:   "get-endpoint",
	Short: "Get the currently configured OpenTelemetry endpoint",
	Long:  "Display the OpenTelemetry endpoint URL that is currently configured.",
	RunE:  getOtelEndpointCmdFunc,
}

var unsetOtelEndpointCmd = &cobra.Command{
	Use:   "unset-endpoint",
	Short: "Remove the configured OpenTelemetry endpoint",
	Long:  "Remove the OpenTelemetry endpoint configuration.",
	RunE:  unsetOtelEndpointCmdFunc,
}

var setOtelSamplingRateCmd = &cobra.Command{
	Use:   "set-sampling-rate <rate>",
	Short: "Set the OpenTelemetry sampling rate",
	Long: `Set the OpenTelemetry trace sampling rate (between 0.0 and 1.0).
This sampling rate will be used by default when running MCP servers unless overridden by the --otel-sampling-rate flag.

Example:
  thv config otel set-sampling-rate 0.1`,
	Args: cobra.ExactArgs(1),
	RunE: setOtelSamplingRateCmdFunc,
}

var getOtelSamplingRateCmd = &cobra.Command{
	Use:   "get-sampling-rate",
	Short: "Get the currently configured OpenTelemetry sampling rate",
	Long:  "Display the OpenTelemetry sampling rate that is currently configured.",
	RunE:  getOtelSamplingRateCmdFunc,
}

var unsetOtelSamplingRateCmd = &cobra.Command{
	Use:   "unset-sampling-rate",
	Short: "Remove the configured OpenTelemetry sampling rate",
	Long:  "Remove the OpenTelemetry sampling rate configuration.",
	RunE:  unsetOtelSamplingRateCmdFunc,
}

var setOtelEnvVarsCmd = &cobra.Command{
	Use:   "set-env-vars <var1,var2,...>",
	Short: "Set the OpenTelemetry environment variables",
	Long: `Set the list of environment variable names to include in OpenTelemetry spans.
These environment variables will be used by default when running MCP servers unless overridden by the --otel-env-vars flag.

Example:
  thv config otel set-env-vars USER,HOME,PATH`,
	Args: cobra.ExactArgs(1),
	RunE: setOtelEnvVarsCmdFunc,
}

var getOtelEnvVarsCmd = &cobra.Command{
	Use:   "get-env-vars",
	Short: "Get the currently configured OpenTelemetry environment variables",
	Long:  "Display the OpenTelemetry environment variables that are currently configured.",
	RunE:  getOtelEnvVarsCmdFunc,
}

var unsetOtelEnvVarsCmd = &cobra.Command{
	Use:   "unset-env-vars",
	Short: "Remove the configured OpenTelemetry environment variables",
	Long:  "Remove the OpenTelemetry environment variables configuration.",
	RunE:  unsetOtelEnvVarsCmdFunc,
}

// init sets up the OTEL command hierarchy
func init() {
	// Add OTEL subcommands to otel command
	OtelCmd.AddCommand(setOtelEndpointCmd)
	OtelCmd.AddCommand(getOtelEndpointCmd)
	OtelCmd.AddCommand(unsetOtelEndpointCmd)
	OtelCmd.AddCommand(setOtelSamplingRateCmd)
	OtelCmd.AddCommand(getOtelSamplingRateCmd)
	OtelCmd.AddCommand(unsetOtelSamplingRateCmd)
	OtelCmd.AddCommand(setOtelEnvVarsCmd)
	OtelCmd.AddCommand(getOtelEnvVarsCmd)
	OtelCmd.AddCommand(unsetOtelEnvVarsCmd)
}

func setOtelEndpointCmdFunc(cmd *cobra.Command, args []string) error {
	endpoint := args[0]

	// The endpoint should not start with http:// or https://
	if endpoint != "" && (strings.HasPrefix(endpoint, "http://") || strings.HasPrefix(endpoint, "https://")) {
		return fmt.Errorf("endpoint URL should not start with http:// or https://")
	}

	// Get manager instance
	ctx := cmd.Context()
	manager, err := client.NewManager(ctx)
	if err != nil {
		return fmt.Errorf("failed to create client manager: %w", err)
	}

	// Update the configuration using manager
	err = manager.UpdateOtelConfig(func(otel *config.OpenTelemetryConfig) {
		otel.Endpoint = endpoint
	})
	if err != nil {
		return fmt.Errorf("failed to update configuration: %w", err)
	}

	fmt.Printf("Successfully set OpenTelemetry endpoint: %s\n", endpoint)
	return nil
}

func getOtelEndpointCmdFunc(cmd *cobra.Command, _ []string) error {
	// Get manager instance
	ctx := cmd.Context()
	manager, err := client.NewManager(ctx)
	if err != nil {
		return fmt.Errorf("failed to create client manager: %w", err)
	}

	cfg, err := manager.GetConfig()
	if err != nil {
		return fmt.Errorf("failed to get configuration: %w", err)
	}

	if cfg.OTEL.Endpoint == "" {
		fmt.Println("No OpenTelemetry endpoint is currently configured.")
		return nil
	}

	fmt.Printf("Current OpenTelemetry endpoint: %s\n", cfg.OTEL.Endpoint)
	return nil
}

func unsetOtelEndpointCmdFunc(cmd *cobra.Command, _ []string) error {
	// Get manager instance
	ctx := cmd.Context()
	manager, err := client.NewManager(ctx)
	if err != nil {
		return fmt.Errorf("failed to create client manager: %w", err)
	}

	cfg, err := manager.GetConfig()
	if err != nil {
		return fmt.Errorf("failed to get configuration: %w", err)
	}

	if cfg.OTEL.Endpoint == "" {
		fmt.Println("No OpenTelemetry endpoint is currently configured.")
		return nil
	}

	// Update the configuration using manager
	err = manager.UpdateOtelConfig(func(otel *config.OpenTelemetryConfig) {
		otel.Endpoint = ""
	})
	if err != nil {
		return fmt.Errorf("failed to update configuration: %w", err)
	}

	fmt.Println("Successfully removed OpenTelemetry endpoint configuration.")
	return nil
}

func setOtelSamplingRateCmdFunc(cmd *cobra.Command, args []string) error {
	rate, err := strconv.ParseFloat(args[0], 64)
	if err != nil {
		return fmt.Errorf("invalid sampling rate format: %w", err)
	}

	// Validate the rate
	if rate < 0.0 || rate > 1.0 {
		return fmt.Errorf("sampling rate must be between 0.0 and 1.0")
	}

	// Get manager instance
	ctx := cmd.Context()
	manager, err := client.NewManager(ctx)
	if err != nil {
		return fmt.Errorf("failed to create client manager: %w", err)
	}

	// Update the configuration using manager
	err = manager.UpdateOtelConfig(func(otel *config.OpenTelemetryConfig) {
		otel.SamplingRate = rate
	})
	if err != nil {
		return fmt.Errorf("failed to update configuration: %w", err)
	}

	fmt.Printf("Successfully set OpenTelemetry sampling rate: %f\n", rate)
	return nil
}

func getOtelSamplingRateCmdFunc(cmd *cobra.Command, _ []string) error {
	// Get manager instance
	ctx := cmd.Context()
	manager, err := client.NewManager(ctx)
	if err != nil {
		return fmt.Errorf("failed to create client manager: %w", err)
	}

	cfg, err := manager.GetConfig()
	if err != nil {
		return fmt.Errorf("failed to get configuration: %w", err)
	}

	if cfg.OTEL.SamplingRate == 0.0 {
		fmt.Println("No OpenTelemetry sampling rate is currently configured.")
		return nil
	}

	fmt.Printf("Current OpenTelemetry sampling rate: %f\n", cfg.OTEL.SamplingRate)
	return nil
}

func unsetOtelSamplingRateCmdFunc(cmd *cobra.Command, _ []string) error {
	// Get manager instance
	ctx := cmd.Context()
	manager, err := client.NewManager(ctx)
	if err != nil {
		return fmt.Errorf("failed to create client manager: %w", err)
	}

	cfg, err := manager.GetConfig()
	if err != nil {
		return fmt.Errorf("failed to get configuration: %w", err)
	}

	if cfg.OTEL.SamplingRate == 0.0 {
		fmt.Println("No OpenTelemetry sampling rate is currently configured.")
		return nil
	}

	// Update the configuration using manager
	err = manager.UpdateOtelConfig(func(otel *config.OpenTelemetryConfig) {
		otel.SamplingRate = 0.0
	})
	if err != nil {
		return fmt.Errorf("failed to update configuration: %w", err)
	}

	fmt.Println("Successfully removed OpenTelemetry sampling rate configuration.")
	return nil
}

func setOtelEnvVarsCmdFunc(cmd *cobra.Command, args []string) error {
	vars := strings.Split(args[0], ",")

	// Trim whitespace from each variable name
	for i, varName := range vars {
		vars[i] = strings.TrimSpace(varName)
	}

	// Get manager instance
	ctx := cmd.Context()
	manager, err := client.NewManager(ctx)
	if err != nil {
		return fmt.Errorf("failed to create client manager: %w", err)
	}

	// Update the configuration using manager
	err = manager.UpdateOtelConfig(func(otel *config.OpenTelemetryConfig) {
		otel.EnvVars = vars
	})
	if err != nil {
		return fmt.Errorf("failed to update configuration: %w", err)
	}

	fmt.Printf("Successfully set OpenTelemetry environment variables: %v\n", vars)
	return nil
}

func getOtelEnvVarsCmdFunc(cmd *cobra.Command, _ []string) error {
	// Get manager instance
	ctx := cmd.Context()
	manager, err := client.NewManager(ctx)
	if err != nil {
		return fmt.Errorf("failed to create client manager: %w", err)
	}

	cfg, err := manager.GetConfig()
	if err != nil {
		return fmt.Errorf("failed to get configuration: %w", err)
	}

	if len(cfg.OTEL.EnvVars) == 0 {
		fmt.Println("No OpenTelemetry environment variables are currently configured.")
		return nil
	}

	fmt.Printf("Current OpenTelemetry environment variables: %v\n", cfg.OTEL.EnvVars)
	return nil
}

func unsetOtelEnvVarsCmdFunc(cmd *cobra.Command, _ []string) error {
	// Get manager instance
	ctx := cmd.Context()
	manager, err := client.NewManager(ctx)
	if err != nil {
		return fmt.Errorf("failed to create client manager: %w", err)
	}

	cfg, err := manager.GetConfig()
	if err != nil {
		return fmt.Errorf("failed to get configuration: %w", err)
	}

	if len(cfg.OTEL.EnvVars) == 0 {
		fmt.Println("No OpenTelemetry environment variables are currently configured.")
		return nil
	}

	// Update the configuration using manager
	err = manager.UpdateOtelConfig(func(otel *config.OpenTelemetryConfig) {
		otel.EnvVars = []string{}
	})
	if err != nil {
		return fmt.Errorf("failed to update configuration: %w", err)
	}

	fmt.Println("Successfully removed OpenTelemetry environment variables configuration.")
	return nil
}

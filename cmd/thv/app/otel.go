package app

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

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

var setOtelMetricsEnabledCmd = &cobra.Command{
	Use:   "set-metrics-enabled <enabled>",
	Short: "Set the OpenTelemetry metrics export to enabled",
	Long: `Set the OpenTelemetry metrics flag to enable to export metrics to an OTel collector.

	thv config otel set-metrics-enabled true`,
	Args: cobra.ExactArgs(1),
	RunE: setOtelMetricsEnabledCmdFunc,
}

var getOtelMetricsEnabledCmd = &cobra.Command{
	Use:   "get-metrics-enabled",
	Short: "Get the currently configured OpenTelemetry metrics export flag",
	Long:  "Display the OpenTelemetry metrics export flag that is currently configured.",
	RunE:  getOtelMetricsEnabledCmdFunc,
}

var unsetOtelMetricsEnabledCmd = &cobra.Command{
	Use:   "unset-metrics-enabled",
	Short: "Remove the configured OpenTelemetry metrics export flag",
	Long:  "Remove the OpenTelemetry metrics export flag configuration.",
	RunE:  unsetOtelMetricsEnabledCmdFunc,
}

var setOtelTracingEnabledCmd = &cobra.Command{
	Use:   "set-tracing-enabled <enabled>",
	Short: "Set the OpenTelemetry tracing export to enabled",
	Long: `Set the OpenTelemetry tracing flag to enable to export traces to an OTel collector.

	thv config otel set-tracing-enabled true`,
	Args: cobra.ExactArgs(1),
	RunE: setOtelTracingEnabledCmdFunc,
}

var getOtelTracingEnabledCmd = &cobra.Command{
	Use:   "get-tracing-enabled",
	Short: "Get the currently configured OpenTelemetry tracing export flag",
	Long:  "Display the OpenTelemetry tracing export flag that is currently configured.",
	RunE:  getOtelTracingEnabledCmdFunc,
}

var unsetOtelTracingEnabledCmd = &cobra.Command{
	Use:   "unset-tracing-enabled",
	Short: "Remove the configured OpenTelemetry tracing export flag",
	Long:  "Remove the OpenTelemetry tracing export flag configuration.",
	RunE:  unsetOtelTracingEnabledCmdFunc,
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

var setOtelInsecureCmd = &cobra.Command{
	Use:   "set-insecure <enabled>",
	Short: "Set the OpenTelemetry insecure transport flag",
	Long: `Set the OpenTelemetry insecure flag to enable HTTP instead of HTTPS for OTLP endpoints.

	thv config otel set-insecure true`,
	Args: cobra.ExactArgs(1),
	RunE: setOtelInsecureCmdFunc,
}

var getOtelInsecureCmd = &cobra.Command{
	Use:   "get-insecure",
	Short: "Get the currently configured OpenTelemetry insecure transport flag",
	Long:  "Display the OpenTelemetry insecure transport flag that is currently configured.",
	RunE:  getOtelInsecureCmdFunc,
}

var unsetOtelInsecureCmd = &cobra.Command{
	Use:   "unset-insecure",
	Short: "Remove the configured OpenTelemetry insecure transport flag",
	Long:  "Remove the OpenTelemetry insecure transport flag configuration.",
	RunE:  unsetOtelInsecureCmdFunc,
}

var setOtelEnablePrometheusMetricsPathCmd = &cobra.Command{
	Use:   "set-enable-prometheus-metrics-path <enabled>",
	Short: "Set the OpenTelemetry Prometheus metrics path flag",
	Long: `Set the OpenTelemetry Prometheus metrics path flag to enable /metrics endpoint.

	thv config otel set-enable-prometheus-metrics-path true`,
	Args: cobra.ExactArgs(1),
	RunE: setOtelEnablePrometheusMetricsPathCmdFunc,
}

var getOtelEnablePrometheusMetricsPathCmd = &cobra.Command{
	Use:   "get-enable-prometheus-metrics-path",
	Short: "Get the currently configured OpenTelemetry Prometheus metrics path flag",
	Long:  "Display the OpenTelemetry Prometheus metrics path flag that is currently configured.",
	RunE:  getOtelEnablePrometheusMetricsPathCmdFunc,
}

var unsetOtelEnablePrometheusMetricsPathCmd = &cobra.Command{
	Use:   "unset-enable-prometheus-metrics-path",
	Short: "Remove the configured OpenTelemetry Prometheus metrics path flag",
	Long:  "Remove the OpenTelemetry Prometheus metrics path flag configuration.",
	RunE:  unsetOtelEnablePrometheusMetricsPathCmdFunc,
}

// init sets up the OTEL command hierarchy
func init() {
	// Add OTEL subcommands to otel command
	OtelCmd.AddCommand(setOtelEndpointCmd)
	OtelCmd.AddCommand(getOtelEndpointCmd)
	OtelCmd.AddCommand(unsetOtelEndpointCmd)
	OtelCmd.AddCommand(setOtelMetricsEnabledCmd)
	OtelCmd.AddCommand(getOtelMetricsEnabledCmd)
	OtelCmd.AddCommand(unsetOtelMetricsEnabledCmd)
	OtelCmd.AddCommand(setOtelTracingEnabledCmd)
	OtelCmd.AddCommand(getOtelTracingEnabledCmd)
	OtelCmd.AddCommand(unsetOtelTracingEnabledCmd)
	OtelCmd.AddCommand(setOtelSamplingRateCmd)
	OtelCmd.AddCommand(getOtelSamplingRateCmd)
	OtelCmd.AddCommand(unsetOtelSamplingRateCmd)
	OtelCmd.AddCommand(setOtelEnvVarsCmd)
	OtelCmd.AddCommand(getOtelEnvVarsCmd)
	OtelCmd.AddCommand(unsetOtelEnvVarsCmd)
	OtelCmd.AddCommand(setOtelInsecureCmd)
	OtelCmd.AddCommand(getOtelInsecureCmd)
	OtelCmd.AddCommand(unsetOtelInsecureCmd)
	OtelCmd.AddCommand(setOtelEnablePrometheusMetricsPathCmd)
	OtelCmd.AddCommand(getOtelEnablePrometheusMetricsPathCmd)
	OtelCmd.AddCommand(unsetOtelEnablePrometheusMetricsPathCmd)
}

func setOtelEndpointCmdFunc(_ *cobra.Command, args []string) error {
	endpoint := args[0]

	// The endpoint should not start with http:// or https://
	if endpoint != "" && (strings.HasPrefix(endpoint, "http://") || strings.HasPrefix(endpoint, "https://")) {
		return fmt.Errorf("endpoint URL should not start with http:// or https://")
	}

	// Update the configuration
	err := config.UpdateConfig(func(c *config.Config) {
		c.OTEL.Endpoint = endpoint
	})
	if err != nil {
		return fmt.Errorf("failed to update configuration: %w", err)
	}

	fmt.Printf("Successfully set OpenTelemetry endpoint: %s\n", endpoint)
	return nil
}

func getOtelEndpointCmdFunc(_ *cobra.Command, _ []string) error {
	configProvider := config.NewDefaultProvider()
	cfg := configProvider.GetConfig()

	if cfg.OTEL.Endpoint == "" {
		fmt.Println("No OpenTelemetry endpoint is currently configured.")
		return nil
	}

	fmt.Printf("Current OpenTelemetry endpoint: %s\n", cfg.OTEL.Endpoint)
	return nil
}

func unsetOtelEndpointCmdFunc(_ *cobra.Command, _ []string) error {
	configProvider := config.NewDefaultProvider()
	cfg := configProvider.GetConfig()

	if cfg.OTEL.Endpoint == "" {
		fmt.Println("No OpenTelemetry endpoint is currently configured.")
		return nil
	}

	// Update the configuration
	err := config.UpdateConfig(func(c *config.Config) {
		c.OTEL.Endpoint = ""
	})
	if err != nil {
		return fmt.Errorf("failed to update configuration: %w", err)
	}

	fmt.Println("Successfully removed OpenTelemetry endpoint configuration.")
	return nil
}

func setOtelSamplingRateCmdFunc(_ *cobra.Command, args []string) error {
	rate, err := strconv.ParseFloat(args[0], 64)
	if err != nil {
		return fmt.Errorf("invalid sampling rate format: %w", err)
	}

	// Validate the rate
	if rate < 0.0 || rate > 1.0 {
		return fmt.Errorf("sampling rate must be between 0.0 and 1.0")
	}

	// Update the configuration
	err = config.UpdateConfig(func(c *config.Config) {
		c.OTEL.SamplingRate = rate
	})
	if err != nil {
		return fmt.Errorf("failed to update configuration: %w", err)
	}

	fmt.Printf("Successfully set OpenTelemetry sampling rate: %f\n", rate)
	return nil
}

func getOtelSamplingRateCmdFunc(_ *cobra.Command, _ []string) error {
	configProvider := config.NewDefaultProvider()
	cfg := configProvider.GetConfig()

	if cfg.OTEL.SamplingRate == 0.0 {
		fmt.Println("No OpenTelemetry sampling rate is currently configured.")
		return nil
	}

	fmt.Printf("Current OpenTelemetry sampling rate: %f\n", cfg.OTEL.SamplingRate)
	return nil
}

func unsetOtelSamplingRateCmdFunc(_ *cobra.Command, _ []string) error {
	configProvider := config.NewDefaultProvider()
	cfg := configProvider.GetConfig()

	if cfg.OTEL.SamplingRate == 0.0 {
		fmt.Println("No OpenTelemetry sampling rate is currently configured.")
		return nil
	}

	// Update the configuration
	err := config.UpdateConfig(func(c *config.Config) {
		c.OTEL.SamplingRate = 0.0
	})
	if err != nil {
		return fmt.Errorf("failed to update configuration: %w", err)
	}

	fmt.Println("Successfully removed OpenTelemetry sampling rate configuration.")
	return nil
}

func setOtelEnvVarsCmdFunc(_ *cobra.Command, args []string) error {
	vars := strings.Split(args[0], ",")

	// Trim whitespace from each variable name
	for i, varName := range vars {
		vars[i] = strings.TrimSpace(varName)
	}

	// Update the configuration
	err := config.UpdateConfig(func(c *config.Config) {
		c.OTEL.EnvVars = vars
	})
	if err != nil {
		return fmt.Errorf("failed to update configuration: %w", err)
	}

	fmt.Printf("Successfully set OpenTelemetry environment variables: %v\n", vars)
	return nil
}

func getOtelEnvVarsCmdFunc(_ *cobra.Command, _ []string) error {
	configProvider := config.NewDefaultProvider()
	cfg := configProvider.GetConfig()

	if len(cfg.OTEL.EnvVars) == 0 {
		fmt.Println("No OpenTelemetry environment variables are currently configured.")
		return nil
	}

	fmt.Printf("Current OpenTelemetry environment variables: %v\n", cfg.OTEL.EnvVars)
	return nil
}

func unsetOtelEnvVarsCmdFunc(_ *cobra.Command, _ []string) error {
	configProvider := config.NewDefaultProvider()
	cfg := configProvider.GetConfig()

	if len(cfg.OTEL.EnvVars) == 0 {
		fmt.Println("No OpenTelemetry environment variables are currently configured.")
		return nil
	}

	// Update the configuration
	err := config.UpdateConfig(func(c *config.Config) {
		c.OTEL.EnvVars = []string{}
	})
	if err != nil {
		return fmt.Errorf("failed to update configuration: %w", err)
	}

	fmt.Println("Successfully removed OpenTelemetry environment variables configuration.")
	return nil
}

func setOtelMetricsEnabledCmdFunc(_ *cobra.Command, args []string) error {
	enabled, err := strconv.ParseBool(args[0])
	if err != nil {
		return fmt.Errorf("invalid boolean value for metrics enabled flag: %w", err)
	}

	// Update the configuration
	err = config.UpdateConfig(func(c *config.Config) {
		c.OTEL.MetricsEnabled = enabled
	})
	if err != nil {
		return fmt.Errorf("failed to update configuration: %w", err)
	}

	fmt.Printf("Successfully set OpenTelemetry metrics enabled: %t\n", enabled)
	return nil
}

func getOtelMetricsEnabledCmdFunc(_ *cobra.Command, _ []string) error {
	configProvider := config.NewDefaultProvider()
	cfg := configProvider.GetConfig()

	fmt.Printf("Current OpenTelemetry metrics enabled: %t\n", cfg.OTEL.MetricsEnabled)
	return nil
}

func unsetOtelMetricsEnabledCmdFunc(_ *cobra.Command, _ []string) error {
	configProvider := config.NewDefaultProvider()
	cfg := configProvider.GetConfig()

	if !cfg.OTEL.MetricsEnabled {
		fmt.Println("OpenTelemetry metrics enabled is already disabled.")
		return nil
	}

	// Update the configuration
	err := config.UpdateConfig(func(c *config.Config) {
		c.OTEL.MetricsEnabled = false
	})
	if err != nil {
		return fmt.Errorf("failed to update configuration: %w", err)
	}

	fmt.Println("Successfully disabled OpenTelemetry metrics enabled configuration.")
	return nil
}

func setOtelTracingEnabledCmdFunc(_ *cobra.Command, args []string) error {
	enabled, err := strconv.ParseBool(args[0])
	if err != nil {
		return fmt.Errorf("invalid boolean value for tracing enabled flag: %w", err)
	}

	// Update the configuration
	err = config.UpdateConfig(func(c *config.Config) {
		c.OTEL.TracingEnabled = enabled
	})
	if err != nil {
		return fmt.Errorf("failed to update configuration: %w", err)
	}

	fmt.Printf("Successfully set OpenTelemetry tracing enabled: %t\n", enabled)
	return nil
}

func getOtelTracingEnabledCmdFunc(_ *cobra.Command, _ []string) error {
	configProvider := config.NewDefaultProvider()
	cfg := configProvider.GetConfig()

	fmt.Printf("Current OpenTelemetry tracing enabled: %t\n", cfg.OTEL.TracingEnabled)
	return nil
}

func unsetOtelTracingEnabledCmdFunc(_ *cobra.Command, _ []string) error {
	configProvider := config.NewDefaultProvider()
	cfg := configProvider.GetConfig()

	if !cfg.OTEL.TracingEnabled {
		fmt.Println("OpenTelemetry tracing enabled is already disabled.")
		return nil
	}

	// Update the configuration
	err := config.UpdateConfig(func(c *config.Config) {
		c.OTEL.TracingEnabled = false
	})
	if err != nil {
		return fmt.Errorf("failed to update configuration: %w", err)
	}

	fmt.Println("Successfully disabled OpenTelemetry tracing enabled configuration.")
	return nil
}

func setOtelInsecureCmdFunc(_ *cobra.Command, args []string) error {
	enabled, err := strconv.ParseBool(args[0])
	if err != nil {
		return fmt.Errorf("invalid boolean value for insecure flag: %w", err)
	}

	// Update the configuration
	err = config.UpdateConfig(func(c *config.Config) {
		c.OTEL.Insecure = enabled
	})
	if err != nil {
		return fmt.Errorf("failed to update configuration: %w", err)
	}

	fmt.Printf("Successfully set OpenTelemetry insecure transport: %t\n", enabled)
	return nil
}

func getOtelInsecureCmdFunc(_ *cobra.Command, _ []string) error {
	configProvider := config.NewDefaultProvider()
	cfg := configProvider.GetConfig()

	fmt.Printf("Current OpenTelemetry insecure transport: %t\n", cfg.OTEL.Insecure)
	return nil
}

func unsetOtelInsecureCmdFunc(_ *cobra.Command, _ []string) error {
	configProvider := config.NewDefaultProvider()
	cfg := configProvider.GetConfig()

	if !cfg.OTEL.Insecure {
		fmt.Println("OpenTelemetry insecure transport is already disabled.")
		return nil
	}

	// Update the configuration
	err := config.UpdateConfig(func(c *config.Config) {
		c.OTEL.Insecure = false
	})
	if err != nil {
		return fmt.Errorf("failed to update configuration: %w", err)
	}

	fmt.Println("Successfully disabled OpenTelemetry insecure transport configuration.")
	return nil
}

func setOtelEnablePrometheusMetricsPathCmdFunc(_ *cobra.Command, args []string) error {
	enabled, err := strconv.ParseBool(args[0])
	if err != nil {
		return fmt.Errorf("invalid boolean value for Prometheus metrics path flag: %w", err)
	}

	// Update the configuration
	err = config.UpdateConfig(func(c *config.Config) {
		c.OTEL.EnablePrometheusMetricsPath = enabled
	})
	if err != nil {
		return fmt.Errorf("failed to update configuration: %w", err)
	}

	fmt.Printf("Successfully set Prometheus metrics path: %t\n", enabled)
	return nil
}

func getOtelEnablePrometheusMetricsPathCmdFunc(_ *cobra.Command, _ []string) error {
	configProvider := config.NewDefaultProvider()
	cfg := configProvider.GetConfig()

	fmt.Printf("Current Prometheus metrics path flag: %t\n", cfg.OTEL.EnablePrometheusMetricsPath)
	return nil
}

func unsetOtelEnablePrometheusMetricsPathCmdFunc(_ *cobra.Command, _ []string) error {
	configProvider := config.NewDefaultProvider()
	cfg := configProvider.GetConfig()

	if !cfg.OTEL.EnablePrometheusMetricsPath {
		fmt.Println("Prometheus metrics path is already disabled.")
		return nil
	}

	// Update the configuration
	err := config.UpdateConfig(func(c *config.Config) {
		c.OTEL.EnablePrometheusMetricsPath = false
	})
	if err != nil {
		return fmt.Errorf("failed to update configuration: %w", err)
	}

	fmt.Println("Successfully disabled the Prometheus metrics path configuration.")
	return nil
}

package app

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/stacklok/toolhive/pkg/config"
)

// OtelCmd is the parent command for OpenTelemetry configuration
var OtelCmd = &cobra.Command{
	Use:   "otel",
	Short: "Manage OpenTelemetry configuration",
	Long:  "Configure OpenTelemetry settings for observability and monitoring of MCP servers.",
}

// createOTELSetCommand creates a generic set command for an OTEL field
func createOTELSetCommand(fieldName, commandName, description, example string) *cobra.Command {
	return &cobra.Command{
		Use:   fmt.Sprintf("set-%s <%s>", commandName, commandName),
		Short: fmt.Sprintf("Set the OpenTelemetry %s", description),
		Long:  fmt.Sprintf("Set the OpenTelemetry %s.\n\nExample:\n\n\tthv config otel set-%s %s", description, commandName, example),
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			provider := config.NewDefaultProvider()
			err := config.SetConfigField(provider, fieldName, args[0])
			if err != nil {
				return err
			}
			fmt.Printf("Successfully set OpenTelemetry %s: %s\n", description, args[0])
			return nil
		},
	}
}

// createOTELGetCommand creates a generic get command for an OTEL field
func createOTELGetCommand(fieldName, commandName, description string) *cobra.Command {
	return &cobra.Command{
		Use:   fmt.Sprintf("get-%s", commandName),
		Short: fmt.Sprintf("Get the currently configured OpenTelemetry %s", description),
		Long:  fmt.Sprintf("Display the OpenTelemetry %s that is currently configured.", description),
		RunE: func(_ *cobra.Command, _ []string) error {
			provider := config.NewDefaultProvider()
			value, isSet, err := config.GetConfigField(provider, fieldName)
			if err != nil {
				return err
			}

			if !isSet {
				fmt.Printf("No OpenTelemetry %s is currently configured.\n", description)
				return nil
			}

			fmt.Printf("Current OpenTelemetry %s: %s\n", description, value)
			return nil
		},
	}
}

// createOTELUnsetCommand creates a generic unset command for an OTEL field
func createOTELUnsetCommand(fieldName, commandName, description string) *cobra.Command {
	return &cobra.Command{
		Use:   fmt.Sprintf("unset-%s", commandName),
		Short: fmt.Sprintf("Remove the configured OpenTelemetry %s", description),
		Long:  fmt.Sprintf("Remove the OpenTelemetry %s configuration.", description),
		RunE: func(_ *cobra.Command, _ []string) error {
			provider := config.NewDefaultProvider()

			// Check if it's set before unsetting
			_, isSet, err := config.GetConfigField(provider, fieldName)
			if err != nil {
				return err
			}

			if !isSet {
				fmt.Printf("No OpenTelemetry %s is currently configured.\n", description)
				return nil
			}

			err = config.UnsetConfigField(provider, fieldName)
			if err != nil {
				return err
			}

			fmt.Printf("Successfully removed OpenTelemetry %s configuration.\n", description)
			return nil
		},
	}
}

func init() {
	// Endpoint commands
	OtelCmd.AddCommand(createOTELSetCommand("otel-endpoint", "endpoint", "endpoint URL", "https://api.honeycomb.io"))
	OtelCmd.AddCommand(createOTELGetCommand("otel-endpoint", "endpoint", "endpoint"))
	OtelCmd.AddCommand(createOTELUnsetCommand("otel-endpoint", "endpoint", "endpoint"))

	// Sampling rate commands
	OtelCmd.AddCommand(createOTELSetCommand("otel-sampling-rate", "sampling-rate", "sampling rate", "0.5"))
	OtelCmd.AddCommand(createOTELGetCommand("otel-sampling-rate", "sampling-rate", "sampling rate"))
	OtelCmd.AddCommand(createOTELUnsetCommand("otel-sampling-rate", "sampling-rate", "sampling rate"))

	// Environment variables commands
	OtelCmd.AddCommand(createOTELSetCommand("otel-env-vars", "env-vars", "environment variables", "VAR1,VAR2,VAR3"))
	OtelCmd.AddCommand(createOTELGetCommand("otel-env-vars", "env-vars", "environment variables"))
	OtelCmd.AddCommand(createOTELUnsetCommand("otel-env-vars", "env-vars", "environment variables"))

	// Metrics enabled commands
	OtelCmd.AddCommand(createOTELSetCommand("otel-metrics-enabled", "metrics-enabled", "metrics export flag", "true"))
	OtelCmd.AddCommand(createOTELGetCommand("otel-metrics-enabled", "metrics-enabled", "metrics export flag"))
	OtelCmd.AddCommand(createOTELUnsetCommand("otel-metrics-enabled", "metrics-enabled", "metrics export flag"))

	// Tracing enabled commands
	OtelCmd.AddCommand(createOTELSetCommand("otel-tracing-enabled", "tracing-enabled", "tracing export flag", "true"))
	OtelCmd.AddCommand(createOTELGetCommand("otel-tracing-enabled", "tracing-enabled", "tracing export flag"))
	OtelCmd.AddCommand(createOTELUnsetCommand("otel-tracing-enabled", "tracing-enabled", "tracing export flag"))

	// Insecure commands
	OtelCmd.AddCommand(createOTELSetCommand("otel-insecure", "insecure", "insecure connection flag", "true"))
	OtelCmd.AddCommand(createOTELGetCommand("otel-insecure", "insecure", "insecure connection flag"))
	OtelCmd.AddCommand(createOTELUnsetCommand("otel-insecure", "insecure", "insecure connection flag"))

	// Enable Prometheus metrics path commands
	OtelCmd.AddCommand(createOTELSetCommand("otel-enable-prometheus-metrics-path", "enable-prometheus-metrics-path", "Prometheus metrics path flag", "true"))
	OtelCmd.AddCommand(createOTELGetCommand("otel-enable-prometheus-metrics-path", "enable-prometheus-metrics-path", "Prometheus metrics path flag"))
	OtelCmd.AddCommand(createOTELUnsetCommand("otel-enable-prometheus-metrics-path", "enable-prometheus-metrics-path", "Prometheus metrics path flag"))
}

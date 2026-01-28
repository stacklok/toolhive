// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package app

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/stacklok/toolhive/pkg/config"
	"github.com/stacklok/toolhive/pkg/registry"
)

var configCmd = &cobra.Command{
	Use:   "config",
	Short: "Manage application configuration",
	Long:  "The config command provides subcommands to manage application configuration settings.",
}

var setCACertCmd = &cobra.Command{
	Use:   "set-ca-cert <path>",
	Short: "Set the default CA certificate for container builds",
	Long: `Set the default CA certificate file path that will be used for all container builds.
This is useful in corporate environments with TLS inspection where custom CA certificates are required.

Example:
  thv config set-ca-cert /path/to/corporate-ca.crt`,
	Args: cobra.ExactArgs(1),
	RunE: setCACertCmdFunc,
}

var getCACertCmd = &cobra.Command{
	Use:   "get-ca-cert",
	Short: "Get the currently configured CA certificate path",
	Long:  "Display the path to the CA certificate file that is currently configured for container builds.",
	RunE:  getCACertCmdFunc,
}

var unsetCACertCmd = &cobra.Command{
	Use:   "unset-ca-cert",
	Short: "Remove the configured CA certificate",
	Long:  "Remove the CA certificate configuration, reverting to default behavior without custom CA certificates.",
	RunE:  unsetCACertCmdFunc,
}

var setRegistryCmd = &cobra.Command{
	Use:   "set-registry <url-or-path>",
	Short: "Set the MCP server registry",
	Long: `Set the MCP server registry to a remote URL, local file path, or API endpoint.
The command automatically detects the registry type:
  - URLs ending with .json are treated as static registry files
  - Other URLs are treated as MCP Registry API endpoints (v0.1 spec)
  - Local paths are treated as local registry files

Examples:
  thv config set-registry https://example.com/registry.json           # Static remote file
  thv config set-registry https://registry.example.com                # API endpoint
  thv config set-registry /path/to/local-registry.json               # Local file path
  thv config set-registry file:///path/to/local-registry.json        # Explicit file URL`,
	Args: cobra.ExactArgs(1),
	RunE: setRegistryCmdFunc,
}

var getRegistryCmd = &cobra.Command{
	Use:   "get-registry",
	Short: "Get the currently configured registry",
	Long:  "Display the currently configured registry (URL or file path).",
	RunE:  getRegistryCmdFunc,
}

var unsetRegistryCmd = &cobra.Command{
	Use:   "unset-registry",
	Short: "Remove the configured registry",
	Long:  "Remove the registry configuration, reverting to the built-in registry.",
	RunE:  unsetRegistryCmdFunc,
}

var usageMetricsCmd = &cobra.Command{
	Use:   "usage-metrics <enable|disable>",
	Short: "Enable or disable anonymous usage metrics",
	Args:  cobra.ExactArgs(1),
	RunE:  usageMetricsCmdFunc,
}

var (
	allowPrivateRegistryIp bool
)

func init() {
	// Add config command to root command
	rootCmd.AddCommand(configCmd)

	// Add subcommands to config command
	configCmd.AddCommand(setCACertCmd)
	configCmd.AddCommand(getCACertCmd)
	configCmd.AddCommand(unsetCACertCmd)
	configCmd.AddCommand(setRegistryCmd)
	setRegistryCmd.Flags().BoolVarP(
		&allowPrivateRegistryIp,
		"allow-private-ip",
		"p",
		false,
		"Allow setting the registry URL or API endpoint, even if it references a private IP address (default false)",
	)
	configCmd.AddCommand(getRegistryCmd)
	configCmd.AddCommand(unsetRegistryCmd)
	configCmd.AddCommand(usageMetricsCmd)

	// Add OTEL parent command to config
	configCmd.AddCommand(OtelCmd)
}

func setCACertCmdFunc(_ *cobra.Command, args []string) error {
	certPath := args[0]

	provider := config.NewDefaultProvider()
	err := provider.SetCACert(certPath)
	if err != nil {
		return err
	}

	fmt.Printf("Successfully set CA certificate path: %s\n", filepath.Clean(certPath))
	return nil
}

func getCACertCmdFunc(_ *cobra.Command, _ []string) error {
	provider := config.NewDefaultProvider()
	certPath, exists, accessible := provider.GetCACert()

	if !exists {
		fmt.Println("No CA certificate is currently configured.")
		return nil
	}

	fmt.Printf("Current CA certificate path: %s\n", certPath)

	if !accessible {
		fmt.Printf("Warning: The configured CA certificate file is not accessible\n")
	}

	return nil
}

func unsetCACertCmdFunc(_ *cobra.Command, _ []string) error {
	provider := config.NewDefaultProvider()
	certPath, exists, _ := provider.GetCACert()

	if !exists {
		fmt.Println("No CA certificate is currently configured.")
		return nil
	}

	err := provider.UnsetCACert()
	if err != nil {
		return err
	}

	fmt.Printf("Successfully removed CA certificate configuration: %s\n", certPath)
	return nil
}

func setRegistryCmdFunc(_ *cobra.Command, args []string) error {
	input := args[0]

	service := registry.NewConfigurator()
	registryType, message, err := service.SetRegistryFromInput(input, allowPrivateRegistryIp)
	if err != nil {
		// Enhance error message for better user experience
		return enhanceRegistryError(err, input, string(registryType))
	}

	// Reset the registry provider cache to pick up the new configuration
	registry.ResetDefaultProvider()

	// Print success message
	fmt.Println(message)

	// Add additional security warnings for private IP usage
	if allowPrivateRegistryIp {
		fmt.Print("Successfully enabled use of private IP addresses for the registry\n")
		fmt.Print("Caution: allowing registry URLs containing private IP addresses may decrease your security.\n" +
			"Make sure you trust any registries you configure with ToolHive.\n")
	} else if registryType != config.RegistryTypeFile {
		// Only show this message for URL/API types
		fmt.Print("Use of private IP addresses for the registry has been disabled" +
			" as it's not needed for the provided registry.\n")
	}

	return nil
}

func getRegistryCmdFunc(_ *cobra.Command, _ []string) error {
	service := registry.NewConfigurator()
	registryType, source := service.GetRegistryInfo()

	switch registryType {
	case config.RegistryTypeAPI:
		fmt.Printf("Current registry: %s (API endpoint)\n", source)
	case config.RegistryTypeURL:
		fmt.Printf("Current registry: %s (remote file)\n", source)
	case config.RegistryTypeFile:
		fmt.Printf("Current registry: %s (local file)\n", source)
		// Check if the file still exists
		if _, err := os.Stat(source); err != nil {
			fmt.Printf("Warning: The configured local registry file is not accessible: %v\n", err)
		}
	default:
		fmt.Println("No custom registry is currently configured. Using built-in registry.")
	}
	return nil
}

func unsetRegistryCmdFunc(_ *cobra.Command, _ []string) error {
	service := registry.NewConfigurator()
	message, err := service.UnsetRegistry()
	if err != nil {
		return fmt.Errorf("failed to update configuration: %w", err)
	}

	// Reset the registry provider cache to pick up the default configuration
	registry.ResetDefaultProvider()

	fmt.Println(message)
	return nil
}

// enhanceRegistryError enhances registry errors with helpful user-facing messages.
// Error type mapping (matches API HTTP status codes):
//   - Timeout/Unreachable errors → 504 Gateway Timeout
//   - Validation errors → 502 Bad Gateway
func enhanceRegistryError(err error, url, registryType string) error {
	if err == nil {
		return nil
	}

	// Check if this is a RegistryError with structured error information
	var regErr *config.RegistryError
	if errors.As(err, &regErr) {
		// Check for timeout errors (504 Gateway Timeout)
		if errors.Is(regErr.Err, config.ErrRegistryTimeout) {
			return fmt.Errorf("connection timed out after 5 seconds\n"+
				"The %s at %s is not responding.\n"+
				"Possible causes:\n"+
				"  - The URL is incorrect\n"+
				"  - The registry server is down or slow to respond\n"+
				"  - Network connectivity issues\n"+
				"Original error: %v", registryType, url, regErr.Err)
		}

		// Check for unreachable errors (504 Gateway Timeout)
		if errors.Is(regErr.Err, config.ErrRegistryUnreachable) {
			return fmt.Errorf("connection failed\n"+
				"The %s at %s is not reachable.\n"+
				"Please check:\n"+
				"  - The URL is correct: %s\n"+
				"  - The registry server is running and accessible\n"+
				"  - Your network connection\n"+
				"  - Firewall or proxy settings\n"+
				"Original error: %v", registryType, url, url, regErr.Err)
		}

		// Check for validation errors (502 Bad Gateway)
		if errors.Is(regErr.Err, config.ErrRegistryValidationFailed) {
			return fmt.Errorf("validation failed\n"+
				"The %s at %s returned an invalid response or does not appear to be a valid registry.\n"+
				"Please verify:\n"+
				"  - The URL points to a valid MCP registry\n"+
				"  - The registry format is correct\n"+
				"  - The registry contains at least one server\n"+
				"Original error: %v", registryType, url, regErr.Err)
		}
	}

	// For other errors, return the original error with minimal enhancement
	return fmt.Errorf("failed to set %s: %w", registryType, err)
}

func usageMetricsCmdFunc(_ *cobra.Command, args []string) error {
	action := args[0]

	var disable bool
	switch action {
	case "enable":
		disable = false
	case "disable":
		disable = true
	default:
		return fmt.Errorf("invalid argument: %s (expected 'enable' or 'disable')", action)
	}

	err := config.UpdateConfig(func(c *config.Config) {
		c.DisableUsageMetrics = disable
	})
	if err != nil {
		return fmt.Errorf("failed to update configuration: %w", err)
	}

	if disable {
		fmt.Println("Usage metrics disabled.")
	} else {
		fmt.Println("Usage metrics enabled.")
	}
	return nil
}

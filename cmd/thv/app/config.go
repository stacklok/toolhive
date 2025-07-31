package app

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/stacklok/toolhive/pkg/certs"
	"github.com/stacklok/toolhive/pkg/config"
	"github.com/stacklok/toolhive/pkg/networking"
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
	Long: `Set the MCP server registry to either a remote URL or local file path.
The command automatically detects whether the input is a URL or file path.

Examples:
  thv config set-registry https://example.com/registry.json           # Remote URL
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
		"Allow setting the registry URL, even if it references a private IP address",
	)
	configCmd.AddCommand(getRegistryCmd)
	configCmd.AddCommand(unsetRegistryCmd)

	// Add OTEL parent command to config
	configCmd.AddCommand(OtelCmd)
}

func setCACertCmdFunc(_ *cobra.Command, args []string) error {
	certPath := filepath.Clean(args[0])

	// Validate that the file exists and is readable
	if _, err := os.Stat(certPath); err != nil {
		return fmt.Errorf("CA certificate file not found or not accessible: %w", err)
	}

	// Read and validate the certificate
	certContent, err := os.ReadFile(certPath)
	if err != nil {
		return fmt.Errorf("failed to read CA certificate file: %w", err)
	}

	// Validate the certificate format
	if err := certs.ValidateCACertificate(certContent); err != nil {
		return fmt.Errorf("invalid CA certificate: %w", err)
	}

	// Update the configuration
	err = config.UpdateConfig(func(c *config.Config) {
		c.CACertificatePath = certPath
	})
	if err != nil {
		return fmt.Errorf("failed to update configuration: %w", err)
	}

	fmt.Printf("Successfully set CA certificate path: %s\n", certPath)
	return nil
}

func getCACertCmdFunc(_ *cobra.Command, _ []string) error {
	cfg := config.GetConfig()

	if cfg.CACertificatePath == "" {
		fmt.Println("No CA certificate is currently configured.")
		return nil
	}

	fmt.Printf("Current CA certificate path: %s\n", cfg.CACertificatePath)

	// Check if the file still exists
	if _, err := os.Stat(cfg.CACertificatePath); err != nil {
		fmt.Printf("Warning: The configured CA certificate file is not accessible: %v\n", err)
	}

	return nil
}

func unsetCACertCmdFunc(_ *cobra.Command, _ []string) error {
	cfg := config.GetConfig()

	if cfg.CACertificatePath == "" {
		fmt.Println("No CA certificate is currently configured.")
		return nil
	}

	// Update the configuration
	err := config.UpdateConfig(func(c *config.Config) {
		c.CACertificatePath = ""
	})
	if err != nil {
		return fmt.Errorf("failed to update configuration: %w", err)
	}

	fmt.Println("Successfully removed CA certificate configuration.")
	return nil
}

const (
	registryTypeFile = "file"
	registryTypeURL  = "url"
)

func detectRegistryType(input string) (registryType string, cleanPath string) {
	// Check for explicit file:// protocol
	if strings.HasPrefix(input, "file://") {
		return registryTypeFile, strings.TrimPrefix(input, "file://")
	}

	// Check for HTTP/HTTPS URLs
	if strings.HasPrefix(input, "http://") || strings.HasPrefix(input, "https://") {
		return registryTypeURL, input
	}

	// Default: treat as file path
	return registryTypeFile, filepath.Clean(input)
}

func setRegistryCmdFunc(_ *cobra.Command, args []string) error {
	input := args[0]
	registryType, cleanPath := detectRegistryType(input)

	switch registryType {
	case registryTypeURL:
		return setRegistryURL(cleanPath)
	case registryTypeFile:
		return setRegistryFile(cleanPath)
	default:
		return fmt.Errorf("unsupported registry type")
	}
}

func setRegistryURL(registryURL string) error {
	// Basic URL validation - check if it starts with http:// or https://
	if registryURL != "" && !strings.HasPrefix(registryURL, "http://") && !strings.HasPrefix(registryURL, "https://") {
		return fmt.Errorf("registry URL must start with http:// or https://")
	}

	if !allowPrivateRegistryIp {
		registryClient, err := networking.NewHttpClientBuilder().Build()
		if err != nil {
			return fmt.Errorf("failed to create HTTP client: %w", err)
		}
		_, err = registryClient.Get(registryURL)
		if err != nil && strings.Contains(fmt.Sprint(err), networking.ErrPrivateIpAddress) {
			return err
		}
	}

	// Update the configuration
	err := config.UpdateConfig(func(c *config.Config) {
		c.RegistryUrl = registryURL
		c.LocalRegistryPath = "" // Clear local path when setting URL
		c.AllowPrivateRegistryIp = allowPrivateRegistryIp
	})
	if err != nil {
		return fmt.Errorf("failed to update configuration: %w", err)
	}

	fmt.Printf("Successfully set registry URL: %s\n", registryURL)
	if allowPrivateRegistryIp {
		fmt.Print("Successfully enabled use of private IP addresses for the remote registry\n")
		fmt.Print("Caution: allowing registry URLs containing private IP addresses may decrease your security.\n" +
			"Make sure you trust any remote registries you configure with ToolHive.")
	} else {
		fmt.Printf("Use of private IP addresses for the remote registry has been disabled" +
			" as it's not needed for the provided registry.\n")
	}

	return nil
}

func setRegistryFile(registryPath string) error {
	// Validate that the file exists and is readable
	if _, err := os.Stat(registryPath); err != nil {
		return fmt.Errorf("local registry file not found or not accessible: %w", err)
	}

	// Basic validation - check if it's a JSON file
	if !strings.HasSuffix(strings.ToLower(registryPath), ".json") {
		return fmt.Errorf("registry file must be a JSON file (*.json)")
	}

	// Try to read and parse the file to validate it's a valid registry
	// #nosec G304: File path is user-provided but validated above
	registryContent, err := os.ReadFile(registryPath)
	if err != nil {
		return fmt.Errorf("failed to read registry file: %w", err)
	}

	// Basic JSON validation
	var registry map[string]interface{}
	if err := json.Unmarshal(registryContent, &registry); err != nil {
		return fmt.Errorf("invalid JSON format in registry file: %w", err)
	}

	// Update the configuration
	err = config.UpdateConfig(func(c *config.Config) {
		c.LocalRegistryPath = registryPath
		c.RegistryUrl = "" // Clear URL when setting local path
	})
	if err != nil {
		return fmt.Errorf("failed to update configuration: %w", err)
	}

	fmt.Printf("Successfully set local registry file: %s\n", registryPath)
	return nil
}

func getRegistryCmdFunc(_ *cobra.Command, _ []string) error {
	cfg := config.GetConfig()

	if cfg.RegistryUrl != "" {
		fmt.Printf("Current registry: %s (remote URL)\n", cfg.RegistryUrl)
		return nil
	}

	if cfg.LocalRegistryPath != "" {
		fmt.Printf("Current registry: %s (local file)\n", cfg.LocalRegistryPath)

		// Check if the file still exists
		if _, err := os.Stat(cfg.LocalRegistryPath); err != nil {
			fmt.Printf("Warning: The configured local registry file is not accessible: %v\n", err)
		}
		return nil
	}

	fmt.Println("No custom registry is currently configured. Using built-in registry.")
	return nil
}

func unsetRegistryCmdFunc(_ *cobra.Command, _ []string) error {
	cfg := config.GetConfig()

	if cfg.RegistryUrl == "" && cfg.LocalRegistryPath == "" {
		fmt.Println("No custom registry is currently configured.")
		return nil
	}

	// Update the configuration
	err := config.UpdateConfig(func(c *config.Config) {
		c.RegistryUrl = ""
		c.LocalRegistryPath = ""
	})
	if err != nil {
		return fmt.Errorf("failed to update configuration: %w", err)
	}

	fmt.Println("Successfully removed registry configuration. Will use built-in registry.")
	return nil
}

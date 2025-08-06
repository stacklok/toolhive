package app

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/stacklok/toolhive/pkg/certs"
	"github.com/stacklok/toolhive/pkg/config"
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

func setRegistryCmdFunc(_ *cobra.Command, args []string) error {
	input := args[0]
	registryType, cleanPath := config.DetectRegistryType(input)

	switch registryType {
	case config.RegistryTypeURL:
		err := config.SetRegistryURL(cleanPath, allowPrivateRegistryIp)
		if err != nil {
			return err
		}
		fmt.Printf("Successfully set registry URL: %s\n", cleanPath)
		if allowPrivateRegistryIp {
			fmt.Print("Successfully enabled use of private IP addresses for the remote registry\n")
			fmt.Print("Caution: allowing registry URLs containing private IP addresses may decrease your security.\n" +
				"Make sure you trust any remote registries you configure with ToolHive.")
		} else {
			fmt.Printf("Use of private IP addresses for the remote registry has been disabled" +
				" as it's not needed for the provided registry.\n")
		}
		return nil
	case config.RegistryTypeFile:
		return config.SetRegistryFile(cleanPath)
	default:
		return fmt.Errorf("unsupported registry type")
	}
}

func getRegistryCmdFunc(_ *cobra.Command, _ []string) error {
	url, localPath, _, registryType := config.GetRegistryConfig()

	switch registryType {
	case config.RegistryTypeURL:
		fmt.Printf("Current registry: %s (remote URL)\n", url)
	case config.RegistryTypeFile:
		fmt.Printf("Current registry: %s (local file)\n", localPath)
		// Check if the file still exists
		if _, err := os.Stat(localPath); err != nil {
			fmt.Printf("Warning: The configured local registry file is not accessible: %v\n", err)
		}
	default:
		fmt.Println("No custom registry is currently configured. Using built-in registry.")
	}
	return nil
}

func unsetRegistryCmdFunc(_ *cobra.Command, _ []string) error {
	url, localPath, _, registryType := config.GetRegistryConfig()

	if registryType == "default" {
		fmt.Println("No custom registry is currently configured.")
		return nil
	}

	err := config.UnsetRegistry()
	if err != nil {
		return fmt.Errorf("failed to update configuration: %w", err)
	}

	if url != "" {
		fmt.Printf("Successfully removed registry URL: %s\n", url)
	} else if localPath != "" {
		fmt.Printf("Successfully removed local registry file: %s\n", localPath)
	}
	fmt.Println("Will use built-in registry.")
	return nil
}

package config

import (
	"fmt"
	"strings"

	"github.com/stacklok/toolhive/pkg/certs"
	"github.com/stacklok/toolhive/pkg/networking"
)

// init registers all built-in config fields
func init() {
	registerCACertField()
	registerRegistryURLField()
	registerRegistryFileField()
}

// registerCACertField registers the CA certificate config field
func registerCACertField() {
	RegisterConfigField(ConfigFieldSpec{
		Name: "ca-cert",
		SetValidator: func(_ Provider, value string) error {
			// Validate and clean the file path
			cleanPath, err := validateFilePath(value)
			if err != nil {
				return fmt.Errorf("CA certificate %w", err)
			}

			// Read the certificate
			certContent, err := readFile(cleanPath)
			if err != nil {
				return fmt.Errorf("CA certificate %w", err)
			}

			// Validate the certificate format
			if err := certs.ValidateCACertificate(certContent); err != nil {
				return fmt.Errorf("invalid CA certificate: %w", err)
			}

			return nil
		},
		Setter: func(cfg *Config, value string) {
			// Clean the path again when setting (validation already confirmed it's valid)
			cleanPath, _ := validateFilePath(value)
			cfg.CACertificatePath = cleanPath
		},
		Getter: func(cfg *Config) string {
			return cfg.CACertificatePath
		},
		Unsetter: func(cfg *Config) {
			cfg.CACertificatePath = ""
		},
		IsSet: func(cfg *Config) bool {
			return cfg.CACertificatePath != ""
		},
		DisplayName: "CA Certificate",
		HelpText:    "Path to a CA certificate file for validating TLS connections",
	})
}

// registerRegistryURLField registers the registry URL config field
func registerRegistryURLField() {
	RegisterConfigField(ConfigFieldSpec{
		Name: "registry-url",
		SetValidator: func(provider Provider, value string) error {
			// Parse the URL to extract the allowInsecure flag
			// Format: "url" or "url|insecure" for backward compatibility
			parts := strings.Split(value, "|")
			registryURL := parts[0]
			allowInsecure := len(parts) > 1 && parts[1] == "insecure"

			// Validate URL scheme
			_, err := validateURLScheme(registryURL, allowInsecure)
			if err != nil {
				return fmt.Errorf("invalid registry URL: %w", err)
			}

			// Check for private IP addresses if not allowed
			cfg := provider.GetConfig()
			if !cfg.AllowPrivateRegistryIp && !allowInsecure {
				registryClient, err := networking.NewHttpClientBuilder().Build()
				if err != nil {
					return fmt.Errorf("failed to create HTTP client: %w", err)
				}
				_, err = registryClient.Get(registryURL)
				if err != nil && strings.Contains(fmt.Sprint(err), networking.ErrPrivateIpAddress) {
					return err
				}
			}

			return nil
		},
		Setter: func(cfg *Config, value string) {
			// Parse the value to extract URL and allowInsecure flag
			parts := strings.Split(value, "|")
			registryURL := parts[0]
			allowInsecure := len(parts) > 1 && parts[1] == "insecure"

			cfg.RegistryUrl = registryURL
			cfg.LocalRegistryPath = "" // Clear local path when setting URL
			if allowInsecure {
				cfg.AllowPrivateRegistryIp = true
			}
		},
		Getter: func(cfg *Config) string {
			if cfg.RegistryUrl == "" {
				return ""
			}
			// Return URL with insecure flag if set
			if cfg.AllowPrivateRegistryIp {
				return cfg.RegistryUrl + "|insecure"
			}
			return cfg.RegistryUrl
		},
		Unsetter: func(cfg *Config) {
			cfg.RegistryUrl = ""
			cfg.AllowPrivateRegistryIp = false
		},
		IsSet: func(cfg *Config) bool {
			return cfg.RegistryUrl != ""
		},
		DisplayName: "Registry URL",
		HelpText:    "URL of the MCP server registry (HTTP/HTTPS)",
	})
}

// registerRegistryFileField registers the registry file config field
func registerRegistryFileField() {
	RegisterConfigField(ConfigFieldSpec{
		Name: "registry-file",
		SetValidator: func(_ Provider, value string) error {
			// Validate file path exists
			cleanPath, err := validateFilePath(value)
			if err != nil {
				return fmt.Errorf("local registry %w", err)
			}

			// Validate JSON file
			if err := validateJSONFile(cleanPath); err != nil {
				return fmt.Errorf("registry file: %w", err)
			}

			return nil
		},
		Setter: func(cfg *Config, value string) {
			// Clean and make absolute
			cleanPath, _ := validateFilePath(value)
			absPath, err := makeAbsolutePath(cleanPath)
			if err != nil {
				// Fall back to cleaned path if absolute path resolution fails
				absPath = cleanPath
			}

			cfg.LocalRegistryPath = absPath
			cfg.RegistryUrl = "" // Clear URL when setting local path
		},
		Getter: func(cfg *Config) string {
			return cfg.LocalRegistryPath
		},
		Unsetter: func(cfg *Config) {
			cfg.LocalRegistryPath = ""
		},
		IsSet: func(cfg *Config) bool {
			return cfg.LocalRegistryPath != ""
		},
		DisplayName: "Registry File",
		HelpText:    "Path to a local JSON file containing the MCP server registry",
	})
}

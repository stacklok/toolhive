package config

import (
	"fmt"
	neturl "net/url"
	"path/filepath"
	"strings"

	"github.com/stacklok/toolhive/pkg/networking"
)

const (
	// RegistryTypeFile represents a local file registry
	RegistryTypeFile = "file"
	// RegistryTypeURL represents a remote URL registry
	RegistryTypeURL = "url"
	// RegistryTypeAPI represents an MCP Registry API endpoint
	RegistryTypeAPI = "api"
)

// DetectRegistryType determines if input is a URL or file path and returns cleaned path
func DetectRegistryType(input string) (registryType string, cleanPath string) {
	// Check for explicit file:// protocol
	if strings.HasPrefix(input, "file://") {
		return RegistryTypeFile, strings.TrimPrefix(input, "file://")
	}

	// Check for HTTP/HTTPS URLs
	if networking.IsURL(input) {
		// If URL ends with .json, treat as static registry file
		// Otherwise, treat as MCP Registry API endpoint
		if strings.HasSuffix(input, ".json") {
			return RegistryTypeURL, input
		}
		return RegistryTypeAPI, input
	}

	// Default: treat as file path
	return RegistryTypeFile, filepath.Clean(input)
}

// setRegistryURL validates and sets a registry URL using the provided provider
func setRegistryURL(provider Provider, registryURL string, allowPrivateRegistryIp bool) error {
	// Validate URL scheme
	_, err := validateURLScheme(registryURL, allowPrivateRegistryIp)
	if err != nil {
		return fmt.Errorf("invalid registry URL: %w", err)
	}

	// Check for private IP addresses if not allowed
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
	err = provider.UpdateConfig(func(c *Config) {
		c.RegistryUrl = registryURL
		c.LocalRegistryPath = "" // Clear local path when setting URL
		c.AllowPrivateRegistryIp = allowPrivateRegistryIp
	})
	if err != nil {
		return fmt.Errorf("failed to update configuration: %w", err)
	}

	return nil
}

// setRegistryFile validates and sets a local registry file using the provided provider
func setRegistryFile(provider Provider, registryPath string) error {
	// Validate file path exists
	cleanPath, err := validateFilePath(registryPath)
	if err != nil {
		return fmt.Errorf("local registry %w", err)
	}

	// Validate JSON file
	if err := validateJSONFile(cleanPath); err != nil {
		return fmt.Errorf("registry file: %w", err)
	}

	// Make the path absolute
	absPath, err := makeAbsolutePath(cleanPath)
	if err != nil {
		return fmt.Errorf("registry file: %w", err)
	}

	// Update the configuration
	err = provider.UpdateConfig(func(c *Config) {
		c.LocalRegistryPath = absPath
		c.RegistryUrl = "" // Clear URL when setting local path
	})
	if err != nil {
		return fmt.Errorf("failed to update configuration: %w", err)
	}

	return nil
}

// setRegistryAPI validates and sets an MCP Registry API URL using the provided provider
func setRegistryAPI(provider Provider, apiURL string, allowPrivateRegistryIp bool) error {
	parsedURL, err := neturl.Parse(apiURL)
	if err != nil {
		return fmt.Errorf("invalid registry API URL: %w", err)
	}

	if allowPrivateRegistryIp {
		// we validate either https or http URLs
		if parsedURL.Scheme != networking.HttpScheme && parsedURL.Scheme != networking.HttpsScheme {
			return fmt.Errorf("registry API URL must start with http:// or https:// when allowing private IPs")
		}
	} else {
		// we just allow https
		if parsedURL.Scheme != networking.HttpsScheme {
			return fmt.Errorf("registry API URL must start with https:// when not allowing private IPs")
		}
	}

	if !allowPrivateRegistryIp {
		registryClient, err := networking.NewHttpClientBuilder().Build()
		if err != nil {
			return fmt.Errorf("failed to create HTTP client: %w", err)
		}
		// Try to fetch the /openapi.yaml endpoint to validate
		// Use JoinPath for safe URL construction
		openapiURL, err := neturl.JoinPath(apiURL, "openapi.yaml")
		if err != nil {
			return fmt.Errorf("failed to construct OpenAPI URL: %w", err)
		}
		// #nosec G107 -- URL is validated above and path is a constant
		_, err = registryClient.Get(openapiURL)
		if err != nil && strings.Contains(fmt.Sprint(err), networking.ErrPrivateIpAddress) {
			return err
		}
	}

	// Update the configuration
	err = provider.UpdateConfig(func(c *Config) {
		c.RegistryApiUrl = apiURL
		c.RegistryUrl = ""       // Clear static registry URL when setting API URL
		c.LocalRegistryPath = "" // Clear local path when setting API URL
		c.AllowPrivateRegistryIp = allowPrivateRegistryIp
	})
	if err != nil {
		return fmt.Errorf("failed to update configuration: %w", err)
	}

	return nil
}

// unsetRegistry resets registry configuration to defaults using the provided provider
func unsetRegistry(provider Provider) error {
	err := provider.UpdateConfig(func(c *Config) {
		c.RegistryUrl = ""
		c.RegistryApiUrl = ""
		c.LocalRegistryPath = ""
		c.AllowPrivateRegistryIp = false
	})
	if err != nil {
		return fmt.Errorf("failed to update configuration: %w", err)
	}
	return nil
}

// getRegistryConfig returns current registry configuration using the provided provider
func getRegistryConfig(provider Provider) (url, localPath string, allowPrivateIP bool, registryType string) {
	cfg := provider.GetConfig()

	// Check API URL first (highest priority for live data)
	if cfg.RegistryApiUrl != "" {
		return cfg.RegistryApiUrl, "", cfg.AllowPrivateRegistryIp, RegistryTypeAPI
	}

	if cfg.RegistryUrl != "" {
		return cfg.RegistryUrl, "", cfg.AllowPrivateRegistryIp, RegistryTypeURL
	}

	if cfg.LocalRegistryPath != "" {
		return "", cfg.LocalRegistryPath, false, RegistryTypeFile
	}

	return "", "", false, "default"
}

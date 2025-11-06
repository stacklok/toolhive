package config

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/stacklok/toolhive/pkg/networking"
)

const (
	// RegistryTypeFile represents a local file registry
	RegistryTypeFile = "file"
	// RegistryTypeURL represents a remote URL registry
	RegistryTypeURL = "url"
)

// DetectRegistryType determines if input is a URL or file path and returns cleaned path
func DetectRegistryType(input string) (registryType string, cleanPath string) {
	// Check for explicit file:// protocol
	if strings.HasPrefix(input, "file://") {
		return RegistryTypeFile, strings.TrimPrefix(input, "file://")
	}

	// Check for HTTP/HTTPS URLs
	if networking.IsURL(input) {
		return RegistryTypeURL, input
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

// unsetRegistry resets registry configuration to defaults using the provided provider
func unsetRegistry(provider Provider) error {
	err := provider.UpdateConfig(func(c *Config) {
		c.RegistryUrl = ""
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

	if cfg.RegistryUrl != "" {
		return cfg.RegistryUrl, "", cfg.AllowPrivateRegistryIp, RegistryTypeURL
	}

	if cfg.LocalRegistryPath != "" {
		return "", cfg.LocalRegistryPath, false, RegistryTypeFile
	}

	return "", "", false, "default"
}

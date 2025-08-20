package config

import (
	"encoding/json"
	"fmt"
	"os"
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
	if strings.HasPrefix(input, "http://") || strings.HasPrefix(input, "https://") {
		return RegistryTypeURL, input
	}

	// Default: treat as file path
	return RegistryTypeFile, filepath.Clean(input)
}

// SetRegistryURL validates and sets a registry URL
func SetRegistryURL(registryURL string, allowPrivateRegistryIp bool) error {
	if allowPrivateRegistryIp {
		// we validate either https or http URLs
		if !strings.HasPrefix(registryURL, "http://") && !strings.HasPrefix(registryURL, "https://") {
			return fmt.Errorf("registry URL must start with http:// or https:// when allowing private IPs")
		}
	} else {
		// we just allow https
		if !strings.HasPrefix(registryURL, "https://") {
			return fmt.Errorf("registry URL must start with https:// when not allowing private IPs")
		}
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
	err := UpdateConfig(func(c *Config) {
		c.RegistryUrl = registryURL
		c.LocalRegistryPath = "" // Clear local path when setting URL
		c.AllowPrivateRegistryIp = allowPrivateRegistryIp
	})
	if err != nil {
		return fmt.Errorf("failed to update configuration: %w", err)
	}

	return nil
}

// SetRegistryFile validates and sets a local registry file
func SetRegistryFile(registryPath string) error {
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
	err = UpdateConfig(func(c *Config) {
		c.LocalRegistryPath = registryPath
		c.RegistryUrl = "" // Clear URL when setting local path
	})
	if err != nil {
		return fmt.Errorf("failed to update configuration: %w", err)
	}

	return nil
}

// UnsetRegistry resets registry configuration to defaults
func UnsetRegistry() error {
	err := UpdateConfig(func(c *Config) {
		c.RegistryUrl = ""
		c.LocalRegistryPath = ""
		c.AllowPrivateRegistryIp = false
	})
	if err != nil {
		return fmt.Errorf("failed to update configuration: %w", err)
	}
	return nil
}

// GetRegistryConfig returns current registry configuration
func GetRegistryConfig() (url, localPath string, allowPrivateIP bool, registryType string) {
	cfg := GetConfig()

	if cfg.RegistryUrl != "" {
		return cfg.RegistryUrl, "", cfg.AllowPrivateRegistryIp, RegistryTypeURL
	}

	if cfg.LocalRegistryPath != "" {
		return "", cfg.LocalRegistryPath, false, RegistryTypeFile
	}

	return "", "", false, "default"
}

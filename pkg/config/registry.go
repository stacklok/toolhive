package config

import (
	"encoding/json"
	"fmt"
	"net/http"
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
func DetectRegistryType(input string, allowPrivateIPs bool) (registryType string, cleanPath string) {
	// Check for explicit file:// protocol
	if strings.HasPrefix(input, "file://") {
		return RegistryTypeFile, strings.TrimPrefix(input, "file://")
	}

	// Check for HTTP/HTTPS URLs
	if networking.IsURL(input) {
		// If URL ends with .json, treat as static registry file
		if strings.HasSuffix(input, ".json") {
			return RegistryTypeURL, input
		}

		// For URLs without .json extension, probe to determine the type
		registryType := probeRegistryURL(input, allowPrivateIPs)
		return registryType, input
	}

	// Default: treat as file path
	return RegistryTypeFile, filepath.Clean(input)
}

// probeRegistryURL attempts to determine if a URL is a static JSON file or an API endpoint
// by checking if the MCP Registry API endpoint (/v0.1/servers) exists.
func probeRegistryURL(url string, allowPrivateIPs bool) string {
	// Create HTTP client for probing with user's private IP preference
	client, err := networking.NewHttpClientBuilder().WithPrivateIPs(allowPrivateIPs).Build()
	if err != nil {
		// If we can't create a client, default to static JSON
		return RegistryTypeURL
	}

	// Check if the MCP Registry API endpoint exists
	apiURL, err := neturl.JoinPath(url, "/v0.1/servers")
	if err == nil {
		resp, err := client.Head(apiURL)
		if err == nil {
			_ = resp.Body.Close()
			// If API endpoint returns 2xx or 401/403 (auth errors), it's an API
			// 404 means endpoint doesn't exist, 5xx means server error
			if (resp.StatusCode >= 200 && resp.StatusCode < 300) ||
				resp.StatusCode == http.StatusUnauthorized ||
				resp.StatusCode == http.StatusForbidden {
				return RegistryTypeAPI
			}
		}
	}

	// If no API endpoint found, check if it's valid registry JSON
	if isValidRegistryJSON(client, url) {
		return RegistryTypeURL
	}

	// Default to static JSON file (validation will catch errors later)
	return RegistryTypeURL
}

// isValidRegistryJSON checks if a URL returns valid ToolHive registry JSON
func isValidRegistryJSON(client *http.Client, url string) bool {
	resp, err := client.Get(url)
	if err != nil {
		return false
	}
	defer resp.Body.Close()

	// Try to parse as JSON with registry structure
	// We just check for basic registry fields to avoid pulling in the full types package
	var data map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return false
	}

	// Check if it has registry-like structure (servers or remoteServers fields)
	_, hasServers := data["servers"]
	_, hasRemoteServers := data["remoteServers"]
	return hasServers || hasRemoteServers
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
		c.RegistryApiUrl = ""    // Clear API URL when setting static URL
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
		c.RegistryUrl = ""    // Clear URL when setting local path
		c.RegistryApiUrl = "" // Clear API URL when setting local path
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

	// Validate that the URL is accessible if not allowing private IPs
	if !allowPrivateRegistryIp {
		registryClient, err := networking.NewHttpClientBuilder().Build()
		if err != nil {
			return fmt.Errorf("failed to create HTTP client: %w", err)
		}
		// Just check the base URL is accessible (don't require specific endpoints)
		_, err = registryClient.Head(apiURL)
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

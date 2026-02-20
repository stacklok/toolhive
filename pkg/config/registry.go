// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	neturl "net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	registrytypes "github.com/stacklok/toolhive-core/registry/types"
	"github.com/stacklok/toolhive/pkg/networking"
)

const (
	// RegistryTypeFile represents a local file registry
	RegistryTypeFile = "file"
	// RegistryTypeURL represents a remote URL registry
	RegistryTypeURL = "url"
	// RegistryTypeAPI represents an MCP Registry API endpoint
	RegistryTypeAPI = "api"
	// RegistryTypeDefault represents a built-in registry
	RegistryTypeDefault = "default"
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
// by checking if the MCP Registry API endpoint (/v0.1/servers) exists and returns valid API responses.
// Uses a 5-second timeout for connectivity check.
func probeRegistryURL(url string, allowPrivateIPs bool) string {
	// Create HTTP client for probing with user's private IP preference and 5-second timeout
	// If private IPs are allowed, also allow HTTP (for localhost testing)
	builder := networking.NewHttpClientBuilder().
		WithPrivateIPs(allowPrivateIPs).
		WithTimeout(5 * time.Second)
	if allowPrivateIPs {
		builder = builder.WithInsecureAllowHTTP(true)
	}
	client, err := builder.Build()
	if err != nil {
		// If we can't create a client, default to static JSON
		return RegistryTypeURL
	}

	// Check if the MCP Registry API endpoint exists by trying a lightweight GET request
	// Note: We use GET instead of HEAD because some API implementations don't support HEAD
	apiURL, err := neturl.JoinPath(url, "/v0.1/servers")
	if err == nil {
		// Add query parameters to minimize response size
		params := neturl.Values{}
		params.Add("limit", "1")
		params.Add("version", "latest")
		fullAPIURL := fmt.Sprintf("%s?%s", apiURL, params.Encode())

		resp, err := client.Get(fullAPIURL)
		if err == nil {
			defer func() {
				if err := resp.Body.Close(); err != nil {
					slog.Debug("failed to close response body", "error", err)
				}
			}()
			// If API endpoint returns 2xx or 401/403 (auth errors), it's an API
			// 404 means endpoint doesn't exist, 405 means method not supported, 5xx means server error
			if resp.StatusCode >= 200 && resp.StatusCode < 300 {
				// Verify the response looks like an API response
				if isValidAPIResponse(resp) {
					return RegistryTypeAPI
				}
			} else if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
				// Auth errors indicate an API endpoint (it exists but requires auth)
				return RegistryTypeAPI
			}
		}
	}

	// If no API endpoint found, check if it's valid registry JSON
	if err := isValidRegistryJSON(client, url); err == nil {
		return RegistryTypeURL
	}

	// Default to static JSON file (validation will catch errors later)
	return RegistryTypeURL
}

// isValidAPIResponse checks if an HTTP response contains a valid MCP Registry API response
// by verifying the JSON structure matches the expected API format (ServerListResponse).
func isValidAPIResponse(resp *http.Response) bool {
	// Check Content-Type header
	contentType := resp.Header.Get("Content-Type")
	if !strings.Contains(contentType, "application/json") {
		return false
	}

	// Try to parse as MCP Registry API response structure
	var data map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return false
	}

	// Check for API-specific structure (servers array and metadata object)
	servers, hasServers := data["servers"]
	metadata, hasMetadata := data["metadata"]

	// Valid API response should have both 'servers' (array) and 'metadata' (object)
	if !hasServers || !hasMetadata {
		return false
	}

	// Verify servers is an array
	if _, ok := servers.([]interface{}); !ok {
		return false
	}

	// Verify metadata is an object
	if _, ok := metadata.(map[string]interface{}); !ok {
		return false
	}

	return true
}

// isValidRegistryJSON checks if a URL returns valid ToolHive registry JSON
// by attempting to parse it into the actual Registry type
func isValidRegistryJSON(client *http.Client, url string) error {
	resp, err := client.Get(url)
	if err != nil {
		return classifyNetworkError(err)
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			slog.Debug("failed to close response body", "error", err)
		}
	}()

	// Parse into the actual Registry type for strong validation
	registry := &registrytypes.Registry{}
	if err := json.NewDecoder(resp.Body).Decode(registry); err != nil {
		return fmt.Errorf("%w: invalid JSON format: %v", ErrRegistryValidationFailed, err)
	}

	// Verify registry contains at least one server (in top-level or groups)
	if !registryHasServers(registry) {
		return fmt.Errorf("%w: registry contains no servers", ErrRegistryValidationFailed)
	}

	return nil
}

// classifyNetworkError wraps network errors with appropriate custom error types
func classifyNetworkError(err error) error {
	if err == nil {
		return nil
	}

	// Check for timeout errors
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return fmt.Errorf("%w: %v", ErrRegistryTimeout, err)
	}

	// Check for context deadline exceeded (another form of timeout)
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("%w: %v", ErrRegistryTimeout, err)
	}

	// Check for connection errors
	errStr := err.Error()
	if strings.Contains(errStr, "connection refused") ||
		strings.Contains(errStr, "no route to host") ||
		strings.Contains(errStr, "network is unreachable") ||
		strings.Contains(errStr, networking.ErrPrivateIpAddress) {
		return fmt.Errorf("%w: %v", ErrRegistryUnreachable, err)
	}

	// Check for DNS errors (name resolution failures)
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return fmt.Errorf("%w: %v", ErrRegistryUnreachable, err)
	}

	// Default: return original error
	return err
}

// setRegistryURL validates and sets a registry URL using the provided provider
// Validates connectivity with a 5-second timeout.
func setRegistryURL(provider Provider, registryURL string, allowPrivateRegistryIp bool) error {
	// Validate URL scheme
	_, err := validateURLScheme(registryURL, allowPrivateRegistryIp)
	if err != nil {
		return fmt.Errorf("invalid registry URL: %w", err)
	}

	// Build HTTP client with appropriate security settings and 5-second timeout
	builder := networking.NewHttpClientBuilder().
		WithPrivateIPs(allowPrivateRegistryIp).
		WithTimeout(5 * time.Second)
	if allowPrivateRegistryIp {
		builder = builder.WithInsecureAllowHTTP(true)
	}
	registryClient, err := builder.Build()
	if err != nil {
		return fmt.Errorf("failed to create HTTP client: %w", err)
	}

	// Check for private IP addresses if not allowed
	if !allowPrivateRegistryIp {
		_, err = registryClient.Get(registryURL)
		if err != nil && strings.Contains(fmt.Sprint(err), networking.ErrPrivateIpAddress) {
			return &RegistryError{
				Type: RegistryTypeURL,
				URL:  registryURL,
				Err:  classifyNetworkError(err),
			}
		}
	}

	// Validate that the URL returns valid ToolHive registry JSON
	if err := isValidRegistryJSON(registryClient, registryURL); err != nil {
		return &RegistryError{
			Type: RegistryTypeURL,
			URL:  registryURL,
			Err:  err,
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
		return &RegistryError{
			Type: RegistryTypeFile,
			URL:  registryPath,
			Err:  fmt.Errorf("%w: %v", ErrRegistryValidationFailed, err),
		}
	}

	// Validate registry structure
	if err := validateRegistryFileStructure(cleanPath); err != nil {
		return &RegistryError{
			Type: RegistryTypeFile,
			URL:  registryPath,
			Err:  fmt.Errorf("%w: %v", ErrRegistryValidationFailed, err),
		}
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

// registryHasServers checks if a registry contains at least one server
// (either in top-level servers/remote_servers or within groups)
func registryHasServers(registry *registrytypes.Registry) bool {
	// Check top-level servers
	if len(registry.Servers) > 0 || len(registry.RemoteServers) > 0 {
		return true
	}

	// Check servers within groups
	for _, group := range registry.Groups {
		if group != nil && (len(group.Servers) > 0 || len(group.RemoteServers) > 0) {
			return true
		}
	}

	return false
}

// validateRegistryFileStructure checks if a file contains valid ToolHive registry structure
// by parsing it into the actual Registry type
func validateRegistryFileStructure(path string) error {
	// Read file content
	// #nosec G304: File path is user-provided but validated by caller
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("failed to read file: %w", err)
	}

	// Parse into the actual Registry type for strong validation
	registry := &registrytypes.Registry{}
	if err := json.Unmarshal(data, registry); err != nil {
		return fmt.Errorf("invalid registry format: %w", err)
	}

	// Verify registry contains at least one server (in top-level or groups)
	if !registryHasServers(registry) {
		return fmt.Errorf("registry contains no servers (expected at least one server in 'servers', 'remote_servers', or 'groups')")
	}

	return nil
}

// setRegistryAPI validates and sets an MCP Registry API URL using the provided provider
// Validates connectivity with a 5-second timeout.
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

	// Validate that the URL is accessible with 5-second timeout
	if !allowPrivateRegistryIp {
		registryClient, err := networking.NewHttpClientBuilder().
			WithTimeout(5 * time.Second).
			Build()
		if err != nil {
			return fmt.Errorf("failed to create HTTP client: %w", err)
		}
		// Just check the base URL is accessible (don't require specific endpoints)
		_, err = registryClient.Head(apiURL)
		if err != nil {
			return &RegistryError{
				Type: RegistryTypeAPI,
				URL:  apiURL,
				Err:  classifyNetworkError(err),
			}
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

package config

import (
	"encoding/json"
	"errors"
	"fmt"
	neturl "net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/stacklok/toolhive/pkg/networking"
)

// Error message templates for consistent error formatting
const (
	errFileNotFound        = "file not found or not accessible: %w"
	errFileRead            = "failed to read file: %w"
	errInvalidJSON         = "invalid JSON format: %w"
	errInvalidURL          = "invalid URL format: %w"
	errInvalidURLScheme    = "URL must start with %s://"
	errJSONExtensionOnly   = "file must be a JSON file (*.json)"
	errAbsolutePathResolve = "failed to resolve absolute path: %w"
)

// validateFilePath validates that a file path exists and is accessible.
// It also cleans the file path using filepath.Clean.
// Returns the cleaned path and an error if the file doesn't exist or isn't accessible.
func validateFilePath(path string) (string, error) {
	cleanPath := filepath.Clean(path)

	if _, err := os.Stat(cleanPath); err != nil {
		return "", fmt.Errorf(errFileNotFound, err)
	}

	return cleanPath, nil
}

// validateFileExists checks if a file exists and is accessible without cleaning the path.
// This is useful when the path has already been cleaned.
func validateFileExists(path string) error {
	if _, err := os.Stat(path); err != nil {
		return fmt.Errorf(errFileNotFound, err)
	}
	return nil
}

// readFile reads the contents of a file and returns the data.
// This is a wrapper around os.ReadFile with consistent error messaging.
func readFile(path string) ([]byte, error) {
	// #nosec G304: File path is user-provided but should be validated by caller
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf(errFileRead, err)
	}
	return data, nil
}

// validateJSONFile validates that a file contains valid JSON.
// It checks the file extension and attempts to parse the content.
func validateJSONFile(path string) error {
	// Check file extension
	if !strings.HasSuffix(strings.ToLower(path), ".json") {
		return errors.New(errJSONExtensionOnly)
	}

	// Read and validate JSON content
	data, err := readFile(path)
	if err != nil {
		return err
	}

	// Basic JSON validation - unmarshal into generic map
	var jsonData map[string]interface{}
	if err := json.Unmarshal(data, &jsonData); err != nil {
		return fmt.Errorf(errInvalidJSON, err)
	}

	return nil
}

// validateURLScheme validates that a URL has the correct scheme (http or https).
// If allowInsecure is false, only https is allowed.
// If allowInsecure is true, both http and https are allowed.
func validateURLScheme(rawURL string, allowInsecure bool) (*neturl.URL, error) {
	parsedURL, err := neturl.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf(errInvalidURL, err)
	}

	if allowInsecure {
		// Allow both http and https
		if parsedURL.Scheme != networking.HttpScheme && parsedURL.Scheme != networking.HttpsScheme {
			return nil, fmt.Errorf("URL must start with http:// or https://")
		}
	} else {
		// Only allow https
		if parsedURL.Scheme != networking.HttpsScheme {
			return nil, fmt.Errorf(errInvalidURLScheme, networking.HttpsScheme)
		}
	}

	return parsedURL, nil
}

// makeAbsolutePath converts a relative path to an absolute path.
func makeAbsolutePath(path string) (string, error) {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf(errAbsolutePathResolve, err)
	}
	return absPath, nil
}

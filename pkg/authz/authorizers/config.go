// Package authorizers provides the authorization framework and abstractions for ToolHive.
// It defines interfaces for authorization decisions and configuration handling.
package authorizers

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"sigs.k8s.io/yaml"
)

// ConfigType represents the type of authorization configuration.
type ConfigType string

// Config represents the authorization configuration.
// This struct contains the common fields (version/type) needed to identify
// which authorizer factory to use. The full raw configuration is preserved
// so that each authorizer implementation can parse it with domain-specific
// knowledge (e.g., Cedar configs have a "cedar" field at the top level).
type Config struct {
	// Version is the version of the configuration format.
	Version string `json:"version" yaml:"version"`

	// Type is the type of authorization configuration (e.g., "cedarv1").
	Type ConfigType `json:"type" yaml:"type"`

	// rawConfig stores the original raw configuration bytes for re-parsing
	// by the authorizer factory with domain-specific knowledge.
	rawConfig json.RawMessage
}

// UnmarshalJSON implements custom JSON unmarshaling that preserves the raw config
// while extracting the version and type fields.
func (c *Config) UnmarshalJSON(data []byte) error {
	// First, extract just version and type
	var header struct {
		Version string     `json:"version"`
		Type    ConfigType `json:"type"`
	}
	if err := json.Unmarshal(data, &header); err != nil {
		return err
	}

	c.Version = header.Version
	c.Type = header.Type
	c.rawConfig = data

	return nil
}

// MarshalJSON implements custom JSON marshaling.
// If we have the original raw config, use that to preserve all fields.
// Otherwise, just marshal version and type.
func (c *Config) MarshalJSON() ([]byte, error) {
	if len(c.rawConfig) > 0 {
		return c.rawConfig, nil
	}

	// Fallback: just marshal version and type
	type alias struct {
		Version string     `json:"version"`
		Type    ConfigType `json:"type"`
	}
	return json.Marshal(&alias{
		Version: c.Version,
		Type:    c.Type,
	})
}

// RawConfig returns the raw configuration bytes for the authorizer factory
// to parse with domain-specific knowledge.
func (c *Config) RawConfig() json.RawMessage {
	return c.rawConfig
}

// LoadConfig loads the authorization configuration from a file.
// It supports both JSON and YAML formats, detected by file extension.
func LoadConfig(path string) (*Config, error) {
	// Validate and clean the path to prevent directory traversal attacks
	cleanPath := filepath.Clean(path)
	if strings.Contains(cleanPath, "..") {
		return nil, fmt.Errorf("path contains directory traversal elements: %s", path)
	}

	// Read the file
	data, err := os.ReadFile(cleanPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read authorization configuration file: %w", err)
	}

	// Determine the file format based on extension
	var config Config
	ext := strings.ToLower(filepath.Ext(cleanPath))

	// Parse the file based on its format
	switch ext {
	case ".yaml", ".yml":
		// Parse YAML - first convert to JSON for consistent handling
		jsonData, err := yaml.YAMLToJSON(data)
		if err != nil {
			return nil, fmt.Errorf("failed to parse YAML authorization configuration file: %w", err)
		}
		if err := json.Unmarshal(jsonData, &config); err != nil {
			return nil, fmt.Errorf("failed to parse authorization configuration: %w", err)
		}
	case ".json", "":
		// Parse JSON (default if no extension)
		if err := json.Unmarshal(data, &config); err != nil {
			return nil, fmt.Errorf("failed to parse JSON authorization configuration file: %w", err)
		}
	default:
		return nil, fmt.Errorf("unsupported file format: %s (supported formats: .json, .yaml, .yml)", ext)
	}

	// Validate the configuration
	if err := config.Validate(); err != nil {
		return nil, err
	}

	return &config, nil
}

// Validate validates the authorization configuration.
func (c *Config) Validate() error {
	// Check if the version is provided
	if c.Version == "" {
		return fmt.Errorf("version is required")
	}

	// Check if the type is provided
	if c.Type == "" {
		return fmt.Errorf("type is required")
	}

	// Get the factory for this config type
	factory := GetFactory(string(c.Type))
	if factory == nil {
		return fmt.Errorf("unsupported configuration type: %s (registered types: %v)",
			c.Type, RegisteredTypes())
	}

	// Check if we have raw config to validate
	if len(c.rawConfig) == 0 {
		return fmt.Errorf("configuration data is required for type %s", c.Type)
	}

	// Delegate validation to the authorizer factory, passing the full raw config
	if err := factory.ValidateConfig(c.rawConfig); err != nil {
		return fmt.Errorf("invalid %s configuration: %w", c.Type, err)
	}

	return nil
}

// NewConfig creates a new Config from a full configuration structure.
// The fullConfig parameter should be the complete configuration including
// version, type, and authorizer-specific fields (e.g., "cedar" field for Cedar configs).
// This maintains backwards compatibility with the v1.0 configuration schema.
func NewConfig(fullConfig interface{}) (*Config, error) {
	rawConfig, err := json.Marshal(fullConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal configuration: %w", err)
	}

	// Parse the raw config to extract version and type
	var config Config
	if err := json.Unmarshal(rawConfig, &config); err != nil {
		return nil, fmt.Errorf("failed to parse configuration: %w", err)
	}

	return &config, nil
}

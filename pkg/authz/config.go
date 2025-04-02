// Package authz provides authorization utilities using Cedar policies.
package authz

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
)

// ConfigType represents the type of authorization configuration.
type ConfigType string

const (
	// ConfigTypeCedarV1 represents the Cedar v1 authorization configuration.
	ConfigTypeCedarV1 ConfigType = "cedarv1"
)

// Config represents the authorization configuration.
type Config struct {
	// Version is the version of the configuration format.
	Version string `json:"version"`

	// Type is the type of authorization configuration.
	Type ConfigType `json:"type"`

	// Cedar is the Cedar-specific configuration.
	// This is only used when Type is ConfigTypeCedarV1.
	Cedar *CedarConfig `json:"cedar,omitempty"`
}

// CedarConfig represents the Cedar-specific authorization configuration.
type CedarConfig struct {
	// Policies is a list of Cedar policy strings
	Policies []string `json:"policies"`

	// EntitiesJSON is the JSON string representing Cedar entities
	EntitiesJSON string `json:"entities_json"`
}

// LoadConfig loads the authorization configuration from a file.
//
//nolint:gosec // This is intentionally loading a file specified by the user
func LoadConfig(path string) (*Config, error) {
	// Read the file
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read authorization configuration file: %w", err)
	}

	// Parse the JSON
	var config Config
	if err := json.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("failed to parse authorization configuration file: %w", err)
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

	// Validate based on the type
	switch c.Type {
	case ConfigTypeCedarV1:
		// Check if the Cedar configuration is provided
		if c.Cedar == nil {
			return fmt.Errorf("cedar configuration is required for type %s", c.Type)
		}

		// Check if policies are provided
		if len(c.Cedar.Policies) == 0 {
			return fmt.Errorf("at least one policy is required for type %s", c.Type)
		}
	default:
		return fmt.Errorf("unsupported configuration type: %s", c.Type)
	}

	return nil
}

// CreateMiddleware creates an HTTP middleware from the configuration.
func (c *Config) CreateMiddleware() (func(http.Handler) http.Handler, error) {
	// Create the appropriate middleware based on the configuration type
	switch c.Type {
	case ConfigTypeCedarV1:
		// Create the Cedar authorizer
		authorizer, err := NewCedarAuthorizer(CedarAuthorizerConfig{
			Policies:     c.Cedar.Policies,
			EntitiesJSON: c.Cedar.EntitiesJSON,
		})
		if err != nil {
			return nil, fmt.Errorf("failed to create Cedar authorizer: %w", err)
		}

		// Return the Cedar middleware
		return authorizer.Middleware, nil
	default:
		return nil, fmt.Errorf("unsupported configuration type: %s", c.Type)
	}
}

// GetMiddlewareFromFile loads the authorization configuration from a file and creates an HTTP middleware.
func GetMiddlewareFromFile(path string) (func(http.Handler) http.Handler, error) {
	// Load the configuration
	config, err := LoadConfig(path)
	if err != nil {
		return nil, err
	}

	// Create the middleware
	return config.CreateMiddleware()
}

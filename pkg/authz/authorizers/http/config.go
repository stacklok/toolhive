// Package http provides authorization using HTTP-based Policy Decision Points (PDPs).
package http

import (
	"encoding/json"
	"fmt"
)

// ConfigType is the configuration type identifier for HTTP-based PDP authorization.
const ConfigType = "httpv1"

// Config represents the complete authorization configuration file structure
// for HTTP-based PDP authorization. This includes the common version/type fields
// plus the PDP-specific "pdp" field.
type Config struct {
	Version string         `json:"version"`
	Type    string         `json:"type"`
	Options *ConfigOptions `json:"pdp"`
}

// ConfigOptions represents the HTTP PDP authorization configuration options.
type ConfigOptions struct {
	// HTTP contains the HTTP connection configuration.
	HTTP *ConnectionConfig `json:"http,omitempty" yaml:"http,omitempty"`

	// Context configures what context information is included in the PORC.
	// By default, no MCP context is included in the PORC.
	Context *ContextConfig `json:"context,omitempty" yaml:"context,omitempty"`
}

// ContextConfig configures what context information is included in the PORC.
// All options default to false, meaning no MCP context is included by default.
type ContextConfig struct {
	// IncludeArgs enables inclusion of tool/prompt arguments in context.mcp.args.
	// Default is false.
	IncludeArgs bool `json:"include_args,omitempty" yaml:"include_args,omitempty"`

	// IncludeOperation enables inclusion of MCP operation metadata in context.mcp:
	// feature, operation, and resource_id fields.
	// Default is false.
	IncludeOperation bool `json:"include_operation,omitempty" yaml:"include_operation,omitempty"`
}

// ConnectionConfig contains configuration for the HTTP connection to the PDP.
type ConnectionConfig struct {
	// URL is the base URL of the PDP server (e.g., "http://localhost:9000").
	URL string `json:"url" yaml:"url"`

	// Timeout is the HTTP request timeout in seconds. Default is 30.
	Timeout int `json:"timeout,omitempty" yaml:"timeout,omitempty"`

	// InsecureSkipVerify skips TLS certificate verification. Use only for testing.
	InsecureSkipVerify bool `json:"insecure_skip_verify,omitempty" yaml:"insecure_skip_verify,omitempty"`
}

// parseConfig parses the raw JSON configuration into a Config struct.
func parseConfig(rawConfig json.RawMessage) (*Config, error) {
	var config Config
	if err := json.Unmarshal(rawConfig, &config); err != nil {
		return nil, fmt.Errorf("failed to parse HTTP PDP configuration: %w", err)
	}
	return &config, nil
}

// Validate validates the HTTP PDP configuration options.
func (c *ConfigOptions) Validate() error {
	if c == nil {
		return fmt.Errorf("pdp configuration is required (missing 'pdp' field)")
	}

	// Validate HTTP configuration
	if c.HTTP == nil {
		return fmt.Errorf("http configuration is required")
	}
	if c.HTTP.URL == "" {
		return fmt.Errorf("http.url is required")
	}

	return nil
}

// GetContextConfig returns the context configuration, or a default empty config if nil.
func (c *ConfigOptions) GetContextConfig() ContextConfig {
	if c.Context == nil {
		return ContextConfig{}
	}
	return *c.Context
}

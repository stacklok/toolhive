package audit

import (
	"encoding/json"
	"fmt"

	"github.com/stacklok/toolhive/pkg/transport/types"
)

// Middleware type constant
const (
	MiddlewareType = "audit"
)

// MiddlewareParams represents the parameters for audit middleware
type MiddlewareParams struct {
	ConfigPath string  `json:"config_path,omitempty"` // Kept for backwards compatibility
	ConfigData *Config `json:"config_data,omitempty"` // New field for config contents
	Component  string  `json:"component,omitempty"`
}

// Middleware wraps audit middleware functionality
type Middleware struct {
	middleware types.MiddlewareFunction
}

// Handler returns the middleware function used by the proxy.
func (m *Middleware) Handler() types.MiddlewareFunction {
	return m.middleware
}

// Close cleans up any resources used by the middleware.
func (*Middleware) Close() error {
	// Audit middleware doesn't need cleanup
	return nil
}

// CreateMiddleware factory function for audit middleware
func CreateMiddleware(config *types.MiddlewareConfig, runner types.MiddlewareRunner) error {

	var params MiddlewareParams
	if err := json.Unmarshal(config.Parameters, &params); err != nil {
		return fmt.Errorf("failed to unmarshal audit middleware parameters: %w", err)
	}

	var auditConfig *Config
	var err error

	if params.ConfigData != nil {
		// Use provided config data (preferred method)
		auditConfig = params.ConfigData
	} else if params.ConfigPath != "" {
		// Load config from file (backwards compatibility)
		auditConfig, err = LoadFromFile(params.ConfigPath)
		if err != nil {
			return fmt.Errorf("failed to load audit configuration: %w", err)
		}
	} else {
		// Use default config
		auditConfig = DefaultConfig()
	}

	// Set component name if provided
	if params.Component != "" {
		auditConfig.Component = params.Component
	}

	middleware, err := auditConfig.CreateMiddleware()
	if err != nil {
		return fmt.Errorf("failed to create audit middleware: %w", err)
	}

	auditMw := &Middleware{middleware: middleware}
	runner.AddMiddleware(auditMw)
	return nil
}

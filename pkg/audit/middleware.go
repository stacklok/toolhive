// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

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
	// Transport information for dynamic transport detection
	TransportType string `json:"transport_type,omitempty"` // e.g., "sse", "streamable-http"
}

// Middleware wraps audit middleware functionality
type Middleware struct {
	middleware types.MiddlewareFunction
	auditor    *Auditor
}

// Handler returns the middleware function used by the proxy.
func (m *Middleware) Handler() types.MiddlewareFunction {
	return m.middleware
}

// Close cleans up any resources used by the middleware.
func (m *Middleware) Close() error {
	if m.auditor != nil {
		return m.auditor.Close()
	}
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

	// Set component name if provided and config doesn't already have one
	if params.Component != "" && auditConfig.Component == "" {
		auditConfig.Component = params.Component
	}

	// Validate and apply defaults to the config
	if err := auditConfig.Validate(); err != nil {
		return fmt.Errorf("invalid audit configuration: %w", err)
	}

	// Create the auditor directly so we can store a reference for cleanup
	auditor, err := NewAuditorWithTransport(auditConfig, params.TransportType)
	if err != nil {
		return fmt.Errorf("failed to create audit middleware: %w", err)
	}

	auditMw := &Middleware{
		middleware: auditor.Middleware,
		auditor:    auditor,
	}
	runner.AddMiddleware(config.Type, auditMw)
	return nil
}

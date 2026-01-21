// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package authz provides authorization utilities for MCP servers.
// It supports a pluggable authorizer architecture where different authorization
// backends (e.g., Cedar, OPA) can be registered and used based on configuration.
package authz

import (
	"fmt"
	"net/http"

	"github.com/stacklok/toolhive/pkg/authz/authorizers"
	"github.com/stacklok/toolhive/pkg/transport/types"
)

// ConfigType is an alias for authorizers.ConfigType for backward compatibility.
type ConfigType = authorizers.ConfigType

// Config is an alias for authorizers.Config for backward compatibility.
type Config = authorizers.Config

// LoadConfig is an alias for authorizers.LoadConfig for backward compatibility.
var LoadConfig = authorizers.LoadConfig

// NewConfig is an alias for authorizers.NewConfig for backward compatibility.
var NewConfig = authorizers.NewConfig

// CreateMiddlewareFromConfig creates an HTTP middleware from the configuration.
func CreateMiddlewareFromConfig(c *Config, serverName string) (types.MiddlewareFunction, error) {
	// Get the factory for this config type
	factory := authorizers.GetFactory(string(c.Type))
	if factory == nil {
		return nil, fmt.Errorf("unsupported configuration type: %s", c.Type)
	}

	// Create the authorizer using the factory, passing the full raw config
	authz, err := factory.CreateAuthorizer(c.RawConfig(), serverName)
	if err != nil {
		return nil, fmt.Errorf("failed to create %s authorizer: %w", c.Type, err)
	}

	// Return the middleware
	return func(handler http.Handler) http.Handler { return Middleware(authz, handler) }, nil
}

// GetMiddlewareFromFile loads the authorization configuration from a file and creates an HTTP middleware.
func GetMiddlewareFromFile(serverName, path string) (func(http.Handler) http.Handler, error) {
	// Load the configuration
	config, err := LoadConfig(path)
	if err != nil {
		return nil, err
	}

	// Create the middleware
	return CreateMiddlewareFromConfig(config, serverName)
}

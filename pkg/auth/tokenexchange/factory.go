// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package tokenexchange

import (
	"encoding/json"
	"fmt"

	"github.com/stacklok/toolhive/pkg/transport/types"
)

// Middleware wraps token exchange middleware functionality
type Middleware struct {
	middleware types.MiddlewareFunction
}

// Handler returns the middleware function used by the proxy.
func (m *Middleware) Handler() types.MiddlewareFunction {
	return m.middleware
}

// Close cleans up any resources used by the middleware.
func (*Middleware) Close() error {
	// Token exchange middleware doesn't need cleanup
	return nil
}

// CreateMiddleware factory function for token exchange middleware
func CreateMiddleware(config *types.MiddlewareConfig, runner types.MiddlewareRunner) error {
	var params MiddlewareParams
	if err := json.Unmarshal(config.Parameters, &params); err != nil {
		return fmt.Errorf("failed to unmarshal token exchange middleware parameters: %w", err)
	}

	// Token exchange config is required when this middleware type is specified
	if params.TokenExchangeConfig == nil {
		return fmt.Errorf("token exchange configuration is required but not provided")
	}

	// Validate configuration
	if err := validateTokenExchangeConfig(params.TokenExchangeConfig); err != nil {
		return fmt.Errorf("invalid token exchange configuration: %w", err)
	}

	middleware, err := createTokenExchangeMiddleware(*params.TokenExchangeConfig, nil, defaultEnvGetter)
	if err != nil {
		return fmt.Errorf("invalid token exchange middleware config: %w", err)
	}

	tokenExchangeMw := &Middleware{
		middleware: middleware,
	}

	// Add middleware to runner
	runner.AddMiddleware(config.Type, tokenExchangeMw)

	return nil
}

// validateTokenExchangeConfig validates the token exchange configuration
func validateTokenExchangeConfig(config *Config) error {
	if config.HeaderStrategy == HeaderStrategyCustom && config.ExternalTokenHeaderName == "" {
		return fmt.Errorf("external_token_header_name must be specified when header_strategy is '%s'", HeaderStrategyCustom)
	}

	if config.HeaderStrategy != "" &&
		config.HeaderStrategy != HeaderStrategyReplace &&
		config.HeaderStrategy != HeaderStrategyCustom {
		return fmt.Errorf("invalid header_strategy: %s (valid values: '%s', '%s')",
			config.HeaderStrategy, HeaderStrategyReplace, HeaderStrategyCustom)
	}

	return nil
}

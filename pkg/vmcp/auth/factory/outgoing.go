// Copyright 2025 Stacklok, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Package factory provides factory functions for creating vMCP authentication components.
package factory

import (
	"context"
	"fmt"
	"strings"

	"github.com/stacklok/toolhive/pkg/vmcp/auth"
	"github.com/stacklok/toolhive/pkg/vmcp/auth/strategies"
	"github.com/stacklok/toolhive/pkg/vmcp/config"
)

// NewOutgoingAuthenticator creates an OutgoingAuthenticator from configuration.
// It registers all strategies found in the configuration (both default and backend-specific).
//
// The factory scans the configuration to identify all unique strategy types used,
// instantiates each strategy once, and registers them with the authenticator.
// This allows multiple backends to share the same strategy instance.
//
// Parameters:
//   - ctx: Context for any initialization that requires it
//   - cfg: The outgoing authentication configuration
//
// Returns:
//   - auth.OutgoingAuthenticator: Configured authenticator with registered strategies
//   - error: Any error during strategy initialization or registration
func NewOutgoingAuthenticator(_ context.Context, cfg *config.OutgoingAuthConfig) (auth.OutgoingAuthenticator, error) {
	authenticator := auth.NewDefaultOutgoingAuthenticator()

	// Handle nil config gracefully - return empty authenticator
	if cfg == nil {
		return authenticator, nil
	}

	// Validate configuration structure
	if cfg.Default != nil && strings.TrimSpace(cfg.Default.Type) == "" {
		return nil, fmt.Errorf("default auth strategy type cannot be empty")
	}

	for backendID, backendCfg := range cfg.Backends {
		if backendCfg != nil && strings.TrimSpace(backendCfg.Type) == "" {
			return nil, fmt.Errorf("backend %q has empty auth strategy type", backendID)
		}
	}

	// Collect all unique strategy types from the configuration
	strategyTypes := make(map[string]struct{})

	// Add default strategy type if present
	if cfg.Default != nil && cfg.Default.Type != "" {
		strategyTypes[cfg.Default.Type] = struct{}{}
	}

	// Add all backend strategy types
	for _, backendCfg := range cfg.Backends {
		if backendCfg != nil && backendCfg.Type != "" {
			strategyTypes[backendCfg.Type] = struct{}{}
		}
	}

	// Instantiate and register each unique strategy type
	for strategyType := range strategyTypes {
		strategy, err := CreateStrategy(strategyType)
		if err != nil {
			return nil, fmt.Errorf("failed to create strategy %q: %w", strategyType, err)
		}

		if err := authenticator.RegisterStrategy(strategyType, strategy); err != nil {
			return nil, fmt.Errorf("failed to register strategy %q: %w", strategyType, err)
		}
	}

	return authenticator, nil
}

// CreateStrategy instantiates a strategy based on its type.
//
// Each strategy instance is stateless (except token_exchange which has internal caching).
// This function validates that the strategy type is not empty and returns an appropriate
// error for unknown strategy types.
//
// Parameters:
//   - strategyType: The type identifier of the strategy to create
//
// Returns:
//   - auth.Strategy: The instantiated strategy
//   - error: Any error during strategy creation or validation
func CreateStrategy(strategyType string) (auth.Strategy, error) {
	// Validate strategy type is not empty
	if strings.TrimSpace(strategyType) == "" {
		return nil, fmt.Errorf("strategy type cannot be empty")
	}

	switch strategyType {
	case "pass_through":
		return strategies.NewPassThroughStrategy(), nil
	case "apikey", "header_injection":
		// Support both "apikey" (legacy) and "header_injection" (new name)
		return strategies.NewHeaderInjectionStrategy(), nil
	case "token_exchange":
		return strategies.NewTokenExchangeStrategy(), nil
	default:
		return nil, fmt.Errorf("unknown strategy type: %s", strategyType)
	}
}

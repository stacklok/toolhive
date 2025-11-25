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

	"github.com/stacklok/toolhive/pkg/env"
	"github.com/stacklok/toolhive/pkg/vmcp/auth"
	"github.com/stacklok/toolhive/pkg/vmcp/auth/strategies"
	"github.com/stacklok/toolhive/pkg/vmcp/config"
)

// NewOutgoingAuthRegistry creates an OutgoingAuthRegistry from configuration.
// It registers all strategies found in the configuration (both default and backend-specific).
//
// The factory ALWAYS registers the "unauthenticated" strategy as a default fallback,
// ensuring that backends without explicit authentication configuration can function.
// This makes empty/nil configuration safe: the registry will have at least one
// usable strategy.
//
// Strategy Registration:
//   - "unauthenticated" is always registered (default fallback)
//   - Additional strategies are registered based on configuration
//   - Each strategy is instantiated once and shared across backends
//   - Strategies are stateless (except token_exchange which has internal caching)
//
// Parameters:
//   - ctx: Context for any initialization that requires it
//   - cfg: The outgoing authentication configuration (may be nil)
//   - envReader: Environment variable reader for dependency injection
//
// Returns:
//   - auth.OutgoingAuthRegistry: Configured registry with registered strategies
//   - error: Any error during strategy initialization or registration
func NewOutgoingAuthRegistry(
	_ context.Context,
	cfg *config.OutgoingAuthConfig,
	envReader env.Reader,
) (auth.OutgoingAuthRegistry, error) {
	registry := auth.NewDefaultOutgoingAuthRegistry()

	// ALWAYS register the unauthenticated strategy as the default fallback.
	if err := registerUnauthenticatedStrategy(registry); err != nil {
		return nil, err
	}

	// Handle nil config gracefully - return registry with unauthenticated strategy
	if cfg == nil {
		return registry, nil
	}

	// Validate configuration structure
	if err := validateConfig(cfg); err != nil {
		return nil, err
	}

	// Collect and register all unique strategy types from configuration
	strategyTypes := collectStrategyTypes(cfg)

	// In "discovered" mode, backends' auth is determined at runtime from their
	// ExternalAuthConfigRef. We don't know what strategies will be needed upfront,
	// so we must register ALL possible strategies to ensure they're available.
	if cfg.Source == "discovered" || cfg.Source == "mixed" {
		// Register all known strategy types for discovered/mixed mode
		strategyTypes[strategies.StrategyTypeHeaderInjection] = struct{}{}
		strategyTypes[strategies.StrategyTypeTokenExchange] = struct{}{}
	}

	if err := registerStrategies(registry, strategyTypes, envReader); err != nil {
		return nil, err
	}

	return registry, nil
}

// registerUnauthenticatedStrategy registers the default unauthenticated strategy.
func registerUnauthenticatedStrategy(registry auth.OutgoingAuthRegistry) error {
	unauthStrategy := strategies.NewUnauthenticatedStrategy()
	if err := registry.RegisterStrategy(strategies.StrategyTypeUnauthenticated, unauthStrategy); err != nil {
		return fmt.Errorf("failed to register default unauthenticated strategy: %w", err)
	}
	return nil
}

// validateConfig validates the configuration structure.
func validateConfig(cfg *config.OutgoingAuthConfig) error {
	if cfg.Default != nil && strings.TrimSpace(cfg.Default.Type) == "" {
		return fmt.Errorf("default auth strategy type cannot be empty")
	}

	for backendID, backendCfg := range cfg.Backends {
		if backendCfg != nil && strings.TrimSpace(backendCfg.Type) == "" {
			return fmt.Errorf("backend %q has empty auth strategy type", backendID)
		}
	}

	return nil
}

// collectStrategyTypes collects all unique strategy types from configuration.
func collectStrategyTypes(cfg *config.OutgoingAuthConfig) map[string]struct{} {
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

	return strategyTypes
}

// registerStrategies instantiates and registers each unique strategy type.
func registerStrategies(registry auth.OutgoingAuthRegistry, strategyTypes map[string]struct{}, envReader env.Reader) error {
	for strategyType := range strategyTypes {
		// Skip "unauthenticated" - already registered
		if strategyType == strategies.StrategyTypeUnauthenticated {
			continue
		}

		strategy, err := createStrategy(strategyType, envReader)
		if err != nil {
			return fmt.Errorf("failed to create strategy %q: %w", strategyType, err)
		}

		if err := registry.RegisterStrategy(strategyType, strategy); err != nil {
			return fmt.Errorf("failed to register strategy %q: %w", strategyType, err)
		}
	}

	return nil
}

// createStrategy instantiates a strategy based on its type.
//
// Each strategy instance is stateless (except token_exchange which has internal caching).
// This function validates that the strategy type is not empty and returns an appropriate
// error for unknown strategy types.
//
// Parameters:
//   - strategyType: The type identifier of the strategy to create
//   - envReader: Environment variable reader for dependency injection
//
// Returns:
//   - auth.Strategy: The instantiated strategy
//   - error: Any error during strategy creation or validation
func createStrategy(strategyType string, envReader env.Reader) (auth.Strategy, error) {
	// Validate strategy type is not empty
	if strings.TrimSpace(strategyType) == "" {
		return nil, fmt.Errorf("strategy type cannot be empty")
	}

	switch strategyType {
	case strategies.StrategyTypeHeaderInjection:
		return strategies.NewHeaderInjectionStrategy(), nil
	case strategies.StrategyTypeTokenExchange:
		return strategies.NewTokenExchangeStrategy(envReader), nil
	case strategies.StrategyTypeUnauthenticated:
		return strategies.NewUnauthenticatedStrategy(), nil
	default:
		return nil, fmt.Errorf("unknown strategy type: %s", strategyType)
	}
}

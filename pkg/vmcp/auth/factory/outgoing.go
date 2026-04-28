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

	"github.com/stacklok/toolhive-core/env"
	"github.com/stacklok/toolhive/pkg/vmcp/auth"
	"github.com/stacklok/toolhive/pkg/vmcp/auth/strategies"
	authtypes "github.com/stacklok/toolhive/pkg/vmcp/auth/types"
)

// NewOutgoingAuthRegistry creates an OutgoingAuthRegistry with all available strategies.
//
// All strategies are registered upfront. Most are stateless; token_exchange and
// aws_sts maintain an internal per-config cache initialized on first use. This
// simplifies the factory and eliminates on-demand strategy registration.
//
// Registered Strategies:
//   - "unauthenticated": Default fallback for backends without auth
//   - "header_injection": Custom HTTP header injection
//   - "token_exchange": RFC-8693 OAuth 2.0 token exchange
//   - "upstream_inject": Per-upstream token injection from stored credentials
//   - "aws_sts": AWS STS AssumeRoleWithWebIdentity + SigV4 request signing
//
// Parameters:
//   - ctx: Context for any initialization that requires it
//   - envReader: Environment variable reader for dependency injection
//
// Returns:
//   - auth.OutgoingAuthRegistry: Registry with all strategies registered
//   - error: Any error during strategy initialization or registration
func NewOutgoingAuthRegistry(
	_ context.Context,
	envReader env.Reader,
) (auth.OutgoingAuthRegistry, error) {
	registry := auth.NewDefaultOutgoingAuthRegistry()

	// Register all strategies upfront.
	if err := registry.RegisterStrategy(
		authtypes.StrategyTypeUnauthenticated,
		strategies.NewUnauthenticatedStrategy(),
	); err != nil {
		return nil, err
	}
	if err := registry.RegisterStrategy(
		authtypes.StrategyTypeHeaderInjection,
		strategies.NewHeaderInjectionStrategy(),
	); err != nil {
		return nil, err
	}
	if err := registry.RegisterStrategy(
		authtypes.StrategyTypeTokenExchange,
		strategies.NewTokenExchangeStrategy(envReader),
	); err != nil {
		return nil, err
	}
	if err := registry.RegisterStrategy(
		authtypes.StrategyTypeUpstreamInject,
		strategies.NewUpstreamInjectStrategy(),
	); err != nil {
		return nil, err
	}
	if err := registry.RegisterStrategy(
		authtypes.StrategyTypeAwsSts,
		strategies.NewAwsStsStrategy(),
	); err != nil {
		return nil, err
	}

	return registry, nil
}

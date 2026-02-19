// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package http

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/authz/authorizers"
)

func init() {
	// Register the HTTP PDP authorizer factory with the authorizers registry.
	authorizers.Register(ConfigType, &Factory{})
}

// Factory implements the authorizers.AuthorizerFactory interface for HTTP PDPs.
type Factory struct{}

// ValidateConfig validates the HTTP PDP configuration.
func (*Factory) ValidateConfig(rawConfig json.RawMessage) error {
	config, err := parseConfig(rawConfig)
	if err != nil {
		return err
	}

	if config.Options == nil {
		return fmt.Errorf("pdp configuration is required (missing 'pdp' field)")
	}

	return config.Options.Validate()
}

// CreateAuthorizer creates an HTTP PDP Authorizer from the configuration.
func (*Factory) CreateAuthorizer(rawConfig json.RawMessage, serverName string) (authorizers.Authorizer, error) {
	config, err := parseConfig(rawConfig)
	if err != nil {
		return nil, err
	}

	if config.Options == nil {
		return nil, fmt.Errorf("pdp configuration is required (missing 'pdp' field)")
	}

	// Validate configuration before creating the authorizer
	if err := config.Options.Validate(); err != nil {
		return nil, err
	}

	return NewAuthorizer(*config.Options, serverName)
}

// pdp defines the interface for Policy Decision Point implementations.
type pdp interface {
	Authorize(ctx context.Context, porc PORC, probe bool) (bool, error)
	Close() error
}

// Authorizer implements the authorizers.Authorizer interface using an HTTP PDP.
type Authorizer struct {
	config      ConfigOptions
	pdp         pdp
	porcBuilder *PORCBuilder
}

// NewAuthorizer creates a new HTTP PDP authorizer from the provided configuration.
// Note: This function validates the config as a defensive measure, even though the
// factory also validates. This protects against direct calls to NewAuthorizer that
// bypass the factory.
func NewAuthorizer(config ConfigOptions, serverName string) (*Authorizer, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}

	slog.Debug("creating new HTTP PDP authorizer", "config", config)
	p, err := NewClient(config.HTTP)
	if err != nil {
		return nil, fmt.Errorf("failed to create HTTP client: %w", err)
	}

	// Create the claim mapper based on configuration
	claimMapper, err := config.CreateClaimMapper()
	if err != nil {
		return nil, fmt.Errorf("failed to create claim mapper: %w", err)
	}

	return &Authorizer{
		config:      config,
		pdp:         p,
		porcBuilder: NewPORCBuilder(serverName, config.GetContextConfig(), claimMapper),
	}, nil
}

// AuthorizeWithJWTClaims implements the authorizers.Authorizer interface.
// It extracts JWT claims from the context, builds a PORC expression,
// and delegates the authorization decision to the configured PDP.
func (a *Authorizer) AuthorizeWithJWTClaims(
	ctx context.Context,
	feature authorizers.MCPFeature,
	operation authorizers.MCPOperation,
	resourceID string,
	arguments map[string]interface{},
) (bool, error) {
	// Extract Identity from the context
	identity, ok := auth.IdentityFromContext(ctx)
	if !ok {
		return false, fmt.Errorf("missing principal: no identity in context")
	}

	// Build PORC expression using identity claims
	porc := a.porcBuilder.Build(feature, operation, resourceID, identity.Claims, arguments)

	// Log the authorization request
	slog.Debug("HTTP PDP authorization check",
		"operation", porc["operation"], "resource", porc["resource"])

	// Delegate to PDP (not in probe mode for actual authorization)
	allowed, err := a.pdp.Authorize(ctx, porc, false)
	if err != nil {
		slog.Debug("HTTP PDP authorization check failed", "error", err)
		return false, fmt.Errorf("authorization failed: %w", err)
	}

	slog.Debug("HTTP PDP authorization result", "allowed", allowed)

	return allowed, nil
}

// Close releases resources used by the authorizer.
func (a *Authorizer) Close() error {
	if a.pdp != nil {
		return a.pdp.Close()
	}
	return nil
}

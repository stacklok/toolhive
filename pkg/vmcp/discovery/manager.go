// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package discovery provides lazy per-user capability discovery for vMCP servers.
//
// This package implements per-request capability discovery with user-specific
// authentication context, enabling truly multi-tenant operation where different
// users may see different capabilities based on their permissions.
package discovery

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/aggregator"
)

//go:generate mockgen -destination=mocks/mock_manager.go -package=mocks -source=manager.go Manager

var (
	// ErrAggregatorNil is returned when aggregator is nil.
	ErrAggregatorNil = errors.New("aggregator cannot be nil")
	// ErrDiscoveryFailed is returned when capability discovery fails.
	ErrDiscoveryFailed = errors.New("capability discovery failed")
	// ErrNoIdentity is returned when user identity is not found in context.
	ErrNoIdentity = errors.New("user identity not found in context")
)

// Manager performs capability discovery with user context.
type Manager interface {
	// Discover performs capability aggregation for the given backends with user context.
	Discover(ctx context.Context, backends []vmcp.Backend) (*aggregator.AggregatedCapabilities, error)
	// Stop gracefully stops the manager and cleans up resources.
	Stop()
}

// DefaultManager is the default implementation of Manager.
type DefaultManager struct {
	aggregator aggregator.Aggregator
}

// NewManager creates a new discovery manager with the given aggregator.
func NewManager(agg aggregator.Aggregator) (Manager, error) {
	if agg == nil {
		return nil, ErrAggregatorNil
	}

	return &DefaultManager{
		aggregator: agg,
	}, nil
}

// Discover performs capability aggregation for the given backends.
//
// Results are computed fresh on each call — no caching is performed. New MCP
// sessions are infrequent and aggregation latency is negligible compared to
// LLM round-trips, so caching adds complexity without meaningful benefit.
//
// The context must contain an authenticated user identity (set by auth middleware).
// Returns ErrNoIdentity if user identity is not found in context.
func (m *DefaultManager) Discover(ctx context.Context, backends []vmcp.Backend) (*aggregator.AggregatedCapabilities, error) {
	// Validate user identity is present (set by auth middleware)
	// This ensures discovery happens with proper user authentication context
	identity, ok := auth.IdentityFromContext(ctx)
	if !ok {
		return nil, fmt.Errorf("%w: ensure auth middleware runs before discovery middleware", ErrNoIdentity)
	}

	slog.Debug("performing capability discovery", "user", identity.Subject, "backends", len(backends))

	caps, err := m.aggregator.AggregateCapabilities(ctx, backends)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrDiscoveryFailed, err)
	}

	return caps, nil
}

// Stop is a no-op. Retained for interface compatibility.
func (*DefaultManager) Stop() {}

// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package registry

import (
	"context"
	"sync"
)

// UpdateRegistryConfig contains the configuration for a registry update,
// used by both CLI and API callers for policy evaluation. At most one of URL,
// APIURL, or LocalPath is set.
type UpdateRegistryConfig struct {
	// URL is the remote registry file URL being set.
	URL string
	// APIURL is the MCP Registry API endpoint URL being set.
	APIURL string
	// LocalPath is the local registry file path being set.
	LocalPath string
	// AllowPrivateIP indicates whether private IP addresses are permitted.
	AllowPrivateIP bool
	// HasAuth indicates whether authentication is being configured.
	HasAuth bool
}

// DeleteRegistryConfig contains the configuration for a registry deletion,
// used by both CLI and API callers for policy evaluation.
type DeleteRegistryConfig struct {
	// Name is the registry name being removed (e.g. "default").
	Name string
}

// PolicyGate is called before registry mutation operations to allow external
// policy enforcement. Downstream implementations should embed NoopPolicyGate
// to remain forward-compatible when new methods are added.
//
// Error messages returned from the check methods are surfaced directly to the
// end user (HTTP response body or CLI stderr). The policy gate implementer is
// responsible for producing clear, actionable messages.
type PolicyGate interface {
	// CheckUpdateRegistry is called before a registry is created or updated.
	// Return a non-nil error to block the operation.
	CheckUpdateRegistry(ctx context.Context, cfg *UpdateRegistryConfig) error

	// CheckDeleteRegistry is called before a registry is deleted or unset.
	// Return a non-nil error to block the operation.
	CheckDeleteRegistry(ctx context.Context, cfg *DeleteRegistryConfig) error
}

// NoopPolicyGate is a policy gate that allows all registry mutations.
// Downstream implementations should embed this struct to remain
// forward-compatible when new methods are added to the PolicyGate interface.
type NoopPolicyGate struct{}

// CheckUpdateRegistry implements PolicyGate by allowing all updates.
func (NoopPolicyGate) CheckUpdateRegistry(_ context.Context, _ *UpdateRegistryConfig) error {
	return nil
}

// CheckDeleteRegistry implements PolicyGate by allowing all deletions.
func (NoopPolicyGate) CheckDeleteRegistry(_ context.Context, _ *DeleteRegistryConfig) error {
	return nil
}

// allowAllGate is the default policy gate used when no gate has been registered.
type allowAllGate struct {
	NoopPolicyGate
}

var (
	regGateMu sync.RWMutex
	regGate   PolicyGate = allowAllGate{}
)

// RegisterPolicyGate replaces the active registry policy gate with g. It is
// safe to call from multiple goroutines, though it is intended to be called
// once at program startup.
func RegisterPolicyGate(g PolicyGate) {
	regGateMu.Lock()
	defer regGateMu.Unlock()
	regGate = g
}

// ActivePolicyGate returns the currently registered registry policy gate.
func ActivePolicyGate() PolicyGate {
	regGateMu.RLock()
	defer regGateMu.RUnlock()
	return regGate
}

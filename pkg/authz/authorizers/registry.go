// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package authorizers

import (
	"encoding/json"
	"fmt"
	"sync"
)

// AuthorizerFactory is the interface that authorizer implementations must satisfy
// to register themselves with the authorizers registry. Each authorizer type
// (e.g., Cedar, OPA) implements this interface to provide validation and
// instantiation of authorizers from their specific configuration format.
type AuthorizerFactory interface {
	// ConfigKey returns the JSON key under which the backend-specific
	// configuration is nested in the full authorizer config blob.
	// For example, Cedar returns "cedar" and HTTP PDP returns "pdp".
	ConfigKey() string

	// ValidateConfig validates the authorizer-specific configuration.
	// The rawConfig is the JSON-encoded authorizer configuration.
	ValidateConfig(rawConfig json.RawMessage) error

	// CreateAuthorizer creates an Authorizer instance from the configuration.
	// The rawConfig is the JSON-encoded authorizer configuration.
	CreateAuthorizer(rawConfig json.RawMessage, serverName string) (Authorizer, error)
}

// registry holds the registered authorizer factories, keyed by config type.
var (
	registryMu sync.RWMutex
	registry   = make(map[string]AuthorizerFactory)
)

// reservedConfigKeys are envelope keys that a factory's ConfigKey() must not
// shadow. The MCPAuthzConfig controller assembles the runtime config as a
// JSON object with reserved top-level keys ("version", "type") plus the
// factory's ConfigKey(); a factory returning one of these would silently
// overwrite the envelope metadata in the map literal. Empty string is
// reserved too — a factory must declare a non-empty key.
var reservedConfigKeys = map[string]struct{}{
	"":        {},
	"version": {},
	"type":    {},
}

// Register registers an AuthorizerFactory for the given config type.
// This is typically called from an init() function in the authorizer package.
// It panics if a factory is already registered for the given type, or if the
// factory's ConfigKey() returns a reserved value ("", "version", "type").
func Register(configType string, factory AuthorizerFactory) {
	registryMu.Lock()
	defer registryMu.Unlock()

	if _, exists := registry[configType]; exists {
		panic(fmt.Sprintf("authorizer factory already registered for type: %s", configType))
	}
	if _, reserved := reservedConfigKeys[factory.ConfigKey()]; reserved {
		panic(fmt.Sprintf(
			"authorizer factory for type %q uses reserved ConfigKey() %q; "+
				"reserved keys are: \"\", \"version\", \"type\"",
			configType, factory.ConfigKey(),
		))
	}
	registry[configType] = factory
}

// GetFactory returns the AuthorizerFactory for the given config type.
// Returns nil if no factory is registered for the type.
func GetFactory(configType string) AuthorizerFactory {
	registryMu.RLock()
	defer registryMu.RUnlock()

	return registry[configType]
}

// IsRegistered returns true if a factory is registered for the given config type.
func IsRegistered(configType string) bool {
	registryMu.RLock()
	defer registryMu.RUnlock()

	_, exists := registry[configType]
	return exists
}

// RegisteredTypes returns a list of all registered config types.
func RegisteredTypes() []string {
	registryMu.RLock()
	defer registryMu.RUnlock()

	types := make([]string, 0, len(registry))
	for t := range registry {
		types = append(types, t)
	}
	return types
}

// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package registry

import (
	"fmt"

	"github.com/stacklok/toolhive/pkg/config"
)

// Configurator provides high-level operations for registry configuration management.
// It encapsulates registry type detection, validation, and persistence.
//
// Note: Callers are responsible for resetting the registry provider cache after configuration
// changes by calling registry.ResetDefaultStore(). This avoids circular dependencies between
// the config and registry packages.
//
//go:generate mockgen -destination=mocks/mock_service.go -package=mocks -source=service.go Configurator
type Configurator interface {
	// SetRegistryFromInput auto-detects the registry type (URL/API/File) and configures it.
	// Returns the detected registry type and any error.
	// Callers should call registry.ResetDefaultStore() after this method succeeds.
	SetRegistryFromInput(input string, allowPrivateIP bool) (registryType string, err error)

	// UnsetRegistry resets the registry configuration to defaults (built-in registry).
	// Returns any error that occurred during the operation.
	// Callers should call registry.ResetDefaultStore() after this method succeeds.
	UnsetRegistry() error

	// GetRegistryInfo returns information about the currently configured registry.
	// Returns the registry type (api/url/file/default) and the source (URL or path).
	GetRegistryInfo() (registryType, source string)
}

// DefaultConfigurator is the default implementation of Configurator.
type DefaultConfigurator struct {
	provider config.Provider
}

// NewConfigurator creates a new registry configurator with the default provider.
func NewConfigurator() Configurator {
	return &DefaultConfigurator{
		provider: config.NewDefaultProvider(),
	}
}

// NewConfiguratorWithProvider creates a new registry configurator with a custom provider.
// This is useful for testing.
func NewConfiguratorWithProvider(provider config.Provider) Configurator {
	return &DefaultConfigurator{
		provider: provider,
	}
}

// SetRegistryFromInput auto-detects the registry type and configures it.
// The input is stored as the "default" registry entry.
func (s *DefaultConfigurator) SetRegistryFromInput(input string, allowPrivateIP bool) (string, error) {
	// Determine the registry source type from the input.
	sourceType := detectSourceType(input)

	source := config.RegistrySource{
		Name:           "default",
		Type:           sourceType,
		Location:       input,
		AllowPrivateIP: allowPrivateIP,
	}

	if err := s.provider.AddRegistry(source); err != nil {
		return string(sourceType), fmt.Errorf("failed to add registry: %w", err)
	}

	// Set as the default registry
	if err := s.provider.SetDefaultRegistry("default"); err != nil {
		return string(sourceType), fmt.Errorf("failed to set default registry: %w", err)
	}

	// Reset the config singleton to clear cached configuration
	config.ResetSingleton()

	return string(sourceType), nil
}

// UnsetRegistry resets the registry configuration to defaults.
func (s *DefaultConfigurator) UnsetRegistry() error {
	cfg := s.provider.GetConfig()

	if len(cfg.Registries) == 0 && cfg.DefaultRegistry == "" {
		// Already using default registry, nothing to do
		return nil
	}

	// Remove the "default" registry entry
	if err := s.provider.RemoveRegistry("default"); err != nil {
		return fmt.Errorf("failed to remove registry configuration: %w", err)
	}

	// Clear the default registry setting
	if err := s.provider.SetDefaultRegistry(""); err != nil {
		return fmt.Errorf("failed to clear default registry: %w", err)
	}

	// Reset the config singleton to clear cached configuration
	config.ResetSingleton()

	return nil
}

// GetRegistryInfo returns information about the currently configured registry.
func (s *DefaultConfigurator) GetRegistryInfo() (string, string) {
	cfg := s.provider.GetConfig()

	// Look for the default registry entry first
	defaultName := cfg.EffectiveDefaultRegistry()
	src := cfg.FindRegistry(defaultName)
	if src != nil {
		return string(src.Type), src.Location
	}

	// If there are any registries, return info about the first one
	if len(cfg.Registries) > 0 {
		first := cfg.Registries[0]
		return string(first.Type), first.Location
	}

	return config.RegistryTypeDefault, ""
}

// detectSourceType determines the registry source type from the input string.
func detectSourceType(input string) config.RegistrySourceType {
	// Check for explicit file:// protocol
	if len(input) > 7 && input[:7] == "file://" {
		return config.RegistrySourceTypeFile
	}

	// HTTP/HTTPS URLs
	if (len(input) > 7 && input[:7] == "http://") || (len(input) > 8 && input[:8] == "https://") {
		// URLs ending with .json are treated as static registry files
		if len(input) > 5 && input[len(input)-5:] == ".json" {
			return config.RegistrySourceTypeURL
		}
		// Other URLs are treated as API endpoints
		return config.RegistrySourceTypeAPI
	}

	// Default: local file path
	return config.RegistrySourceTypeFile
}

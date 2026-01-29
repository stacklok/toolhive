// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
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
// changes by calling registry.ResetDefaultProvider(). This avoids circular dependencies between
// the config and registry packages.
//
//go:generate mockgen -destination=mocks/mock_service.go -package=mocks -source=service.go Configurator
type Configurator interface {
	// SetRegistryFromInput auto-detects the registry type (URL/API/File) and configures it.
	// Returns the detected registry type and any error.
	// Callers should call registry.ResetDefaultProvider() after this method succeeds.
	SetRegistryFromInput(input string, allowPrivateIP bool) (registryType string, err error)

	// UnsetRegistry resets the registry configuration to defaults (built-in registry).
	// Returns any error that occurred during the operation.
	// Callers should call registry.ResetDefaultProvider() after this method succeeds.
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
func (s *DefaultConfigurator) SetRegistryFromInput(input string, allowPrivateIP bool) (string, error) {
	// Auto-detect the registry type
	registryType, cleanPath := config.DetectRegistryType(input, allowPrivateIP)

	var err error

	switch registryType {
	case config.RegistryTypeURL:
		err = s.provider.SetRegistryURL(cleanPath, allowPrivateIP)
		if err != nil {
			return "", fmt.Errorf("failed to set remote registry: %w", err)
		}

	case config.RegistryTypeAPI:
		err = s.provider.SetRegistryAPI(cleanPath, allowPrivateIP)
		if err != nil {
			return "", fmt.Errorf("failed to set registry API: %w", err)
		}

	case config.RegistryTypeFile:
		err = s.provider.SetRegistryFile(cleanPath)
		if err != nil {
			return "", fmt.Errorf("failed to set local registry file: %w", err)
		}

	default:
		return "", fmt.Errorf("unsupported registry type: %s", registryType)
	}

	// Reset the config singleton to clear cached configuration
	// Note: Callers are responsible for resetting the registry provider cache
	config.ResetSingleton()

	return registryType, nil
}

// UnsetRegistry resets the registry configuration to defaults.
func (s *DefaultConfigurator) UnsetRegistry() error {
	// Get current config before unsetting
	_, _, _, registryType := s.provider.GetRegistryConfig()

	if registryType == config.RegistryTypeDefault {
		// Already using default registry, nothing to do
		return nil
	}

	err := s.provider.UnsetRegistry()
	if err != nil {
		return fmt.Errorf("failed to reset registry configuration: %w", err)
	}

	// Reset the config singleton to clear cached configuration
	// Note: Callers are responsible for resetting the registry provider cache
	config.ResetSingleton()

	return nil
}

// GetRegistryInfo returns information about the currently configured registry.
func (s *DefaultConfigurator) GetRegistryInfo() (string, string) {
	url, localPath, _, registryType := s.provider.GetRegistryConfig()

	switch registryType {
	case config.RegistryTypeAPI:
		return config.RegistryTypeAPI, url
	case config.RegistryTypeURL:
		return config.RegistryTypeURL, url
	case config.RegistryTypeFile:
		return config.RegistryTypeFile, localPath
	default:
		return config.RegistryTypeDefault, ""
	}
}

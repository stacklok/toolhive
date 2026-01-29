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
	// Returns the detected registry type, a user-friendly message, and any error.
	// Callers should call registry.ResetDefaultProvider() after this method succeeds.
	SetRegistryFromInput(input string, allowPrivateIP bool) (registryType, message string, err error)

	// UnsetRegistry resets the registry configuration to defaults (built-in registry).
	// Returns a user-friendly message and any error.
	// Callers should call registry.ResetDefaultProvider() after this method succeeds.
	UnsetRegistry() (message string, err error)

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
func (s *DefaultConfigurator) SetRegistryFromInput(input string, allowPrivateIP bool) (string, string, error) {
	// Auto-detect the registry type
	registryType, cleanPath := config.DetectRegistryType(input, allowPrivateIP)

	var err error
	var message string

	switch registryType {
	case config.RegistryTypeURL:
		err = s.provider.SetRegistryURL(cleanPath, allowPrivateIP)
		if err != nil {
			return "", "", fmt.Errorf("failed to set remote registry: %w", err)
		}
		message = fmt.Sprintf("Successfully set a remote registry file: %s", cleanPath)

	case config.RegistryTypeAPI:
		err = s.provider.SetRegistryAPI(cleanPath, allowPrivateIP)
		if err != nil {
			return "", "", fmt.Errorf("failed to set registry API: %w", err)
		}
		message = fmt.Sprintf("Successfully set registry API endpoint: %s", cleanPath)

	case config.RegistryTypeFile:
		err = s.provider.SetRegistryFile(cleanPath)
		if err != nil {
			return "", "", fmt.Errorf("failed to set local registry file: %w", err)
		}
		message = fmt.Sprintf("Successfully set local registry file: %s", cleanPath)

	default:
		return "", "", fmt.Errorf("unsupported registry type: %s", registryType)
	}

	// Reset the config singleton to clear cached configuration
	// Note: Callers are responsible for resetting the registry provider cache
	config.ResetSingleton()

	return registryType, message, nil
}

// UnsetRegistry resets the registry configuration to defaults.
func (s *DefaultConfigurator) UnsetRegistry() (string, error) {
	// Get current config before unsetting (for informational message)
	url, localPath, _, registryType := s.provider.GetRegistryConfig()

	if registryType == config.RegistryTypeDefault {
		return "No custom registry is currently configured.", nil
	}

	err := s.provider.UnsetRegistry()
	if err != nil {
		return "", fmt.Errorf("failed to reset registry configuration: %w", err)
	}

	// Reset the config singleton to clear cached configuration
	// Note: Callers are responsible for resetting the registry provider cache
	config.ResetSingleton()

	// Build informational message
	var message string
	if url != "" {
		message = fmt.Sprintf("Successfully removed registry URL: %s\n", url)
	} else if localPath != "" {
		message = fmt.Sprintf("Successfully removed local registry file: %s\n", localPath)
	} else {
		message = "Successfully removed registry configuration\n"
	}
	message += "Will use built-in registry."

	return message, nil
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

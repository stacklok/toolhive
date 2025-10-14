// Package keyring provides a composite keyring provider that supports multiple backends.
// It supports macOS Keychain, Windows Credential Manager, and Linux D-Bus Secret Service,
// with keyctl as a fallback on Linux systems.
package keyring

import (
	"fmt"
	"runtime"

	"github.com/stacklok/toolhive/pkg/logger"
)

const linuxOS = "linux"

type compositeProvider struct {
	providers []Provider
	active    Provider
}

// NewCompositeProvider creates a new composite keyring provider that combines multiple backends.
// It uses zalando/go-keyring as the primary provider and keyctl as a fallback on Linux.
//
//nolint:staticcheck
func NewCompositeProvider() Provider {
	var providers []Provider

	// Add zalando/go-keyring as primary provider
	// Handles macOS, Windows, and Linux D-Bus natively
	zkProvider := NewZalandoKeyringProvider()
	providers = append(providers, zkProvider)

	// Add keyctl provider as fallback ONLY on Linux
	if runtime.GOOS == linuxOS {
		if keyctl, err := NewKeyctlProvider(); err == nil {
			providers = append(providers, keyctl)
		}
	}

	return &compositeProvider{
		providers: providers,
	}
}

func (c *compositeProvider) getActiveProvider() Provider {
	if c.active != nil && c.active.IsAvailable() {
		return c.active
	}

	for _, provider := range c.providers {
		if provider.IsAvailable() {
			if c.active == nil || c.active.Name() != provider.Name() {
				// Log provider selection if logger is available
				// Use fmt.Printf as fallback since logger might not be initialized in tests
				c.logProviderSelection(provider.Name())
			}
			c.active = provider
			return provider
		}
	}

	return nil
}

// logProviderSelection safely logs the provider selection
func (*compositeProvider) logProviderSelection(providerName string) {
	// Try to use the logger, but don't panic if it's not available
	defer func() {
		if r := recover(); r != nil {
			// Logger not available, use fallback
			fmt.Printf("Using keyring provider: %s\n", providerName)
		}
	}()

	logger.Info(fmt.Sprintf("Using keyring provider: %s", providerName))
}

func (c *compositeProvider) Set(service, key, value string) error {
	provider := c.getActiveProvider()
	if provider == nil {
		return fmt.Errorf("no keyring provider available")
	}
	return provider.Set(service, key, value)
}

func (c *compositeProvider) Get(service, key string) (string, error) {
	provider := c.getActiveProvider()
	if provider == nil {
		return "", fmt.Errorf("no keyring provider available")
	}
	return provider.Get(service, key)
}

func (c *compositeProvider) Delete(service, key string) error {
	provider := c.getActiveProvider()
	if provider == nil {
		return fmt.Errorf("no keyring provider available")
	}
	return provider.Delete(service, key)
}

func (c *compositeProvider) DeleteAll(service string) error {
	provider := c.getActiveProvider()
	if provider == nil {
		return fmt.Errorf("no keyring provider available")
	}
	return provider.DeleteAll(service)
}

func (c *compositeProvider) IsAvailable() bool {
	return c.getActiveProvider() != nil
}

func (c *compositeProvider) Name() string {
	if provider := c.getActiveProvider(); provider != nil {
		return provider.Name()
	}
	return "None Available"
}

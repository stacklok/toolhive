// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"

	"github.com/stacklok/toolhive/pkg/networking"
)

const (
	// RegistryTypeFile represents a local file registry
	RegistryTypeFile = "file"
	// RegistryTypeURL represents a remote URL registry
	RegistryTypeURL = "url"
	// RegistryTypeAPI represents an MCP Registry API endpoint
	RegistryTypeAPI = "api"
	// RegistryTypeDefault represents a built-in registry
	RegistryTypeDefault = "default"
)

// addRegistry adds or replaces a registry source in the config.
func addRegistry(provider Provider, source RegistrySource) error {
	return provider.UpdateConfig(func(c *Config) {
		// Remove existing registry with same name
		filtered := make([]RegistrySource, 0, len(c.Registries))
		for _, r := range c.Registries {
			if r.Name != source.Name {
				filtered = append(filtered, r)
			}
		}
		c.Registries = append(filtered, source)
	})
}

// removeRegistry removes a registry source from the config by name.
func removeRegistry(provider Provider, name string) error {
	return provider.UpdateConfig(func(c *Config) {
		filtered := make([]RegistrySource, 0, len(c.Registries))
		for _, r := range c.Registries {
			if r.Name != name {
				filtered = append(filtered, r)
			}
		}
		c.Registries = filtered
		// Clear default if the removed registry was the default
		if c.DefaultRegistry == name {
			c.DefaultRegistry = ""
		}
	})
}

// setDefaultRegistry sets the default registry name.
func setDefaultRegistry(provider Provider, name string) error {
	return provider.UpdateConfig(func(c *Config) {
		c.DefaultRegistry = name
	})
}

// classifyNetworkError wraps network errors with appropriate custom error types
func classifyNetworkError(err error) error {
	if err == nil {
		return nil
	}

	// Check for timeout errors
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return fmt.Errorf("%w: %v", ErrRegistryTimeout, err)
	}

	// Check for context deadline exceeded (another form of timeout)
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("%w: %v", ErrRegistryTimeout, err)
	}

	// Check for connection errors
	errStr := err.Error()
	if strings.Contains(errStr, "connection refused") ||
		strings.Contains(errStr, "no route to host") ||
		strings.Contains(errStr, "network is unreachable") ||
		strings.Contains(errStr, networking.ErrPrivateIpAddress) {
		return fmt.Errorf("%w: %v", ErrRegistryUnreachable, err)
	}

	// Check for DNS errors (name resolution failures)
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return fmt.Errorf("%w: %v", ErrRegistryUnreachable, err)
	}

	// Default: return original error
	return err
}

// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"fmt"
	"strings"
)

// BuildAuthFileSecretPrefix is the prefix used for storing build auth file content in secrets
// #nosec G101 -- This is not a credential, just a prefix for secret names
const BuildAuthFileSecretPrefix = "BUILD_AUTH_FILE_"

// SupportedAuthFiles maps file type names to their target paths in the container
var SupportedAuthFiles = map[string]string{
	"npmrc":  "/root/.npmrc",
	"netrc":  "/root/.netrc",
	"yarnrc": "/root/.yarnrc",
}

// ValidateBuildAuthFileName checks if the file name is supported
func ValidateBuildAuthFileName(name string) error {
	if _, ok := SupportedAuthFiles[name]; !ok {
		supported := make([]string, 0, len(SupportedAuthFiles))
		for k := range SupportedAuthFiles {
			supported = append(supported, k)
		}
		return fmt.Errorf("unsupported auth file type %q; supported types: %s", name, strings.Join(supported, ", "))
	}
	return nil
}

// BuildAuthFileSecretName returns the secret name for a given auth file type
func BuildAuthFileSecretName(fileType string) string {
	return BuildAuthFileSecretPrefix + fileType
}

// markBuildAuthFileConfigured marks an auth file type as configured in the config.
// The actual content is stored in the secrets provider, not in the config.
func markBuildAuthFileConfigured(p Provider, name string) error {
	if err := ValidateBuildAuthFileName(name); err != nil {
		return err
	}

	return p.UpdateConfig(func(c *Config) {
		if c.BuildAuthFiles == nil {
			c.BuildAuthFiles = make(map[string]string)
		}
		// Store only a marker - actual content is in secrets
		c.BuildAuthFiles[name] = "secret:" + BuildAuthFileSecretName(name)
	})
}

// isBuildAuthFileConfigured checks if an auth file type is configured
func isBuildAuthFileConfigured(p Provider, name string) bool {
	config := p.GetConfig()
	if config.BuildAuthFiles == nil {
		return false
	}
	_, exists := config.BuildAuthFiles[name]
	return exists
}

// getConfiguredBuildAuthFiles returns a list of configured auth file types.
// Note: This only returns which files are configured, not their content.
// Use the secrets provider to retrieve actual content.
func getConfiguredBuildAuthFiles(p Provider) []string {
	config := p.GetConfig()
	if config.BuildAuthFiles == nil {
		return nil
	}
	result := make([]string, 0, len(config.BuildAuthFiles))
	for k := range config.BuildAuthFiles {
		result = append(result, k)
	}
	return result
}

// unsetBuildAuthFile removes an auth file configuration marker.
// Note: This only removes the config marker. The caller should also delete
// the corresponding secret from the secrets provider.
func unsetBuildAuthFile(p Provider, name string) error {
	return p.UpdateConfig(func(c *Config) {
		if c.BuildAuthFiles != nil {
			delete(c.BuildAuthFiles, name)
		}
	})
}

// unsetAllBuildAuthFiles removes all auth file configuration markers.
// Note: This only removes the config markers. The caller should also delete
// the corresponding secrets from the secrets provider.
func unsetAllBuildAuthFiles(p Provider) error {
	return p.UpdateConfig(func(c *Config) {
		c.BuildAuthFiles = nil
	})
}

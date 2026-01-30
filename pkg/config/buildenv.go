// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"fmt"
	"regexp"
	"strings"
)

// Build environment validation constants
const (
	errInvalidEnvKeyFormat  = "invalid environment variable name: %s (must match pattern %s)"
	errReservedEnvKey       = "environment variable name %s is reserved and cannot be overridden"
	errInvalidEnvValueChars = "environment variable value contains potentially dangerous characters"
)

// envKeyPattern matches valid environment variable names.
// Must start with uppercase letter, followed by uppercase letters, numbers, or underscores.
var envKeyPattern = regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)

// reservedEnvKeys lists environment variables that cannot be overridden for security reasons.
var reservedEnvKeys = map[string]bool{
	"PATH":            true,
	"HOME":            true,
	"USER":            true,
	"SHELL":           true,
	"PWD":             true,
	"HOSTNAME":        true,
	"TERM":            true,
	"LANG":            true,
	"LC_ALL":          true,
	"LD_PRELOAD":      true,
	"LD_LIBRARY_PATH": true,
}

// ValidateBuildEnvKey validates that an environment variable key follows the required pattern
// and is not a reserved variable.
func ValidateBuildEnvKey(key string) error {
	if !envKeyPattern.MatchString(key) {
		return fmt.Errorf(errInvalidEnvKeyFormat, key, "^[A-Z][A-Z0-9_]*$")
	}

	if reservedEnvKeys[key] {
		return fmt.Errorf(errReservedEnvKey, key)
	}

	return nil
}

// ValidateBuildEnvValue validates that an environment variable value does not contain
// potentially dangerous characters that could enable shell injection in Dockerfiles.
func ValidateBuildEnvValue(value string) error {
	// Check for shell metacharacters that could enable injection
	dangerousPatterns := []string{
		"`",  // Command substitution
		"$(", // Command substitution
		"${", // Variable expansion (could be used for injection)
		"\\", // Escape sequences
		"\n", // Newlines could break Dockerfile syntax
		"\r", // Carriage returns
		"\"", // Double quotes could break ENV syntax
		";",  // Command separator
		"&&", // Command chaining
		"||", // Command chaining
		"|",  // Pipe
		">",  // Redirection
		"<",  // Redirection
	}

	for _, pattern := range dangerousPatterns {
		if strings.Contains(value, pattern) {
			return fmt.Errorf("%s: contains '%s'", errInvalidEnvValueChars, pattern)
		}
	}

	return nil
}

// ValidateBuildEnvEntry validates both the key and value of a build environment variable.
func ValidateBuildEnvEntry(key, value string) error {
	if err := ValidateBuildEnvKey(key); err != nil {
		return err
	}
	return ValidateBuildEnvValue(value)
}

// checkBuildEnvKeyConflict checks if a key is already configured in another source.
func checkBuildEnvKeyConflict(p Provider, key string) error {
	config := p.GetConfig()

	// Check literal values
	if config.BuildEnv != nil {
		if _, exists := config.BuildEnv[key]; exists {
			return fmt.Errorf("key %s already configured as literal value; unset it first with 'thv config unset-build-env %s'", key, key)
		}
	}

	// Check secret references
	if config.BuildEnvFromSecrets != nil {
		if _, exists := config.BuildEnvFromSecrets[key]; exists {
			return fmt.Errorf("key %s already configured from secret; unset it first", key)
		}
	}

	// Check shell references
	for _, k := range config.BuildEnvFromShell {
		if k == key {
			return fmt.Errorf("key %s already configured from shell; unset it first", key)
		}
	}

	return nil
}

// setBuildEnv is a helper function that validates and sets a build environment variable.
func setBuildEnv(p Provider, key, value string) error {
	if err := ValidateBuildEnvEntry(key, value); err != nil {
		return err
	}

	// Check for conflicts with other sources
	if err := checkBuildEnvKeyConflict(p, key); err != nil {
		return err
	}

	return p.UpdateConfig(func(c *Config) {
		if c.BuildEnv == nil {
			c.BuildEnv = make(map[string]string)
		}
		c.BuildEnv[key] = value
	})
}

// getBuildEnv is a helper function that retrieves a build environment variable.
func getBuildEnv(p Provider, key string) (value string, exists bool) {
	config := p.GetConfig()
	if config.BuildEnv == nil {
		return "", false
	}
	value, exists = config.BuildEnv[key]
	return value, exists
}

// getAllBuildEnv is a helper function that retrieves all build environment variables.
func getAllBuildEnv(p Provider) map[string]string {
	config := p.GetConfig()
	if config.BuildEnv == nil {
		return make(map[string]string)
	}
	// Return a copy to prevent external modifications
	result := make(map[string]string, len(config.BuildEnv))
	for k, v := range config.BuildEnv {
		result[k] = v
	}
	return result
}

// unsetBuildEnv is a helper function that removes a specific build environment variable.
func unsetBuildEnv(p Provider, key string) error {
	return p.UpdateConfig(func(c *Config) {
		if c.BuildEnv != nil {
			delete(c.BuildEnv, key)
		}
	})
}

// unsetAllBuildEnv is a helper function that removes all build environment variables.
func unsetAllBuildEnv(p Provider) error {
	return p.UpdateConfig(func(c *Config) {
		c.BuildEnv = nil
	})
}

// setBuildEnvFromSecret validates and stores a secret reference for a build environment variable.
func setBuildEnvFromSecret(p Provider, key, secretName string) error {
	// Validate the key follows the pattern
	if err := ValidateBuildEnvKey(key); err != nil {
		return err
	}

	// Check for conflicts with other sources
	if err := checkBuildEnvKeyConflict(p, key); err != nil {
		return err
	}

	return p.UpdateConfig(func(c *Config) {
		if c.BuildEnvFromSecrets == nil {
			c.BuildEnvFromSecrets = make(map[string]string)
		}
		c.BuildEnvFromSecrets[key] = secretName
	})
}

// getBuildEnvFromSecret retrieves the secret name for a build environment variable.
func getBuildEnvFromSecret(p Provider, key string) (secretName string, exists bool) {
	config := p.GetConfig()
	if config.BuildEnvFromSecrets == nil {
		return "", false
	}
	secretName, exists = config.BuildEnvFromSecrets[key]
	return secretName, exists
}

// getAllBuildEnvFromSecrets returns all build env secret references.
func getAllBuildEnvFromSecrets(p Provider) map[string]string {
	config := p.GetConfig()
	if config.BuildEnvFromSecrets == nil {
		return make(map[string]string)
	}
	result := make(map[string]string, len(config.BuildEnvFromSecrets))
	for k, v := range config.BuildEnvFromSecrets {
		result[k] = v
	}
	return result
}

// unsetBuildEnvFromSecret removes a secret reference.
func unsetBuildEnvFromSecret(p Provider, key string) error {
	return p.UpdateConfig(func(c *Config) {
		if c.BuildEnvFromSecrets != nil {
			delete(c.BuildEnvFromSecrets, key)
		}
	})
}

// setBuildEnvFromShell adds an environment variable name to read from shell at build time.
func setBuildEnvFromShell(p Provider, key string) error {
	// Validate the key follows the pattern
	if err := ValidateBuildEnvKey(key); err != nil {
		return err
	}

	// Check if already in the list - skip if so
	if getBuildEnvFromShell(p, key) {
		return nil // Already exists, nothing to do
	}

	// Check for conflicts with other sources (not including shell since we checked above)
	if err := checkBuildEnvKeyConflictExcludingShell(p, key); err != nil {
		return err
	}

	return p.UpdateConfig(func(c *Config) {
		c.BuildEnvFromShell = append(c.BuildEnvFromShell, key)
	})
}

// checkBuildEnvKeyConflictExcludingShell checks if a key is already configured in literal or secret sources.
func checkBuildEnvKeyConflictExcludingShell(p Provider, key string) error {
	config := p.GetConfig()

	// Check literal values
	if config.BuildEnv != nil {
		if _, exists := config.BuildEnv[key]; exists {
			return fmt.Errorf("key %s already configured as literal value; unset it first with 'thv config unset-build-env %s'", key, key)
		}
	}

	// Check secret references
	if config.BuildEnvFromSecrets != nil {
		if _, exists := config.BuildEnvFromSecrets[key]; exists {
			return fmt.Errorf("key %s already configured from secret; unset it first", key)
		}
	}

	return nil
}

// getBuildEnvFromShell checks if a key is configured to read from shell.
func getBuildEnvFromShell(p Provider, key string) bool {
	config := p.GetConfig()
	for _, k := range config.BuildEnvFromShell {
		if k == key {
			return true
		}
	}
	return false
}

// getAllBuildEnvFromShell returns all keys configured to read from shell.
func getAllBuildEnvFromShell(p Provider) []string {
	config := p.GetConfig()
	if config.BuildEnvFromShell == nil {
		return []string{}
	}
	result := make([]string, len(config.BuildEnvFromShell))
	copy(result, config.BuildEnvFromShell)
	return result
}

// unsetBuildEnvFromShell removes a key from shell environment list.
func unsetBuildEnvFromShell(p Provider, key string) error {
	return p.UpdateConfig(func(c *Config) {
		if c.BuildEnvFromShell == nil {
			return
		}
		newList := make([]string, 0, len(c.BuildEnvFromShell))
		for _, k := range c.BuildEnvFromShell {
			if k != key {
				newList = append(newList, k)
			}
		}
		c.BuildEnvFromShell = newList
	})
}

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

// setBuildEnv is a helper function that validates and sets a build environment variable.
func setBuildEnv(p Provider, key, value string) error {
	if err := ValidateBuildEnvEntry(key, value); err != nil {
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

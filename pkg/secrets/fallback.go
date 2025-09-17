package secrets

import (
	"context"
	"os"
	"strings"

	"github.com/stacklok/toolhive/pkg/logger"
)

// FallbackProvider wraps a primary provider with environment variable fallback
type FallbackProvider struct {
	primary     Provider
	envProvider Provider
}

// NewFallbackProvider creates a new provider with environment variable fallback
func NewFallbackProvider(primary Provider) Provider {
	return &FallbackProvider{
		primary: primary,
		envProvider: &EnvironmentProvider{
			prefix: EnvVarPrefix,
		},
	}
}

// GetSecret attempts to get a secret from the primary provider,
// falling back to environment variables if not found
func (f *FallbackProvider) GetSecret(ctx context.Context, name string) (string, error) {
	// First, try the primary provider
	value, err := f.primary.GetSecret(ctx, name)
	if err == nil {
		return value, nil
	}

	// Check if it's a "not found" error
	if !IsNotFoundError(err) {
		return "", err
	}

	// Try environment variable fallback with prefix first
	envValue, envErr := f.envProvider.GetSecret(ctx, name)
	if envErr == nil {
		logger.Debugf("Secret '%s' retrieved from environment variable fallback with prefix", name)
		return envValue, nil
	}

	// Try environment variable without prefix (for Kubernetes secrets)
	directValue := os.Getenv(name)
	if directValue != "" {
		logger.Debugf("Secret '%s' retrieved from direct environment variable (no prefix)", name)
		return directValue, nil
	}

	// Return the original error if no fallback found
	return "", err
}

// SetSecret always uses the primary provider (no env var writes)
func (f *FallbackProvider) SetSecret(ctx context.Context, name, value string) error {
	return f.primary.SetSecret(ctx, name, value)
}

// DeleteSecret always uses the primary provider (no env var deletes)
func (f *FallbackProvider) DeleteSecret(ctx context.Context, name string) error {
	return f.primary.DeleteSecret(ctx, name)
}

// ListSecrets only lists from the primary provider
// (env vars not listed in fallback mode for security)
func (f *FallbackProvider) ListSecrets(ctx context.Context) ([]SecretDescription, error) {
	return f.primary.ListSecrets(ctx)
}

// Cleanup delegates to the primary provider
func (f *FallbackProvider) Cleanup() error {
	return f.primary.Cleanup()
}

// Capabilities returns the primary provider's capabilities
func (f *FallbackProvider) Capabilities() ProviderCapabilities {
	return f.primary.Capabilities()
}

// IsNotFoundError checks if an error indicates a secret was not found
func IsNotFoundError(err error) bool {
	if err == nil {
		return false
	}
	errStr := err.Error()
	return strings.Contains(errStr, "not found") ||
		strings.Contains(errStr, "does not exist")
}

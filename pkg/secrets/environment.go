package secrets

import (
	"context"
	"errors"
	"fmt"
	"os"
)

// EnvironmentProvider reads secrets from environment variables
type EnvironmentProvider struct {
	prefix string
}

// NewEnvironmentProvider creates a new environment variable secrets provider
func NewEnvironmentProvider() Provider {
	return &EnvironmentProvider{
		prefix: EnvVarPrefix,
	}
}

// GetSecret retrieves a secret from environment variables
func (e *EnvironmentProvider) GetSecret(_ context.Context, name string) (string, error) {
	if name == "" {
		return "", errors.New("secret name cannot be empty")
	}

	envVar := e.prefix + name
	value := os.Getenv(envVar)
	if value == "" {
		return "", fmt.Errorf("secret not found: %s", name)
	}

	return value, nil
}

// SetSecret is not supported for environment variables
func (*EnvironmentProvider) SetSecret(_ context.Context, name, _ string) error {
	if name == "" {
		return errors.New("secret name cannot be empty")
	}
	return errors.New("environment provider is read-only")
}

// DeleteSecret is not supported for environment variables
func (*EnvironmentProvider) DeleteSecret(_ context.Context, name string) error {
	if name == "" {
		return errors.New("secret name cannot be empty")
	}
	return errors.New("environment provider is read-only")
}

// ListSecrets is not supported for environment variables for security reasons
func (*EnvironmentProvider) ListSecrets(_ context.Context) ([]SecretDescription, error) {
	return nil, errors.New("environment provider does not support listing secrets for security reasons")
}

// Cleanup is a no-op for environment provider
func (*EnvironmentProvider) Cleanup() error {
	return nil
}

// Capabilities returns the capabilities of the environment provider
func (*EnvironmentProvider) Capabilities() ProviderCapabilities {
	return ProviderCapabilities{
		CanRead:    true,
		CanWrite:   false,
		CanDelete:  false,
		CanList:    false,
		CanCleanup: false,
	}
}

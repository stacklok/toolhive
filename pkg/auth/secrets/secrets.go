// Package secrets provides generic secret management utilities for authentication.
// This package contains functions that can be used by any authentication method
// (OAuth, bearer tokens, etc.) to process secrets and store them in a secrets manager.
package secrets

import (
	"context"
	"crypto/rand"
	"fmt"
	"time"

	"github.com/stacklok/toolhive/pkg/config"
	"github.com/stacklok/toolhive/pkg/secrets"
)

// ProcessSecretWithProvider processes a secret, converting plain text to CLI format if needed.
// This is a generic function that can be used for any secret type.
// Parameters:
//   - workloadName: Name of the workload (used for secret naming)
//   - secretValue: The secret value (plain text or already in CLI format)
//   - secretManager: The secrets provider to use
//   - prefix: Prefix for the secret name (e.g., "OAUTH_CLIENT_SECRET_", "BEARER_TOKEN_")
//   - target: Target identifier for CLI format (e.g., "oauth_secret", "bearer_token")
//   - errorContext: Context for error messages (e.g., "OAuth client secret", "bearer token")
//
// Returns the secret in CLI format: "secret-name,target=target_value"
func ProcessSecretWithProvider(
	workloadName,
	secretValue string,
	secretManager secrets.Provider,
	prefix,
	target,
	errorContext string,
) (string, error) {
	if secretValue == "" {
		return "", nil
	}

	// Check if it's already in CLI format
	if _, err := secrets.ParseSecretParameter(secretValue); err == nil {
		return secretValue, nil // Already in CLI format
	}

	// It's plain text - convert to CLI format
	secretName, err := GenerateUniqueSecretNameWithPrefix(workloadName, prefix, secretManager)
	if err != nil {
		return "", fmt.Errorf("failed to generate unique secret name: %w", err)
	}

	// Store the secret in the secret manager
	if err := StoreSecretInManagerWithProvider(context.Background(), secretName, secretValue, secretManager); err != nil {
		return "", fmt.Errorf("failed to store %s in manager: %w", errorContext, err)
	}

	// Return CLI format reference
	return secrets.SecretParameter{Name: secretName, Target: target}.ToCLIString(), nil
}

// GenerateUniqueSecretNameWithPrefix generates a unique secret name with a custom prefix
// This is a generic function that can be used for any secret type
func GenerateUniqueSecretNameWithPrefix(workloadName, prefix string, secretManager secrets.Provider) (string, error) {
	// Generate base name
	baseName := fmt.Sprintf("%s%s", prefix, workloadName)

	// Check if the base name is available
	_, err := secretManager.GetSecret(context.Background(), baseName)
	if err != nil {
		// Secret doesn't exist, we can use the base name
		return baseName, nil
	}

	// Secret exists, generate a unique name with timestamp
	// Use nanosecond precision + random suffix for collision resistance
	timestamp := time.Now().UnixNano()
	randomSuffix := make([]byte, 4)
	if _, err := rand.Read(randomSuffix); err != nil {
		return "", fmt.Errorf("failed to generate random suffix: %w", err)
	}
	uniqueName := fmt.Sprintf("%s_%d_%x", baseName, timestamp, randomSuffix)
	return uniqueName, nil
}

// StoreSecretInManagerWithProvider stores a secret using the provided secret manager
// This version is testable with dependency injection
func StoreSecretInManagerWithProvider(ctx context.Context, secretName, secretValue string, secretManager secrets.Provider) error {
	// Check if the provider supports writing secrets
	if !secretManager.Capabilities().CanWrite {
		configProvider := config.NewDefaultProvider()
		cfg := configProvider.GetConfig()
		providerType, _ := cfg.Secrets.GetProviderType()
		return fmt.Errorf("secrets provider %s does not support writing secrets", providerType)
	}

	// Store the secret
	if err := secretManager.SetSecret(ctx, secretName, secretValue); err != nil {
		return fmt.Errorf("failed to store secret %s: %w", secretName, err)
	}

	return nil
}

// GetSecretsManager returns the secrets manager instance
// This is exported so it can be reused by other packages
func GetSecretsManager() (secrets.Provider, error) {
	configProvider := config.NewDefaultProvider()
	cfg := configProvider.GetConfig()

	// Check if secrets setup has been completed
	if !cfg.Secrets.SetupCompleted {
		return nil, secrets.ErrSecretsNotSetup
	}

	providerType, err := cfg.Secrets.GetProviderType()
	if err != nil {
		return nil, fmt.Errorf("failed to get secrets provider type: %w", err)
	}

	manager, err := secrets.CreateSecretProvider(providerType)
	if err != nil {
		return nil, fmt.Errorf("failed to create secrets manager: %w", err)
	}

	return manager, nil
}

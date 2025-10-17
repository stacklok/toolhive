// Package oauth contains the OAuth management logic for ToolHive.
package oauth

import (
	"context"
	"crypto/rand"
	"fmt"
	"time"

	"github.com/stacklok/toolhive/pkg/config"
	"github.com/stacklok/toolhive/pkg/secrets"
)

// ProcessOAuthClientSecret processes an OAuth client secret, converting plain text to CLI format if needed
func ProcessOAuthClientSecret(workloadName, clientSecret string) (string, error) {
	secretManager, err := getSecretsManager()
	if err != nil {
		return "", fmt.Errorf("failed to get secrets manager: %w", err)
	}
	return ProcessOAuthClientSecretWithProvider(workloadName, clientSecret, secretManager)
}

// ProcessOAuthClientSecretWithProvider processes an OAuth client secret using the provided secret manager
// This version is testable with dependency injection
func ProcessOAuthClientSecretWithProvider(workloadName, clientSecret string, secretManager secrets.Provider) (string, error) {
	if clientSecret == "" {
		return "", nil
	}

	// Check if it's already in CLI format
	if _, err := secrets.ParseSecretParameter(clientSecret); err == nil {
		return clientSecret, nil // Already in CLI format
	}

	// It's plain text - convert to CLI format
	secretName, err := GenerateUniqueSecretNameWithProvider(workloadName, secretManager)
	if err != nil {
		return "", fmt.Errorf("failed to generate unique secret name: %w", err)
	}

	// Store the secret in the secret manager
	if err := StoreSecretInManagerWithProvider(context.Background(), secretName, clientSecret, secretManager); err != nil {
		return "", fmt.Errorf("failed to store secret in manager: %w", err)
	}

	// Return CLI format reference
	return secrets.SecretParameter{Name: secretName, Target: "oauth_secret"}.ToCLIString(), nil
}

// generateOAuthClientSecretName generates a base secret name for an OAuth client secret
func generateOAuthClientSecretName(workloadName string) string {
	return fmt.Sprintf("OAUTH_CLIENT_SECRET_%s", workloadName)
}

// GenerateUniqueSecretNameWithProvider generates a unique secret name using the provided secret manager
// This version is testable with dependency injection
func GenerateUniqueSecretNameWithProvider(workloadName string, secretManager secrets.Provider) (string, error) {
	// Generate base name
	baseName := generateOAuthClientSecretName(workloadName)

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

// StoreSecretInManager stores a secret in the configured secret manager
func StoreSecretInManager(ctx context.Context, secretName, secretValue string) error {
	secretManager, err := getSecretsManager()
	if err != nil {
		return fmt.Errorf("failed to get secrets manager: %w", err)
	}
	return StoreSecretInManagerWithProvider(ctx, secretName, secretValue, secretManager)
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

// getSecretsManager returns the secrets manager instance
func getSecretsManager() (secrets.Provider, error) {
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

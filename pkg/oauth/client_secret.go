// Package oauth contains the OAuth management logic for ToolHive.
package oauth

import (
	"context"
	"fmt"
	"time"

	"github.com/stacklok/toolhive/pkg/config"
	"github.com/stacklok/toolhive/pkg/secrets"
)

// ProcessOAuthClientSecret processes an OAuth client secret, converting plain text to CLI format if needed
func ProcessOAuthClientSecret(workloadName, clientSecret string) (string, error) {
	if clientSecret == "" {
		return "", nil
	}

	// Check if it's already in CLI format
	if _, err := secrets.ParseSecretParameter(clientSecret); err == nil {
		return clientSecret, nil // Already in CLI format
	}

	// It's plain text - convert to CLI format
	secretName, err := GenerateUniqueSecretName(workloadName)
	if err != nil {
		return "", fmt.Errorf("failed to generate unique secret name: %w", err)
	}

	// Store the secret in the secret manager
	if err := StoreSecretInManager(context.Background(), secretName, clientSecret); err != nil {
		return "", fmt.Errorf("failed to store secret in manager: %w", err)
	}

	// Return CLI format reference
	return secrets.SecretParameter{Name: secretName, Target: "oauth_secret"}.ToCLIString(), nil
}

// generateOAuthClientSecretName generates a base secret name for an OAuth client secret
func generateOAuthClientSecretName(workloadName string) string {
	return fmt.Sprintf("OAUTH_CLIENT_SECRET_%s", workloadName)
}

// GenerateUniqueSecretName generates a unique secret name for an OAuth client secret
// It handles both name generation and uniqueness checking in a single function
func GenerateUniqueSecretName(workloadName string) (string, error) {
	// Generate base name
	baseName := generateOAuthClientSecretName(workloadName)

	// Get the secrets manager to check for existing secrets
	secretManager, err := getSecretsManager()
	if err != nil {
		return "", fmt.Errorf("failed to get secrets manager: %w", err)
	}

	// Check if the base name is available
	_, err = secretManager.GetSecret(context.Background(), baseName)
	if err != nil {
		// Secret doesn't exist, we can use the base name
		return baseName, nil
	}

	// Secret exists, generate a unique name with timestamp
	// Second precision is sufficient for OAuth client secret generation
	// as workload creation is not a high-frequency concurrent operation
	timestamp := time.Now().Unix()
	uniqueName := fmt.Sprintf("%s_%d", baseName, timestamp)
	return uniqueName, nil
}

// StoreSecretInManager stores a secret in the configured secret manager
func StoreSecretInManager(ctx context.Context, secretName, secretValue string) error {
	// Use existing getSecretsManager function from secret.go
	secretManager, err := getSecretsManager()
	if err != nil {
		return fmt.Errorf("failed to get secrets manager: %w", err)
	}

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

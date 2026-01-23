// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

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

// TokenType represents the type of authentication token/secret being processed
type TokenType string

const (
	// TokenTypeOAuthClientSecret represents an OAuth client secret
	// #nosec G101 - this is a type identifier, not a credential
	TokenTypeOAuthClientSecret TokenType = "oauth_client_secret"
	// TokenTypeBearerToken represents a bearer token
	TokenTypeBearerToken TokenType = "bearer_token"
	// TokenTypeOAuthRefreshToken represents a cached OAuth refresh token
	// #nosec G101 - this is a type identifier, not a credential
	TokenTypeOAuthRefreshToken TokenType = "oauth_refresh_token"
	// TokenTypeOAuthClientID represents a cached OAuth client ID from Dynamic Client Registration
	TokenTypeOAuthClientID TokenType = "oauth_client_id" // #nosec G101
)

// tokenTypeConfig holds configuration for each token type
type tokenTypeConfig struct {
	prefix       string
	target       string
	errorContext string
}

var tokenTypeConfigs = map[TokenType]tokenTypeConfig{
	TokenTypeOAuthClientSecret: {
		prefix:       "OAUTH_CLIENT_SECRET_",
		target:       "oauth_secret",
		errorContext: "OAuth client secret",
	},
	TokenTypeBearerToken: {
		prefix:       "BEARER_TOKEN_",
		target:       "bearer_token",
		errorContext: "bearer token",
	},
	TokenTypeOAuthRefreshToken: {
		prefix:       "OAUTH_REFRESH_TOKEN_",
		target:       "oauth_refresh",
		errorContext: "OAuth refresh token",
	},
	TokenTypeOAuthClientID: {
		prefix:       "OAUTH_CLIENT_ID_",
		target:       "oauth_client_id",
		errorContext: "OAuth client ID",
	},
}

// ProcessSecret processes a secret, converting plain text to CLI format if needed.
// This is a generic function that can be used for any secret type.
// Parameters:
//   - workloadName: Name of the workload (used for secret naming)
//   - secretValue: The secret value (plain text or already in CLI format)
//   - tokenType: The type of token/secret (determines prefix, target, and error context)
//
// Returns the secret in CLI format: "secret-name,target=target_value"
func ProcessSecret(workloadName, secretValue string, tokenType TokenType) (string, error) {
	// Early return if no secret is provided - no need to access secrets manager
	if secretValue == "" {
		return "", nil
	}

	// Get the configuration for this token type
	tokenConfig, ok := tokenTypeConfigs[tokenType]
	if !ok {
		return "", fmt.Errorf("unknown token type: %s", tokenType)
	}

	// Get the secrets manager
	secretManager, err := GetSecretsManager()
	if err != nil {
		return "", fmt.Errorf("failed to get secrets manager: %w", err)
	}

	// Process the secret using the provider
	return processSecretWithProvider(workloadName, secretValue, secretManager, tokenConfig)
}

// ProcessSecretWithProvider processes a secret using the provided secret manager
// This version is testable with dependency injection and is used for testing
func ProcessSecretWithProvider(
	workloadName,
	secretValue string,
	secretManager secrets.Provider,
	tokenType TokenType,
) (string, error) {
	// Get the configuration for this token type
	tokenConfig, ok := tokenTypeConfigs[tokenType]
	if !ok {
		return "", fmt.Errorf("unknown token type: %s", tokenType)
	}

	return processSecretWithProvider(workloadName, secretValue, secretManager, tokenConfig)
}

// processSecretWithProvider processes a secret using the provided secret manager
// This is the internal implementation
func processSecretWithProvider(
	workloadName,
	secretValue string,
	secretManager secrets.Provider,
	tokenConfig tokenTypeConfig,
) (string, error) {
	if secretValue == "" {
		return "", nil
	}

	// Check if it's already in CLI format
	if _, err := secrets.ParseSecretParameter(secretValue); err == nil {
		return secretValue, nil // Already in CLI format
	}

	// It's plain text - convert to CLI format
	secretName, err := GenerateUniqueSecretNameWithPrefix(workloadName, tokenConfig.prefix, secretManager)
	if err != nil {
		return "", fmt.Errorf("failed to generate unique secret name: %w", err)
	}

	// Store the secret in the secret manager
	if err := StoreSecretInManagerWithProvider(context.Background(), secretName, secretValue, secretManager); err != nil {
		return "", fmt.Errorf("failed to store %s in manager: %w", tokenConfig.errorContext, err)
	}

	// Return CLI format reference
	return secrets.SecretParameter{Name: secretName, Target: tokenConfig.target}.ToCLIString(), nil
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

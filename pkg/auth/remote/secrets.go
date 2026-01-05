// Package remote contains remote authentication configuration and utilities.
package remote

import (
	"fmt"

	authsecrets "github.com/stacklok/toolhive/pkg/auth/secrets"
)

// ProcessBearerToken processes a bearer token, converting plain text to CLI format if needed
func ProcessBearerToken(workloadName, bearerToken string) (string, error) {
	// Early return if no token is provided - no need to access secrets manager
	if bearerToken == "" {
		return "", nil
	}

	secretManager, err := authsecrets.GetSecretsManager()
	if err != nil {
		return "", fmt.Errorf("failed to get secrets manager: %w", err)
	}
	return authsecrets.ProcessSecretWithProvider(
		workloadName,
		bearerToken,
		secretManager,
		"BEARER_TOKEN_",
		"bearer_token",
		"bearer token",
	)
}

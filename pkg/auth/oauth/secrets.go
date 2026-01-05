// Package oauth contains OAuth/OIDC protocol implementation for ToolHive.
package oauth

import (
	"fmt"

	authsecrets "github.com/stacklok/toolhive/pkg/auth/secrets"
)

// ProcessOAuthClientSecret processes an OAuth client secret, converting plain text to CLI format if needed
func ProcessOAuthClientSecret(workloadName, clientSecret string) (string, error) {
	// Early return if no secret is provided - no need to access secrets manager
	if clientSecret == "" {
		return "", nil
	}

	secretManager, err := authsecrets.GetSecretsManager()
	if err != nil {
		return "", fmt.Errorf("failed to get secrets manager: %w", err)
	}
	return authsecrets.ProcessSecretWithProvider(
		workloadName,
		clientSecret,
		secretManager,
		"OAUTH_CLIENT_SECRET_",
		"oauth_secret",
		"OAuth client secret",
	)
}

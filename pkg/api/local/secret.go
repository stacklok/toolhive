package local

import (
	"context"
	"fmt"

	"github.com/StacklokLabs/toolhive/pkg/api"
	"github.com/StacklokLabs/toolhive/pkg/logger"
	"github.com/StacklokLabs/toolhive/pkg/secrets"
)

// Secret is the local implementation of the api.SecretAPI interface.
type Secret struct {
	// debug indicates whether debug mode is enabled
	debug bool
}

// NewSecret creates a new local SecretAPI with the provided debug flag.
func NewSecret(debug bool) api.SecretAPI {
	return &Secret{
		debug: debug,
	}
}

// Set sets a secret.
func (s *Secret) Set(_ context.Context, name string, opts *api.SecretSetOptions) error {
	s.logDebug("Setting secret: %s", name)

	// Get the secret manager
	secretManager, err := getSecretManager(opts.Provider)
	if err != nil {
		return err
	}

	// Set the secret
	return secretManager.SetSecret(name, opts.Value)
}

// Get gets a secret.
func (s *Secret) Get(_ context.Context, name string, opts *api.SecretGetOptions) (string, error) {
	s.logDebug("Getting secret: %s", name)

	// Get the secret manager
	secretManager, err := getSecretManager(opts.Provider)
	if err != nil {
		return "", err
	}

	// Get the secret
	return secretManager.GetSecret(name)
}

// Delete deletes a secret.
func (s *Secret) Delete(_ context.Context, name string, opts *api.SecretDeleteOptions) error {
	s.logDebug("Deleting secret: %s", name)

	// Get the secret manager
	secretManager, err := getSecretManager(opts.Provider)
	if err != nil {
		return err
	}

	// Delete the secret
	return secretManager.DeleteSecret(name)
}

// List lists secrets.
func (s *Secret) List(_ context.Context, opts *api.SecretListOptions) ([]string, error) {
	s.logDebug("Listing secrets")

	// Get the secret manager
	secretManager, err := getSecretManager(opts.Provider)
	if err != nil {
		return nil, err
	}

	// List the secrets
	return secretManager.ListSecrets()
}

// ResetKeyring resets the keyring.
func (s *Secret) ResetKeyring(_ context.Context, _ *api.SecretResetKeyringOptions) error {
	s.logDebug("Resetting keyring")

	// Reset the keyring
	return secrets.ResetKeyringSecret()
}

// getSecretManager returns a secret manager for the specified provider type.
// If the provider type is empty, it uses the default provider type.
func getSecretManager(provider string) (secrets.Provider, error) {
	// Get the provider type
	providerType := secrets.EncryptedType
	if provider != "" {
		// Validate the provider type
		switch provider {
		// Checking for "" just in case the user wants to use the default provider
		case string(secrets.EncryptedType), "":
			// Valid provider type
		default:
			return nil, fmt.Errorf("invalid secrets provider type: %s (valid types: encrypted)", provider)
		}
		providerType = secrets.ProviderType(provider)
	}

	// Create a secret manager
	secretManager, err := secrets.CreateSecretManager(providerType)
	if err != nil {
		return nil, fmt.Errorf("failed to create secret manager: %w", err)
	}

	return secretManager, nil
}

// logDebug logs a debug message if debug mode is enabled
func (s *Secret) logDebug(format string, args ...interface{}) {
	if s.debug {
		logger.Log.Infof(format, args...)
	}
}

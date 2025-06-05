package secrets

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/stacklok/toolhive/pkg/secrets/clients"
)

//go:generate mockgen -destination=mocks/mock_onepassword.go -package=mocks -source=1password.go OPSecretsService

// Err1PasswordReadOnly indicates that the 1Password secrets manager is read-only.
// Is it returned by operations which attempt to change values in 1Password.
var Err1PasswordReadOnly = fmt.Errorf("1Password secrets manager is read-only, write operations are not supported")

// OnePasswordManager manages secrets in 1Password.
type OnePasswordManager struct {
	client    clients.OnePasswordClient
	vaultName string
}

var timeout = 5 * time.Second

// GetSecret retrieves a secret from 1Password.
func (o *OnePasswordManager) GetSecret(ctx context.Context, path string) (string, error) {
	if !strings.Contains(path, "op://") {
		return "", fmt.Errorf("invalid secret path: %s", path)
	}

	secret, err := o.client.Resolve(ctx, path)
	if err != nil {
		return "", fmt.Errorf("error resolving secret: %v", err)
	}

	return secret, nil
}

// SetSecret is not supported for 1Password unless there is
// demand for it.
func (*OnePasswordManager) SetSecret(_ context.Context, _, _ string) error {
	return Err1PasswordReadOnly
}

// DeleteSecret is not supported for 1Password unless there is
// demand for it.
func (*OnePasswordManager) DeleteSecret(_ context.Context, _ string) error {
	return Err1PasswordReadOnly
}

// ListSecrets lists the secrets for the specified vault.
func (o *OnePasswordManager) ListSecrets(ctx context.Context) ([]SecretDescription, error) {
	items, err := o.client.List(ctx, o.vaultName)
	if err != nil {
		return nil, fmt.Errorf("error retrieving secrets from 1password API: %v", err)
	}

	secrets := make([]SecretDescription, 0, len(items))
	for _, item := range items {
		if item.ID == "" || item.Title == "" {
			continue // Skip items without ID or Title
		}
		description := SecretDescription{
			Key:         fmt.Sprintf("op://%s/%s", item.VaultID, item.ID),
			Description: item.Title,
		}
		secrets = append(secrets, description)
	}
	return secrets, nil
}

// Cleanup is not needed for 1Password.
func (*OnePasswordManager) Cleanup() error {
	return nil
}

// NewOnePasswordManager creates an instance of OnePasswordManager.
func NewOnePasswordManager() (Provider, error) {
	token := os.Getenv("OP_SERVICE_ACCOUNT_TOKEN")
	if token == "" {
		return nil, fmt.Errorf("OP_SERVICE_ACCOUNT_TOKEN is not set")
	}

	vaultName := os.Getenv("OP_VAULT_ID")
	if token == "" {
		return nil, fmt.Errorf("OP_VAULT_ID is not set")
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	client, err := clients.NewOnePasswordClient(ctx, token)
	if err != nil {
		return nil, fmt.Errorf("error creating 1Password client: %v", err)
	}

	return &OnePasswordManager{
		client:    client,
		vaultName: vaultName,
	}, nil
}

// NewOnePasswordManagerWithClient creates an instance of OnePasswordManager with a provided 1password client.
// This function is primarily intended for testing purposes.
func NewOnePasswordManagerWithClient(client clients.OnePasswordClient) *OnePasswordManager {
	return &OnePasswordManager{
		client: client,
	}
}

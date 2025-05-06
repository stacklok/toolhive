package secrets

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/1password/onepassword-sdk-go"
)

//go:generate mockgen -destination=mocks/mock_onepassword.go -package=mocks -source=1password.go OPSecretsService

// OPSecretsService defines the interface for the 1Password Secrets service
type OPSecretsService interface {
	Resolve(ctx context.Context, secretReference string) (string, error)
}

// OnePasswordManager manages secrets in 1Password.
type OnePasswordManager struct {
	secretsService OPSecretsService
}

var timeout = 5 * time.Second

// GetSecret retrieves a secret from 1Password.
func (opm *OnePasswordManager) GetSecret(path string) (string, error) {
	if !strings.Contains(path, "op://") {
		return "", fmt.Errorf("invalid secret path: %s", path)
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	secret, err := opm.secretsService.Resolve(ctx, path)
	if err != nil {
		return "", fmt.Errorf("error resolving secret: %v", err)
	}

	return secret, nil
}

// SetSecret is not supported for 1Password unless there is
// demand for it.
func (*OnePasswordManager) SetSecret(_, _ string) error {
	return nil
}

// DeleteSecret is not supported for 1Password unless there is
// demand for it.
func (*OnePasswordManager) DeleteSecret(_ string) error {
	return nil
}

// ListSecrets is not supported for 1Password unless there is
// demand for it.
func (*OnePasswordManager) ListSecrets() ([]string, error) {
	return nil, nil
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

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	client, err := onepassword.NewClient(
		ctx,
		onepassword.WithServiceAccountToken(token),
		onepassword.WithIntegrationInfo(onepassword.DefaultIntegrationName, onepassword.DefaultIntegrationVersion),
	)
	if err != nil {
		return nil, fmt.Errorf("error creating 1Password client: %v", err)
	}

	return &OnePasswordManager{
		secretsService: client.Secrets(),
	}, nil
}

// NewOnePasswordManagerWithService creates an instance of OnePasswordManager with a provided secrets service.
// This function is primarily intended for testing purposes.
func NewOnePasswordManagerWithService(secretsService OPSecretsService) *OnePasswordManager {
	return &OnePasswordManager{
		secretsService: secretsService,
	}
}

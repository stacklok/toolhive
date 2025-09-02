package secrets

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/adrg/xdg"
	"golang.org/x/term"

	"github.com/stacklok/toolhive/pkg/logger"
	"github.com/stacklok/toolhive/pkg/process"
	"github.com/stacklok/toolhive/pkg/secrets/keyring"
)

const (
	// PasswordEnvVar is the environment variable used to specify the password for encrypting and decrypting secrets.
	PasswordEnvVar = "TOOLHIVE_SECRETS_PASSWORD"

	// ProviderEnvVar is the environment variable used to specify the secrets provider type.
	ProviderEnvVar = "TOOLHIVE_SECRETS_PROVIDER"

	keyringService = "toolhive"
)

var (
	keyringProvider keyring.Provider
	keyringOnce     sync.Once
)

func getKeyringProvider() keyring.Provider {
	keyringOnce.Do(func() {
		keyringProvider = keyring.NewCompositeProvider()
	})
	return keyringProvider
}

// ProviderType represents an enum of the types of available secrets providers.
type ProviderType string

const (
	// EncryptedType represents the encrypted secret provider.
	EncryptedType ProviderType = "encrypted"

	// OnePasswordType represents the 1Password secret provider.
	OnePasswordType ProviderType = "1password"

	// NoneType represents the none secret provider.
	NoneType ProviderType = "none"

	// EnvironmentType represents the environment variable secret provider
	EnvironmentType ProviderType = "environment"
)

// ErrUnknownManagerType is returned when an invalid value for ProviderType is specified.
var ErrUnknownManagerType = errors.New("unknown secret manager type")

// ErrSecretsNotSetup is returned when secrets functionality is used before running setup.
var ErrSecretsNotSetup = errors.New("secrets provider not configured. " +
	"Please run 'thv secret setup' to configure a secrets provider first")

// SetupResult contains the result of a provider setup operation
type SetupResult struct {
	ProviderType ProviderType
	Success      bool
	Message      string
	Error        error
}

// ValidateProvider validates that a provider can be created and performs basic functionality tests
func ValidateProvider(ctx context.Context, providerType ProviderType) *SetupResult {
	return ValidateProviderWithPassword(ctx, providerType, "")
}

// ValidateProviderWithPassword validates that a provider can be created and performs basic functionality tests.
// If password is provided for encrypted provider, it uses that password instead of reading from stdin.
func ValidateProviderWithPassword(ctx context.Context, providerType ProviderType, password string) *SetupResult {
	result := &SetupResult{
		ProviderType: providerType,
		Success:      false,
	}

	// Test that we can create the provider
	provider, err := CreateSecretProviderWithPassword(providerType, password)
	if err != nil {
		result.Error = fmt.Errorf("failed to create provider: %w", err)
		result.Message = fmt.Sprintf("Failed to initialize %s provider", providerType)
		return result
	}

	// Perform provider-specific validation
	switch providerType {
	case EncryptedType:
		return validateEncryptedProvider(ctx, provider, result)
	case OnePasswordType:
		return validateOnePasswordProvider(ctx, provider, result)
	case NoneType:
		return validateNoneProvider(result)
	case EnvironmentType:
		return ValidateEnvironmentProvider(ctx, provider, result)
	default:
		result.Error = fmt.Errorf("unknown provider type: %s", providerType)
		result.Message = "Unknown provider type"
		return result
	}
}

// ValidateEnvironmentProvider tests environment provider functionality
func ValidateEnvironmentProvider(ctx context.Context, provider Provider, result *SetupResult) *SetupResult {
	// Test basic functionality by attempting to get a test secret
	// We don't expect it to exist, but we should get a proper "not found" error
	_, err := provider.GetSecret(ctx, "nonexistent-test-secret")
	if err == nil {
		result.Error = fmt.Errorf("expected error for nonexistent secret, but got none")
		result.Message = "Environment provider validation failed"
		return result
	}

	// Check that we get the expected error message
	if !strings.Contains(err.Error(), "secret not found") {
		result.Error = fmt.Errorf("unexpected error format: %v", err)
		result.Message = "Environment provider validation failed"
		return result
	}

	result.Success = true
	result.Message = "Environment provider validation successful"
	return result
}

// validateEncryptedProvider tests basic encrypted provider functionality
func validateEncryptedProvider(ctx context.Context, provider Provider, result *SetupResult) *SetupResult {
	// Test basic functionality with a temporary secret
	testKey := "setup-validation-test"
	testValue := "test-value"

	// Test setting a secret
	if err := provider.SetSecret(ctx, testKey, testValue); err != nil {
		result.Error = fmt.Errorf("failed to test secret storage: %w", err)
		result.Message = "Failed to store test secret"
		return result
	}

	// Test retrieving the secret
	retrievedValue, err := provider.GetSecret(ctx, testKey)
	if err != nil {
		result.Error = fmt.Errorf("failed to test secret retrieval: %w", err)
		result.Message = "Failed to retrieve test secret"
		return result
	}

	// Verify the value matches
	if retrievedValue != testValue {
		result.Error = fmt.Errorf("secret test failed: expected %s, got %s", testValue, retrievedValue)
		result.Message = "Secret value mismatch during validation"
		return result
	}

	// Clean up test secret
	_ = provider.DeleteSecret(ctx, testKey)

	result.Success = true
	result.Message = "Encrypted provider validation successful"
	return result
}

// validateOnePasswordProvider tests 1Password provider connectivity
func validateOnePasswordProvider(ctx context.Context, provider Provider, result *SetupResult) *SetupResult {
	// Test basic functionality by attempting to list secrets
	_, err := provider.ListSecrets(ctx)
	if err != nil {
		result.Error = fmt.Errorf("failed to connect to 1Password: %w", err)
		result.Message = "Failed to connect to 1Password service"
		return result
	}

	result.Success = true
	result.Message = "1Password provider validation successful"
	return result
}

// validateNoneProvider validates the none provider (always succeeds)
func validateNoneProvider(result *SetupResult) *SetupResult {
	// None provider doesn't need validation, it always works
	result.Success = true
	result.Message = "None provider validation successful"
	return result
}

// ErrKeyringNotAvailable is returned when the OS keyring is not available for the encrypted provider.
var ErrKeyringNotAvailable = errors.New("OS keyring is not available. " +
	"The encrypted provider requires an OS keyring to securely store passwords. " +
	"Please use a different secrets provider (e.g., 1password) " +
	"or ensure your system has a keyring service available")

// IsKeyringAvailable tests if any keyring backend is available
func IsKeyringAvailable() bool {
	provider := getKeyringProvider()
	return provider.IsAvailable()
}

// CreateSecretProvider creates the specified type of secrets provider.
// TODO CREATE function does not actually create anything, refactor or rename
func CreateSecretProvider(managerType ProviderType) (Provider, error) {
	return CreateSecretProviderWithPassword(managerType, "")
}

// CreateSecretProviderWithPassword creates the specified type of secrets provider with an optional password.
// If password is empty, it uses the current functionality (read from keyring or stdin).
// If password is provided, it uses that password and stores it in the keyring if not already setup.
func CreateSecretProviderWithPassword(managerType ProviderType, password string) (Provider, error) {
	// Create the primary provider
	var primary Provider
	var err error

	switch managerType {
	case EncryptedType:
		// Enforce keyring availability for encrypted provider
		if !IsKeyringAvailable() {
			return nil, ErrKeyringNotAvailable
		}

		secretsPassword, err := GetSecretsPassword(password)
		if err != nil {
			return nil, fmt.Errorf("failed to get secrets password: %w", err)
		}
		// Convert to 256-bit hash for use with AES-GCM.
		key := sha256.Sum256(secretsPassword)
		secretsPath, err := xdg.DataFile("toolhive/secrets_encrypted")
		if err != nil {
			return nil, fmt.Errorf("unable to access secrets file path %v", err)
		}
		primary, err = NewEncryptedManager(secretsPath, key[:])
		if err != nil {
			return nil, err
		}
	case OnePasswordType:
		primary, err = NewOnePasswordManager()
	case NoneType:
		primary, err = NewNoneManager()
	case EnvironmentType:
		// Direct environment provider - no fallback needed
		return NewEnvironmentProvider(), nil
	default:
		return nil, ErrUnknownManagerType
	}

	if err != nil {
		return nil, err
	}

	// Wrap with fallback provider if enabled
	if shouldEnableFallback() {
		return NewFallbackProvider(primary), nil
	}

	return primary, nil
}

// shouldEnableFallback determines if environment variable fallback should be enabled
func shouldEnableFallback() bool {
	// Check for explicit opt-out
	if os.Getenv("TOOLHIVE_DISABLE_ENV_FALLBACK") == "true" {
		return false
	}

	// Enable by default for non-environment providers
	return true
}

// GetSecretsPassword returns the password to use for encrypting and decrypting secrets.
// If optionalPassword is provided and keyring is not yet setup, it uses that password and stores it.
// Otherwise, it uses the current functionality (read from keyring or stdin).
func GetSecretsPassword(optionalPassword string) ([]byte, error) {
	provider := getKeyringProvider()

	// Attempt to load the password from the keyring
	keyringSecret, err := provider.Get(keyringService, keyringService)
	if err == nil {
		return []byte(keyringSecret), nil
	}

	// Handle key not found
	if errors.Is(err, keyring.ErrNotFound) {
		var password []byte

		// If optional password is provided, use it
		if optionalPassword != "" {
			if len(optionalPassword) == 0 {
				return nil, errors.New("password cannot be empty")
			}
			password = []byte(optionalPassword)
		} else {
			// Keyring is available but no password stored - this should only happen during setup
			if process.IsDetached() {
				return nil, fmt.Errorf("detached process detected, cannot ask for password")
			}

			// Prompt for password during setup
			var err error
			password, err = readPasswordStdin()
			if err != nil {
				return nil, fmt.Errorf("failed to read password: %w", err)
			}
		}

		// Store the password in the keyring for future use
		logger.Info(fmt.Sprintf("writing password to %s", provider.Name()))
		err = provider.Set(keyringService, keyringService, string(password))
		if err != nil {
			return nil, fmt.Errorf("failed to store password in keyring: %w", err)
		}

		return password, nil
	}

	// Assume any other keyring error means keyring is not available
	return nil, fmt.Errorf("keyring is not available: %w", err)
}

func readPasswordStdin() ([]byte, error) {
	printPasswordPrompt()
	password, err := term.ReadPassword(int(os.Stdin.Fd()))
	// Start new line after receiving password to ensure errors are printed correctly.
	fmt.Println()
	if err != nil {
		return nil, fmt.Errorf("failed to read password: %w", err)
	}

	// Ensure the password is non-empty.
	if len(password) == 0 {
		return nil, errors.New("password cannot be empty")
	}
	return password, nil
}

// ResetKeyringSecret clears out the secret from the keystore (if present).
func ResetKeyringSecret() error {
	provider := getKeyringProvider()
	return provider.DeleteAll(keyringService)
}

// GenerateSecurePassword generates a cryptographically secure random password
func GenerateSecurePassword() (string, error) {
	// Generate 32 random bytes (256 bits)
	bytes := make([]byte, 32)
	if _, err := rand.Read(bytes); err != nil {
		return "", fmt.Errorf("failed to generate random bytes: %w", err)
	}

	// Encode as base64 to make it a readable string
	// This gives us a 44-character password with good entropy
	password := base64.URLEncoding.EncodeToString(bytes)
	return password, nil
}

func printPasswordPrompt() {
	fmt.Print("ToolHive needs a password to secure your credentials in the OS keyring.\n" +
		"This password will be used to encrypt and decrypt API tokens and other secrets\n" +
		"that need to be accessed by MCP servers. It will be securely stored in your OS keyring\n" +
		"so you won't need to enter it each time.\n" +
		"Please enter your keyring password: ")
}

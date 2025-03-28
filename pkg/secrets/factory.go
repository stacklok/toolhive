package secrets

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"os"

	"github.com/adrg/xdg"
	"golang.org/x/term"
)

// SecretsPasswordEnvVar is the environment variable used to specify the password for encrypting and decrypting secrets.
const SecretsPasswordEnvVar = "VIBETOOL_SECRETS_PASSWORD"

// ManagerType represents an enum of the types of available secrets providers.
type ManagerType string

const (
	// BasicType represents the basic secret provider.
	BasicType ManagerType = "basic"
	// EncryptedType represents the encrypted secret provider.
	EncryptedType ManagerType = "encrypted"
)

// ErrUnknownManagerType is returned when an invalid value for ManagerType is specified.
var ErrUnknownManagerType = errors.New("unknown secret manager type")

// CreateSecretManager creates the specified type of secret manager.
func CreateSecretManager(managerType ManagerType) (Manager, error) {
	switch managerType {
	case BasicType:
		secretsPath, err := xdg.DataFile("vibetool/secrets")
		if err != nil {
			return nil, fmt.Errorf("unable to access secrets file path %v", err)
		}
		return NewBasicManager(secretsPath)
	case EncryptedType:
		password, err := GetSecretsPassword()
		if err != nil {
			return nil, fmt.Errorf("failed to get secrets password: %w", err)
		}
		// Convert to 256-bit hash for use with AES-GCM.
		key := sha256.Sum256(password)
		secretsPath, err := xdg.DataFile("vibetool/secrets_encrypted")
		if err != nil {
			return nil, fmt.Errorf("unable to access secrets file path %v", err)
		}
		return NewEncryptedManager(secretsPath, key[:])
	default:
		return nil, ErrUnknownManagerType
	}
}

// GetSecretsPassword returns the password to use for encrypting and decrypting secrets.
// It will attempt to retrieve it from the environment variable VIBETOOL_SECRETS_PASSWORD.
// If the environment variable is not set, it will prompt the user to enter a password.
func GetSecretsPassword() ([]byte, error) {
	var err error
	password := []byte(os.Getenv(SecretsPasswordEnvVar))
	if len(password) == 0 {
		// Prompt the user for a password to encrypt the secrets file.
		// TODO: Consider integration with a keychain.
		fmt.Print("Enter a password for secrets encryption and decryption: ")
		password, err = term.ReadPassword(int(os.Stdin.Fd()))
		// Start new line after receiving password.
		fmt.Printf("\n")
		if err != nil {
			return nil, fmt.Errorf("failed to read password: %w", err)
		}
		if len(password) == 0 {
			return nil, errors.New("password cannot be empty")
		}
	}
	return password, nil
}

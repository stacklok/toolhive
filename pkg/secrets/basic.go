package secrets

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path"

	"golang.org/x/sync/syncmap"
)

// BasicManager is a simple secrets manager that stores secrets in an
// unencrypted file. This is for testing/development purposes only.
type BasicManager struct {
	filePath string
	secrets  syncmap.Map // Thread-safe map for storing secrets
}

// GetSecret retrieves a secret from the secret store.
func (b *BasicManager) GetSecret(name string) (string, error) {
	if name == "" {
		return "", errors.New("secret name cannot be empty")
	}

	value, ok := b.secrets.Load(name)
	if !ok {
		return "", fmt.Errorf("secret not found: %s", name)
	}
	return value.(string), nil
}

// SetSecret stores a secret in the secret store.
func (b *BasicManager) SetSecret(name, value string) error {
	if name == "" {
		return errors.New("secret name cannot be empty")
	}

	b.secrets.Store(name, value)
	return b.updateFile()
}

// DeleteSecret removes a secret from the secret store.
func (b *BasicManager) DeleteSecret(name string) error {
	if name == "" {
		return errors.New("secret name cannot be empty")
	}

	// Check if the secret exists first
	_, ok := b.secrets.Load(name)
	if !ok {
		return fmt.Errorf("cannot delete non-existent secret: %s", name)
	}

	b.secrets.Delete(name)
	return b.updateFile()
}

// ListSecrets returns a list of all secret names stored in the manager.
func (b *BasicManager) ListSecrets() ([]string, error) {
	var secretNames []string

	b.secrets.Range(func(key, _ interface{}) bool {
		secretNames = append(secretNames, key.(string))
		return true
	})

	return secretNames, nil
}

// Cleanup removes all secrets managed by this manager.
func (b *BasicManager) Cleanup() error {
	// Create a new empty syncmap.Map
	b.secrets = syncmap.Map{}

	// Update the file to reflect the empty state
	return b.updateFile()
}

func (b *BasicManager) updateFile() error {
	// Convert syncmap.Map to map[string]string for JSON marshaling
	secretsMap := make(map[string]string)
	b.secrets.Range(func(key, value interface{}) bool {
		secretsMap[key.(string)] = value.(string)
		return true
	})

	contents, err := json.Marshal(fileStructure{Secrets: secretsMap})
	if err != nil {
		return fmt.Errorf("failed to marshal secrets: %w", err)
	}

	secretsFile, err := os.OpenFile(b.filePath, os.O_CREATE|os.O_RDWR|os.O_TRUNC, 0600)
	if err != nil {
		return fmt.Errorf("failed to open secrets file: %w", err)
	}
	defer secretsFile.Close()

	_, err = secretsFile.Write(contents)
	if err != nil {
		return fmt.Errorf("failed to write secrets to file: %w", err)
	}
	return nil
}

// fileStructure is the structure of the secrets file.
type fileStructure struct {
	Secrets map[string]string `json:"secrets"`
}

// NewBasicManager creates an instance of BasicManager.
func NewBasicManager(filePath string) (Manager, error) {
	// Add warning for production use
	if os.Getenv("ENVIRONMENT") == "production" {
		log.Println("WARNING: BasicManager is not secure for production use")
	}

	filePath = path.Clean(filePath)
	// #nosec G304: File path is not configurable at this time.
	secretsFile, err := os.OpenFile(filePath, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, fmt.Errorf("failed to open secrets file: %w", err)
	}
	defer secretsFile.Close()

	stat, err := secretsFile.Stat()
	if err != nil {
		return nil, fmt.Errorf("failed to stat secrets file: %w", err)
	}

	// Create a new BasicManager with an empty syncmap.Map
	manager := &BasicManager{
		filePath: filePath,
		secrets:  syncmap.Map{},
	}

	// If the file is not empty, load the secrets into the syncmap.Map
	if stat.Size() > 0 {
		var contents fileStructure
		err := json.NewDecoder(secretsFile).Decode(&contents)
		if err != nil {
			return nil, fmt.Errorf("failed to decode secrets file: %w", err)
		}

		// Store each secret in the syncmap.Map
		for key, value := range contents.Secrets {
			manager.secrets.Store(key, value)
		}
	}

	return manager, nil
}

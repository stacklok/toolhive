package secrets

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path"
	"sync"

	"github.com/adrg/xdg"
)

// BasicManager is a simple secrets manager that stores secrets in an
// unencrypted file. This is for testing/development purposes only.
type BasicManager struct {
	filePath string
	secrets  map[string]string
	mu       sync.RWMutex // Protects concurrent access to secrets map
}

// GetSecret retrieves a secret from the secret store.
func (b *BasicManager) GetSecret(name string) (string, error) {
	if name == "" {
		return "", errors.New("secret name cannot be empty")
	}

	b.mu.RLock()
	defer b.mu.RUnlock()

	value, ok := b.secrets[name]
	if !ok {
		return "", fmt.Errorf("secret not found: %s", name)
	}
	return value, nil
}

// SetSecret stores a secret in the secret store.
func (b *BasicManager) SetSecret(name, value string) error {
	if name == "" {
		return errors.New("secret name cannot be empty")
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	b.secrets[name] = value
	return b.updateFile()
}

// DeleteSecret removes a secret from the secret store.
func (b *BasicManager) DeleteSecret(name string) error {
	if name == "" {
		return errors.New("secret name cannot be empty")
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	if _, exists := b.secrets[name]; !exists {
		return fmt.Errorf("cannot delete non-existent secret: %s", name)
	}

	delete(b.secrets, name)
	return b.updateFile()
}

// ListSecrets returns a list of all secret names stored in the manager.
func (b *BasicManager) ListSecrets() ([]string, error) {
	b.mu.RLock()
	defer b.mu.RUnlock()

	secretNames := make([]string, 0, len(b.secrets))
	for name := range b.secrets {
		secretNames = append(secretNames, name)
	}

	return secretNames, nil
}

func (b *BasicManager) updateFile() error {
	contents, err := json.Marshal(fileStructure{Secrets: b.secrets})
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

// BasicManagerFactory is an implementation of the ManagerFactory interface for BasicManager.
type BasicManagerFactory struct{}

// Build creates an instance of BasicManager.
func (BasicManagerFactory) Build(config map[string]interface{}) (Manager, error) {
	filePath, ok := config["secretsFile"].(string)
	if !ok {
		return nil, errors.New("secretsFile is required")
	}

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

	var secrets map[string]string
	if stat.Size() == 0 {
		secrets = make(map[string]string)
	} else {
		var contents fileStructure
		err := json.NewDecoder(secretsFile).Decode(&contents)
		if err != nil {
			return nil, fmt.Errorf("failed to decode secrets file: %w", err)
		}
		secrets = contents.Secrets
	}

	return &BasicManager{
		filePath: filePath,
		secrets:  secrets,
		mu:       sync.RWMutex{},
	}, nil
}

// CreateDefaultSecretsManager creates a secret manager instance with a default config.
// TODO: Remove once we support more than one provider
func CreateDefaultSecretsManager() (Manager, error) {
	// TODO: make this configurable?
	secretsPath, err := xdg.DataFile("vibetool/secrets")
	if err != nil {
		return nil, fmt.Errorf("unable to access secrets file path %v", err)
	}
	return BasicManagerFactory{}.Build(map[string]any{"secretsFile": secretsPath})
}

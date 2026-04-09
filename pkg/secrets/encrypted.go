// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package secrets

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path"
	"strings"

	"github.com/stacklok/toolhive/pkg/fileutils"
	"github.com/stacklok/toolhive/pkg/secrets/aes"
)

// EncryptedManager stores secrets in an encrypted file.
// AES-256-GCM is used for encryption.
type EncryptedManager struct {
	filePath string
	// Key used to re-encrypt the secrets file if changes are needed.
	key []byte
}

// fileStructure is the structure of the secrets file.
type fileStructure struct {
	Secrets map[string]string `json:"secrets"`
}

// GetSecret retrieves a secret from the secret store.
//
// The file is read and decrypted on every call so that changes written by
// other processes are immediately visible.
func (e *EncryptedManager) GetSecret(_ context.Context, name string) (string, error) {
	if name == "" {
		return "", errors.New("secret name cannot be empty")
	}

	secrets, err := e.readFileSecrets()
	if err != nil {
		return "", fmt.Errorf("reading secrets: %w", err)
	}
	value, ok := secrets[name]
	if !ok {
		return "", fmt.Errorf("%w: %s", ErrSecretNotFound, name)
	}
	return value, nil
}

// SetSecret stores a secret in the secret store.
func (e *EncryptedManager) SetSecret(_ context.Context, name, value string) error {
	if name == "" {
		return errors.New("secret name cannot be empty")
	}

	return fileutils.WithFileLock(e.filePath, func() error {
		// Re-read the file inside the lock to avoid overwriting changes
		// made by other processes since this manager was created.
		secrets, err := e.readFileSecrets()
		if err != nil {
			return err
		}
		secrets[name] = value
		return e.writeFileSecrets(secrets)
	})
}

// DeleteSecret removes a secret from the secret store.
func (e *EncryptedManager) DeleteSecret(_ context.Context, name string) error {
	if name == "" {
		return errors.New("secret name cannot be empty")
	}

	return fileutils.WithFileLock(e.filePath, func() error {
		// Re-read the file inside the lock so the existence check
		// reflects the current on-disk state.
		secrets, err := e.readFileSecrets()
		if err != nil {
			return err
		}
		if _, ok := secrets[name]; !ok {
			return fmt.Errorf("cannot delete non-existent secret: %s", name)
		}
		delete(secrets, name)
		return e.writeFileSecrets(secrets)
	})
}

// ListSecrets returns a list of all secret names stored in the manager.
func (e *EncryptedManager) ListSecrets(_ context.Context) ([]SecretDescription, error) {
	secrets, err := e.readFileSecrets()
	if err != nil {
		return nil, fmt.Errorf("reading secrets: %w", err)
	}
	result := make([]SecretDescription, 0, len(secrets))
	for key := range secrets {
		result = append(result, SecretDescription{Key: key})
	}
	return result, nil
}

// DeleteSecrets removes all named keys from the store.
func (e *EncryptedManager) DeleteSecrets(_ context.Context, keys []string) error {
	return fileutils.WithFileLock(e.filePath, func() error {
		// Re-read the file inside the lock to avoid losing changes made
		// by other processes since this manager was created.
		current, err := e.readFileSecrets()
		if err != nil {
			return err
		}
		for _, key := range keys {
			delete(current, key)
		}
		return e.writeFileSecrets(current)
	})
}

// Cleanup removes all secrets managed by this manager.
func (e *EncryptedManager) Cleanup() error {
	return fileutils.WithFileLock(e.filePath, func() error {
		return e.writeFileSecrets(make(map[string]string))
	})
}

// Capabilities returns the capabilities of the encrypted provider.
func (*EncryptedManager) Capabilities() ProviderCapabilities {
	return ProviderCapabilities{
		CanRead:    true,
		CanWrite:   true,
		CanDelete:  true,
		CanList:    true,
		CanCleanup: true,
	}
}

// readFileSecrets reads and decrypts the secrets file, returning the current
// on-disk secrets. Returns an empty map for an empty or non-existent file.
func (e *EncryptedManager) readFileSecrets() (map[string]string, error) {
	// #nosec G304: File path is not configurable at this time.
	data, err := os.ReadFile(e.filePath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return make(map[string]string), nil
		}
		return nil, fmt.Errorf("failed to read secrets file: %w", err)
	}
	if len(data) == 0 {
		return make(map[string]string), nil
	}

	decrypted, err := aes.Decrypt(data, e.key)
	if err != nil {
		return nil, fmt.Errorf("unable to decrypt secrets file: %w", err)
	}

	var contents fileStructure
	if err := json.Unmarshal(decrypted, &contents); err != nil {
		return nil, fmt.Errorf("failed to decode secrets file: %w", err)
	}
	if contents.Secrets == nil {
		return make(map[string]string), nil
	}
	return contents.Secrets, nil
}

// writeFileSecrets encrypts and atomically writes the secrets map to disk.
// Must be called while holding the file lock.
func (e *EncryptedManager) writeFileSecrets(secrets map[string]string) error {
	contents, err := json.Marshal(fileStructure{Secrets: secrets})
	if err != nil {
		return fmt.Errorf("failed to marshal secrets: %w", err)
	}

	encryptedContents, err := aes.Encrypt(contents, e.key)
	if err != nil {
		return fmt.Errorf("failed to encrypt secrets: %w", err)
	}

	if err := fileutils.AtomicWriteFile(e.filePath, encryptedContents, 0600); err != nil {
		return fmt.Errorf("failed to write secrets to file: %w", err)
	}
	return nil
}

// NewEncryptedManager creates an instance of EncryptedManager.
func NewEncryptedManager(filePath string, key []byte) (Provider, error) {
	if len(key) == 0 {
		return nil, errors.New("key cannot be empty")
	}

	filePath = path.Clean(filePath)

	// Ensure the file exists (create if needed).
	// #nosec G304: File path is not configurable at this time.
	f, err := os.OpenFile(filePath, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, fmt.Errorf("failed to open secrets file: %w", err)
	}
	if err := f.Close(); err != nil {
		slog.Warn("Failed to close secrets file", "error", err)
	}

	manager := &EncryptedManager{
		filePath: filePath,
		key:      key,
	}

	// Validate the file is readable and correctly encrypted at startup.
	stat, err := os.Stat(filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to stat secrets file: %w", err)
	}
	if stat.Size() > 0 {
		if _, err := manager.readFileSecrets(); err != nil {
			if strings.Contains(err.Error(), "unable to decrypt") {
				fmt.Fprintf(os.Stderr, "\nSecrets file decryption failed: this usually means the password "+
					"is incorrect or the secrets file has been corrupted.\n"+
					"If your keyring was recently reset, try again with your original password.\n"+
					"If the secrets file is corrupted, delete it at %s and run 'thv secret setup' to start fresh.\n\n",
					filePath)
			}
			return nil, err
		}
	}

	return manager, nil
}

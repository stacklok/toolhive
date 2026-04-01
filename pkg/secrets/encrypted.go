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

	"golang.org/x/sync/syncmap"

	"github.com/stacklok/toolhive/pkg/fileutils"
	"github.com/stacklok/toolhive/pkg/secrets/aes"
)

// EncryptedManager stores secrets in an encrypted file.
// AES-256-GCM is used for encryption.
type EncryptedManager struct {
	filePath string
	// Key used to re-encrypt the secrets file if changes are needed.
	key     []byte
	secrets syncmap.Map // Thread-safe map for storing secrets
}

// fileStructure is the structure of the secrets file.
type fileStructure struct {
	Secrets map[string]string `json:"secrets"`
}

// GetSecret retrieves a secret from the in-memory cache.
// It does not re-read the file; secrets written by other processes after
// construction may not be visible. This is intentional: CLI invocations
// create a fresh manager per call, and long-running proxies only need
// their own tokens.
func (e *EncryptedManager) GetSecret(_ context.Context, name string) (string, error) {
	if name == "" {
		return "", errors.New("secret name cannot be empty")
	}

	value, ok := e.secrets.Load(name)
	if !ok {
		return "", fmt.Errorf("%w: %s", ErrSecretNotFound, name)
	}
	return value.(string), nil
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
		if err := e.writeFileSecrets(secrets); err != nil {
			return err
		}
		// Update the in-memory cache after the disk write. There is a brief
		// window where a concurrent GetSecret may return a stale value; this
		// is acceptable because the file is the authoritative source of truth.
		e.secrets.Store(name, value)
		return nil
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
			// Evict stale cache entry: another process may have already
			// deleted this key from disk while it remained in our cache.
			e.secrets.Delete(name)
			return fmt.Errorf("cannot delete non-existent secret: %s", name)
		}
		delete(secrets, name)
		if err := e.writeFileSecrets(secrets); err != nil {
			return err
		}
		// Update the in-memory cache after the disk write. There is a brief
		// window where a concurrent GetSecret may return a stale value; this
		// is acceptable because the file is the authoritative source of truth.
		e.secrets.Delete(name)
		return nil
	})
}

// ListSecrets returns a list of all secret names stored in the manager.
func (e *EncryptedManager) ListSecrets(_ context.Context) ([]SecretDescription, error) {
	var secretNames []SecretDescription

	e.secrets.Range(func(key, _ interface{}) bool {
		secretNames = append(secretNames, SecretDescription{Key: key.(string)})
		return true
	})

	return secretNames, nil
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
		if err := e.writeFileSecrets(current); err != nil {
			return err
		}
		// Update in-memory cache after the disk write.
		for _, key := range keys {
			e.secrets.Delete(key)
		}
		return nil
	})
}

// Cleanup removes all secrets managed by this manager.
func (e *EncryptedManager) Cleanup() error {
	return fileutils.WithFileLock(e.filePath, func() error {
		empty := make(map[string]string)
		if err := e.writeFileSecrets(empty); err != nil {
			return err
		}
		// Clear the in-memory cache
		e.secrets.Range(func(key, _ interface{}) bool {
			e.secrets.Delete(key)
			return true
		})
		return nil
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
// Must be called while holding the file lock.
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
	secretsFile, err := os.OpenFile(filePath, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, fmt.Errorf("failed to open secrets file: %w", err)
	}
	if err := secretsFile.Close(); err != nil {
		// Non-fatal: secrets file cleanup failure
		slog.Warn("Failed to close secrets file", "error", err)
	}

	manager := &EncryptedManager{
		filePath: filePath,
		secrets:  syncmap.Map{},
		key:      key,
	}

	// Load the initial snapshot into the in-memory cache.
	secrets, err := manager.readFileSecrets()
	if err != nil {
		if strings.Contains(err.Error(), "unable to decrypt") {
			fmt.Fprintf(os.Stderr, "\nSecrets file decryption failed: this usually means the password "+
				"is incorrect or the secrets file has been corrupted.\n"+
				"If your keyring was recently reset, try again with your original password.\n"+
				"If the secrets file is corrupted, delete it at %s and run 'thv secret setup' to start fresh.\n\n",
				filePath)
		}
		return nil, err
	}
	for k, v := range secrets {
		manager.secrets.Store(k, v)
	}

	return manager, nil
}

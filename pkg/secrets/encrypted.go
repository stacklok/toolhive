// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package secrets

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path"
	"sync"

	"github.com/stacklok/toolhive/pkg/fileutils"
	"github.com/stacklok/toolhive/pkg/secrets/aes"
)

// On-disk framing for the encrypted secrets file:
//
//	magic    "THVSEC"  6 bytes
//	version  0x01      1 byte
//	salt               16 bytes
//	body               nonce|ciphertext|tag, as produced by aes.Encrypt
//
// The version selects the Argon2id cost parameters, which are fixed in code
// (see kdf.go) rather than stored here.
//
// Files without the magic prefix predate this framing: they are the bare output
// of aes.Encrypt under an unsalted SHA-256 of the password. They are read with
// that key and rewritten in the framed format.
const (
	secretsFileMagic   = "THVSEC"
	secretsFileVersion = 0x01

	headerLength = len(secretsFileMagic) + 1 + saltLength
)

// ErrMalformedSecretsFile is returned when the secrets file header cannot be parsed.
var ErrMalformedSecretsFile = errors.New("malformed secrets file")

// ErrDecryptionFailed is returned when the secrets file cannot be decrypted,
// which almost always means the password is wrong.
var ErrDecryptionFailed = errors.New("unable to decrypt secrets file")

// EncryptedManager stores secrets in an encrypted file.
// AES-256-GCM is used for encryption, with the key derived from the password
// using Argon2id and a per-file salt stored in the file header.
type EncryptedManager struct {
	filePath string
	// Password the encryption key is derived from.
	password []byte

	// mu guards the fields below.
	mu sync.Mutex
	// salt of the file as last read or written. Nil until the file's salt is
	// known, which is the case for a new file and for a legacy file that has
	// not been rewritten yet.
	salt []byte
	// legacy records that the data last read was in the pre-framing format.
	legacy bool
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
			return fmt.Errorf("%w: %s", ErrSecretNotFound, name)
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

// NewEncryptedManager creates an instance of EncryptedManager.
//
// The manager takes the password rather than a key because the key derivation
// salt lives in the secrets file itself and is only known once the file is read.
func NewEncryptedManager(filePath string, password []byte) (Provider, error) {
	if len(password) == 0 {
		return nil, errors.New("password cannot be empty")
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
		password: bytes.Clone(password),
	}

	// Validate the file is readable and correctly encrypted at startup.
	stat, err := os.Stat(filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to stat secrets file: %w", err)
	}
	if stat.Size() > 0 {
		if _, err := manager.readFileSecrets(); err != nil {
			printSecretsFileHint(filePath, err)
			return nil, err
		}
		manager.migrateLegacyFile()
	}

	return manager, nil
}

// printSecretsFileHint explains why the secrets file could not be opened. The
// underlying error surfaces deep inside a CLI command, where it is easy to
// mistake for an unrelated failure.
func printSecretsFileHint(filePath string, err error) {
	switch {
	case errors.Is(err, ErrDecryptionFailed):
		fmt.Fprintf(os.Stderr, "\nSecrets file decryption failed: this usually means the password "+
			"is incorrect or the secrets file has been corrupted.\n"+
			"If your keyring was recently reset, try again with your original password.\n"+
			"If the secrets file is corrupted, delete it at %s and run 'thv secret setup' to start fresh.\n\n",
			filePath)
	case errors.Is(err, ErrMalformedSecretsFile):
		fmt.Fprintf(os.Stderr, "\nThe secrets file at %s is not in a format this version of thv understands.\n"+
			"If it was written by a newer version of thv, upgrade. Otherwise the file has been corrupted; "+
			"delete it and run 'thv secret setup' to start fresh.\n\n", filePath)
	}
}

// migrateLegacyFile rewrites a pre-framing secrets file in the framed format, so
// that it is protected by Argon2id rather than by an unsalted SHA-256.
//
// This happens when the file is opened rather than on its next write: a file
// that is only ever read would otherwise keep the weaker derivation forever,
// which is the common case for workload credential injection.
//
// It is best effort. A read-only filesystem or a full disk leaves the file in
// the legacy format, which this version still reads, so a failure is logged
// rather than returned.
func (e *EncryptedManager) migrateLegacyFile() {
	if !e.isLegacy() {
		return
	}

	err := fileutils.WithFileLock(e.filePath, func() error {
		// Re-read inside the lock: another process may have migrated already.
		secrets, err := e.readFileSecrets()
		if err != nil || !e.isLegacy() {
			return err
		}
		return e.writeFileSecrets(secrets)
	})
	if err != nil {
		slog.Debug("Could not migrate secrets file to salted key derivation",
			"path", e.filePath, "error", err)
		return
	}
	slog.Debug("Migrated secrets file to salted key derivation", "path", e.filePath)
}

// isLegacy reports whether the data last read was in the pre-framing format.
func (e *EncryptedManager) isLegacy() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.legacy
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

	key, body, err := e.decryptionKey(data)
	if err != nil {
		return nil, err
	}

	decrypted, err := aes.Decrypt(body, key)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrDecryptionFailed, err)
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

// decryptionKey returns the key that decrypts the given file contents, along
// with the body that key applies to.
//
// Framed files carry their salt in the header. Files without the magic prefix
// are in the legacy format and use an unsalted SHA-256 of the password.
func (e *EncryptedManager) decryptionKey(data []byte) ([]byte, []byte, error) {
	if !bytes.HasPrefix(data, []byte(secretsFileMagic)) {
		// Legacy files were encrypted with an unsalted SHA-256 of the password.
		// Retained only so that they can be read and then migrated.
		sum := sha256.Sum256(e.password)
		e.setFileState(nil, true)
		return sum[:], data, nil
	}

	salt, body, err := parseSecretsHeader(data)
	if err != nil {
		return nil, nil, err
	}
	e.setFileState(salt, false)

	return deriveKeyCached(e.filePath, e.password, salt), body, nil
}

// writeFileSecrets encrypts and atomically writes the secrets map to disk in
// the framed format. Must be called while holding the file lock.
func (e *EncryptedManager) writeFileSecrets(secrets map[string]string) error {
	contents, err := json.Marshal(fileStructure{Secrets: secrets})
	if err != nil {
		return fmt.Errorf("failed to marshal secrets: %w", err)
	}

	salt, err := e.saltForWrite()
	if err != nil {
		return err
	}

	body, err := aes.Encrypt(contents, deriveKeyCached(e.filePath, e.password, salt))
	if err != nil {
		return fmt.Errorf("failed to encrypt secrets: %w", err)
	}

	if err := fileutils.AtomicWriteFile(e.filePath, append(encodeSecretsHeader(salt), body...), 0600); err != nil {
		return fmt.Errorf("failed to write secrets to file: %w", err)
	}

	// Only now that the write is durable does the manager's view of the file change.
	e.setFileState(salt, false)
	return nil
}

// saltForWrite returns the salt to write with: the file's existing salt when one
// is known, or a fresh one for a new file or a legacy file being migrated.
func (e *EncryptedManager) saltForWrite() ([]byte, error) {
	e.mu.Lock()
	salt := e.salt
	e.mu.Unlock()

	if len(salt) > 0 {
		return salt, nil
	}
	return generateSalt()
}

// setFileState records what the manager last saw on disk.
func (e *EncryptedManager) setFileState(salt []byte, legacy bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.salt, e.legacy = salt, legacy
}

// encodeSecretsHeader builds the fixed-size header that precedes the body.
func encodeSecretsHeader(salt []byte) []byte {
	header := make([]byte, 0, headerLength)
	header = append(header, secretsFileMagic...)
	header = append(header, secretsFileVersion)
	return append(header, salt...)
}

// parseSecretsHeader parses a framed secrets file, returning its salt and the
// encrypted body. The caller must have already checked the magic prefix.
func parseSecretsHeader(data []byte) ([]byte, []byte, error) {
	if len(data) < headerLength {
		return nil, nil, fmt.Errorf("%w: header is truncated (%d bytes)", ErrMalformedSecretsFile, len(data))
	}
	if version := data[len(secretsFileMagic)]; version != secretsFileVersion {
		return nil, nil, fmt.Errorf("%w: unsupported format version %d", ErrMalformedSecretsFile, version)
	}
	// Clone the salt so that the retained copy does not pin the whole file buffer.
	return bytes.Clone(data[len(secretsFileMagic)+1 : headerLength]), data[headerLength:], nil
}

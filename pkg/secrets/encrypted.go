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
	"maps"
	"os"
	"path"

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
// of aes.Encrypt under an unsalted SHA-256 of the password. Ordinary operations
// preserve that format for compatibility; UpgradeProtection explicitly upgrades it.
const (
	secretsFileMagic   = "THVSEC"
	secretsFileVersion = 0x01

	headerLength = len(secretsFileMagic) + 1 + saltLength
)

// ErrMalformedSecretsFile is returned when the secrets file header cannot be parsed.
var ErrMalformedSecretsFile = errors.New("malformed secrets file")

// ErrUnsupportedSecretsFileVersion is returned when the secrets file uses a
// framed format version this binary does not understand.
var ErrUnsupportedSecretsFileVersion = errors.New("unsupported secrets file format version")

// ErrDecryptionFailed is returned when the secrets file cannot be decrypted,
// which almost always means the password is wrong.
var ErrDecryptionFailed = errors.New("unable to decrypt secrets file")

type fileFormatVersion uint8

const (
	fileFormatEmpty fileFormatVersion = iota
	fileFormatLegacy
	fileFormatV1
)

// fileFormat describes the on-disk format returned by a read. Its salt is set
// only for framed version 1 files.
type fileFormat struct {
	version fileFormatVersion
	salt    []byte
}

// EncryptedManager stores secrets in an encrypted file.
// AES-256-GCM is used for encryption, with the key derived from the password
// using Argon2id and a per-file salt stored in the file header.
type EncryptedManager struct {
	filePath string
	// Password the encryption key is derived from.
	password []byte
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

	secrets, _, err := e.readFileSecrets()
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
		secrets, format, err := e.readFileSecrets()
		if err != nil {
			return err
		}
		secrets[name] = value
		return e.writeFileSecrets(secrets, format)
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
		secrets, format, err := e.readFileSecrets()
		if err != nil {
			return err
		}
		if _, ok := secrets[name]; !ok {
			return fmt.Errorf("%w: %s", ErrSecretNotFound, name)
		}
		delete(secrets, name)
		return e.writeFileSecrets(secrets, format)
	})
}

// ListSecrets returns a list of all secret names stored in the manager.
func (e *EncryptedManager) ListSecrets(_ context.Context) ([]SecretDescription, error) {
	secrets, _, err := e.readFileSecrets()
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
		current, format, err := e.readFileSecrets()
		if err != nil {
			return err
		}
		for _, key := range keys {
			delete(current, key)
		}
		return e.writeFileSecrets(current, format)
	})
}

// Cleanup removes all secrets managed by this manager.
func (e *EncryptedManager) Cleanup() error {
	return fileutils.WithFileLock(e.filePath, func() error {
		// Re-read under the lock so the replacement retains the current format.
		_, format, err := e.readFileSecrets()
		if err != nil {
			return err
		}
		return e.writeFileSecrets(make(map[string]string), format)
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

// UpgradeProtection explicitly upgrades a legacy store to the current framed
// format. It authenticates the original bytes, installs the upgraded file
// atomically, and verifies the installed file before returning success.
func (e *EncryptedManager) UpgradeProtection() error {
	return fileutils.WithFileLock(e.filePath, func() error {
		original, err := os.ReadFile(e.filePath) // #nosec G304: File path is not configurable at this time.
		if err != nil {
			return fmt.Errorf("reading secrets file for upgrade: %w", err)
		}
		secrets, format, err := e.decodeSecretsFile(original)
		if err != nil {
			return fmt.Errorf("authenticating secrets file for upgrade: %w", err)
		}
		if format.version != fileFormatLegacy {
			return nil
		}
		newSalt, err := generateSalt()
		if err != nil {
			return err
		}
		if err := e.installUpgrade(secrets, fileFormat{version: fileFormatV1, salt: newSalt}); err != nil {
			return err
		}
		return e.verifyUpgradeOrRollback(original, secrets)
	})
}

// installUpgrade writes secrets in the given (already-migrated) format. Split
// out from UpgradeProtection so tests can install a real upgrade and then
// corrupt the result on disk before exercising verifyUpgradeOrRollback,
// which is the only way to deterministically reach the rollback branch
// without a production-only test seam.
func (e *EncryptedManager) installUpgrade(secrets map[string]string, newFormat fileFormat) error {
	return e.writeFileSecrets(secrets, newFormat)
}

// verifyUpgradeOrRollback re-reads what installUpgrade just wrote and confirms
// it decrypts back to want under the new format. On any mismatch — a failed
// read, a version that isn't fileFormatV1, or decrypted secrets that differ —
// it restores original, the pre-upgrade encrypted bytes, so a verification
// failure never leaves the store in a state only the failed write can read.
func (e *EncryptedManager) verifyUpgradeOrRollback(original []byte, want map[string]string) error {
	verified, verifiedFormat, err := e.readFileSecrets()
	if err == nil && verifiedFormat.version == fileFormatV1 && maps.Equal(want, verified) {
		return nil
	}
	verifyErr := err
	if verifyErr == nil {
		verifyErr = errors.New("decrypted secrets do not match")
	}
	rollbackErr := fileutils.AtomicWriteFile(e.filePath, original, 0600)
	if rollbackErr != nil {
		return fmt.Errorf("verifying upgraded secrets file: %w; restoring original encrypted bytes: %v", verifyErr, rollbackErr)
	}
	return fmt.Errorf("verifying upgraded secrets file: %w; restored original encrypted bytes", verifyErr)
}

// NewEncryptedManager creates an instance of EncryptedManager.
//
// password is the user's password, not a derived key. Callers that previously
// passed sha256(password) still compile against this signature but will fail to
// decrypt, because that value is now hashed again as if it were a password.
//
// The manager takes the password rather than a key because the key derivation
// salt lives in the secrets file itself and is only known once the file is read.
//
// Opening an existing store authenticates that it is readable, but does not
// change its format. Use UpgradeProtection to explicitly upgrade legacy files.
func NewEncryptedManager(filePath string, password []byte) (Provider, error) {
	return newEncryptedManager(filePath, password)
}

func newEncryptedManager(filePath string, password []byte) (*EncryptedManager, error) {
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
		if _, _, err := manager.readFileSecrets(); err != nil {
			printSecretsFileHint(filePath, err)
			return nil, err
		}
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
	case errors.Is(err, ErrUnsupportedSecretsFileVersion):
		fmt.Fprintf(os.Stderr, "\nThe secrets file at %s was written by a newer version of thv. "+
			"Upgrade thv to access it.\n\n", filePath)
	case errors.Is(err, ErrMalformedSecretsFile):
		fmt.Fprintf(os.Stderr, "\nThe secrets file at %s is not in a format this version of thv understands.\n"+
			"If it was written by a newer version of thv, upgrade. Otherwise the file has been corrupted; "+
			"delete it and run 'thv secret setup' to start fresh.\n\n", filePath)
	}
}

// readFileSecrets reads and decrypts the on-disk secrets, returning the format
// required to preserve it on an ordinary write.
func (e *EncryptedManager) readFileSecrets() (map[string]string, fileFormat, error) {
	// #nosec G304: File path is not configurable at this time.
	data, err := os.ReadFile(e.filePath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return make(map[string]string), fileFormat{version: fileFormatEmpty}, nil
		}
		return nil, fileFormat{}, fmt.Errorf("failed to read secrets file: %w", err)
	}
	return e.decodeSecretsFile(data)
}

// decodeSecretsFile authenticates and decodes encrypted file bytes.
func (e *EncryptedManager) decodeSecretsFile(data []byte) (map[string]string, fileFormat, error) {
	if len(data) == 0 {
		return make(map[string]string), fileFormat{version: fileFormatEmpty}, nil
	}
	key, body, format, err := e.decryptionKey(data)
	if err != nil {
		return nil, fileFormat{}, err
	}
	decrypted, err := aes.Decrypt(body, key)
	if err != nil {
		return nil, fileFormat{}, fmt.Errorf("%w: %w", ErrDecryptionFailed, err)
	}
	var contents fileStructure
	if err := json.Unmarshal(decrypted, &contents); err != nil {
		return nil, fileFormat{}, fmt.Errorf("failed to decode secrets file: %w", err)
	}
	if contents.Secrets == nil {
		return make(map[string]string), format, nil
	}
	return contents.Secrets, format, nil
}

func (e *EncryptedManager) decryptionKey(data []byte) ([]byte, []byte, fileFormat, error) {
	if !bytes.HasPrefix(data, []byte(secretsFileMagic)) {
		sum := sha256.Sum256(e.password)
		return sum[:], data, fileFormat{version: fileFormatLegacy}, nil
	}
	salt, body, err := parseSecretsHeader(data)
	if err != nil {
		return nil, nil, fileFormat{}, err
	}
	format := fileFormat{version: fileFormatV1, salt: salt}
	return deriveKeyCached(e.filePath, e.password, salt), body, format, nil
}

// writeFileSecrets encrypts and atomically writes the secrets map using format.
// It must be called while holding the file lock.
func (e *EncryptedManager) writeFileSecrets(secrets map[string]string, format fileFormat) error {
	contents, err := json.Marshal(fileStructure{Secrets: secrets})
	if err != nil {
		return fmt.Errorf("failed to marshal secrets: %w", err)
	}
	if format.version == fileFormatLegacy {
		sum := sha256.Sum256(e.password)
		body, err := aes.Encrypt(contents, sum[:])
		if err != nil {
			return fmt.Errorf("failed to encrypt secrets: %w", err)
		}
		if err := fileutils.AtomicWriteFile(e.filePath, body, 0600); err != nil {
			return fmt.Errorf("failed to write secrets to file: %w", err)
		}
		return nil
	}
	salt := format.salt
	if format.version == fileFormatEmpty {
		salt, err = generateSalt()
		if err != nil {
			return err
		}
	}
	body, err := aes.Encrypt(contents, deriveKeyCached(e.filePath, e.password, salt))
	if err != nil {
		return fmt.Errorf("failed to encrypt secrets: %w", err)
	}
	if err := fileutils.AtomicWriteFile(e.filePath, append(encodeSecretsHeader(salt), body...), 0600); err != nil {
		return fmt.Errorf("failed to write secrets to file: %w", err)
	}
	return nil
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
		return nil, nil, fmt.Errorf("%w: %d", ErrUnsupportedSecretsFileVersion, version)
	}
	// Clone the salt so that the retained copy does not pin the whole file buffer.
	return bytes.Clone(data[len(secretsFileMagic)+1 : headerLength]), data[headerLength:], nil
}

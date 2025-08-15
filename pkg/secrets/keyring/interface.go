package keyring

import "errors"

// ErrNotFound indicates that the requested key was not found
var ErrNotFound = errors.New("key not found")

// Provider defines the interface for keyring backends
type Provider interface {
	// Set stores a key-value pair in the keyring
	Set(service, key, value string) error

	// Get retrieves a value from the keyring
	Get(service, key string) (string, error)

	// Delete removes a specific key from the keyring
	Delete(service, key string) error

	// DeleteAll removes all keys for a service from the keyring
	DeleteAll(service string) error

	// IsAvailable tests if this keyring backend is functional
	IsAvailable() bool

	// Name returns a human-readable name for this backend
	Name() string
}

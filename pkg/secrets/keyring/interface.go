package keyring

import "errors"

// ErrNotFound is returned when a requested key is not found in the keyring
var ErrNotFound = errors.New("key not found")

// Provider defines the interface for keyring backends
type Provider interface {
	Set(service, key, value string) error

	Get(service, key string) (string, error)

	Delete(service, key string) error

	DeleteAll(service string) error

	IsAvailable() bool

	Name() string
}

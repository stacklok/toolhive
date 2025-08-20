package keyring

import (
	"errors"
	"runtime"

	"github.com/zalando/go-keyring"
)

type dbusWrapperProvider struct{}

// NewZalandoKeyringProvider creates a new provider that wraps zalando/go-keyring.
// This provider supports macOS Keychain, Windows Credential Manager, and Linux D-Bus Secret Service.
func NewZalandoKeyringProvider() Provider {
	return &dbusWrapperProvider{}
}

func (*dbusWrapperProvider) Set(service, key, value string) error {
	return keyring.Set(service, key, value)
}

func (*dbusWrapperProvider) Get(service, key string) (string, error) {
	value, err := keyring.Get(service, key)
	if errors.Is(err, keyring.ErrNotFound) {
		return "", ErrNotFound
	}
	return value, err
}

func (*dbusWrapperProvider) Delete(service, key string) error {
	return keyring.Delete(service, key)
}

func (*dbusWrapperProvider) DeleteAll(service string) error {
	return keyring.DeleteAll(service)
}

func (*dbusWrapperProvider) IsAvailable() bool {
	testKey := "toolhive-keyring-test"
	testValue := "test"

	if err := keyring.Set("toolhive-test", testKey, testValue); err != nil {
		return false
	}

	_ = keyring.Delete("toolhive-test", testKey)
	return true
}

func (*dbusWrapperProvider) Name() string {
	switch runtime.GOOS {
	case "darwin":
		return "macOS Keychain"
	case "windows":
		return "Windows Credential Manager"
	case linuxOS:
		return "D-Bus Secret Service"
	default:
		return "Platform Keyring"
	}
}

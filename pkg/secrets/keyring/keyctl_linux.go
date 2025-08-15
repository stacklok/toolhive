//go:build linux
// +build linux

package keyring

import (
	"fmt"
	"sync"

	"golang.org/x/sys/unix"
)

type keyctlProvider struct {
	ringID int
	mu     sync.RWMutex
	keys   map[string]map[string]int // service -> key -> keyid mapping
}

func NewKeyctlProvider() (Provider, error) {
	// Use user keyring for persistence across process invocations
	ringID, err := unix.KeyctlGetKeyringID(unix.KEY_SPEC_USER_KEYRING, false)
	if err != nil {
		return nil, fmt.Errorf("could not get user keyring: %w", err)
	}

	// Link to thread keyring for reads
	_, err = unix.KeyctlInt(unix.KEYCTL_LINK, ringID, unix.KEY_SPEC_THREAD_KEYRING, 0, 0)
	if err != nil {
		return nil, fmt.Errorf("unable to link user keyring to thread keyring: %w", err)
	}

	return &keyctlProvider{
		ringID: ringID,
		keys:   make(map[string]map[string]int),
	}, nil
}

func (k *keyctlProvider) Set(service, key, value string) error {
	k.mu.Lock()
	defer k.mu.Unlock()

	keyName := fmt.Sprintf("%s:%s", service, key)
	keyID, err := unix.AddKey("user", keyName, []byte(value), k.ringID)
	if err != nil {
		return fmt.Errorf("failed to set key '%s' in user keyring: %w", keyName, err)
	}

	// Track the key for deletion
	if k.keys[service] == nil {
		k.keys[service] = make(map[string]int)
	}
	k.keys[service][key] = keyID

	return nil
}

func (k *keyctlProvider) Get(service, key string) (string, error) {
	k.mu.RLock()
	defer k.mu.RUnlock()

	keyName := fmt.Sprintf("%s:%s", service, key)
	keyID, err := unix.KeyctlSearch(k.ringID, "user", keyName, 0)
	if err != nil {
		// Key not found
		return "", ErrNotFound
	}

	bufSize := 2048
	buf := make([]byte, bufSize)
	readBytes, err := unix.KeyctlBuffer(unix.KEYCTL_READ, keyID, buf, bufSize)
	if err != nil {
		return "", fmt.Errorf("read of key '%s' failed: %w", keyName, err)
	}

	if readBytes > bufSize {
		return "", fmt.Errorf("buffer too small for keyring payload")
	}

	return string(buf[:readBytes]), nil
}

func (k *keyctlProvider) Delete(service, key string) error {
	k.mu.Lock()
	defer k.mu.Unlock()

	keyName := fmt.Sprintf("%s:%s", service, key)
	keyID, err := unix.KeyctlSearch(k.ringID, "user", keyName, 0)
	if err != nil {
		// Key not found - this is not an error for Delete
		return nil
	}

	_, err = unix.KeyctlInt(unix.KEYCTL_REVOKE, keyID, 0, 0, 0)
	if err != nil {
		return fmt.Errorf("failed to delete key '%s': %w", keyName, err)
	}

	// Remove from tracking
	if serviceKeys, exists := k.keys[service]; exists {
		delete(serviceKeys, key)
		if len(serviceKeys) == 0 {
			delete(k.keys, service)
		}
	}

	return nil
}

func (k *keyctlProvider) DeleteAll(service string) error {
	k.mu.Lock()
	defer k.mu.Unlock()

	serviceKeys, exists := k.keys[service]
	if !exists {
		return nil // No keys to delete
	}

	var lastErr error
	for key := range serviceKeys {
		if err := k.Delete(service, key); err != nil {
			lastErr = err
		}
	}

	delete(k.keys, service)
	return lastErr
}

func (k *keyctlProvider) IsAvailable() bool {
	testKey := "toolhive-keyring-test"
	testValue := "test"

	if err := k.Set("toolhive-test", testKey, testValue); err != nil {
		return false
	}

	_ = k.Delete("toolhive-test", testKey)
	return true
}

func (k *keyctlProvider) Name() string {
	return "Linux Keyctl"
}

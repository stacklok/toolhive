// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

//go:build linux

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

// NewKeyctlProvider creates a new keyring provider using Linux keyctl.
// It initializes access to the user keyring for persistence across process invocations.
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
	return k.deleteKeyUnlocked(service, key)
}

// deleteKeyUnlocked performs the actual key deletion without acquiring the lock.
// This is used internally by Delete and DeleteAll to avoid deadlocks.
func (k *keyctlProvider) deleteKeyUnlocked(service, key string) error {
	keyName := fmt.Sprintf("%s:%s", service, key)
	keyID, err := unix.KeyctlSearch(k.ringID, "user", keyName, 0)
	if err != nil {
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

	// Always try to delete the known key pattern from kernel keyring
	// regardless of what's in the in-memory map. This ensures cross-process
	// deletion works since the in-memory map is not persisted.
	if err := k.deleteKeyUnlocked(service, service); err != nil {
		return err
	}

	// Also delete any keys tracked in the in-memory map (for keys added in this process)
	if serviceKeys, exists := k.keys[service]; exists {
		var lastErr error
		for key := range serviceKeys {
			if key == service {
				// Already deleted above
				continue
			}
			if err := k.deleteKeyUnlocked(service, key); err != nil {
				lastErr = err
			}
		}
		delete(k.keys, service)
		if lastErr != nil {
			return lastErr
		}
	}

	return nil
}

func (k *keyctlProvider) IsAvailable() bool {
	// Use a unique test key to avoid conflicts with concurrent calls
	testKey := GenerateUniqueTestKey()
	testValue := "test"

	if err := k.Set("toolhive-test", testKey, testValue); err != nil {
		return false
	}

	_ = k.Delete("toolhive-test", testKey)
	return true
}

func (*keyctlProvider) Name() string {
	return "Linux Keyctl"
}

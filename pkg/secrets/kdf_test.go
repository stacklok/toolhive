// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package secrets

import (
	"crypto/sha256"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDeriveKey(t *testing.T) {
	t.Parallel()

	password := []byte("correct horse battery staple")
	otherPassword := []byte("correct horse battery stapl3")
	salt := []byte("0123456789abcdef")
	otherSalt := []byte("fedcba9876543210")

	key := deriveKey(password, salt)

	assert.Len(t, key, derivedKeyLength, "The derived key should be usable as an AES-256 key")
	assert.Equal(t, key, deriveKey(password, salt), "Deriving twice from the same inputs should give the same key")
	assert.NotEqual(t, key, deriveKey(password, otherSalt), "A different salt should give a different key")
	assert.NotEqual(t, key, deriveKey(otherPassword, salt), "A different password should give a different key")

	// The point of the change: the key is no longer a plain hash of the password.
	legacy := sha256.Sum256(password)
	assert.NotEqual(t, legacy[:], key, "The derived key should not be an unsalted SHA-256 of the password")
}

func TestGenerateSalt(t *testing.T) {
	t.Parallel()

	salt, err := generateSalt()
	require.NoError(t, err, "Generating a salt should not return an error")
	assert.Len(t, salt, saltLength, "The salt should be saltLength bytes")

	other, err := generateSalt()
	require.NoError(t, err, "Generating a second salt should not return an error")
	assert.NotEqual(t, salt, other, "Two generated salts should differ")
}

func TestDeriveKeyCached(t *testing.T) {
	t.Parallel()

	password := []byte("a-password")
	otherPassword := []byte("another-password")
	salt := []byte("0123456789abcdef")
	path := t.Name()

	key := deriveKeyCached(path, password, salt)
	assert.Equal(t, deriveKey(password, salt), key, "The cache should return the same key deriveKey would")
	assert.Equal(t, key, deriveKeyCached(path, password, salt), "A cache hit should return the same key")

	// A cache keyed only by path and salt would hand the first password's key to
	// the second, and the correct password would then fail to decrypt.
	assert.Equal(t, deriveKey(otherPassword, salt), deriveKeyCached(path, otherPassword, salt),
		"A different password for the same file must derive its own key")
}

func TestDeriveKeyCached_Concurrent(t *testing.T) {
	t.Parallel()

	password := []byte("a-password")
	salt := []byte("0123456789abcdef")
	path := t.Name()
	want := deriveKey(password, salt)

	const goroutines = 8
	keys := make([][]byte, goroutines)

	var wg sync.WaitGroup
	for i := range goroutines {
		wg.Add(1)
		go func() {
			defer wg.Done()
			keys[i] = deriveKeyCached(path, password, salt)
		}()
	}

	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("timeout waiting for concurrent derivations")
	}

	for i, got := range keys {
		assert.Equal(t, want, got, "Goroutine %d should derive the same key", i)
	}
}

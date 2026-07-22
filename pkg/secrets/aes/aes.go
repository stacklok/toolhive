// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package aes contains functions for encrypting and decrypting data using AES-GCM
package aes

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"io"
)

const maxPlaintextSize = 32 * 1024 * 1024

// ErrExceedsMaxSize is returned when the plaintext is too large to encrypt.
var ErrExceedsMaxSize = errors.New("plaintext is too large, limited to 32MiB")

// Encrypt encrypts data using 256-bit AES-GCM.  This both hides the content of
// the data and provides a check that it hasn't been altered. Output takes the
// form nonce|ciphertext|tag where '|' indicates concatenation.
func Encrypt(plaintext []byte, key []byte) ([]byte, error) {
	return EncryptWithAAD(plaintext, key, nil)
}

// EncryptWithAAD is Encrypt with additional authenticated data (may be nil).
// The AAD is authenticated but not encrypted: decryption with different AAD
// fails, which cryptographically binds the ciphertext to its context (e.g.
// the storage key a value is written under).
func EncryptWithAAD(plaintext, key, aad []byte) ([]byte, error) {
	if len(plaintext) > maxPlaintextSize {
		return nil, ErrExceedsMaxSize
	}

	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, err
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}

	nonce := make([]byte, gcm.NonceSize())
	_, err = io.ReadFull(rand.Reader, nonce)
	if err != nil {
		return nil, err
	}

	return gcm.Seal(nonce, nonce, plaintext, aad), nil
}

// Decrypt decrypts data using 256-bit AES-GCM.  This both hides the content of
// the data and provides a check that it hasn't been altered. Expects input
// form nonce|ciphertext|tag where '|' indicates concatenation.
func Decrypt(ciphertext []byte, key []byte) ([]byte, error) {
	return DecryptWithAAD(ciphertext, key, nil)
}

// DecryptWithAAD is Decrypt with additional authenticated data; it must match
// the AAD passed to EncryptWithAAD or decryption fails.
func DecryptWithAAD(ciphertext, key, aad []byte) ([]byte, error) {
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, err
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}

	if len(ciphertext) < gcm.NonceSize() {
		return nil, errors.New("malformed ciphertext")
	}

	return gcm.Open(nil,
		ciphertext[:gcm.NonceSize()],
		ciphertext[gcm.NonceSize():],
		aad,
	)
}

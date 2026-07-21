// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package tokenenc implements envelope encryption for upstream OAuth tokens
// stored at rest in Redis.
//
// It lives at pkg/authserver/tokenenc (not under storage/internal) because two
// sibling packages must reference the Keyring type: pkg/authserver/storage
// (seal/open on the Redis value path) and pkg/authserver/runner (keyring
// construction from serializable config). Callers should treat this package
// as an implementation detail of the auth-server storage layer, not a
// general-purpose encryption API.
//
// Scheme: a fresh random 256-bit data encryption key (DEK) is generated per
// sealed record. The plaintext is encrypted with AES-256-GCM under the DEK,
// and the DEK is itself encrypted ("wrapped") under a key-encryption key (KEK)
// resolved from a Keyring. The stored value is a JSON envelope:
//
//	{"v":1,"kid":"k1","edek":"<base64 nonce|wrapped-DEK|tag>","ct":"<base64 nonce|ciphertext|tag>"}
//
// The Redis key under which the envelope is stored is fed as AES-GCM
// additional authenticated data on the payload ciphertext, cryptographically
// binding each ciphertext to its key: copying a value to another key fails
// decryption, defeating cut-and-paste row swaps.
//
// Values that predate encryption (legacy plaintext JSON) are detected by
// envelope-shape inspection and passed through untouched, enabling
// read-old/write-new migration.
package tokenenc

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

const (
	// envelopeVersion is the only supported envelope format version.
	envelopeVersion = 1

	// keySize is the required KEK and DEK size in bytes (AES-256).
	keySize = 32
)

// envelope is the on-the-wire representation of an encrypted token record.
// Nonce layout follows the pkg/secrets/aes convention: nonce|ciphertext|tag.
type envelope struct {
	V    int    `json:"v"`
	KID  string `json:"kid"`
	EDEK string `json:"edek"`
	CT   string `json:"ct"`
}

// Keyring resolves key-encryption keys by ID. Exactly one key is active and
// encrypts new writes; retired keys remain available for decryption only,
// supporting lazy read-old/write-new rotation.
type Keyring interface {
	// Active returns the ID and key used to encrypt new writes.
	Active() (id string, key []byte)
	// ByID resolves a key (active or retired) for decryption. The second
	// return value reports whether the ID is known.
	ByID(id string) (key []byte, ok bool)
}

// staticKeyring is a Keyring backed by a fixed, startup-validated key set.
type staticKeyring struct {
	activeID string
	keys     map[string][]byte
}

// NewStaticKeyring builds a Keyring from configuration. It fails loudly on
// any misconfiguration: no keys, an empty or unknown active ID, duplicate
// handling, or any key that is not exactly 32 bytes. Key material is cloned
// so later mutation of the caller's slices or map cannot corrupt the keyring.
func NewStaticKeyring(activeID string, keys map[string][]byte) (Keyring, error) {
	if len(keys) == 0 {
		return nil, errors.New("token encryption: at least one key is required")
	}
	if activeID == "" {
		return nil, errors.New("token encryption: active key ID is required")
	}
	if _, ok := keys[activeID]; !ok {
		return nil, fmt.Errorf("token encryption: active key ID %q not present in key map", activeID)
	}

	cloned := make(map[string][]byte, len(keys))
	for id, key := range keys {
		if id == "" {
			return nil, errors.New("token encryption: key ID cannot be empty")
		}
		if len(key) != keySize {
			return nil, fmt.Errorf("token encryption: key %q must be %d bytes, got %d", id, keySize, len(key))
		}
		// An all-zero KEK silently encrypts every row with a public constant —
		// worse than plaintext because it looks protected. Reject it.
		allZero := true
		for _, b := range key {
			if b != 0 {
				allZero = false
				break
			}
		}
		if allZero {
			return nil, fmt.Errorf("token encryption: key %q must not be all zero bytes", id)
		}
		cloned[id] = append([]byte(nil), key...)
	}

	return &staticKeyring{activeID: activeID, keys: cloned}, nil
}

// Active returns the active key ID and key.
func (k *staticKeyring) Active() (string, []byte) {
	return k.activeID, k.keys[k.activeID]
}

// ByID resolves a key by ID (active or retired).
func (k *staticKeyring) ByID(id string) ([]byte, bool) {
	key, ok := k.keys[id]
	return key, ok
}

// Seal encrypts plaintext for storage under redisKey. The redisKey is bound
// to the ciphertext as AES-GCM additional authenticated data. The returned
// value is a JSON envelope safe to store as the Redis value.
//
// The Keyring must be non-nil; a nil keyring is a programming error and
// returns an error rather than silently storing plaintext.
func Seal(kr Keyring, redisKey string, plaintext []byte) ([]byte, error) {
	if kr == nil {
		return nil, errors.New("token encryption: keyring is required")
	}

	// Fresh random DEK per record.
	dek := make([]byte, keySize)
	if _, err := io.ReadFull(rand.Reader, dek); err != nil {
		return nil, fmt.Errorf("token encryption: failed to generate data key: %w", err)
	}

	activeID, kek := kr.Active()

	edek, err := gcmSeal(kek, dek, nil)
	if err != nil {
		return nil, fmt.Errorf("token encryption: failed to wrap data key: %w", err)
	}

	ct, err := gcmSeal(dek, plaintext, []byte(redisKey))
	if err != nil {
		return nil, fmt.Errorf("token encryption: failed to encrypt record: %w", err)
	}

	env := envelope{
		V:    envelopeVersion,
		KID:  activeID,
		EDEK: base64.StdEncoding.EncodeToString(edek),
		CT:   base64.StdEncoding.EncodeToString(ct),
	}
	data, err := json.Marshal(env)
	if err != nil {
		return nil, fmt.Errorf("token encryption: failed to marshal envelope: %w", err)
	}
	return data, nil
}

// Open decrypts an envelope previously produced by Seal. redisKey must be the
// key the value is stored under — it is verified as AAD, so a value copied to
// a different key fails decryption.
//
// If value is not an envelope (it is legacy plaintext JSON from before
// encryption was enabled), Open returns it unchanged with legacy=true so the
// caller can unmarshal it directly. The "null" deletion tombstone carries no
// secret and is never sealed; callers must check for it before calling Open.
//
// A nil keyring means encryption is disabled: plaintext passes through
// (legacy=true), but an actual envelope is a hard error — "disabling"
// encryption on an encrypted fleet must fail loudly, never return corrupt
// tokens.
func Open(kr Keyring, redisKey string, value []byte) (plaintext []byte, legacy bool, err error) {
	var env envelope
	if jsonErr := json.Unmarshal(value, &env); jsonErr != nil ||
		env.V != envelopeVersion || env.KID == "" || env.EDEK == "" || env.CT == "" {
		// Not an envelope: legacy plaintext JSON.
		return value, true, nil
	}

	if kr == nil {
		return nil, false, errors.New("token encryption: encrypted value found but no keyring configured")
	}

	kek, ok := kr.ByID(env.KID)
	if !ok {
		return nil, false, fmt.Errorf("token encryption: unknown key ID %q", env.KID)
	}

	edek, err := base64.StdEncoding.DecodeString(env.EDEK)
	if err != nil {
		return nil, false, fmt.Errorf("token encryption: malformed wrapped data key: %w", err)
	}
	dek, err := gcmOpen(kek, edek, nil)
	if err != nil {
		return nil, false, fmt.Errorf("token encryption: failed to unwrap data key: %w", err)
	}

	ct, err := base64.StdEncoding.DecodeString(env.CT)
	if err != nil {
		return nil, false, fmt.Errorf("token encryption: malformed ciphertext: %w", err)
	}
	plaintext, err = gcmOpen(dek, ct, []byte(redisKey))
	if err != nil {
		return nil, false, fmt.Errorf("token encryption: failed to decrypt record: %w", err)
	}

	return plaintext, false, nil
}

// IsLegacyValue reports whether value predates envelope encryption (i.e. Open
// would return it with legacy=true). Callers use it for migration decisions —
// e.g. sealing a plaintext row on rewrite — without attempting decryption.
func IsLegacyValue(value []byte) bool {
	var env envelope
	if jsonErr := json.Unmarshal(value, &env); jsonErr != nil ||
		env.V != envelopeVersion || env.KID == "" || env.EDEK == "" || env.CT == "" {
		return true
	}
	return false
}

// NeedsRotation reports whether value is an envelope sealed under a key ID
// other than the active one. Legacy plaintext and non-envelope values return
// false: their migration path is a full re-seal, not a rotation. A nil
// keyring reports false (rotation is meaningless when encryption is off).
func NeedsRotation(kr Keyring, value []byte) bool {
	if kr == nil {
		return false
	}
	var env envelope
	if jsonErr := json.Unmarshal(value, &env); jsonErr != nil ||
		env.V != envelopeVersion || env.KID == "" || env.EDEK == "" || env.CT == "" {
		return false
	}
	activeID, _ := kr.Active()
	return env.KID != activeID
}

// EnvelopeKeyID extracts the key ID from an envelope value for observability
// (never for security decisions). ok is false for non-envelope values.
func EnvelopeKeyID(value []byte) (kid string, ok bool) {
	var env envelope
	if jsonErr := json.Unmarshal(value, &env); jsonErr != nil ||
		env.V != envelopeVersion || env.KID == "" || env.EDEK == "" || env.CT == "" {
		return "", false
	}
	return env.KID, true
}

// gcmSeal encrypts plaintext with AES-256-GCM under key, returning
// nonce|ciphertext|tag. aad is additional authenticated data (may be nil).
func gcmSeal(key, plaintext, aad []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	return gcm.Seal(nonce, nonce, plaintext, aad), nil
}

// gcmOpen reverses gcmSeal.
func gcmOpen(key, ciphertext, aad []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
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
	return gcm.Open(nil, ciphertext[:gcm.NonceSize()], ciphertext[gcm.NonceSize():], aad)
}

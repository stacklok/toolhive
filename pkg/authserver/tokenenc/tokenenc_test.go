// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package tokenenc

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testKey returns a deterministic 32-byte key derived from seed.
func testKey(seed byte) []byte {
	key := make([]byte, 32)
	for i := range key {
		key[i] = seed + byte(i)
	}
	return key
}

func newTestKeyring(t *testing.T, activeID string, keys map[string][]byte) Keyring {
	t.Helper()
	kr, err := NewStaticKeyring(activeID, keys)
	require.NoError(t, err)
	return kr
}

func TestSealOpen_RoundTrip(t *testing.T) {
	t.Parallel()

	kr := newTestKeyring(t, "k1", map[string][]byte{"k1": testKey(1)})
	plaintext := []byte(`{"access_token":"secret-token-value","refresh_token":"refresh-secret"}`)

	sealed, err := Seal(kr, "test:auth:upstream:sess1:providerA", plaintext)
	require.NoError(t, err)

	// The sealed value is an envelope and contains no plaintext material.
	assert.Contains(t, string(sealed), `"v":1`)
	assert.Contains(t, string(sealed), `"kid":"k1"`)
	assert.NotContains(t, string(sealed), "secret-token-value")
	assert.NotContains(t, string(sealed), "refresh-secret")

	opened, legacy, err := Open(kr, "test:auth:upstream:sess1:providerA", sealed)
	require.NoError(t, err)
	assert.False(t, legacy)
	assert.Equal(t, plaintext, opened)
}

func TestOpen_UnknownKeyID(t *testing.T) {
	t.Parallel()

	sealKR := newTestKeyring(t, "k1", map[string][]byte{"k1": testKey(1)})
	openKR := newTestKeyring(t, "k2", map[string][]byte{"k2": testKey(2)})

	sealed, err := Seal(sealKR, "key1", []byte("payload"))
	require.NoError(t, err)

	opened, legacy, err := Open(openKR, "key1", sealed)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `unknown key ID "k1"`)
	assert.Nil(t, opened, "no partial plaintext on failure")
	assert.False(t, legacy)
}

func TestOpen_TamperedCiphertext(t *testing.T) {
	t.Parallel()

	kr := newTestKeyring(t, "k1", map[string][]byte{"k1": testKey(1)})
	sealed, err := Seal(kr, "key1", []byte("payload"))
	require.NoError(t, err)

	// Flip a byte inside the base64-decoded ct, re-encode.
	var env map[string]any
	require.NoError(t, json.Unmarshal(sealed, &env))
	ct, err := base64.StdEncoding.DecodeString(env["ct"].(string))
	require.NoError(t, err)
	ct[len(ct)-1] ^= 0xff
	env["ct"] = base64.StdEncoding.EncodeToString(ct)
	tampered, err := json.Marshal(env)
	require.NoError(t, err)

	opened, legacy, err := Open(kr, "key1", tampered)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to decrypt record")
	assert.Nil(t, opened)
	assert.False(t, legacy)
}

func TestOpen_AADMismatch(t *testing.T) {
	t.Parallel()

	kr := newTestKeyring(t, "k1", map[string][]byte{"k1": testKey(1)})
	sealed, err := Seal(kr, "test:auth:upstream:sess1:providerA", []byte("payload"))
	require.NoError(t, err)

	// Copying the value to another (session, provider) key must fail.
	opened, legacy, err := Open(kr, "test:auth:upstream:sess2:providerB", sealed)
	require.Error(t, err, "cut-and-paste to a different redis key must fail decryption")
	assert.Nil(t, opened)
	assert.False(t, legacy)
}

func TestOpen_LegacyPlaintext(t *testing.T) {
	t.Parallel()

	kr := newTestKeyring(t, "k1", map[string][]byte{"k1": testKey(1)})
	legacy := []byte(`{"access_token":"tok","refresh_token":"ref","user_id":"u1"}`)

	opened, isLegacy, err := Open(kr, "key1", legacy)
	require.NoError(t, err)
	assert.True(t, isLegacy)
	assert.Equal(t, legacy, opened, "legacy plaintext passes through untouched")

	// Envelope-lookalike JSON missing required fields is also legacy.
	partial := []byte(`{"v":1,"kid":"k1"}`)
	opened, isLegacy, err = Open(kr, "key1", partial)
	require.NoError(t, err)
	assert.True(t, isLegacy)
	assert.Equal(t, partial, opened)

	// Non-JSON garbage is legacy (caller's unmarshal will fail downstream).
	garbage := []byte("not json at all")
	opened, isLegacy, err = Open(kr, "key1", garbage)
	require.NoError(t, err)
	assert.True(t, isLegacy)
	assert.Equal(t, garbage, opened)
}

func TestOpen_NilKeyring(t *testing.T) {
	t.Parallel()

	kr := newTestKeyring(t, "k1", map[string][]byte{"k1": testKey(1)})
	sealed, err := Seal(kr, "key1", []byte("payload"))
	require.NoError(t, err)

	// Envelope with no keyring: hard error, never corrupt tokens.
	_, _, err = Open(nil, "key1", sealed)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no keyring configured")

	// Plaintext with no keyring: passthrough (encryption disabled on plaintext fleet).
	legacy := []byte(`{"access_token":"tok"}`)
	opened, isLegacy, err := Open(nil, "key1", legacy)
	require.NoError(t, err)
	assert.True(t, isLegacy)
	assert.Equal(t, legacy, opened)

	// Seal with no keyring: error.
	_, err = Seal(nil, "key1", []byte("payload"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "keyring is required")
}

func TestNewStaticKeyring_Validation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		activeID string
		keys     map[string][]byte
		wantErr  string
	}{
		{
			name:     "zero keys",
			activeID: "k1",
			keys:     map[string][]byte{},
			wantErr:  "at least one key is required",
		},
		{
			name:     "nil keys",
			activeID: "k1",
			keys:     nil,
			wantErr:  "at least one key is required",
		},
		{
			name:     "empty active ID",
			activeID: "",
			keys:     map[string][]byte{"k1": testKey(1)},
			wantErr:  "active key ID is required",
		},
		{
			name:     "active ID not in map",
			activeID: "k2",
			keys:     map[string][]byte{"k1": testKey(1)},
			wantErr:  `active key ID "k2" not present`,
		},
		{
			name:     "16-byte key rejected",
			activeID: "k1",
			keys:     map[string][]byte{"k1": testKey(1)[:16]},
			wantErr:  "must be 32 bytes, got 16",
		},
		{
			name:     "empty key ID rejected",
			activeID: "k1",
			keys:     map[string][]byte{"k1": testKey(1), "": testKey(2)},
			wantErr:  "key ID cannot be empty",
		},
		{
			name:     "all-zero KEK rejected",
			activeID: "k1",
			keys:     map[string][]byte{"k1": make([]byte, 32)},
			wantErr:  `key "k1" must not be all zero bytes`,
		},
		{
			name:     "single non-zero byte is enough",
			activeID: "k1",
			keys:     map[string][]byte{"k1": append(make([]byte, 31), 1)},
			wantErr:  "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			kr, err := NewStaticKeyring(tt.activeID, tt.keys)
			if tt.wantErr == "" {
				require.NoError(t, err)
				require.NotNil(t, kr)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
			assert.Nil(t, kr)
		})
	}
}

func TestNewStaticKeyring_ClonesInput(t *testing.T) {
	t.Parallel()

	key := testKey(1)
	keys := map[string][]byte{"k1": key}
	kr := newTestKeyring(t, "k1", keys)

	// Mutating the caller's map/slice after construction must not corrupt the keyring.
	keys["k1"][0] ^= 0xff
	keys["k2"] = testKey(2)

	_, active := kr.Active()
	assert.Equal(t, testKey(1), active)
	_, ok := kr.ByID("k2")
	assert.False(t, ok)
}

func TestKeyring_RetiredKeyDecryptsNeverEncrypts(t *testing.T) {
	t.Parallel()

	oldKey := testKey(1)
	newKey := testKey(2)

	// Seal under k1 while it is active.
	kr1 := newTestKeyring(t, "k1", map[string][]byte{"k1": oldKey})
	sealed, err := Seal(kr1, "key1", []byte("payload"))
	require.NoError(t, err)
	assert.Contains(t, string(sealed), `"kid":"k1"`)

	// Rotate: k2 active, k1 retired. Retired key still decrypts...
	kr2 := newTestKeyring(t, "k2", map[string][]byte{"k1": oldKey, "k2": newKey})
	opened, legacy, err := Open(kr2, "key1", sealed)
	require.NoError(t, err)
	assert.False(t, legacy)
	assert.Equal(t, []byte("payload"), opened)

	// ...but new writes use only the active key.
	resealed, err := Seal(kr2, "key1", []byte("payload"))
	require.NoError(t, err)
	assert.Contains(t, string(resealed), `"kid":"k2"`)
	assert.NotContains(t, string(resealed), `"kid":"k1"`)

	// k1 alone cannot open k2 envelopes (rotation actually re-keys).
	kr1only := newTestKeyring(t, "k1", map[string][]byte{"k1": oldKey})
	_, _, err = Open(kr1only, "key1", resealed)
	require.Error(t, err)
}

func TestSeal_FreshDEKPerRecord(t *testing.T) {
	t.Parallel()

	kr := newTestKeyring(t, "k1", map[string][]byte{"k1": testKey(1)})
	plaintext := []byte("same plaintext")

	a, err := Seal(kr, "key1", plaintext)
	require.NoError(t, err)
	b, err := Seal(kr, "key1", plaintext)
	require.NoError(t, err)

	assert.False(t, bytes.Equal(a, b), "identical plaintexts must not produce identical envelopes")
}

func TestNeedsRotation(t *testing.T) {
	t.Parallel()

	oldKR := newTestKeyring(t, "k1", map[string][]byte{"k1": testKey(1)})
	sealedK1, err := Seal(oldKR, "key1", []byte("payload"))
	require.NoError(t, err)

	rotatedKR := newTestKeyring(t, "k2", map[string][]byte{"k1": testKey(1), "k2": testKey(2)})
	sealedK2, err := Seal(rotatedKR, "key1", []byte("payload"))
	require.NoError(t, err)

	assert.True(t, NeedsRotation(rotatedKR, sealedK1), "k1 envelope needs rotation when k2 is active")
	assert.False(t, NeedsRotation(rotatedKR, sealedK2), "k2 envelope is current")
	assert.False(t, NeedsRotation(rotatedKR, []byte(`{"access_token":"tok"}`)), "legacy plaintext is not a rotation case")
	assert.False(t, NeedsRotation(nil, sealedK1), "nil keyring: rotation meaningless")
}

func TestIsLegacyValue(t *testing.T) {
	t.Parallel()

	kr := newTestKeyring(t, "k1", map[string][]byte{"k1": testKey(1)})
	sealed, err := Seal(kr, "key1", []byte("payload"))
	require.NoError(t, err)

	assert.False(t, IsLegacyValue(sealed))
	assert.True(t, IsLegacyValue([]byte(`{"access_token":"tok"}`)))
	assert.True(t, IsLegacyValue([]byte("null")))
	assert.True(t, IsLegacyValue([]byte("garbage")))
}

func TestEnvelopeKeyID(t *testing.T) {
	t.Parallel()

	kr := newTestKeyring(t, "k1", map[string][]byte{"k1": testKey(1)})
	sealed, err := Seal(kr, "key1", []byte("payload"))
	require.NoError(t, err)

	kid, ok := EnvelopeKeyID(sealed)
	assert.True(t, ok)
	assert.Equal(t, "k1", kid)

	_, ok = EnvelopeKeyID([]byte(`{"access_token":"tok"}`))
	assert.False(t, ok)
}

// TestSealOpen_RandomKeys guards against accidental dependence on the
// deterministic testKey helper: real random KEKs must round-trip too.
func TestSealOpen_RandomKeys(t *testing.T) {
	t.Parallel()

	kek := make([]byte, 32)
	_, err := rand.Read(kek)
	require.NoError(t, err)

	kr := newTestKeyring(t, "k1", map[string][]byte{"k1": kek})
	sealed, err := Seal(kr, "key1", []byte("payload"))
	require.NoError(t, err)

	opened, legacy, err := Open(kr, "key1", sealed)
	require.NoError(t, err)
	assert.False(t, legacy)
	assert.Equal(t, []byte("payload"), opened)
}

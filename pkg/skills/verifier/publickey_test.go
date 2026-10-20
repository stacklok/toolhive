// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package verifier

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"strings"
	"testing"

	"github.com/sigstore/sigstore/pkg/cryptoutils"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/skills/lockfile"
)

// TestPublicKeyRoundTrip is the contract between the two ends of the wire: the
// CLI encodes a PEM file, the service decodes it back to PEM to verify with.
// A conversion that is not an identity would verify against key material the
// operator never named.
func TestPublicKeyRoundTrip(t *testing.T) {
	t.Parallel()

	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	pubPEM, err := cryptoutils.MarshalPublicKeyToPEM(priv.Public())
	require.NoError(t, err)

	encoded, err := EncodePublicKey(pubPEM)
	require.NoError(t, err)
	// The lock file rejects whitespace and non-graphic runes, which is the
	// whole reason PEM cannot be stored verbatim.
	assert.NotContains(t, encoded, "\n")
	assert.Equal(t, strings.TrimSpace(encoded), encoded)

	decoded, err := DecodePublicKey(encoded)
	require.NoError(t, err)
	assert.Equal(t, string(pubPEM), string(decoded))
}

func TestEncodePublicKeyRejections(t *testing.T) {
	t.Parallel()

	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	privPEM, err := cryptoutils.MarshalPrivateKeyToPEM(priv)
	require.NoError(t, err)
	pubPEM, err := cryptoutils.MarshalPublicKeyToPEM(priv.Public())
	require.NoError(t, err)

	tests := []struct {
		name    string
		input   []byte
		wantMsg string
	}{
		{name: "empty input", input: nil, wantMsg: "no PEM block found"},
		{name: "not PEM at all", input: []byte("just some text"), wantMsg: "no PEM block found"},
		{
			// Sent to the server and written to the lock file, so refusing
			// this by the block's own label — before anything parses its
			// bytes — is what keeps private material out of both.
			name:    "private key refused by its label",
			input:   privPEM,
			wantMsg: "takes the cosign public key",
		},
		{
			// Which of the two is the trust anchor? Guessing would pin one of
			// them silently.
			name:    "two blocks are ambiguous",
			input:   append(append([]byte{}, pubPEM...), pubPEM...),
			wantMsg: "more than one PEM block",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := EncodePublicKey(tc.input)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantMsg)
		})
	}
}

func TestDecodePublicKeyRejections(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		input   string
		wantMsg string
	}{
		{name: "not base64", input: "not!base64", wantMsg: "not valid base64"},
		{
			// Decoding proves the encoding, not the content. This is the
			// input that would otherwise fail deep inside verification, with
			// the lock file no longer the obvious suspect.
			name:    "base64 of something that is not a key",
			input:   "aGVsbG8gd29ybGQ=",
			wantMsg: "not a DER SPKI public key",
		},
		{
			// Bounded before decoding, so the allocation is capped before it
			// is made rather than after.
			name:    "over the length bound",
			input:   strings.Repeat("A", lockfile.MaxEncodedPublicKeyLength+1),
			wantMsg: "exceeding the",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := DecodePublicKey(tc.input)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantMsg)
		})
	}
}

// TestDecodePublicKeyAcceptsTheLockFixture ties the shared test constant to
// the real decoder: every key-pinned fixture in the tree depends on this value
// being a genuine SPKI key, not merely valid base64.
func TestDecodePublicKeyAcceptsTheLockFixture(t *testing.T) {
	t.Parallel()

	pemBytes, err := DecodePublicKey(testPublicKeyB64)
	require.NoError(t, err)
	assert.Contains(t, string(pemBytes), "BEGIN PUBLIC KEY")
}

// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package webhook

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSignPayload(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		secret    []byte
		timestamp int64
		payload   []byte
	}{
		{
			name:      "basic payload",
			secret:    []byte("my-secret"),
			timestamp: 1698057000,
			payload:   []byte(`{"version":"v0.1.0","uid":"test-uid"}`),
		},
		{
			name:      "empty payload",
			secret:    []byte("my-secret"),
			timestamp: 1698057000,
			payload:   []byte{},
		},
		{
			name:      "large payload",
			secret:    []byte("another-secret"),
			timestamp: 9999999999,
			payload:   []byte(`{"key":"` + string(make([]byte, 1024)) + `"}`),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			sig := SignPayload(tt.secret, tt.timestamp, tt.payload)
			assert.NotEmpty(t, sig)
			assert.Contains(t, sig, "sha256=")

			// Round-trip: signature must verify.
			assert.True(t, VerifySignature(tt.secret, tt.timestamp, tt.payload, sig),
				"signature round-trip verification failed")
		})
	}
}

func TestVerifySignature(t *testing.T) {
	t.Parallel()

	secret := []byte("test-secret")
	timestamp := int64(1698057000)
	payload := []byte(`{"version":"v0.1.0","uid":"test"}`)
	validSig := SignPayload(secret, timestamp, payload)

	tests := []struct {
		name      string
		secret    []byte
		timestamp int64
		payload   []byte
		signature string
		expected  bool
	}{
		{
			name:      "valid signature",
			secret:    secret,
			timestamp: timestamp,
			payload:   payload,
			signature: validSig,
			expected:  true,
		},
		{
			name:      "wrong secret",
			secret:    []byte("wrong-secret"),
			timestamp: timestamp,
			payload:   payload,
			signature: validSig,
			expected:  false,
		},
		{
			name:      "wrong timestamp",
			secret:    secret,
			timestamp: timestamp + 1,
			payload:   payload,
			signature: validSig,
			expected:  false,
		},
		{
			name:      "tampered payload",
			secret:    secret,
			timestamp: timestamp,
			payload:   []byte(`{"version":"v0.1.0","uid":"TAMPERED"}`),
			signature: validSig,
			expected:  false,
		},
		{
			name:      "missing sha256 prefix",
			secret:    secret,
			timestamp: timestamp,
			payload:   payload,
			signature: "abcdef1234567890",
			expected:  false,
		},
		{
			name:      "invalid hex after prefix",
			secret:    secret,
			timestamp: timestamp,
			payload:   payload,
			signature: "sha256=not-valid-hex!",
			expected:  false,
		},
		{
			name:      "empty signature",
			secret:    secret,
			timestamp: timestamp,
			payload:   payload,
			signature: "",
			expected:  false,
		},
		{
			name:      "sha256= prefix only",
			secret:    secret,
			timestamp: timestamp,
			payload:   payload,
			signature: "sha256=",
			expected:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result := VerifySignature(tt.secret, tt.timestamp, tt.payload, tt.signature)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestSignPayloadDeterministic(t *testing.T) {
	t.Parallel()

	secret := []byte("deterministic-test")
	timestamp := int64(1234567890)
	payload := []byte("test-payload")

	sig1 := SignPayload(secret, timestamp, payload)
	sig2 := SignPayload(secret, timestamp, payload)

	assert.Equal(t, sig1, sig2, "same inputs must produce the same signature")
}

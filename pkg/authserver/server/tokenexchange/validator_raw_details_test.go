// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package tokenexchange

import (
	"testing"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// signRawPayload signs payload as is, so a test controls every byte of the
// claims, including repeated members and numbers beyond float64 precision.
func (tj *testJWKS) signRawPayload(t *testing.T, payload string) string {
	t.Helper()
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.ES256, Key: tj.jwk}, nil)
	require.NoError(t, err)
	signed, err := signer.Sign([]byte(payload))
	require.NoError(t, err)
	compact, err := signed.CompactSerialize()
	require.NoError(t, err)
	return compact
}

func TestVerifySignature_AuthorizationDetailsRaw(t *testing.T) {
	t.Parallel()

	keys := newTestJWKS(t)

	tests := []struct {
		name    string
		payload string
		want    string // "" means nil
		wantErr bool
	}{
		{name: "absent", payload: `{"sub":"u"}`},
		{
			name:    "integer above 2^53 keeps every digit",
			payload: `{"sub":"u","authorization_details":[{"type":"t","n":9007199254740993}]}`,
			want:    `[{"type":"t","n":9007199254740993}]`,
		},
		{name: "null is present", payload: `{"sub":"u","authorization_details":null}`, want: `null`},
		{name: "other letter case is not the member", payload: `{"sub":"u","AUTHORIZATION_DETAILS":[]}`},
		{
			name:    "repeated member is refused",
			payload: `{"sub":"u","authorization_details":[],"authorization_details":[]}`,
			wantErr: true,
		},
		{
			name:    "repeated member inside an entry is refused",
			payload: `{"sub":"u","authorization_details":[{"type":"t","type":"u"}]}`,
			wantErr: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			token, err := jwt.ParseSigned(keys.signRawPayload(t, tc.payload), allowedSignatureAlgorithms)
			require.NoError(t, err)

			verified, _, err := verifySignature(token, keys.publicJWKS())
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			claims := buildValidatedClaims(verified)
			if tc.want == "" {
				assert.Nil(t, claims.AuthorizationDetails)
				return
			}
			assert.Equal(t, tc.want, string(claims.AuthorizationDetails))
		})
	}
}

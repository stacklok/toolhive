// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package identitytoken

import (
	"testing"

	josejwt "github.com/go-jose/go-jose/v4"
	"github.com/sigstore/sigstore/pkg/oauthflow"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestInteractive proves the wiring against oauthflow.OIDConnect end to end
// without a real OAuth exchange: OIDConnect special-cases
// oauthflow.StaticTokenGetter and calls its GetIDToken directly, skipping
// provider discovery and all network calls. The token must still be a
// structurally valid (if unverified) compact JWS, since StaticTokenGetter
// parses it to extract a subject.
func TestInteractive(t *testing.T) {
	t.Parallel()

	token := signedTestJWT(t, `{"sub":"test-subject"}`)

	got, err := Interactive(&oauthflow.StaticTokenGetter{RawToken: token})
	require.NoError(t, err)
	assert.Equal(t, token, got)
}

func TestInteractivePropagatesGetterError(t *testing.T) {
	t.Parallel()

	_, err := Interactive(&oauthflow.StaticTokenGetter{RawToken: "not-a-jwt"})
	require.Error(t, err)
}

// signedTestJWT builds a real, structurally valid (but unverified) compact
// JWS over payload using an ephemeral HS256 key — enough for
// jose.ParseSigned (used internally by StaticTokenGetter) to accept it.
func signedTestJWT(t *testing.T, payload string) string {
	t.Helper()
	signer, err := josejwt.NewSigner(josejwt.SigningKey{
		Algorithm: josejwt.HS256,
		Key:       []byte("test-signing-key-not-used-for-anything-real"),
	}, nil)
	require.NoError(t, err)

	sig, err := signer.Sign([]byte(payload))
	require.NoError(t, err)

	compact, err := sig.CompactSerialize()
	require.NoError(t, err)
	return compact
}

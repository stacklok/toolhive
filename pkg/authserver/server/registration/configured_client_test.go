// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package registration

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"testing"

	"github.com/go-jose/go-jose/v4"
	"github.com/ory/fosite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/oauthproto"
)

func testECPublicKey(t *testing.T, curve elliptic.Curve) *ecdsa.PublicKey {
	t.Helper()
	key, err := ecdsa.GenerateKey(curve, rand.Reader)
	require.NoError(t, err)
	return &key.PublicKey
}

func TestResolveConfiguredSigningAlgorithm(t *testing.T) {
	t.Parallel()

	ec256 := jose.JSONWebKey{Key: testECPublicKey(t, elliptic.P256()), KeyID: "ec256", Use: "sig"}
	ec384 := jose.JSONWebKey{Key: testECPublicKey(t, elliptic.P384()), KeyID: "ec384", Use: "sig"}
	ec521 := jose.JSONWebKey{Key: testECPublicKey(t, elliptic.P521()), KeyID: "ec521", Use: "sig"}
	rsaPlain := jose.JSONWebKey{Key: testRSAPublicKey(t), KeyID: "rsa", Use: "sig"}
	rsaPS256 := jose.JSONWebKey{Key: testRSAPublicKey(t), KeyID: "ps", Use: "sig", Algorithm: "PS256"}
	rsaRS256 := jose.JSONWebKey{Key: testRSAPublicKey(t), KeyID: "rs", Use: "sig", Algorithm: "RS256"}
	ec256Labeled := jose.JSONWebKey{Key: testECPublicKey(t, elliptic.P256()), KeyID: "lab", Use: "sig", Algorithm: "ES256"}
	oct := jose.JSONWebKey{Key: []byte("secret"), KeyID: "oct", Use: "sig"}
	set := func(keys ...jose.JSONWebKey) *jose.JSONWebKeySet { return &jose.JSONWebKeySet{Keys: keys} }

	tests := []struct {
		name       string
		configured string
		jwks       *jose.JSONWebKeySet
		want       string
		wantErr    []string
	}{
		{name: "P-256 key derives ES256", jwks: set(ec256), want: "ES256"},
		{name: "P-384 key derives ES384", jwks: set(ec384), want: "ES384"},
		{name: "P-521 key derives ES512", jwks: set(ec521), want: "ES512"},
		{name: "RSA key without alg keeps RS256", jwks: set(rsaPlain), want: "RS256"},
		{name: "RSA key with alg PS256 derives PS256", jwks: set(rsaPS256), want: "PS256"},
		{name: "two EC keys of one curve agree", jwks: set(ec256, ec256Labeled), want: "ES256"},
		{name: "configured alg wins over the keys", configured: "PS256", jwks: set(ec256), want: "PS256"},
		{name: "configured alg wins over disagreeing keys", configured: "ES256", jwks: set(ec256, rsaPlain), want: "ES256"},
		{name: "nil key set is RS256", jwks: nil, want: "RS256"},
		{name: "empty key set is RS256", jwks: set(), want: "RS256"},
		{name: "unsupported key type is left to the key-set check", jwks: set(oct), want: "RS256"},
		{
			name: "RSA and EC keys disagree", jwks: set(rsaPlain, ec256),
			wantErr: []string{"ES256, RS256", "have different signing algorithms", "token_endpoint_auth_signing_alg"},
		},
		{
			name: "found algorithms are sorted and deduplicated", jwks: set(rsaRS256, ec521, rsaPlain, ec384, ec521),
			wantErr: []string{"(ES384, ES512, RS256)"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := resolveConfiguredSigningAlgorithm(tt.configured, tt.jwks)
			if len(tt.wantErr) > 0 {
				require.Error(t, err)
				for _, want := range tt.wantErr {
					assert.Contains(t, err.Error(), want)
				}
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestValidateConfiguredClientNamesDerivedAlgorithm(t *testing.T) {
	t.Parallel()

	pkjwt := oauthproto.TokenEndpointAuthMethodPrivateKeyJWT
	edPub, _, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	ec256 := testECPublicKey(t, elliptic.P256())
	set := func(keys ...jose.JSONWebKey) *jose.JSONWebKeySet { return &jose.JSONWebKeySet{Keys: keys} }

	tests := []struct {
		name       string
		spec       ConfiguredClientSpec
		wantErr    string
		notWantErr string
	}{
		{
			name: "derived algorithm is named",
			spec: ConfiguredClientSpec{
				TokenEndpointAuthMethod: pkjwt, JWKS: set(jose.JSONWebKey{Key: edPub, KeyID: "ed", Use: "sig"}),
			},
			wantErr: "not set; RS256 derived from the keys",
		},
		{
			name: "algorithm from the key is named",
			spec: ConfiguredClientSpec{
				TokenEndpointAuthMethod: pkjwt,
				JWKS:                    set(jose.JSONWebKey{Key: edPub, KeyID: "ed", Use: "sig", Algorithm: "EdDSA"}),
			},
			wantErr: "EdDSA derived from the keys",
		},
		{
			name: "configured algorithm is not reported as derived",
			spec: ConfiguredClientSpec{
				TokenEndpointAuthMethod: pkjwt,
				SigningAlgorithm:        string(jose.RS256),
				JWKS:                    set(jose.JSONWebKey{Key: ec256, KeyID: "ec", Use: "sig"}),
			},
			wantErr:    "jwks_file: ",
			notWantErr: "derived from the keys",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := ValidateConfiguredClient(tt.spec)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
			if tt.notWantErr != "" {
				assert.NotContains(t, err.Error(), tt.notWantErr)
			}
		})
	}
}

func TestConfiguredClientDerivedSigningAlgorithm(t *testing.T) {
	t.Parallel()

	pkjwt := oauthproto.TokenEndpointAuthMethodPrivateKeyJWT
	ec256 := jose.JSONWebKey{Key: testECPublicKey(t, elliptic.P256()), KeyID: "ec", Use: "sig"}
	rsaKey := jose.JSONWebKey{Key: testRSAPublicKey(t), KeyID: "rsa", Use: "sig"}
	set := func(keys ...jose.JSONWebKey) *jose.JSONWebKeySet { return &jose.JSONWebKeySet{Keys: keys} }

	tests := []struct {
		name       string
		jwks       *jose.JSONWebKeySet
		wantAlg    string
		wantErrMsg string
	}{
		{name: "EC key without alg", jwks: set(ec256), wantAlg: "ES256"},
		{name: "RSA key without alg", jwks: set(rsaKey), wantAlg: "RS256"},
		{name: "keys that disagree", jwks: set(ec256, rsaKey), wantErrMsg: "have different signing algorithms"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			client, err := NewStaticDelegateClient(Config{
				ID: "configured", Scopes: []string{"openid"}, Audience: []string{"https://mcp.example"},
				TokenEndpointAuthMethod: pkjwt, JSONWebKeys: tt.jwks,
			})
			if tt.wantErrMsg != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErrMsg)
				return
			}
			require.NoError(t, err)
			oidc, ok := client.(fosite.OpenIDConnectClient)
			require.True(t, ok)
			assert.Equal(t, tt.wantAlg, oidc.GetTokenEndpointAuthSigningAlgorithm())
		})
	}
}

// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package oauthproto

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1" // #nosec G505 -- x5t is a SHA-1 thumbprint by definition
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// selfSignedCertWithPaddedDER returns a certificate whose DER length is not a
// multiple of three, so its standard base64 form in x5c ends in "=".
func selfSignedCertWithPaddedDER(t *testing.T) (*x509.Certificate, *ecdsa.PrivateKey) {
	t.Helper()
	for range 10 {
		key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		require.NoError(t, err)
		der, err := x509.CreateCertificate(rand.Reader, &x509.Certificate{
			SerialNumber: big.NewInt(1),
			Subject:      pkix.Name{CommonName: "jwks-test"},
			NotBefore:    time.Now().Add(-time.Hour),
			NotAfter:     time.Now().Add(time.Hour),
		}, &x509.Certificate{}, &key.PublicKey, key)
		require.NoError(t, err)
		if len(der)%3 == 0 {
			continue
		}
		cert, err := x509.ParseCertificate(der)
		require.NoError(t, err)
		return cert, key
	}
	t.Fatal("could not build a certificate with padded base64")
	return nil, nil
}

func TestUnmarshalJWKS(t *testing.T) {
	t.Parallel()

	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	n := base64.URLEncoding.EncodeToString(rsaKey.N.Bytes())
	require.True(t, strings.HasSuffix(n, "="))
	rsaJWKS := func(nValue string) string {
		return fmt.Sprintf(`{"keys":[{"kty":"RSA","kid":"k1","n":%q,"e":"AQAB"}]}`, nValue)
	}

	cert, certKey := selfSignedCertWithPaddedDER(t)
	sum1 := sha1.Sum(cert.Raw) // #nosec G401
	sum256 := sha256.Sum256(cert.Raw)
	x5c := base64.StdEncoding.EncodeToString(cert.Raw)
	require.True(t, strings.HasSuffix(x5c, "="))
	x := base64.URLEncoding.EncodeToString(certKey.X.FillBytes(make([]byte, 32)))
	y := base64.URLEncoding.EncodeToString(certKey.Y.FillBytes(make([]byte, 32)))
	certJWKS := fmt.Sprintf(`{"keys":[{"kty":"EC","crv":"P-256","x":%q,"y":%q,"x5c":[%q],"x5t":%q,"x5t#S256":%q}]}`,
		x, y, x5c,
		base64.URLEncoding.EncodeToString(sum1[:]),
		base64.URLEncoding.EncodeToString(sum256[:]))

	tests := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{name: "padded RSA modulus", input: rsaJWKS(n)},
		{name: "unpadded RSA modulus", input: rsaJWKS(strings.TrimRight(n, "="))},
		{
			name:  "padded EC coordinates, thumbprints and untouched x5c",
			input: certJWKS,
		},
		{
			name:    "member names are case-sensitive like go-jose",
			input:   fmt.Sprintf(`{"keys":[{"kty":"RSA","N":%q,"e":"AQAB"}]}`, n),
			wantErr: true,
		},
		{name: "invalid base64url is rejected", input: rsaJWKS("!!!="), wantErr: true},
		{name: "not JSON is rejected", input: "not json", wantErr: true},
		{name: "non-string member is rejected", input: `{"keys":[{"kty":"RSA","n":5,"e":"AQAB"}]}`, wantErr: true},
		{name: "keys of the wrong type are rejected", input: `{"keys":{}}`, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := UnmarshalJWKS([]byte(tt.input))
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Len(t, got.Keys, 1)
			assert.True(t, got.Keys[0].Valid())
			switch k := got.Keys[0].Key.(type) {
			case *rsa.PublicKey:
				assert.True(t, rsaKey.PublicKey.Equal(k))
			case *ecdsa.PublicKey:
				assert.True(t, certKey.PublicKey.Equal(k))
				require.Len(t, got.Keys[0].Certificates, 1)
				assert.True(t, cert.Equal(got.Keys[0].Certificates[0]))
				assert.Equal(t, sum1[:], got.Keys[0].CertificateThumbprintSHA1)
				assert.Equal(t, sum256[:], got.Keys[0].CertificateThumbprintSHA256)
			default:
				t.Fatalf("unexpected key type %T", k)
			}
		})
	}
}

func TestDynamicClientRegistrationRequest_UnmarshalJSON_JWKS(t *testing.T) {
	t.Parallel()

	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	n := base64.URLEncoding.EncodeToString(rsaKey.N.Bytes())
	require.True(t, strings.HasSuffix(n, "="))

	tests := []struct {
		name    string
		body    string
		wantKey bool
		wantErr bool
	}{
		{
			name:    "padded jwks",
			body:    fmt.Sprintf(`{"client_name":"c","scope":"a b","jwks":{"keys":[{"kty":"RSA","n":%q,"e":"AQAB"}]}}`, n),
			wantKey: true,
		},
		{name: "no jwks", body: `{"client_name":"c"}`},
		{name: "null jwks", body: `{"client_name":"c","jwks":null}`},
		{name: "malformed jwks", body: `{"client_name":"c","jwks":{"keys":[{"kty":"RSA","n":"!!!=","e":"AQAB"}]}}`, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var req DynamicClientRegistrationRequest
			err := json.Unmarshal([]byte(tt.body), &req)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, "c", req.ClientName)
			if !tt.wantKey {
				assert.Nil(t, req.JWKS)
				return
			}
			assert.Equal(t, ScopeList{"a", "b"}, req.Scopes)
			require.NotNil(t, req.JWKS)
			require.Len(t, req.JWKS.Keys, 1)
			assert.True(t, rsaKey.PublicKey.Equal(req.JWKS.Keys[0].Key))
		})
	}
}

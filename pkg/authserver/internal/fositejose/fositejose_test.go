// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package fositejose

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha1" // #nosec G505 -- x5t is a SHA-1 thumbprint by definition
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"math/big"
	"net/url"
	"testing"
	"time"

	josev3 "github.com/go-jose/go-jose/v3"
	"github.com/go-jose/go-jose/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// compactJWS builds a compact JWS whose protected header is the given JSON.
// The payload and signature are placeholders: the guard never verifies them.
func compactJWS(header string) string {
	enc := base64.RawURLEncoding
	return enc.EncodeToString([]byte(header)) + "." + enc.EncodeToString([]byte(`{}`)) + ".c2ln"
}

// fullKey returns a go-jose v4 key with every field the conversions copy set.
func fullKey(t *testing.T) jose.JSONWebKey {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	der, err := x509.CreateCertificate(rand.Reader, &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "fositejose-test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
	}, &x509.Certificate{}, &priv.PublicKey, priv)
	require.NoError(t, err)
	cert, err := x509.ParseCertificate(der)
	require.NoError(t, err)
	sum1 := sha1.Sum(cert.Raw) // #nosec G401 -- x5t is a SHA-1 thumbprint by definition
	sum256 := sha256.Sum256(cert.Raw)
	return jose.JSONWebKey{
		Key:                         &priv.PublicKey,
		KeyID:                       "kid-1",
		Algorithm:                   "ES256",
		Use:                         "sig",
		Certificates:                []*x509.Certificate{cert},
		CertificatesURL:             &url.URL{Scheme: "https", Host: "example.com", Path: "/cert.pem"},
		CertificateThumbprintSHA1:   sum1[:],
		CertificateThumbprintSHA256: sum256[:],
	}
}

func TestKeyConversions(t *testing.T) {
	t.Parallel()

	original := &jose.JSONWebKeySet{Keys: []jose.JSONWebKey{fullKey(t)}}
	tests := []struct {
		name string
		got  any
		want any
	}{
		{name: "nil key", got: ToFositeKey(nil), want: (*josev3.JSONWebKey)(nil)},
		{name: "nil set", got: ToFositeKeySet(nil), want: (*josev3.JSONWebKeySet)(nil)},
		{name: "nil fosite set", got: FromFositeKeySet(nil), want: (*jose.JSONWebKeySet)(nil)},
		{name: "round trip keeps every field", got: FromFositeKeySet(ToFositeKeySet(original)), want: original},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, tt.got)
		})
	}
}

func TestIsUnsafeJWS(t *testing.T) {
	t.Parallel()

	paddedHeader := base64.URLEncoding.EncodeToString([]byte(`{"alg":"RS256","jwk":{"kty":"RSA"}}`))

	tests := []struct {
		name  string
		token string
		want  bool
	}{
		{name: "plain compact JWS", token: compactJWS(`{"alg":"RS256","kid":"k1"}`), want: false},
		{name: "embedded jwk", token: compactJWS(`{"alg":"RS256","jwk":{"kty":"RSA","n":"AQ","e":"AQAB"}}`), want: true},
		{name: "embedded jwk with differently cased key", token: compactJWS(`{"alg":"RS256","JWK":{"kty":"RSA"}}`), want: true},
		{name: "embedded jwk hidden by whitespace", token: " " + compactJWS(`{"alg":"RS256","jwk":{}}`)[:4] + "\n" + compactJWS(`{"alg":"RS256","jwk":{}}`)[4:], want: true},
		{name: "embedded jwk behind base64 padding", token: paddedHeader + ".e30.c2ln", want: true},
		{name: "JSON serialization", token: `{"payload":"e30","signatures":[]}`, want: true},
		{name: "JSON serialization after whitespace", token: "\t{\"payload\":\"e30\"}", want: true},
		{name: "too few segments", token: "e30.e30", want: true},
		{name: "too many segments", token: "e30.e30.e30.e30.e30", want: true},
		{name: "header not base64", token: "!!!.e30.c2ln", want: true},
		{name: "header not a JSON object", token: compactJWS(`["jwk"]`), want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, IsUnsafeJWS(tt.token))
		})
	}
}

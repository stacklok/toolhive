// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllerutil

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
)

func jwksChecksumTestClient(t *testing.T, objects ...client.Object) client.Client {
	t.Helper()
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))
	return fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()
}

func jwksTestConfigMap(data string) *corev1.ConfigMap {
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "jwks", Namespace: "ns"},
		Data:       map[string]string{"jwks.json": data, "other": "unselected"},
	}
}

func TestEmbeddedAuthServerConfigMapChecksum_DelegateClientJWKS(t *testing.T) {
	t.Parallel()

	clients := []mcpv1beta1.DelegateClientConfig{{ClientID: "broker", JWKSRef: delegateJWKSTestRef("jwks", "jwks.json")}}
	canonical := &mcpv1beta1.EmbeddedAuthServerConfig{InboundGrants: &mcpv1beta1.InboundGrantsConfig{
		TokenExchange: &mcpv1beta1.TokenExchangeInboundGrantConfig{DelegateClients: clients},
	}}
	deprecated := &mcpv1beta1.EmbeddedAuthServerConfig{DelegateClients: clients}

	for name, cfg := range map[string]*mcpv1beta1.EmbeddedAuthServerConfig{"canonical": canonical, "deprecated": deprecated} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			cm := jwksTestConfigMap(`{"keys":[1]}`)
			first, err := EmbeddedAuthServerConfigMapChecksumForConfig(t.Context(), jwksChecksumTestClient(t, cm), "ns", cfg)
			require.NoError(t, err)
			assert.NotEmpty(t, first)

			cm.Data["other"] = "changed"
			same, err := EmbeddedAuthServerConfigMapChecksumForConfig(t.Context(), jwksChecksumTestClient(t, cm), "ns", cfg)
			require.NoError(t, err)
			assert.Equal(t, first, same, "unselected keys must not roll the pod")

			cm.Data["jwks.json"] = `{"keys":[2]}`
			rotated, err := EmbeddedAuthServerConfigMapChecksumForConfig(t.Context(), jwksChecksumTestClient(t, cm), "ns", cfg)
			require.NoError(t, err)
			assert.NotEqual(t, first, rotated, "a JWKS edit must change the checksum")
		})
	}
}

// TestEmbeddedAuthServerConfigMapChecksum_UnchangedWithoutJWKS pins the checksum
// of configs without a delegate client jwksRef to the pre-JWKS byte sequence: a
// sha256 over each CA bundle followed by a NUL byte, with the trusted issuer
// bundles behind a "trustedIssuers\x00" marker. Changing it would roll every
// embedded-auth pod on operator upgrade. The expected value is a hand-written
// hash, not a call to the function under test.
func TestEmbeddedAuthServerConfigMapChecksum_UnchangedWithoutJWKS(t *testing.T) {
	t.Parallel()

	upstreamPEM := testCertificatePEM(t)
	issuerPEM := testCertificatePEM(t)
	config := &mcpv1beta1.EmbeddedAuthServerConfig{
		UpstreamProviders: []mcpv1beta1.UpstreamProviderConfig{{
			Name: "issuer", Type: mcpv1beta1.UpstreamProviderTypeOIDC,
			OIDCConfig: &mcpv1beta1.OIDCUpstreamConfig{CABundleRef: caBundleTestRef("")},
		}},
		TrustedIssuers: []mcpv1beta1.TrustedIssuerConfig{{CABundleRef: &mcpv1beta1.CABundleSource{
			ConfigMapRef: &corev1.ConfigMapKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: "issuer-ca"}},
		}}},
		InboundGrants: &mcpv1beta1.InboundGrantsConfig{TokenExchange: &mcpv1beta1.TokenExchangeInboundGrantConfig{
			DelegateClients: []mcpv1beta1.DelegateClientConfig{{ClientID: "secret-client"}},
		}},
	}
	upstreamCM := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "bundle", Namespace: "ns"}, Data: map[string]string{"ca.crt": string(upstreamPEM)},
	}
	issuerCM := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "issuer-ca", Namespace: "ns"}, Data: map[string]string{"ca.crt": string(issuerPEM)},
	}

	var want []byte
	want = append(want, upstreamPEM...)
	want = append(want, 0)
	want = append(want, []byte("trustedIssuers\x00")...)
	want = append(want, issuerPEM...)
	want = append(want, 0)
	sum := sha256.Sum256(want)

	got, err := EmbeddedAuthServerConfigMapChecksumForConfig(
		t.Context(), jwksChecksumTestClient(t, upstreamCM, issuerCM), "ns", config)
	require.NoError(t, err)
	assert.Equal(t, hex.EncodeToString(sum[:]), got)
}

func TestValidateEmbeddedAuthServerCABundles_DelegateClientJWKS(t *testing.T) {
	t.Parallel()

	cfg := &mcpv1beta1.EmbeddedAuthServerConfig{DelegateClients: []mcpv1beta1.DelegateClientConfig{
		{ClientID: "broker", JWKSRef: delegateJWKSTestRef("jwks", "jwks.json")},
	}}

	require.NoError(t, ValidateEmbeddedAuthServerCABundles(t.Context(), jwksChecksumTestClient(t, jwksTestConfigMap("x")), "ns", cfg))

	missingKey := jwksTestConfigMap("x")
	delete(missingKey.Data, "jwks.json")
	err := ValidateEmbeddedAuthServerCABundles(t.Context(), jwksChecksumTestClient(t, missingKey), "ns", cfg)
	require.ErrorContains(t, err, `does not contain key "jwks.json"`)
	var invalid *InvalidCABundleError
	assert.ErrorAs(t, err, &invalid, "a missing key is terminal")

	err = ValidateEmbeddedAuthServerCABundles(t.Context(), jwksChecksumTestClient(t), "ns", cfg)
	require.Error(t, err)
	assert.NotErrorAs(t, err, &invalid, "a missing ConfigMap read may be transient")
}

func TestDelegateClientJWKSConfigMapNames(t *testing.T) {
	t.Parallel()

	clients := []mcpv1beta1.DelegateClientConfig{
		{ClientID: "a", JWKSRef: delegateJWKSTestRef("jwks-a", "k")},
		{ClientID: "secret"},
		{ClientID: "c", JWKSRef: delegateJWKSTestRef("jwks-c", "k")},
	}
	assert.Nil(t, DelegateClientJWKSConfigMapNames(nil))
	assert.Equal(t, []string{"jwks-a", "jwks-c"}, DelegateClientJWKSConfigMapNames(&mcpv1beta1.EmbeddedAuthServerConfig{
		InboundGrants: &mcpv1beta1.InboundGrantsConfig{TokenExchange: &mcpv1beta1.TokenExchangeInboundGrantConfig{DelegateClients: clients}},
	}))
	assert.Equal(t, []string{"jwks-a", "jwks-c"}, DelegateClientJWKSConfigMapNames(
		&mcpv1beta1.EmbeddedAuthServerConfig{DelegateClients: clients}))
}

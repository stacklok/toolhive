// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllerutil

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
	"github.com/stacklok/toolhive/cmd/thv-operator/pkg/validation"
)

func TestResolveCABundle(t *testing.T) {
	t.Parallel()

	validPEM := testCertificatePEM(t)
	optional := true
	tests := []struct {
		name      string
		namespace string
		ref       *mcpv1beta1.CABundleSource
		objects   []client.Object
		want      []byte
		wantErr   string
	}{
		{
			name:      "default key from Data",
			namespace: "workload",
			ref:       caBundleTestRef(""),
			objects:   []client.Object{&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "bundle", Namespace: "workload"}, Data: map[string]string{validation.OIDCCABundleDefaultKey: string(validPEM)}}},
			want:      validPEM,
		},
		{
			name:      "explicit key from BinaryData",
			namespace: "workload",
			ref:       caBundleTestRef("custom.pem"),
			objects:   []client.Object{&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "bundle", Namespace: "workload"}, BinaryData: map[string][]byte{"custom.pem": validPEM}}},
			want:      validPEM,
		},
		{
			name:      "missing key",
			namespace: "workload",
			ref:       caBundleTestRef("missing"),
			objects:   []client.Object{&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "bundle", Namespace: "workload"}, Data: map[string]string{}}},
			wantErr:   "does not contain key",
		},
		{
			name:      "empty PEM",
			namespace: "workload",
			ref:       caBundleTestRef("ca.crt"),
			objects:   []client.Object{&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "bundle", Namespace: "workload"}, Data: map[string]string{"ca.crt": "  "}}},
			wantErr:   "certificate data is empty",
		},
		{
			name:      "invalid PEM",
			namespace: "workload",
			ref:       caBundleTestRef("ca.crt"),
			objects:   []client.Object{&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "bundle", Namespace: "workload"}, Data: map[string]string{"ca.crt": "not a certificate"}}},
			wantErr:   "non-PEM certificate data",
		},
		{
			name:      "optional is rejected",
			namespace: "workload",
			ref:       &mcpv1beta1.CABundleSource{ConfigMapRef: &corev1.ConfigMapKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: "bundle"}, Key: "ca.crt", Optional: &optional}},
			wantErr:   "optional must be false or omitted",
		},
		{
			name:      "same namespace is required",
			namespace: "workload",
			ref:       caBundleTestRef("ca.crt"),
			objects:   []client.Object{&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "bundle", Namespace: "other"}, Data: map[string]string{"ca.crt": string(validPEM)}}},
			wantErr:   "failed to get CA bundle ConfigMap",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := ResolveCABundle(
				context.Background(),
				fake.NewClientBuilder().WithObjects(tt.objects...).Build(),
				tt.namespace,
				tt.ref,
			)
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestResolveCABundlePreservesTransientAPIError(t *testing.T) {
	t.Parallel()
	transient := errors.New("temporary apiserver failure")
	c := fake.NewClientBuilder().WithInterceptorFuncs(interceptor.Funcs{
		Get: func(context.Context, client.WithWatch, client.ObjectKey, client.Object, ...client.GetOption) error {
			return transient
		},
	}).Build()

	_, err := ResolveCABundle(context.Background(), c, "workload", caBundleTestRef("ca.crt"))
	require.Error(t, err)
	require.ErrorIs(t, err, transient)
}

func TestValidateEmbeddedAuthServerCABundlesClassifiesErrors(t *testing.T) {
	t.Parallel()

	config := &mcpv1beta1.EmbeddedAuthServerConfig{UpstreamProviders: []mcpv1beta1.UpstreamProviderConfig{{
		Name: "upstream",
		Type: mcpv1beta1.UpstreamProviderTypeOIDC,
		OIDCConfig: &mcpv1beta1.OIDCUpstreamConfig{
			CABundleRef: caBundleTestRef("ca.crt"),
		},
	}}}
	transient := errors.New("temporary apiserver failure")
	tests := []struct {
		name           string
		client         client.Reader
		wantInvalidCA  bool
		wantUnderlying error
	}{
		{
			name: "invalid content is terminal through wrapping",
			client: fake.NewClientBuilder().WithObjects(&corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{Name: "bundle", Namespace: "workload"},
				Data:       map[string]string{"ca.crt": "not a certificate"},
			}).Build(),
			wantInvalidCA: true,
		},
		{
			name: "ConfigMap Get failure remains transient",
			client: fake.NewClientBuilder().WithInterceptorFuncs(interceptor.Funcs{
				Get: func(context.Context, client.WithWatch, client.ObjectKey, client.Object, ...client.GetOption) error {
					return transient
				},
			}).Build(),
			wantUnderlying: transient,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := ValidateEmbeddedAuthServerCABundles(context.Background(), tt.client, "workload", config)
			require.Error(t, err)
			var invalidCABundleErr *InvalidCABundleError
			assert.Equal(t, tt.wantInvalidCA, errors.As(err, &invalidCABundleErr))
			if tt.wantUnderlying != nil {
				require.ErrorIs(t, err, tt.wantUnderlying)
			}
		})
	}
}

func caBundleTestRef(key string) *mcpv1beta1.CABundleSource {
	return &mcpv1beta1.CABundleSource{ConfigMapRef: &corev1.ConfigMapKeySelector{
		LocalObjectReference: corev1.LocalObjectReference{Name: "bundle"},
		Key:                  key,
	}}
}

func testCertificatePEM(t *testing.T) []byte {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test"}, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	require.NoError(t, err)
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

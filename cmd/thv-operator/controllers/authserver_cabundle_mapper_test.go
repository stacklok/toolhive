// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
	"github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1/v1beta1test"
	"github.com/stacklok/toolhive/cmd/thv-operator/internal/testutil"
	ctrlutil "github.com/stacklok/toolhive/cmd/thv-operator/pkg/controllerutil"
	"github.com/stacklok/toolhive/pkg/container/kubernetes"
)

func mapperTestCertificatePEM(t *testing.T) []byte {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test"}, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	require.NoError(t, err)
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

func TestMCPRemoteProxyCABundleChecksumDrift(t *testing.T) {
	t.Parallel()
	scheme := testutil.NewScheme(t)
	ca := mapperTestCertificatePEM(t)
	cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "ca", Namespace: "default"}, Data: map[string]string{"ca.crt": string(ca)}}
	auth := mapperCABundleConfig("ca")
	proxy := v1beta1test.NewMCPRemoteProxy("proxy", "default", v1beta1test.WithRemoteProxyExternalAuthConfigRef("auth"))
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(cm, auth, proxy).Build()
	r := &MCPRemoteProxyReconciler{Client: c, Scheme: scheme, PlatformDetector: ctrlutil.NewSharedPlatformDetector()}
	deployment := r.deploymentForMCPRemoteProxy(t.Context(), proxy, "run")
	require.NotNil(t, deployment)
	checksum := deployment.Spec.Template.Annotations[ctrlutil.AuthServerCABundleChecksumAnnotation]
	require.NotEmpty(t, checksum)
	assert.False(t, r.podTemplateMetadataNeedsUpdate(t.Context(), deployment, proxy, "run"))
	cm.Data["ca.crt"] = string(mapperTestCertificatePEM(t))
	require.NoError(t, c.Update(t.Context(), cm))
	assert.True(t, r.podTemplateMetadataNeedsUpdate(t.Context(), deployment, proxy, "run"))
	deployment.Spec.Template.Annotations[ctrlutil.AuthServerCABundleChecksumAnnotation] = "stale"
	assert.True(t, r.podTemplateMetadataNeedsUpdate(t.Context(), deployment, proxy, "run"))
	newChecksum, err := ctrlutil.EmbeddedAuthServerCABundleChecksum(t.Context(), c, "default", "auth")
	require.NoError(t, err)
	deployment.Spec.Template.Annotations[ctrlutil.AuthServerCABundleChecksumAnnotation] = newChecksum
	assert.False(t, r.podTemplateMetadataNeedsUpdate(t.Context(), deployment, proxy, "run"))
	auth.Spec.EmbeddedAuthServer.UpstreamProviders[0].OIDCConfig.CABundleRef = nil
	require.NoError(t, c.Update(t.Context(), auth))
	assert.True(t, r.podTemplateMetadataNeedsUpdate(t.Context(), deployment, proxy, "run"))
}

func mapperCABundleConfig(caName string) *mcpv1beta1.MCPExternalAuthConfig {
	return &mcpv1beta1.MCPExternalAuthConfig{ObjectMeta: metav1.ObjectMeta{Name: "auth", Namespace: "default"}, Spec: mcpv1beta1.MCPExternalAuthConfigSpec{
		Type: mcpv1beta1.ExternalAuthTypeEmbeddedAuthServer,
		EmbeddedAuthServer: &mcpv1beta1.EmbeddedAuthServerConfig{UpstreamProviders: []mcpv1beta1.UpstreamProviderConfig{{
			Name: "issuer", Type: mcpv1beta1.UpstreamProviderTypeOIDC,
			OIDCConfig: &mcpv1beta1.OIDCUpstreamConfig{CABundleRef: &mcpv1beta1.CABundleSource{ConfigMapRef: &corev1.ConfigMapKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: caName}}}},
		}}},
	}}
}

func TestMCPServerCABundleChecksumDrift(t *testing.T) {
	t.Parallel()
	scheme := testutil.NewScheme(t)
	ca := mapperTestCertificatePEM(t)
	cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "ca", Namespace: "default"}, Data: map[string]string{"ca.crt": string(ca)}}
	auth := mapperCABundleConfig("ca")
	server := v1beta1test.NewMCPServer("server", "default", v1beta1test.WithExternalAuthConfigRef("auth"))
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(cm, auth, server).Build()
	r := newTestMCPServerReconciler(c, scheme, kubernetes.PlatformKubernetes)
	deployment, err := r.deploymentForMCPServer(t.Context(), server, "run")
	require.NoError(t, err)
	checksum := deployment.Spec.Template.Annotations[ctrlutil.AuthServerCABundleChecksumAnnotation]
	require.NotEmpty(t, checksum)
	assert.False(t, r.deploymentNeedsUpdate(t.Context(), deployment, server, "run"),
		"matching checksum must not be reported as drift")

	// Rotating the CA content must be detected. Mutate the ConfigMap rather than
	// writing a literal so the checksum function itself is exercised.
	cm.Data["ca.crt"] = string(mapperTestCertificatePEM(t))
	require.NoError(t, c.Update(t.Context(), cm))
	assert.True(t, r.deploymentNeedsUpdate(t.Context(), deployment, server, "run"),
		"changed CA bundle content must be reported as drift")

	rotated, err := ctrlutil.EmbeddedAuthServerCABundleChecksum(t.Context(), c, "default", "auth")
	require.NoError(t, err)
	require.NotEqual(t, checksum, rotated)
	deployment.Spec.Template.Annotations[ctrlutil.AuthServerCABundleChecksumAnnotation] = rotated

	// A foreign annotation written by someone else (kubectl rollout restart and
	// friends) must not be reverted as drift -- see #6344.
	deployment.Spec.Template.Annotations["foreign.example/key"] = "preserve"
	assert.False(t, r.deploymentNeedsUpdate(t.Context(), deployment, server, "run"),
		"externally-written annotations must not be treated as drift")
	auth.Spec.EmbeddedAuthServer.UpstreamProviders[0].OIDCConfig.CABundleRef = nil
	require.NoError(t, c.Update(t.Context(), auth))
	assert.True(t, r.deploymentNeedsUpdate(t.Context(), deployment, server, "run"),
		"removing caBundleRef must be reported as drift so the stale annotation is cleared")
}

func TestMapAuthServerCABundleConfigMapToMCPServer(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name               string
		configMapNamespace string
		config             *mcpv1beta1.MCPExternalAuthConfig
		serverNamespace    string
		want               int
	}{
		{"referenced", "default", mapperCABundleConfig("ca"), "default", 1},
		{"unreferenced", "default", mapperCABundleConfig("other"), "default", 0},
		{"different namespace", "other", mapperCABundleConfig("ca"), "default", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			scheme := testutil.NewScheme(t)
			cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "ca", Namespace: tc.configMapNamespace}}
			server := v1beta1test.NewMCPServer("server", tc.serverNamespace, v1beta1test.WithExternalAuthConfigRef("auth"))
			objects := []client.Object{cm, tc.config, server}
			c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).WithIndex(&mcpv1beta1.MCPExternalAuthConfig{}, EmbeddedAuthCABundleConfigMapIndex, IndexEmbeddedAuthCABundleConfigMaps).WithIndex(&mcpv1beta1.MCPServer{}, EmbeddedAuthConfigIndex, indexMCPServerEmbeddedAuthConfig).Build()
			r := &MCPServerReconciler{Client: c}
			requests := r.mapAuthServerCABundleConfigMapToMCPServer(t.Context(), cm)
			assert.Len(t, requests, tc.want)
		})
	}
}

func TestMapAuthServerCABundleConfigMapToMCPRemoteProxy(t *testing.T) {
	t.Parallel()
	scheme := testutil.NewScheme(t)
	auth := mapperCABundleConfig("ca")
	proxy1 := v1beta1test.NewMCPRemoteProxy("proxy-one", "default", v1beta1test.WithRemoteProxyExternalAuthConfigRef("auth"))
	proxy2 := v1beta1test.NewMCPRemoteProxy("proxy-two", "default", v1beta1test.WithRemoteProxyExternalAuthConfigRef("auth"))
	cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "ca", Namespace: "default"}}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(cm, auth, proxy1, proxy2).
		WithIndex(&mcpv1beta1.MCPExternalAuthConfig{}, EmbeddedAuthCABundleConfigMapIndex, IndexEmbeddedAuthCABundleConfigMaps).
		WithIndex(&mcpv1beta1.MCPRemoteProxy{}, EmbeddedAuthConfigIndex, indexMCPRemoteProxyEmbeddedAuthConfig).Build()
	r := &MCPRemoteProxyReconciler{Client: c}
	requests := r.mapAuthServerCABundleConfigMapToMCPRemoteProxy(t.Context(), cm)
	assert.Len(t, requests, 2)
}

// TestVirtualMCPServerCABundleChecksumDrift guards the vMCP half of the rotation
// path. The bundle is mounted with subPath, so kubelet never refreshes it in a
// running pod: only a pod template change rolls the new trust material out. The
// checksum annotation is the mechanism, and buildPodTemplateMetadata is shared by
// the builder and the drift check so the two cannot disagree.
func TestVirtualMCPServerCABundleChecksumDrift(t *testing.T) {
	t.Parallel()
	scheme := testutil.NewScheme(t)
	r := &VirtualMCPServerReconciler{Scheme: scheme, PlatformDetector: ctrlutil.NewSharedPlatformDetector()}
	vmcp := v1beta1test.NewVirtualMCPServer("vmcp", "default", v1beta1test.WithVMCPGroupRef("group"))

	withBundle := r.deploymentForVirtualMCPServer(t.Context(), vmcp, "run", "checksum-a", nil, nil)
	require.NotNil(t, withBundle)
	assert.Equal(t, "checksum-a",
		withBundle.Spec.Template.Annotations[ctrlutil.AuthServerCABundleChecksumAnnotation],
		"the CA checksum must reach the pod template, otherwise a rotation never rolls out")

	rotated := r.deploymentForVirtualMCPServer(t.Context(), vmcp, "run", "checksum-b", nil, nil)
	require.NotNil(t, rotated)
	assert.NotEqual(t,
		withBundle.Spec.Template.Annotations[ctrlutil.AuthServerCABundleChecksumAnnotation],
		rotated.Spec.Template.Annotations[ctrlutil.AuthServerCABundleChecksumAnnotation],
		"a rotated bundle must produce a different pod template")

	none := r.deploymentForVirtualMCPServer(t.Context(), vmcp, "run", "", nil, nil)
	require.NotNil(t, none)
	assert.NotContains(t, none.Spec.Template.Annotations, ctrlutil.AuthServerCABundleChecksumAnnotation,
		"no CA bundle configured must not stamp an empty annotation")
}

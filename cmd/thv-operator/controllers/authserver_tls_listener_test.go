// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
	"github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1/v1beta1test"
	"github.com/stacklok/toolhive/cmd/thv-operator/internal/testutil"
	ctrlutil "github.com/stacklok/toolhive/cmd/thv-operator/pkg/controllerutil"
	"github.com/stacklok/toolhive/pkg/container/kubernetes"
	"github.com/stacklok/toolhive/pkg/vmcp/workloads"
)

func tlsListenerAuthConfig(certSecret, certKey, keySecret, keyKey string) *mcpv1beta1.MCPExternalAuthConfig {
	return &mcpv1beta1.MCPExternalAuthConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "auth", Namespace: "default"},
		Spec: mcpv1beta1.MCPExternalAuthConfigSpec{
			Type: mcpv1beta1.ExternalAuthTypeEmbeddedAuthServer,
			EmbeddedAuthServer: &mcpv1beta1.EmbeddedAuthServerConfig{
				TLSListener: &mcpv1beta1.TLSListenerConfig{
					CertificateSecretRef: &mcpv1beta1.SecretKeyRef{Name: certSecret, Key: certKey},
					PrivateKeySecretRef:  &mcpv1beta1.SecretKeyRef{Name: keySecret, Key: keyKey},
				},
			},
		},
	}
}

func requireTLSListenerVolumeAndMount(t *testing.T, podSpec corev1.PodSpec) {
	t.Helper()
	require.NotEmpty(t, podSpec.Containers)
	var volume *corev1.Volume
	for i := range podSpec.Volumes {
		if podSpec.Volumes[i].Name == ctrlutil.AuthServerTLSVolumeName {
			volume = &podSpec.Volumes[i]
		}
	}
	require.NotNil(t, volume, "TLS listener volume missing")
	require.NotNil(t, volume.Projected)
	var mounted bool
	for _, mount := range podSpec.Containers[0].VolumeMounts {
		if mount.Name == ctrlutil.AuthServerTLSVolumeName {
			mounted = true
			assert.Equal(t, ctrlutil.AuthServerTLSMountPath, mount.MountPath)
			assert.True(t, mount.ReadOnly)
		}
	}
	assert.True(t, mounted, "TLS listener mount missing")
}

func TestDeploymentsMountTLSListenerCredentials(t *testing.T) {
	t.Parallel()

	auth := tlsListenerAuthConfig("listener-tls", "certificate", "listener-tls", "private-key")

	t.Run("MCPServer", func(t *testing.T) {
		t.Parallel()
		scheme := testutil.NewScheme(t)
		server := v1beta1test.NewMCPServer("server", "default", v1beta1test.WithAuthServerRef("MCPExternalAuthConfig", "auth"))
		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(auth.DeepCopy(), server).Build()
		r := newTestMCPServerReconciler(c, scheme, kubernetes.PlatformKubernetes)

		deployment, err := r.deploymentForMCPServer(t.Context(), server, "run")
		require.NoError(t, err)
		requireTLSListenerVolumeAndMount(t, deployment.Spec.Template.Spec)
	})

	t.Run("MCPRemoteProxy", func(t *testing.T) {
		t.Parallel()
		proxy := v1beta1test.NewMCPRemoteProxy("proxy", "default", v1beta1test.WithRemoteProxyAuthServerRef("MCPExternalAuthConfig", "auth"))
		r, _ := newTestMCPRemoteProxyReconciler(t, auth.DeepCopy(), proxy)

		deployment := r.deploymentForMCPRemoteProxy(t.Context(), proxy, "run")
		require.NotNil(t, deployment)
		requireTLSListenerVolumeAndMount(t, deployment.Spec.Template.Spec)
	})

	t.Run("VirtualMCPServer", func(t *testing.T) {
		t.Parallel()
		vmcp := v1beta1test.NewVirtualMCPServer("vmcp", "default", v1beta1test.WithVMCPGroupRef("group"))
		vmcp.Spec.AuthServerConfig = auth.Spec.EmbeddedAuthServer.DeepCopy()
		r := &VirtualMCPServerReconciler{Scheme: testutil.NewScheme(t), PlatformDetector: ctrlutil.NewSharedPlatformDetector()}

		deployment := r.deploymentForVirtualMCPServer(t.Context(), vmcp, "run", "", nil, []workloads.TypedWorkload{})
		require.NotNil(t, deployment)
		requireTLSListenerVolumeAndMount(t, deployment.Spec.Template.Spec)
	})
}

// Only a Secret or ConfigMap name differs between the cases, so the RunConfig
// checksum and the mounted paths are identical and the volume is the only
// signal. The per-volume-type comparison is covered by
// TestAuthServerVolumesNeedUpdate; this test proves each controller feeds the
// live and desired volumes into it.
func TestDeploymentNeedsUpdateOnAuthServerVolumeRefChange(t *testing.T) {
	t.Parallel()

	newAuth := func() *mcpv1beta1.MCPExternalAuthConfig {
		auth := tlsListenerAuthConfig("listener-cert", "certificate", "listener-key", "private-key")
		auth.Spec.EmbeddedAuthServer.SPIFFETrustDomains = []mcpv1beta1.SPIFFETrustDomainConfig{{
			Name: "example", TrustDomain: "example.org",
			BundleSource: mcpv1beta1.SPIFFEBundleSourceConfig{
				Type: mcpv1beta1.SPIFFEBundleSourceTypeFile,
				File: &mcpv1beta1.SPIFFEFileBundleSourceConfig{ConfigMapName: "bundle", ConfigMapKey: "bundle.json"},
			},
		}}
		return auth
	}
	changes := []struct {
		name   string
		mutate func(*mcpv1beta1.EmbeddedAuthServerConfig)
	}{
		{"certificate secret renamed", func(c *mcpv1beta1.EmbeddedAuthServerConfig) { c.TLSListener.CertificateSecretRef.Name = "other-cert" }},
		{"bundle configmap renamed", func(c *mcpv1beta1.EmbeddedAuthServerConfig) {
			c.SPIFFETrustDomains[0].BundleSource.File.ConfigMapName = "other-bundle"
		}},
	}

	for _, change := range changes {
		t.Run("MCPServer "+change.name, func(t *testing.T) {
			t.Parallel()
			scheme := testutil.NewScheme(t)
			auth := newAuth()
			server := v1beta1test.NewMCPServer("server", "default", v1beta1test.WithAuthServerRef("MCPExternalAuthConfig", "auth"))
			c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(auth, server).Build()
			r := newTestMCPServerReconciler(c, scheme, kubernetes.PlatformKubernetes)

			deployment, err := r.deploymentForMCPServer(t.Context(), server, "run")
			require.NoError(t, err)
			assert.False(t, r.deploymentNeedsUpdate(t.Context(), deployment, server, "run"), "steady state")

			change.mutate(auth.Spec.EmbeddedAuthServer)
			require.NoError(t, c.Update(t.Context(), auth))
			assert.True(t, r.deploymentNeedsUpdate(t.Context(), deployment, server, "run"))
		})

		for _, variant := range []struct {
			name        string
			podTemplate *runtime.RawExtension
		}{
			{name: "MCPRemoteProxy"},
			{name: "MCPRemoteProxy with podTemplateSpec", podTemplate: &runtime.RawExtension{Raw: []byte(`{"spec":{"nodeSelector":{"disk":"ssd"}}}`)}},
		} {
			t.Run(variant.name+" "+change.name, func(t *testing.T) {
				t.Parallel()
				auth := newAuth()
				proxy := v1beta1test.NewMCPRemoteProxy("proxy", "default", v1beta1test.WithRemoteProxyAuthServerRef("MCPExternalAuthConfig", "auth"))
				proxy.Spec.PodTemplateSpec = variant.podTemplate
				r, c := newTestMCPRemoteProxyReconciler(t, auth, proxy)

				deployment := r.deploymentForMCPRemoteProxy(t.Context(), proxy, "run")
				require.NotNil(t, deployment)
				if variant.podTemplate != nil {
					require.Equal(t, map[string]string{"disk": "ssd"}, deployment.Spec.Template.Spec.NodeSelector)
				}
				assert.False(t, r.deploymentNeedsUpdate(t.Context(), deployment, proxy, "run"), "steady state")

				change.mutate(auth.Spec.EmbeddedAuthServer)
				require.NoError(t, c.Update(t.Context(), auth))
				assert.True(t, r.deploymentNeedsUpdate(t.Context(), deployment, proxy, "run"))
			})
		}
	}
}

func requireProxyPortClash(
	t *testing.T, condition *metav1.Condition, wantReason string,
	phase, failedPhase any, generation, observedGeneration int64,
) {
	t.Helper()
	assert.Equal(t, failedPhase, phase)
	assert.Equal(t, generation, observedGeneration)
	require.NotNil(t, condition)
	assert.Equal(t, metav1.ConditionFalse, condition.Status)
	assert.Equal(t, wantReason, condition.Reason)
	assert.Equal(t, generation, condition.ObservedGeneration)
	assert.Contains(t, condition.Message, "proxyPort 8443")
}

func TestReconcileRejectsProxyPortClashWithTLSListener(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		viaAuthServer bool
	}{
		{name: "clash via authServerRef", viaAuthServer: true},
		{name: "clash via externalAuthConfigRef"},
	}
	const proxyPort = 8443
	const resourceURL = "https://mcp.example.com"
	oidcConfig := func() *mcpv1beta1.MCPOIDCConfig {
		return &mcpv1beta1.MCPOIDCConfig{
			ObjectMeta: metav1.ObjectMeta{Name: "oidc", Namespace: "default"},
			Spec: mcpv1beta1.MCPOIDCConfigSpec{
				Type:   mcpv1beta1.MCPOIDCConfigTypeInline,
				Inline: &mcpv1beta1.InlineOIDCSharedConfig{Issuer: "https://auth.example.com"},
			},
			Status: mcpv1beta1.MCPOIDCConfigStatus{Conditions: []metav1.Condition{{
				Type: mcpv1beta1.ConditionTypeValid, Status: metav1.ConditionTrue, Reason: "ValidationSucceeded",
			}}},
		}
	}
	for _, tt := range tests {
		authConfig := func() *mcpv1beta1.MCPExternalAuthConfig {
			auth := tlsListenerAuthConfig("listener-tls", "certificate", "listener-tls", "private-key")
			auth.Spec.EmbeddedAuthServer.Issuer = "https://auth.example.com"
			auth.Spec.EmbeddedAuthServer.UpstreamProviders = []mcpv1beta1.UpstreamProviderConfig{{
				Name:       "github",
				Type:       mcpv1beta1.UpstreamProviderTypeOIDC,
				OIDCConfig: &mcpv1beta1.OIDCUpstreamConfig{IssuerURL: "https://github.com", ClientID: "client-id"},
			}}
			return auth
		}

		t.Run("MCPServer "+tt.name, func(t *testing.T) {
			t.Parallel()
			refOption := v1beta1test.WithExternalAuthConfigRef("auth")
			condType, reason := mcpv1beta1.ConditionTypeExternalAuthConfigValidated, mcpv1beta1.ConditionReasonInvalidConfig
			if tt.viaAuthServer {
				refOption = v1beta1test.WithAuthServerRef("MCPExternalAuthConfig", "auth")
				condType, reason = mcpv1beta1.ConditionTypeAuthServerRefValidated, mcpv1beta1.ConditionReasonAuthServerRefInvalidConfig
			}
			server := v1beta1test.NewMCPServer("server", "default", v1beta1test.WithImage("test"),
				v1beta1test.WithProxyPort(proxyPort), refOption,
				v1beta1test.WithOIDCConfigRef("oidc", resourceURL),
				v1beta1test.Mutate(func(m *mcpv1beta1.MCPServer) { m.Spec.OIDCConfigRef.ResourceURL = resourceURL }))
			scheme := testutil.NewScheme(t)
			c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(server, authConfig(), oidcConfig()).
				WithStatusSubresource(&mcpv1beta1.MCPServer{}).Build()
			r := newTestMCPServerReconciler(c, scheme, kubernetes.PlatformKubernetes)
			req := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(server)}

			result, err := r.Reconcile(t.Context(), req)
			actual := &mcpv1beta1.MCPServer{}
			require.NoError(t, c.Get(t.Context(), req.NamespacedName, actual))
			condition := meta.FindStatusCondition(actual.Status.Conditions, condType)
			require.NoError(t, err, "a port clash is terminal and must not requeue with an error")
			assert.Zero(t, result)
			requireProxyPortClash(t, condition, reason, actual.Status.Phase, mcpv1beta1.MCPServerPhaseFailed,
				actual.Generation, actual.Status.ObservedGeneration)

			initial := actual.DeepCopy()
			result, err = r.Reconcile(t.Context(), req)
			require.NoError(t, err)
			assert.Zero(t, result)
			require.NoError(t, c.Get(t.Context(), req.NamespacedName, actual))
			assert.Equal(t, initial.Status, actual.Status)
		})

		t.Run("MCPRemoteProxy "+tt.name, func(t *testing.T) {
			t.Parallel()
			refOption := v1beta1test.WithRemoteProxyExternalAuthConfigRef("auth")
			condType := mcpv1beta1.ConditionTypeMCPRemoteProxyExternalAuthConfigValidated
			reason := mcpv1beta1.ConditionReasonInvalidConfig
			if tt.viaAuthServer {
				refOption = v1beta1test.WithRemoteProxyAuthServerRef("MCPExternalAuthConfig", "auth")
				condType = mcpv1beta1.ConditionTypeMCPRemoteProxyAuthServerRefValidated
				reason = mcpv1beta1.ConditionReasonMCPRemoteProxyAuthServerRefInvalidConfig
			}
			proxy := v1beta1test.NewMCPRemoteProxy("proxy", "default",
				v1beta1test.WithRemoteProxyURL("https://remote.example.com"), v1beta1test.WithRemoteProxyPort(proxyPort), refOption,
				v1beta1test.WithRemoteProxyOIDCConfigRef("oidc", resourceURL),
				v1beta1test.MutateRemoteProxy(func(p *mcpv1beta1.MCPRemoteProxy) { p.Spec.OIDCConfigRef.ResourceURL = resourceURL }))
			r, c := newTestMCPRemoteProxyReconciler(t, proxy, authConfig(), oidcConfig())
			req := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(proxy)}

			result, err := r.Reconcile(t.Context(), req)
			actual := &mcpv1beta1.MCPRemoteProxy{}
			require.NoError(t, c.Get(t.Context(), req.NamespacedName, actual))
			condition := meta.FindStatusCondition(actual.Status.Conditions, condType)
			require.NoError(t, err, "a port clash is terminal and must not requeue with an error")
			assert.Zero(t, result)
			requireProxyPortClash(t, condition, reason, actual.Status.Phase, mcpv1beta1.MCPRemoteProxyPhaseFailed,
				actual.Generation, actual.Status.ObservedGeneration)

			initial := actual.DeepCopy()
			result, err = r.Reconcile(t.Context(), req)
			require.NoError(t, err)
			assert.Zero(t, result)
			require.NoError(t, c.Get(t.Context(), req.NamespacedName, actual))
			assert.Equal(t, initial.Status, actual.Status)
		})
	}
}

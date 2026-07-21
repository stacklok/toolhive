// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
	"github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1/v1beta1test"
	"github.com/stacklok/toolhive/cmd/thv-operator/internal/testutil"
	ctrlutil "github.com/stacklok/toolhive/cmd/thv-operator/pkg/controllerutil"
	"github.com/stacklok/toolhive/cmd/thv-operator/pkg/validation"
)

const (
	testCABundleConfigMapName = "oidc-ca-bundle-cm"
	testProxyOIDCConfigName   = "proxy-oidc"
	testCABundleNamespace     = "default"
)

// newOIDCConfigWithCABundle builds an inline MCPOIDCConfig in the test namespace,
// optionally carrying a CA bundle reference. A nil caRef produces an inline config
// with no CA bundle.
func newOIDCConfigWithCABundle(caRef *mcpv1beta1.CABundleSource) *mcpv1beta1.MCPOIDCConfig {
	return &mcpv1beta1.MCPOIDCConfig{
		ObjectMeta: metav1.ObjectMeta{Name: testProxyOIDCConfigName, Namespace: testCABundleNamespace},
		Spec: mcpv1beta1.MCPOIDCConfigSpec{
			Type: mcpv1beta1.MCPOIDCConfigTypeInline,
			Inline: &mcpv1beta1.InlineOIDCSharedConfig{
				Issuer:      "https://auth.example.com",
				ClientID:    "test-client",
				CABundleRef: caRef,
			},
		},
	}
}

// caBundleConfigMap builds the CA bundle ConfigMap in the test namespace holding a
// dummy certificate under the default CA bundle key.
func caBundleConfigMap() *corev1.ConfigMap {
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: testCABundleConfigMapName, Namespace: testCABundleNamespace},
		Data:       map[string]string{validation.OIDCCABundleDefaultKey: "cert"},
	}
}

func configMapCABundleRef(name, key string) *mcpv1beta1.CABundleSource {
	return &mcpv1beta1.CABundleSource{
		ConfigMapRef: &corev1.ConfigMapKeySelector{
			LocalObjectReference: corev1.LocalObjectReference{Name: name},
			Key:                  key,
		},
	}
}

// TestMCPRemoteProxyValidateCABundleRef covers the OIDC CA bundle reference validation
// that mirrors MCPServer, including the persisted CABundleRefValidated condition.
func TestMCPRemoteProxyValidateCABundleRef(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name               string
		proxy              *mcpv1beta1.MCPRemoteProxy
		oidcConfig         *mcpv1beta1.MCPOIDCConfig
		configMap          *corev1.ConfigMap
		expectNoCondition  bool
		expectedCondStatus metav1.ConditionStatus
		expectedCondReason string
	}{
		{
			name:              "no OIDCConfigRef sets no condition",
			proxy:             v1beta1test.NewMCPRemoteProxy("p", testCABundleNamespace),
			expectNoCondition: true,
		},
		{
			name: "OIDCConfig without CA bundle sets no condition",
			proxy: v1beta1test.NewMCPRemoteProxy("p", testCABundleNamespace,
				v1beta1test.WithRemoteProxyOIDCConfigRef(testProxyOIDCConfigName, "aud")),
			oidcConfig:        newOIDCConfigWithCABundle(nil),
			expectNoCondition: true,
		},
		{
			name: "valid CA bundle with existing ConfigMap and key",
			proxy: v1beta1test.NewMCPRemoteProxy("p", testCABundleNamespace,
				v1beta1test.WithRemoteProxyOIDCConfigRef(testProxyOIDCConfigName, "aud")),
			oidcConfig:         newOIDCConfigWithCABundle(configMapCABundleRef(testCABundleConfigMapName, "ca.crt")),
			configMap:          caBundleConfigMap(),
			expectedCondStatus: metav1.ConditionTrue,
			expectedCondReason: mcpv1beta1.ConditionReasonMCPRemoteProxyCABundleRefValid,
		},
		{
			name: "valid CA bundle with empty key defaults to ca.crt",
			proxy: v1beta1test.NewMCPRemoteProxy("p", testCABundleNamespace,
				v1beta1test.WithRemoteProxyOIDCConfigRef(testProxyOIDCConfigName, "aud")),
			oidcConfig:         newOIDCConfigWithCABundle(configMapCABundleRef(testCABundleConfigMapName, "")),
			configMap:          caBundleConfigMap(),
			expectedCondStatus: metav1.ConditionTrue,
			expectedCondReason: mcpv1beta1.ConditionReasonMCPRemoteProxyCABundleRefValid,
		},
		{
			name: "CA bundle with empty ConfigMap name is invalid",
			proxy: v1beta1test.NewMCPRemoteProxy("p", testCABundleNamespace,
				v1beta1test.WithRemoteProxyOIDCConfigRef(testProxyOIDCConfigName, "aud")),
			oidcConfig:         newOIDCConfigWithCABundle(configMapCABundleRef("", "ca.crt")),
			expectedCondStatus: metav1.ConditionFalse,
			expectedCondReason: mcpv1beta1.ConditionReasonMCPRemoteProxyCABundleRefInvalid,
		},
		{
			name: "CA bundle referencing missing ConfigMap is not found",
			proxy: v1beta1test.NewMCPRemoteProxy("p", testCABundleNamespace,
				v1beta1test.WithRemoteProxyOIDCConfigRef(testProxyOIDCConfigName, "aud")),
			oidcConfig:         newOIDCConfigWithCABundle(configMapCABundleRef("does-not-exist", "ca.crt")),
			expectedCondStatus: metav1.ConditionFalse,
			expectedCondReason: mcpv1beta1.ConditionReasonMCPRemoteProxyCABundleRefNotFound,
		},
		{
			name: "CA bundle with missing key in ConfigMap is invalid",
			proxy: v1beta1test.NewMCPRemoteProxy("p", testCABundleNamespace,
				v1beta1test.WithRemoteProxyOIDCConfigRef(testProxyOIDCConfigName, "aud")),
			oidcConfig:         newOIDCConfigWithCABundle(configMapCABundleRef(testCABundleConfigMapName, "missing-key")),
			configMap:          caBundleConfigMap(),
			expectedCondStatus: metav1.ConditionFalse,
			expectedCondReason: mcpv1beta1.ConditionReasonMCPRemoteProxyCABundleRefInvalid,
		},
		{
			name: "stale condition is cleared when CA bundle is removed",
			proxy: v1beta1test.NewMCPRemoteProxy("p", testCABundleNamespace,
				v1beta1test.WithRemoteProxyOIDCConfigRef(testProxyOIDCConfigName, "aud"),
				v1beta1test.WithRemoteProxyStatus(mcpv1beta1.MCPRemoteProxyStatus{
					Conditions: []metav1.Condition{{
						Type:   mcpv1beta1.ConditionTypeMCPRemoteProxyCABundleRefValidated,
						Status: metav1.ConditionTrue,
						Reason: mcpv1beta1.ConditionReasonMCPRemoteProxyCABundleRefValid,
					}},
				}),
			),
			oidcConfig:        newOIDCConfigWithCABundle(nil),
			expectNoCondition: true,
		},
		{
			// OIDCConfig cannot be resolved (object absent → fetch error). The existing
			// condition must NOT be cleared on a transient failure; handleOIDCConfig owns
			// the OIDCConfigRef failure and requeues.
			name: "transient OIDCConfig fetch error leaves the existing condition untouched",
			proxy: v1beta1test.NewMCPRemoteProxy("p", testCABundleNamespace,
				v1beta1test.WithRemoteProxyOIDCConfigRef(testProxyOIDCConfigName, "aud"),
				v1beta1test.WithRemoteProxyStatus(mcpv1beta1.MCPRemoteProxyStatus{
					Conditions: []metav1.Condition{{
						Type:   mcpv1beta1.ConditionTypeMCPRemoteProxyCABundleRefValidated,
						Status: metav1.ConditionTrue,
						Reason: mcpv1beta1.ConditionReasonMCPRemoteProxyCABundleRefValid,
					}},
				}),
			),
			oidcConfig:         nil, // absent from the client → GetOIDCConfigForServer errors
			expectedCondStatus: metav1.ConditionTrue,
			expectedCondReason: mcpv1beta1.ConditionReasonMCPRemoteProxyCABundleRefValid,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx := t.Context()

			scheme := testutil.NewScheme(t)
			builder := fake.NewClientBuilder().
				WithScheme(scheme).
				WithStatusSubresource(&mcpv1beta1.MCPRemoteProxy{}).
				WithObjects(tt.proxy)
			if tt.oidcConfig != nil {
				builder = builder.WithObjects(tt.oidcConfig)
			}
			if tt.configMap != nil {
				builder = builder.WithObjects(tt.configMap)
			}
			fakeClient := builder.Build()

			reconciler := &MCPRemoteProxyReconciler{Client: fakeClient, Scheme: scheme}
			reconciler.validateCABundleRef(ctx, tt.proxy)

			persisted := &mcpv1beta1.MCPRemoteProxy{}
			require.NoError(t, fakeClient.Get(ctx, types.NamespacedName{
				Name: tt.proxy.Name, Namespace: tt.proxy.Namespace,
			}, persisted))

			cond := meta.FindStatusCondition(persisted.Status.Conditions,
				mcpv1beta1.ConditionTypeMCPRemoteProxyCABundleRefValidated)

			if tt.expectNoCondition {
				assert.Nil(t, cond, "CABundleRefValidated condition should not be present")
				return
			}

			require.NotNil(t, cond, "expected CABundleRefValidated condition to be set")
			assert.Equal(t, tt.expectedCondStatus, cond.Status)
			assert.Equal(t, tt.expectedCondReason, cond.Reason)
		})
	}
}

// TestAddOIDCCABundleVolumes verifies the deployment mounts the OIDC CA bundle
// ConfigMap when the referenced MCPOIDCConfig declares one, so the runner pod can
// read the CA file at the path the RunConfig points it at.
func TestAddOIDCCABundleVolumes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		proxy       *mcpv1beta1.MCPRemoteProxy
		oidcConfig  *mcpv1beta1.MCPOIDCConfig
		expectMount bool
		expectNilM  bool // build aborts (returns nil) on a transient OIDCConfig fetch error
	}{
		{
			name:        "no OIDCConfigRef mounts nothing",
			proxy:       v1beta1test.NewMCPRemoteProxy("p", testCABundleNamespace),
			expectMount: false,
		},
		{
			name: "OIDCConfig without CA bundle mounts nothing",
			proxy: v1beta1test.NewMCPRemoteProxy("p", testCABundleNamespace,
				v1beta1test.WithRemoteProxyOIDCConfigRef(testProxyOIDCConfigName, "aud")),
			oidcConfig:  newOIDCConfigWithCABundle(nil),
			expectMount: false,
		},
		{
			// OIDCConfigRef set but the MCPOIDCConfig object is absent: the fetch
			// errors, so the build aborts (returns nil) rather than producing a
			// CA-less Deployment that would crash-loop.
			name: "OIDCConfigRef set but MCPOIDCConfig missing aborts the build",
			proxy: v1beta1test.NewMCPRemoteProxy("p", testCABundleNamespace,
				v1beta1test.WithRemoteProxyOIDCConfigRef(testProxyOIDCConfigName, "aud")),
			oidcConfig: nil,
			expectNilM: true,
		},
		{
			name: "OIDCConfig with CA bundle mounts the ConfigMap",
			proxy: v1beta1test.NewMCPRemoteProxy("p", testCABundleNamespace,
				v1beta1test.WithRemoteProxyOIDCConfigRef(testProxyOIDCConfigName, "aud")),
			oidcConfig:  newOIDCConfigWithCABundle(configMapCABundleRef(testCABundleConfigMapName, "ca.crt")),
			expectMount: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			scheme := testutil.NewScheme(t)
			builder := fake.NewClientBuilder().WithScheme(scheme).WithObjects(tt.proxy)
			if tt.oidcConfig != nil {
				builder = builder.WithObjects(tt.oidcConfig)
			}
			var fakeClient client.Client = builder.Build()

			reconciler := &MCPRemoteProxyReconciler{
				Client:           fakeClient,
				Scheme:           scheme,
				PlatformDetector: ctrlutil.NewSharedPlatformDetector(),
			}

			dep := reconciler.deploymentForMCPRemoteProxy(t.Context(), tt.proxy, "test-checksum")
			if tt.expectNilM {
				assert.Nil(t, dep, "build should abort (nil) on a transient OIDCConfig fetch error")
				return
			}
			require.NotNil(t, dep)

			expectedVolumeName := validation.OIDCCABundleVolumePrefix + testCABundleConfigMapName
			expectedMountPath := validation.OIDCCABundleMountBasePath + "/" + testCABundleConfigMapName

			hasVolume := false
			for _, v := range dep.Spec.Template.Spec.Volumes {
				if v.Name == expectedVolumeName {
					hasVolume = true
					require.NotNil(t, v.ConfigMap, "CA bundle volume must source from a ConfigMap")
					assert.Equal(t, testCABundleConfigMapName, v.ConfigMap.Name)
				}
			}

			hasMount := false
			for _, m := range containerVolumeMounts(dep) {
				if m.Name == expectedVolumeName {
					hasMount = true
					assert.Equal(t, expectedMountPath, m.MountPath)
					assert.True(t, m.ReadOnly, "CA bundle mount must be read-only")
				}
			}

			assert.Equal(t, tt.expectMount, hasVolume, "volume presence mismatch")
			assert.Equal(t, tt.expectMount, hasMount, "mount presence mismatch")
		})
	}
}

func containerVolumeMounts(dep *appsv1.Deployment) []corev1.VolumeMount {
	if len(dep.Spec.Template.Spec.Containers) == 0 {
		return nil
	}
	return dep.Spec.Template.Spec.Containers[0].VolumeMounts
}

// TestMCPRemoteProxyValidateCABundleRefIdempotent asserts that a second validation
// pass over a steady-state proxy performs no status write (resourceVersion unchanged),
// per the operator idempotency rule.
func TestMCPRemoteProxyValidateCABundleRefIdempotent(t *testing.T) {
	t.Parallel()
	ctx := t.Context()

	scheme := testutil.NewScheme(t)
	proxy := v1beta1test.NewMCPRemoteProxy("p", testCABundleNamespace,
		v1beta1test.WithRemoteProxyOIDCConfigRef(testProxyOIDCConfigName, "aud"))
	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&mcpv1beta1.MCPRemoteProxy{}).
		WithObjects(
			proxy,
			newOIDCConfigWithCABundle(configMapCABundleRef(testCABundleConfigMapName, "ca.crt")),
			caBundleConfigMap(),
		).
		Build()

	reconciler := &MCPRemoteProxyReconciler{Client: fakeClient, Scheme: scheme}
	key := types.NamespacedName{Name: proxy.Name, Namespace: proxy.Namespace}

	// First pass reconciles to steady state and persists the True condition.
	first := &mcpv1beta1.MCPRemoteProxy{}
	require.NoError(t, fakeClient.Get(ctx, key, first))
	reconciler.validateCABundleRef(ctx, first)

	afterFirst := &mcpv1beta1.MCPRemoteProxy{}
	require.NoError(t, fakeClient.Get(ctx, key, afterFirst))
	require.NotNil(t,
		meta.FindStatusCondition(afterFirst.Status.Conditions, mcpv1beta1.ConditionTypeMCPRemoteProxyCABundleRefValidated),
		"first pass should set the CABundleRefValidated condition")
	steadyRV := afterFirst.ResourceVersion

	// Second pass over the now-steady object must not write status again.
	reconciler.validateCABundleRef(ctx, afterFirst)

	afterSecond := &mcpv1beta1.MCPRemoteProxy{}
	require.NoError(t, fakeClient.Get(ctx, key, afterSecond))
	assert.Equal(t, steadyRV, afterSecond.ResourceVersion,
		"steady-state reconcile must not bump resourceVersion")
}

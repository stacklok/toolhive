// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllerutil

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	"github.com/stacklok/toolhive/pkg/runner"
)

func TestWebhookConfigHelpers(t *testing.T) {
	t.Parallel()
	scheme := runtime.NewScheme()
	require.NoError(t, mcpv1alpha1.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))

	timeout := metav1.Duration{Duration: 10 * time.Second}

	config := &mcpv1alpha1.MCPWebhookConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-webhook",
			Namespace: "default",
		},
		Spec: mcpv1alpha1.MCPWebhookConfigSpec{
			Validating: []mcpv1alpha1.WebhookSpec{
				{
					Name:          "val1",
					URL:           "https://val1",
					Timeout:       &timeout,
					HMACSecretRef: &mcpv1alpha1.SecretKeyRef{Name: "hmac-secret", Key: "key"},
				},
			},
			Mutating: []mcpv1alpha1.WebhookSpec{
				{
					Name: "mut1",
					URL:  "https://mut1",
					TLSConfig: &mcpv1alpha1.WebhookTLSConfig{
						CASecretRef:         &mcpv1alpha1.SecretKeyRef{Name: "ca-secret", Key: "ca.crt"},
						ClientCertSecretRef: &mcpv1alpha1.SecretKeyRef{Name: "client-secret", Key: "tls.crt"},
						ClientKeySecretRef:  &mcpv1alpha1.SecretKeyRef{Name: "client-secret", Key: "tls.key"},
					},
				},
			},
		},
	}

	server := &mcpv1alpha1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-server",
			Namespace: "default",
		},
		Spec: mcpv1alpha1.MCPServerSpec{
			WebhookConfigRef: &mcpv1alpha1.WebhookConfigRef{Name: "test-webhook"},
		},
	}

	fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(config, server).Build()
	ctx := context.Background()

	t.Run("GetWebhookConfigByName", func(t *testing.T) {
		res, err := GetWebhookConfigByName(ctx, fakeClient, "default", "test-webhook")
		require.NoError(t, err)
		assert.Equal(t, "test-webhook", res.Name)

		_, err = GetWebhookConfigByName(ctx, fakeClient, "default", "not-found")
		assert.Error(t, err)
	})

	t.Run("GetWebhookConfigForMCPServer", func(t *testing.T) {
		res, err := GetWebhookConfigForMCPServer(ctx, fakeClient, server)
		require.NoError(t, err)
		assert.Equal(t, "test-webhook", res.Name)

		serverNoRef := &mcpv1alpha1.MCPServer{}
		resNoRef, errNoRef := GetWebhookConfigForMCPServer(ctx, fakeClient, serverNoRef)
		require.NoError(t, errNoRef)
		assert.Nil(t, resNoRef)
	})

	t.Run("GenerateWebhookEnvVars", func(t *testing.T) {
		envVars, err := GenerateWebhookEnvVars(ctx, fakeClient, "default", server.Spec.WebhookConfigRef)
		require.NoError(t, err)
		assert.Len(t, envVars, 3)

		var keys []string
		for _, e := range envVars {
			keys = append(keys, e.ValueFrom.SecretKeyRef.Name)
		}
		assert.Contains(t, keys, "hmac-secret")
		assert.Contains(t, keys, "ca-secret")
		assert.Contains(t, keys, "client-secret")
	})

	t.Run("AddWebhookConfigOptions", func(t *testing.T) {
		opts := []runner.RunConfigBuilderOption{}
		err := AddWebhookConfigOptions(ctx, fakeClient, "default", server.Spec.WebhookConfigRef, &opts)
		require.NoError(t, err)
		assert.Len(t, opts, 2)
	})
}

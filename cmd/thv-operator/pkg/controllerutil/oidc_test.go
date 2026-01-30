// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllerutil

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
)

func TestGenerateOIDCClientSecretEnvVar(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		clientSecretRef *mcpv1alpha1.SecretKeyRef
		secret          *corev1.Secret
		expectError     bool
		errContains     string
		validate        func(*testing.T, *corev1.EnvVar)
	}{
		{
			name:            "nil client secret ref returns nil",
			clientSecretRef: nil,
			expectError:     false,
			validate: func(t *testing.T, envVar *corev1.EnvVar) {
				t.Helper()
				assert.Nil(t, envVar)
			},
		},
		{
			name: "valid secret ref generates env var",
			clientSecretRef: &mcpv1alpha1.SecretKeyRef{
				Name: "oidc-secret",
				Key:  "client-secret",
			},
			secret: &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "oidc-secret",
					Namespace: "default",
				},
				Data: map[string][]byte{
					"client-secret": []byte("secret-value"),
				},
			},
			expectError: false,
			validate: func(t *testing.T, envVar *corev1.EnvVar) {
				t.Helper()
				require.NotNil(t, envVar)
				assert.Equal(t, "TOOLHIVE_OIDC_CLIENT_SECRET", envVar.Name)
				require.NotNil(t, envVar.ValueFrom)
				require.NotNil(t, envVar.ValueFrom.SecretKeyRef)
				assert.Equal(t, "oidc-secret", envVar.ValueFrom.SecretKeyRef.Name)
				assert.Equal(t, "client-secret", envVar.ValueFrom.SecretKeyRef.Key)
			},
		},
		{
			name: "missing secret returns error",
			clientSecretRef: &mcpv1alpha1.SecretKeyRef{
				Name: "missing-secret",
				Key:  "client-secret",
			},
			expectError: true,
			errContains: "failed to get OIDC client secret",
		},
		{
			name: "missing key in secret returns error",
			clientSecretRef: &mcpv1alpha1.SecretKeyRef{
				Name: "oidc-secret",
				Key:  "wrong-key",
			},
			secret: &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "oidc-secret",
					Namespace: "default",
				},
				Data: map[string][]byte{
					"client-secret": []byte("secret-value"),
				},
			},
			expectError: true,
			errContains: "is missing key",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			scheme := runtime.NewScheme()
			err := corev1.AddToScheme(scheme)
			require.NoError(t, err)
			err = mcpv1alpha1.AddToScheme(scheme)
			require.NoError(t, err)

			var fakeClient *fake.ClientBuilder
			if tt.secret != nil {
				fakeClient = fake.NewClientBuilder().WithScheme(scheme).WithObjects(tt.secret)
			} else {
				fakeClient = fake.NewClientBuilder().WithScheme(scheme)
			}

			ctx := context.TODO()
			envVar, err := GenerateOIDCClientSecretEnvVar(
				ctx,
				fakeClient.Build(),
				"default",
				tt.clientSecretRef,
			)

			if tt.expectError {
				assert.Error(t, err)
				if tt.errContains != "" {
					assert.Contains(t, err.Error(), tt.errContains)
				}
			} else {
				assert.NoError(t, err)
				if tt.validate != nil {
					tt.validate(t, envVar)
				}
			}
		})
	}
}

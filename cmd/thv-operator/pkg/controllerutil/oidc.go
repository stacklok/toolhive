// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllerutil

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
)

// GetOIDCConfigForServer fetches the MCPOIDCConfig referenced by an MCPServer.
// Returns nil if the ref is nil or the resource is not found.
func GetOIDCConfigForServer(
	ctx context.Context,
	c client.Client,
	namespace string,
	ref *mcpv1alpha1.MCPOIDCConfigReference,
) (*mcpv1alpha1.MCPOIDCConfig, error) {
	if ref == nil {
		return nil, nil
	}

	oidcConfig := &mcpv1alpha1.MCPOIDCConfig{}
	if err := c.Get(ctx, types.NamespacedName{
		Name:      ref.Name,
		Namespace: namespace,
	}, oidcConfig); err != nil {
		return nil, fmt.Errorf("failed to get MCPOIDCConfig %s/%s: %w", namespace, ref.Name, err)
	}

	return oidcConfig, nil
}

// GenerateOIDCClientSecretEnvVar generates environment variable for OIDC client secret
// when using a SecretKeyRef.
// Returns nil if clientSecretRef is nil.
func GenerateOIDCClientSecretEnvVar(
	ctx context.Context,
	c client.Client,
	namespace string,
	clientSecretRef *mcpv1alpha1.SecretKeyRef,
) (*corev1.EnvVar, error) {
	if clientSecretRef == nil {
		return nil, nil
	}

	// Validate that the referenced secret exists
	var secret corev1.Secret
	if err := c.Get(ctx, types.NamespacedName{
		Namespace: namespace,
		Name:      clientSecretRef.Name,
	}, &secret); err != nil {
		return nil, fmt.Errorf("failed to get OIDC client secret %s/%s: %w",
			namespace, clientSecretRef.Name, err)
	}

	// Validate that the key exists in the secret
	if _, ok := secret.Data[clientSecretRef.Key]; !ok {
		return nil, fmt.Errorf("OIDC client secret %s/%s is missing key %q",
			namespace, clientSecretRef.Name, clientSecretRef.Key)
	}

	// Return environment variable with secret reference
	return &corev1.EnvVar{
		Name: "TOOLHIVE_OIDC_CLIENT_SECRET",
		ValueFrom: &corev1.EnvVarSource{
			SecretKeyRef: &corev1.SecretKeySelector{
				LocalObjectReference: corev1.LocalObjectReference{
					Name: clientSecretRef.Name,
				},
				Key: clientSecretRef.Key,
			},
		},
	}, nil
}

// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package converters

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
)

// resolveSecretKeyRef fetches a secret value from Kubernetes using a SecretKeyRef.
func resolveSecretKeyRef(
	ctx context.Context,
	k8sClient client.Client,
	namespace string,
	ref *mcpv1beta1.SecretKeyRef,
) (string, error) {
	secret := &corev1.Secret{}
	secretKey := types.NamespacedName{
		Name:      ref.Name,
		Namespace: namespace,
	}

	if err := k8sClient.Get(ctx, secretKey, secret); err != nil {
		return "", fmt.Errorf("failed to get secret %s/%s: %w", namespace, ref.Name, err)
	}

	secretValue, ok := secret.Data[ref.Key]
	if !ok {
		return "", fmt.Errorf("secret %s/%s does not contain key %s", namespace, ref.Name, ref.Key)
	}

	return string(secretValue), nil
}

// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package usage

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	// identityConfigMapName is the ConfigMap that stores the anonymous
	// installation ID for the usage PoC. It is intentionally independent of the
	// pkg/operator/telemetry ConfigMap ("toolhive-operator-telemetry") so the
	// PoC can be deleted without touching that service.
	identityConfigMapName = "toolhive-usage-poc"
	// installationIDKey is the ConfigMap data key holding the UUID.
	installationIDKey = "installation_id"
	// defaultNamespace matches the existing telemetry default and is used when
	// the operator pod namespace is unknown.
	defaultNamespace = "toolhive-system"
)

// resolveInstallationID reads the anonymous installation ID from the
// identity ConfigMap, creating it with a fresh random UUID if absent. The ID is
// stable across operator restarts and is NOT derived from any cluster,
// namespace, or resource identity — it is a pure random UUID.
//
// The create path is race-safe: if a concurrent caller (or restarted replica)
// creates the ConfigMap first, the resulting AlreadyExists error triggers a
// re-Get so all callers converge on the same persisted ID.
func resolveInstallationID(ctx context.Context, c client.Client, namespace string) (string, error) {
	if namespace == "" {
		namespace = defaultNamespace
	}
	key := types.NamespacedName{Name: identityConfigMapName, Namespace: namespace}

	cm := &corev1.ConfigMap{}
	err := c.Get(ctx, key, cm)
	switch {
	case err == nil:
		if id := cm.Data[installationIDKey]; id != "" {
			return id, nil
		}
		// ConfigMap exists but lacks the key (e.g. hand-edited). Backfill it.
		return backfillInstallationID(ctx, c, cm)
	case apierrors.IsNotFound(err):
		return createInstallationID(ctx, c, namespace)
	default:
		return "", fmt.Errorf("get usage identity ConfigMap: %w", err)
	}
}

// createInstallationID creates the identity ConfigMap with a fresh UUID,
// re-reading on an AlreadyExists race so callers converge on one ID.
func createInstallationID(ctx context.Context, c client.Client, namespace string) (string, error) {
	id := uuid.NewString()
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      identityConfigMapName,
			Namespace: namespace,
		},
		Data: map[string]string{installationIDKey: id},
	}

	err := c.Create(ctx, cm)
	switch {
	case err == nil:
		return id, nil
	case apierrors.IsAlreadyExists(err):
		// Lost the create race; re-Get the winner's ID.
		existing := &corev1.ConfigMap{}
		if getErr := c.Get(ctx, types.NamespacedName{Name: identityConfigMapName, Namespace: namespace}, existing); getErr != nil {
			return "", fmt.Errorf("re-get usage identity ConfigMap after create race: %w", getErr)
		}
		if existingID := existing.Data[installationIDKey]; existingID != "" {
			return existingID, nil
		}
		return backfillInstallationID(ctx, c, existing)
	default:
		return "", fmt.Errorf("create usage identity ConfigMap: %w", err)
	}
}

// backfillInstallationID writes a fresh UUID into an existing ConfigMap that is
// missing the installation_id key. The passed ConfigMap must be a freshly-read
// object so the Update carries a current resourceVersion.
func backfillInstallationID(ctx context.Context, c client.Client, cm *corev1.ConfigMap) (string, error) {
	id := uuid.NewString()
	if cm.Data == nil {
		cm.Data = map[string]string{}
	}
	cm.Data[installationIDKey] = id
	if err := c.Update(ctx, cm); err != nil {
		return "", fmt.Errorf("backfill usage identity ConfigMap: %w", err)
	}
	return id, nil
}

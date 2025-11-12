// Package config provides management for the registry server config ConfigMap
package config

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	"github.com/stacklok/toolhive/cmd/thv-operator/pkg/runconfig/configmap/checksum"
)

// UpsertConfigMap creates or updates a registry server config ConfigMap based on checksum changes
// It uses exponential backoff retry for handling concurrent modifications
func (cm *configManager) UpsertConfigMap(
	ctx context.Context,
	mcpRegistry *mcpv1alpha1.MCPRegistry,
	desired *corev1.ConfigMap,
) error {
	ctxLogger := log.FromContext(ctx)

	if mcpRegistry == nil {
		return fmt.Errorf("cannot create registry server config because the MCPRegistry object is nil")
	}

	if desired == nil {
		return fmt.Errorf("cannot create registry server config ConfigMap because ConfigMap object is nil")
	}

	objectKey := types.NamespacedName{Name: desired.Name, Namespace: desired.Namespace}

	// Use exponential backoff retry for handling concurrent updates
	return retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		current := &corev1.ConfigMap{}
		err := cm.client.Get(ctx, objectKey, current)

		if errors.IsNotFound(err) {
			// ConfigMap doesn't exist, create it
			if err := controllerutil.SetControllerReference(mcpRegistry, desired, cm.scheme); err != nil {
				return fmt.Errorf("failed to set controller reference while creating registry server config ConfigMap: %w", err)
			}

			ctxLogger.Info("registry server config ConfigMap does not exist, creating", "ConfigMap.Name", desired.Name)
			if err := cm.client.Create(ctx, desired); err != nil {
				// If we get AlreadyExists error, it means another process created it
				// Return conflict error to trigger retry
				if errors.IsAlreadyExists(err) {
					return errors.NewConflict(
						corev1.Resource("configmaps"),
						desired.Name,
						fmt.Errorf("configmap was created by another process"),
					)
				}
				return fmt.Errorf("failed to create registry server config ConfigMap: %w", err)
			}
			ctxLogger.Info("registry server config ConfigMap created", "ConfigMap.Name", desired.Name)
			return nil
		} else if err != nil {
			return fmt.Errorf("failed to get registry server config ConfigMap: %w", err)
		}

		// at this point, the ConfigMap exists and we want to update it if the content has changed
		if cm.checksum.ConfigMapChecksumHasChanged(current, desired) {
			// Content changed, update the ConfigMap with new checksum
			// Create a copy of desired to avoid modifying the original
			updatedConfigMap := desired.DeepCopy()

			// Copy resource version and other metadata for update
			updatedConfigMap.ResourceVersion = current.ResourceVersion
			updatedConfigMap.UID = current.UID

			if err := controllerutil.SetControllerReference(mcpRegistry, updatedConfigMap, cm.scheme); err != nil {
				return fmt.Errorf("failed to set controller reference while updating registry server config ConfigMap: %w", err)
			}

			ctxLogger.Info("registry server config ConfigMap content changed, updating",
				"ConfigMap.Name", updatedConfigMap.Name,
				"oldChecksum", current.Annotations[checksum.ContentChecksumAnnotation],
				"newChecksum", updatedConfigMap.Annotations[checksum.ContentChecksumAnnotation],
			)

			if err := cm.client.Update(ctx, updatedConfigMap); err != nil {
				// If we get a conflict error, it will be automatically retried
				if errors.IsConflict(err) {
					ctxLogger.Info("Conflict detected while updating ConfigMap, will retry", "ConfigMap.Name", updatedConfigMap.Name)
					return err
				}
				return fmt.Errorf("failed to update registry server config ConfigMap: %w", err)
			}
			ctxLogger.Info("registry server config ConfigMap updated", "ConfigMap.Name", updatedConfigMap.Name)
		} else {
			ctxLogger.V(1).Info("registry server config ConfigMap unchanged, skipping update", "ConfigMap.Name", desired.Name)
		}

		return nil
	})
}

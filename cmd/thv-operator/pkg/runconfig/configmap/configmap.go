// Package configmap provides management for RunConfig ConfigMaps
package configmap

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	"github.com/stacklok/toolhive/cmd/thv-operator/pkg/runconfig/configmap/checksum"
)

// Manager defines the interface for managing RunConfig ConfigMaps
type Manager interface {
	UpsertRunConfigMap(ctx context.Context, configMap *corev1.ConfigMap) error
}

// RunConfigConfigMap manages RunConfig ConfigMaps with checksum-based change detection
type RunConfigConfigMap struct {
	client   client.Client
	scheme   *runtime.Scheme
	checksum checksum.RunConfigConfigMapChecksum
}

// NewRunConfigConfigMap creates a new RunConfigConfigMap instance
func NewRunConfigConfigMap(
	k8sClient client.Client,
	scheme *runtime.Scheme,
	checksumManager checksum.RunConfigConfigMapChecksum,
) RunConfigConfigMap {
	return RunConfigConfigMap{client: k8sClient, scheme: scheme, checksum: checksumManager}
}

// UpsertRunConfigMap creates or updates a RunConfig ConfigMap based on checksum changes
func (r *RunConfigConfigMap) UpsertRunConfigMap(
	ctx context.Context,
	mcpServer *mcpv1alpha1.MCPServer,
	desired *corev1.ConfigMap,
) error {
	ctxLogger := log.FromContext(ctx)

	if mcpServer == nil {
		return fmt.Errorf("cannot create RunConfig ConfigMap because MCPServer object is nil")
	}

	if desired == nil {
		return fmt.Errorf("cannot create RunConfig ConfigMap because ConfigMap object is nil")
	}

	current := &corev1.ConfigMap{}
	objectKey := types.NamespacedName{Name: desired.Name, Namespace: desired.Namespace}
	err := r.client.Get(ctx, objectKey, current)

	if errors.IsNotFound(err) {
		// ConfigMap doesn't exist, create it
		if err := controllerutil.SetControllerReference(mcpServer, desired, r.scheme); err != nil {
			return fmt.Errorf("failed to set controller reference while creating RunConfig ConfigMap: %w", err)
		}

		ctxLogger.Info("RunConfig ConfigMap does not exist, creating", "ConfigMap.Name", desired.Name)
		if err := r.client.Create(ctx, desired); err != nil {
			return fmt.Errorf("failed to create RunConfig ConfigMap: %w", err)
		}
		ctxLogger.Info("RunConfig ConfigMap created", "ConfigMap.Name", desired.Name)
		return nil
	} else if err != nil {
		return fmt.Errorf("failed to get RunConfig ConfigMap: %w", err)
	}

	// at this point, the ConfigMap exists and we want to update it if the content has changed
	if r.checksum.ConfigMapChecksumHasChanged(current, desired) {
		// Content changed, update the ConfigMap with new checksum
		// Copy resource version and other metadata for update
		desired.ResourceVersion = current.ResourceVersion
		desired.UID = current.UID

		if err := controllerutil.SetControllerReference(mcpServer, desired, r.scheme); err != nil {
			return fmt.Errorf("failed to set controller reference while updating RunConfig ConfigMap: %w", err)
		}

		ctxLogger.Info("RunConfig ConfigMap content changed, updating",
			"ConfigMap.Name", desired.Name,
			"oldChecksum", current.Annotations[checksum.ContentChecksumAnnotation],
			"newChecksum", desired.Annotations[checksum.ContentChecksumAnnotation])
		if err := r.client.Update(ctx, desired); err != nil {
			return fmt.Errorf("failed to update RunConfig ConfigMap: %w", err)
		}
		ctxLogger.Info("RunConfig ConfigMap updated", "ConfigMap.Name", desired.Name)
	}

	return nil
}

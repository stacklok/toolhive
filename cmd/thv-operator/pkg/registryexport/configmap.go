package registryexport

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	upstreamv0 "github.com/modelcontextprotocol/registry/pkg/api/v0"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/stacklok/toolhive/pkg/registry/registry"
)

const (
	// ContentChecksumAnnotation is the annotation key for ConfigMap content checksum.
	ContentChecksumAnnotation = "toolhive.stacklok.dev/content-checksum"
)

// ConfigMapManager handles creation and updates of registry export ConfigMaps.
type ConfigMapManager struct {
	client client.Client
}

// NewConfigMapManager creates a new ConfigMapManager.
func NewConfigMapManager(c client.Client) *ConfigMapManager {
	return &ConfigMapManager{client: c}
}

// UpsertConfigMap creates or updates the registry export ConfigMap for a namespace.
func (m *ConfigMapManager) UpsertConfigMap(
	ctx context.Context,
	namespace string,
	reg *registry.UpstreamRegistry,
) error {
	desired, err := m.buildConfigMap(namespace, reg)
	if err != nil {
		return fmt.Errorf("failed to build ConfigMap: %w", err)
	}

	return retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		current := &corev1.ConfigMap{}
		err := m.client.Get(ctx, types.NamespacedName{
			Name:      desired.Name,
			Namespace: namespace,
		}, current)

		if errors.IsNotFound(err) {
			return m.client.Create(ctx, desired)
		}
		if err != nil {
			return fmt.Errorf("failed to get existing ConfigMap: %w", err)
		}

		if !m.checksumHasChanged(current, desired) {
			return nil // No update needed
		}

		desired.ResourceVersion = current.ResourceVersion
		return m.client.Update(ctx, desired)
	})
}

// DeleteConfigMap removes the registry export ConfigMap for a namespace.
func (m *ConfigMapManager) DeleteConfigMap(ctx context.Context, namespace string) error {
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      GetConfigMapName(namespace),
			Namespace: namespace,
		},
	}

	err := m.client.Delete(ctx, cm)
	if errors.IsNotFound(err) {
		return nil // Already deleted
	}
	return err
}

// GetConfigMap retrieves the registry export ConfigMap for a namespace.
func (m *ConfigMapManager) GetConfigMap(ctx context.Context, namespace string) (*corev1.ConfigMap, error) {
	cm := &corev1.ConfigMap{}
	err := m.client.Get(ctx, types.NamespacedName{
		Name:      GetConfigMapName(namespace),
		Namespace: namespace,
	}, cm)
	if err != nil {
		return nil, err
	}
	return cm, nil
}

// buildConfigMap creates a ConfigMap with the registry data.
func (m *ConfigMapManager) buildConfigMap(namespace string, reg *registry.UpstreamRegistry) (*corev1.ConfigMap, error) {
	data, err := json.MarshalIndent(reg, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("failed to marshal registry data: %w", err)
	}

	// Compute checksum on servers only (excludes timestamp for stability)
	checksum, err := m.computeServersChecksum(reg.Data.Servers)
	if err != nil {
		return nil, fmt.Errorf("failed to compute checksum: %w", err)
	}

	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      GetConfigMapName(namespace),
			Namespace: namespace,
			Labels: map[string]string{
				LabelRegistryExport: LabelRegistryExportValue,
			},
			Annotations: map[string]string{
				ContentChecksumAnnotation: checksum,
			},
		},
		Data: map[string]string{
			ConfigMapKey: string(data),
		},
	}

	return cm, nil
}

// computeServersChecksum computes a SHA256 checksum of the server entries only.
// This excludes timestamps and metadata for stable checksums across reconciliations.
func (*ConfigMapManager) computeServersChecksum(servers []upstreamv0.ServerJSON) (string, error) {
	// Marshal servers to JSON for deterministic hashing
	// Note: entries are already sorted by name in BuildUpstreamRegistry
	data, err := json.Marshal(servers)
	if err != nil {
		return "", err
	}

	h := sha256.New()
	h.Write(data)
	return hex.EncodeToString(h.Sum(nil)), nil
}

// checksumHasChanged checks if the ConfigMap content has changed.
func (*ConfigMapManager) checksumHasChanged(current, desired *corev1.ConfigMap) bool {
	currentChecksum := current.Annotations[ContentChecksumAnnotation]
	desiredChecksum := desired.Annotations[ContentChecksumAnnotation]

	// If either is missing, consider it changed
	if currentChecksum == "" || desiredChecksum == "" {
		return true
	}

	return currentChecksum != desiredChecksum
}

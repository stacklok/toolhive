// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package checksum provides checksum computation and comparison for ConfigMaps
package checksum

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	// ContentChecksumAnnotation is the annotation key used to store the ConfigMap content checksum
	ContentChecksumAnnotation = "toolhive.stacklok.dev/content-checksum"

	// RunConfigChecksumAnnotation is the annotation key used to store the RunConfig checksum
	// in pod template annotations to trigger pod restarts when configuration changes
	RunConfigChecksumAnnotation = "toolhive.stacklok.dev/runconfig-checksum"
)

// RunConfigConfigMapChecksum provides methods for computing and comparing ConfigMap checksums
type RunConfigConfigMapChecksum interface {
	ComputeConfigMapChecksum(cm *corev1.ConfigMap) string
	ConfigMapChecksumHasChanged(current, desired *corev1.ConfigMap) bool
}

// NewRunConfigConfigMapChecksum creates a new RunConfigConfigMapChecksum
func NewRunConfigConfigMapChecksum() RunConfigConfigMapChecksum {
	return &runConfigConfigMapChecksum{}
}

type runConfigConfigMapChecksum struct{}

// ComputeConfigMapChecksum computes a SHA256 checksum of the ConfigMap content for change detection
func (*runConfigConfigMapChecksum) ComputeConfigMapChecksum(cm *corev1.ConfigMap) string {
	h := sha256.New()

	// Include data content in checksum
	var dataKeys []string
	for key := range cm.Data {
		dataKeys = append(dataKeys, key)
	}
	sort.Strings(dataKeys)

	for _, key := range dataKeys {
		h.Write([]byte(key))
		h.Write([]byte(cm.Data[key]))
	}

	// Include labels in checksum (excluding checksum annotation itself)
	var labelKeys []string
	for key := range cm.Labels {
		labelKeys = append(labelKeys, key)
	}
	sort.Strings(labelKeys)

	for _, key := range labelKeys {
		h.Write([]byte(key))
		h.Write([]byte(cm.Labels[key]))
	}

	// Include relevant annotations in checksum (excluding checksum annotation itself)
	var annotationKeys []string
	for key := range cm.Annotations {
		if key != ContentChecksumAnnotation {
			annotationKeys = append(annotationKeys, key)
		}
	}
	sort.Strings(annotationKeys)

	for _, key := range annotationKeys {
		h.Write([]byte(key))
		h.Write([]byte(cm.Annotations[key]))
	}

	return hex.EncodeToString(h.Sum(nil))
}

func (r *runConfigConfigMapChecksum) ConfigMapChecksumHasChanged(current, desired *corev1.ConfigMap) bool {
	currentChecksum := current.Annotations[ContentChecksumAnnotation]
	desiredChecksum := desired.Annotations[ContentChecksumAnnotation]

	if currentChecksum != "" && desiredChecksum != "" {
		return currentChecksum != desiredChecksum
	}

	// Fallback to compute checksums if they don't exist (for backward compatibility)
	if currentChecksum == "" {
		currentChecksum = r.ComputeConfigMapChecksum(current)
	}
	if desiredChecksum == "" {
		desiredChecksum = r.ComputeConfigMapChecksum(desired)
	}

	return currentChecksum != desiredChecksum
}

// RunConfigChecksumFetcher provides methods for fetching RunConfig ConfigMap checksums.
// This is used to detect configuration changes and trigger pod restarts.
type RunConfigChecksumFetcher struct {
	client client.Client
}

// NewRunConfigChecksumFetcher creates a new RunConfigChecksumFetcher
func NewRunConfigChecksumFetcher(c client.Client) *RunConfigChecksumFetcher {
	return &RunConfigChecksumFetcher{client: c}
}

// GetRunConfigChecksum fetches the RunConfig ConfigMap checksum annotation for a resource.
//
// This checksum is used to trigger pod restarts when the RunConfig content changes.
// The function retrieves the checksum from the ConfigMap's annotations and validates
// that it is non-empty.
//
// Parameters:
//   - ctx: Context for the operation
//   - namespace: Namespace of the ConfigMap
//   - resourceName: Name of the resource (used to construct ConfigMap name as "<resourceName>-runconfig")
//
// Returns:
//   - (checksum, nil) on success - checksum is a non-empty SHA256 hex string
//   - ("", error) on failure - error indicates the specific failure reason
//
// The returned error preserves the error type, allowing callers to check for
// errors.IsNotFound() to handle missing ConfigMaps gracefully during initial creation.
func (f *RunConfigChecksumFetcher) GetRunConfigChecksum(
	ctx context.Context,
	namespace string,
	resourceName string,
) (string, error) {
	if resourceName == "" {
		return "", fmt.Errorf("resourceName cannot be empty")
	}

	configMapName := fmt.Sprintf("%s-runconfig", resourceName)
	configMap := &corev1.ConfigMap{}
	err := f.client.Get(ctx, types.NamespacedName{Name: configMapName, Namespace: namespace}, configMap)
	if err != nil {
		// Return the specific error type so caller can check for IsNotFound
		return "", fmt.Errorf("failed to get RunConfig ConfigMap %s/%s: %w", namespace, configMapName, err)
	}

	checksum, ok := configMap.Annotations[ContentChecksumAnnotation]
	if !ok {
		return "", fmt.Errorf("RunConfig ConfigMap %s/%s missing %s annotation",
			namespace, configMapName, ContentChecksumAnnotation)
	}

	if checksum == "" {
		return "", fmt.Errorf("RunConfig ConfigMap %s/%s has empty %s annotation",
			namespace, configMapName, ContentChecksumAnnotation)
	}

	return checksum, nil
}

// AddRunConfigChecksumToPodTemplate adds the RunConfig checksum as an annotation
// to the provided annotations map. This triggers Kubernetes to perform a rolling
// update when the checksum changes.
//
// If the checksum is empty, no annotation is added. This allows callers to
// gracefully handle cases where the checksum is not yet available.
//
// Returns the updated annotations map.
func AddRunConfigChecksumToPodTemplate(annotations map[string]string, checksum string) map[string]string {
	if annotations == nil {
		annotations = make(map[string]string)
	}

	if checksum != "" {
		annotations[RunConfigChecksumAnnotation] = checksum
	}

	return annotations
}

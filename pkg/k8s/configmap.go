// Package k8s provides Kubernetes-specific utility functions for the ToolHive codebase.
package k8s

import (
	"crypto/sha256"
	"fmt"
	"sort"

	corev1 "k8s.io/api/core/v1"
)

// ComputeConfigMapChecksum computes a SHA256 checksum of the ConfigMap content for change detection.
// This function provides a consistent way to compute checksums across the codebase.
func ComputeConfigMapChecksum(cm *corev1.ConfigMap) string {
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

	// Include labels in checksum
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
		if key != "toolhive.stacklok.dev/content-checksum" {
			annotationKeys = append(annotationKeys, key)
		}
	}
	sort.Strings(annotationKeys)

	for _, key := range annotationKeys {
		h.Write([]byte(key))
		h.Write([]byte(cm.Annotations[key]))
	}

	return fmt.Sprintf("%x", h.Sum(nil))
}

// Package checksum provides checksum computation and comparison for ConfigMaps
package checksum

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"

	corev1 "k8s.io/api/core/v1"
)

const (
	// ContentChecksumAnnotation is the annotation key used to store the ConfigMap content checksum
	ContentChecksumAnnotation = "toolhive.stacklok.dev/content-checksum"
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

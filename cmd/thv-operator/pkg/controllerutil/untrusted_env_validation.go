// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllerutil

import (
	"fmt"

	corev1 "k8s.io/api/core/v1"
)

// ValidateNoSecretEnvForUntrusted inspects a fully-built PodTemplateSpec patch for an
// untrusted-flagged workload and rejects every Secret- or ConfigMap-sourced data
// channel into the backend (mcp) container:
//   - env ValueFrom SecretKeyRef / ConfigMapKeyRef on containers + initContainers,
//   - envFrom secretRef / configMapRef (the whole-source twin of the per-key rules)
//     on containers + initContainers,
//   - Secret-, ConfigMap-, projected (Secret/ConfigMap source), and CSI
//     secretProvider volumes (files are the same channel as env).
//
// It checks BOTH the generated entries (from spec.secrets via WithSecrets) and
// user-supplied entries from spec.podTemplateSpec, because both land in the same
// patch — validating pre-merge would miss the merged result. untrusted is passed
// as a parameter because the MCPServer.spec.untrusted field does not exist until
// Wave 1; the controller currently derives it from an interim opt-in annotation.
//
// This is a reconcile-time defense-in-depth gate; Wave 1 complements it with a
// CEL admission rule (untrusted ⇒ literal-only backend env) per
// .claude/rules/operator.md ("When a Go check intentionally mirrors a CEL rule
// for defense-in-depth, say so in a comment").
//
// FieldRef/ResourceFieldRef env sources expose pod metadata, not credentials, and
// are allowed. Error messages name the env var and its source object (metadata
// only — never values), per .claude/rules/go-style.md "No sensitive data in errors".
//
// When untrusted is false this is a pure no-op and returns nil.
func ValidateNoSecretEnvForUntrusted(podTemplate *corev1.PodTemplateSpec, containerName string, untrusted bool) error {
	if !untrusted || podTemplate == nil {
		return nil
	}

	for _, containers := range [][]corev1.Container{
		podTemplate.Spec.Containers,
		podTemplate.Spec.InitContainers,
	} {
		for _, container := range containers {
			if container.Name != containerName {
				// The gate scopes to the backend container only; sidecars are
				// trusted workloads managed by the operator.
				continue
			}
			if err := validateBackendContainerEnv(containerName, &container); err != nil {
				return err
			}
		}
	}

	// Secrets-as-files is the same channel as secrets-as-env; leaving the volume
	// twin open would make the env gate theater. ConfigMap volumes are rejected
	// for parity with the ConfigMapKeyRef env rule, and the CSI secrets-store
	// driver / projected-Secret sources close the remaining out-of-tree mounts.
	for i := range podTemplate.Spec.Volumes {
		if err := validateUntrustedVolume(&podTemplate.Spec.Volumes[i]); err != nil {
			return err
		}
	}

	return nil
}

// validateBackendContainerEnv rejects Secret- and ConfigMap-sourced env on one
// backend container: per-key ValueFrom refs and whole-source envFrom refs.
func validateBackendContainerEnv(containerName string, container *corev1.Container) error {
	for _, env := range container.Env {
		if env.ValueFrom == nil {
			continue
		}
		if ref := env.ValueFrom.SecretKeyRef; ref != nil {
			return fmt.Errorf(
				"untrusted workload: env var %q on container %q sources its value from Secret %q (key %q), "+
					"which is not allowed; untrusted workloads must use literal env values",
				env.Name, containerName, ref.Name, ref.Key)
		}
		if ref := env.ValueFrom.ConfigMapKeyRef; ref != nil {
			return fmt.Errorf(
				"untrusted workload: env var %q on container %q sources its value from ConfigMap %q (key %q), "+
					"which is not allowed; untrusted workloads must use literal env values",
				env.Name, containerName, ref.Name, ref.Key)
		}
	}
	for _, envFrom := range container.EnvFrom {
		if ref := envFrom.SecretRef; ref != nil {
			return fmt.Errorf(
				"untrusted workload: envFrom on container %q sources values from Secret %q, "+
					"which is not allowed; untrusted workloads must use literal env values",
				containerName, ref.Name)
		}
		if ref := envFrom.ConfigMapRef; ref != nil {
			return fmt.Errorf(
				"untrusted workload: envFrom on container %q sources values from ConfigMap %q, "+
					"which is not allowed; untrusted workloads must use literal env values",
				containerName, ref.Name)
		}
	}
	return nil
}

// validateUntrustedVolume rejects one volume when it sources content from a
// Secret or ConfigMap by any channel: direct, CSI secrets-store, or projected.
func validateUntrustedVolume(volume *corev1.Volume) error {
	if volume.Secret != nil {
		return fmt.Errorf(
			"untrusted workload: volume %q sources its content from Secret %q, "+
				"which is not allowed; untrusted workloads must not mount Secrets",
			volume.Name, volume.Secret.SecretName)
	}
	if volume.ConfigMap != nil {
		return fmt.Errorf(
			"untrusted workload: volume %q sources its content from ConfigMap %q, "+
				"which is not allowed; untrusted workloads must not mount ConfigMaps",
			volume.Name, volume.ConfigMap.Name)
	}
	// The CSI secrets-store driver has no typed PodSpec field: it resolves the
	// SecretProviderClass from a driver-specific volume attribute. Treat any
	// volume carrying that attribute as Secret-sourced regardless of driver.
	if csi := volume.CSI; csi != nil && csi.VolumeAttributes["secretProviderClass"] != "" {
		return fmt.Errorf(
			"untrusted workload: volume %q sources its content from CSI SecretProviderClass %q, "+
				"which is not allowed; untrusted workloads must not mount Secrets",
			volume.Name, csi.VolumeAttributes["secretProviderClass"])
	}
	if projected := volume.Projected; projected != nil {
		for _, source := range projected.Sources {
			if source.Secret != nil {
				return fmt.Errorf(
					"untrusted workload: volume %q projects Secret %q, "+
						"which is not allowed; untrusted workloads must not mount Secrets",
					volume.Name, source.Secret.Name)
			}
			if source.ConfigMap != nil {
				return fmt.Errorf(
					"untrusted workload: volume %q projects ConfigMap %q, "+
						"which is not allowed; untrusted workloads must not mount ConfigMaps",
					volume.Name, source.ConfigMap.Name)
			}
		}
	}
	return nil
}

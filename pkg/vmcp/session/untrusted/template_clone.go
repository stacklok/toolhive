// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package untrusted

import (
	"fmt"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// mcpContainerName is the backend container name in operator-built pod
// templates. It mirrors the operator's constant; the packages must not import
// each other (cmd/thv-operator is a separate module boundary).
const mcpContainerName = "mcp"

// MCPServer ownerReference identity. Kept as constants because the untrusted
// package must not import the operator API package; the group/version/kind is
// stable CRD identity.
const (
	mcpserverAPIVersion = "toolhive.stacklok.dev/v1beta1"
	mcpserverKind       = "MCPServer"
)

// clonePodFromTemplate builds the per-session untrusted backend pod from the
// backend StatefulSet's pod template. The operator's builder output is the pod
// spec of record (ADR D4): cloning it inherits every Wave-0/1 security
// invariant by construction (sentinel env literals, no secret env, security
// context) — duplicating the builder here would create a second source of
// truth for exactly the surface those waves harden.
//
// The template is re-verified fail-closed before the pod is returned: any
// Secret/ConfigMap-sourced env or volume reaching the backend container (or a
// missing mcp container) is a hard error — this is defense-in-depth behind the
// operator-side gates, not a replacement for them.
func clonePodFromTemplate(
	template *corev1.PodTemplateSpec,
	appLabel string,
	req EnsurePodRequest,
	vmcpUID string,
) (*corev1.Pod, error) {
	if err := reverifyNoSecretMaterial(template); err != nil {
		return nil, err
	}

	userKey := req.Session.UserKey()
	labels := map[string]string{
		LabelApp:              appLabel,
		LabelUntrusted:        "true",
		LabelUntrustedUser:    userHash(userKey),
		LabelUntrustedSession: sessionHash(req.Session.SessionID),
		LabelMCPServerUID:     req.MCPServerUID,
	}
	annotations := map[string]string{
		AnnotationIssuer:        req.Session.Issuer,
		AnnotationSubjectHash:   subjectHash(req.Session.Subject),
		AnnotationSubjectRaw:    req.Session.Subject,
		AnnotationMCPServerName: req.MCPServerName,
		AnnotationSessionID:     req.Session.SessionID,
		AnnotationVMCPUid:       vmcpUID,
	}

	spec := *template.Spec.DeepCopy()
	// Standalone pods need an explicit restart policy; StatefulSet templates
	// always carry Always, so this is a belt-and-braces normalization.
	spec.RestartPolicy = corev1.RestartPolicyAlways
	spec.NodeName = ""

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:        PodNameFor(req.MCPServerUID, userKey, req.Session.SessionID),
			Namespace:   req.Session.Namespace,
			Labels:      labels,
			Annotations: annotations,
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: mcpserverAPIVersion,
				Kind:       mcpserverKind,
				Name:       req.MCPServerName,
				UID:        req.OwnerRefUID,
				// Non-controller ownerRef: cascade-deletes session pods when the
				// MCPServer is deleted. No finalizer, no controller flag.
			}},
		},
		Spec: spec,
	}

	// Wave 3: append the egress-broker sidecar data plane (init-container,
	// Envoy, broker, proxy/CA env on the backend). This runs AFTER
	// reverifyNoSecretMaterial: the gate is the authority on the operator
	// template, and the sidecar volumes it adds are operator-managed.
	if err := applyEgressBrokerSidecar(pod, req); err != nil {
		return nil, err
	}

	return pod, nil
}

// reverifyNoSecretMaterial mirrors the operator-side
// controllerutil.ValidateNoSecretEnvForUntrusted for a pod template about to
// be cloned (the two packages must not import each other). It rejects:
//   - env ValueFrom SecretKeyRef / ConfigMapKeyRef on the backend container,
//   - envFrom secretRef / configMapRef on the backend container,
//   - Secret-, ConfigMap-, projected (Secret/ConfigMap source), and CSI
//     secretProvider volumes.
//
// FieldRef/ResourceFieldRef env sources expose pod metadata, not credentials,
// and are allowed. Errors name the source object metadata only, never values.
func reverifyNoSecretMaterial(template *corev1.PodTemplateSpec) error {
	if template == nil {
		return fmt.Errorf("untrusted pod clone: pod template is nil")
	}

	found := false
	for i := range template.Spec.Containers {
		c := &template.Spec.Containers[i]
		if c.Name != mcpContainerName {
			continue
		}
		found = true
		if err := reverifyContainerEnv(c); err != nil {
			return err
		}
	}
	if !found {
		return fmt.Errorf("untrusted pod clone: pod template has no %q container", mcpContainerName)
	}

	for i := range template.Spec.Volumes {
		if err := reverifyVolume(&template.Spec.Volumes[i]); err != nil {
			return err
		}
	}
	return nil
}

func reverifyContainerEnv(c *corev1.Container) error {
	for _, env := range c.Env {
		if env.ValueFrom == nil {
			continue
		}
		if ref := env.ValueFrom.SecretKeyRef; ref != nil {
			return fmt.Errorf(
				"untrusted pod clone: env var %q on container %q sources from Secret %q (key %q); refusing to create pod",
				env.Name, c.Name, ref.Name, ref.Key)
		}
		if ref := env.ValueFrom.ConfigMapKeyRef; ref != nil {
			return fmt.Errorf(
				"untrusted pod clone: env var %q on container %q sources from ConfigMap %q (key %q); refusing to create pod",
				env.Name, c.Name, ref.Name, ref.Key)
		}
	}
	for _, envFrom := range c.EnvFrom {
		if ref := envFrom.SecretRef; ref != nil {
			return fmt.Errorf(
				"untrusted pod clone: envFrom on container %q sources from Secret %q; refusing to create pod",
				c.Name, ref.Name)
		}
		if ref := envFrom.ConfigMapRef; ref != nil {
			return fmt.Errorf(
				"untrusted pod clone: envFrom on container %q sources from ConfigMap %q; refusing to create pod",
				c.Name, ref.Name)
		}
	}
	return nil
}

func reverifyVolume(v *corev1.Volume) error {
	if v.Secret != nil {
		return fmt.Errorf(
			"untrusted pod clone: volume %q sources from Secret %q; refusing to create pod",
			v.Name, v.Secret.SecretName)
	}
	if v.ConfigMap != nil {
		return fmt.Errorf(
			"untrusted pod clone: volume %q sources from ConfigMap %q; refusing to create pod",
			v.Name, v.ConfigMap.Name)
	}
	if csi := v.CSI; csi != nil && csi.VolumeAttributes["secretProviderClass"] != "" {
		return fmt.Errorf(
			"untrusted pod clone: volume %q sources from CSI SecretProviderClass %q; refusing to create pod",
			v.Name, csi.VolumeAttributes["secretProviderClass"])
	}
	if projected := v.Projected; projected != nil {
		for _, source := range projected.Sources {
			if source.Secret != nil {
				return fmt.Errorf(
					"untrusted pod clone: volume %q projects Secret %q; refusing to create pod",
					v.Name, source.Secret.Name)
			}
			if source.ConfigMap != nil {
				return fmt.Errorf(
					"untrusted pod clone: volume %q projects ConfigMap %q; refusing to create pod",
					v.Name, source.ConfigMap.Name)
			}
		}
	}
	return nil
}

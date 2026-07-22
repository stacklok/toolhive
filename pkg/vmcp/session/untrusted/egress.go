// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package untrusted

import (
	"fmt"
	"sort"

	corev1 "k8s.io/api/core/v1"

	"github.com/stacklok/toolhive/pkg/egressbroker"
)

// Wave-3 egress-broker wiring constants. Data-plane resource names come from
// egressbroker.ResourceNamesFor (the shared naming contract — generation-
// named CA objects so a mid-rotation clone always mounts a consistent
// cert/key pair); the operator side mirrors the same functions because
// cmd/thv-operator is a separate module boundary.
const (
	// caKeyCert is the public-cert data key of the CA Secret/bundle.
	caKeyCert = egressbroker.CAKeyCert
	// caKeyKey is the private-key data key of the CA Secret.
	caKeyKey = egressbroker.CAKeyKey

	// EgressBrokerImage is the broker sidecar image (ext_authz + SDS).
	EgressBrokerImage = "ghcr.io/stacklok/toolhive/egressbroker:latest"
	// EnvoyProxyImage is the Envoy data-plane image.
	EnvoyProxyImage = "envoyproxy/envoy:v1.36-latest"

	// brokerListenPort is the ext_authz/SDS gRPC port (loopback).
	brokerListenPort = 9001
	// proxyPort is the Envoy explicit-proxy port the backend's
	// HTTP(S)_PROXY env points at (loopback).
	proxyPort = 15001

	// caSharedVolumeName is the emptyDir shared between the CA-seeding
	// init-container and the backend container (read-only there).
	caSharedVolumeName = "thv-bump-ca"
	// caBundleVolumeName is the init-container's CA-bundle ConfigMap volume.
	caBundleVolumeName = "thv-bump-ca-bundle"
	// policyVolumeName is the sidecar's policy ConfigMap volume.
	policyVolumeName = "thv-egress-policy"
	// caSecretVolumeName is the sidecar's bump-CA Secret volume.
	caSecretVolumeName = "thv-bump-ca-secret" //nolint:gosec // G101: a volume name, not a credential
	// envoyConfigVolumeName is the Envoy bootstrap volume the broker renders.
	envoyConfigVolumeName = "thv-envoy-config"

	// caMountPath is where the backend and init containers see the CA file.
	caMountPath = "/etc/thv-ca"
	// caFileName is the CA file inside the shared emptyDir.
	caFileName = "ca.crt"
	// envoyConfigPath is the shared emptyDir the broker writes the Envoy
	// bootstrap into.
	envoyConfigPath = "/etc/thv-envoy"

	// caSeedInitContainerName is the trusted init-container that seeds the
	// CA emptyDir.
	caSeedInitContainerName = "thv-ca-seed"
	// envoyContainerName is the Envoy data-plane container.
	envoyContainerName = "thv-envoy"
	// brokerContainerName is the credential-broker container.
	brokerContainerName = "thv-egressbroker"
)

// sidecarEnv is the deterministic (sorted) view of the shared annotation→env
// contract (egressbroker.EnvToAnnotation): the broker's THV_UNTRUSTED_* env
// vars are downward-API mirrors of the pod annotations this package stamps.
var sidecarEnv = func() []struct {
	name       string
	annotation string
} {
	names := make([]string, 0, len(egressbroker.EnvToAnnotation))
	for name := range egressbroker.EnvToAnnotation {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]struct {
		name       string
		annotation string
	}, 0, len(names))
	for _, name := range names {
		annotation, _ := egressbroker.AnnotationForEnv(name)
		out = append(out, struct {
			name       string
			annotation string
		}{name: name, annotation: annotation})
	}
	return out
}()

// applyEgressBrokerSidecar extends a freshly cloned untrusted pod with the
// Wave-3 egress data plane (ADR-0001 D3/D9/D10):
//
//  1. a trusted init-container that writes the bump-CA public cert into a
//     shared emptyDir mounted read-only into the backend container (the CA
//     cannot be a ConfigMap volume: the Wave-0 gate forbids ALL ConfigMap
//     volumes in untrusted pod templates);
//  2. the Envoy data-plane container (explicit forward proxy, loopback);
//  3. the credential-broker container (ext_authz + SDS), with downward-API
//     identity env, the bump-CA Secret mount (sidecar-only), and the policy
//     ConfigMap mount;
//  4. HTTP(S)_PROXY + the runtime CA-env matrix on the backend container.
//
// The volumes this function adds are why reverifyNoSecretMaterial runs
// BEFORE it in clonePodFromTemplate: the gate is the authority on what the
// operator-built template may carry, and the sidecar volumes (Secret,
// ConfigMap) are operator-managed additions appended after verification.
//
// The broker's token-store env (THV_EGRESSBROKER_REDIS_ADDR /
// _REDIS_KEY_PREFIX, and the tokenenc KEK) is injected here from
// req.TokenStore. The address and key prefix are non-secret and are rendered
// as env literals; the KEK, when encryption is enabled, is referenced from a
// Secret via SecretKeyRef (never a ConfigMap or env literal). A nil TokenStore
// renders no token-store env at all — the broker then refuses to start
// (fail closed, per cmd/thv-egressbroker).
func applyEgressBrokerSidecar(pod *corev1.Pod, req EnsurePodRequest) error {
	if pod == nil {
		return fmt.Errorf("untrusted egress wiring: pod must not be nil")
	}
	if req.MCPServerName == "" {
		return fmt.Errorf("untrusted egress wiring: MCPServer name must not be empty")
	}
	if req.TokenStore != nil {
		if err := req.TokenStore.validate(); err != nil {
			return err
		}
	}

	// Generation-qualified CA objects: the pod mounts the exact CA generation
	// the operator published in the STS template annotation (cert+key in ONE
	// Secret), so a pod cloned mid-rotation can never get new-key+old-cert.
	caGen := req.Session.CAGeneration
	if caGen == "" {
		return fmt.Errorf("untrusted egress wiring: backend StatefulSet template carries no %s annotation "+
			"(operator must publish the current CA generation)", AnnotationCAGeneration)
	}
	names := egressbroker.ResourceNamesFor(req.MCPServerName, caGen)
	readOnly := true

	pod.Spec.Volumes = append(pod.Spec.Volumes,
		corev1.Volume{Name: caSharedVolumeName, VolumeSource: corev1.VolumeSource{
			EmptyDir: &corev1.EmptyDirVolumeSource{},
		}},
		corev1.Volume{Name: policyVolumeName, VolumeSource: corev1.VolumeSource{
			ConfigMap: &corev1.ConfigMapVolumeSource{
				LocalObjectReference: corev1.LocalObjectReference{Name: names.EgressPolicy},
			},
		}},
		corev1.Volume{Name: caSecretVolumeName, VolumeSource: corev1.VolumeSource{
			Secret: &corev1.SecretVolumeSource{SecretName: names.CASecret},
		}},
		corev1.Volume{Name: envoyConfigVolumeName, VolumeSource: corev1.VolumeSource{
			EmptyDir: &corev1.EmptyDirVolumeSource{},
		}},
	)

	// Trusted init-container: seeds the shared CA emptyDir from the
	// operator-owned bundle ConfigMap. Runs before every app container; the
	// backend's TLS trust matrix points at the seeded file. ConfigMap volumes
	// are forbidden by reverifyNoSecretMaterial only on the OPERATOR template;
	// this pod is assembled by the vMCP after verification, and only the
	// trusted init-container mounts the bundle — the backend container never
	// gets a ConfigMap-backed path.
	pod.Spec.InitContainers = append(pod.Spec.InitContainers, corev1.Container{
		Name:    caSeedInitContainerName,
		Image:   EgressBrokerImage,
		Command: []string{"sh", "-c"},
		Args: []string{
			fmt.Sprintf("cp /bundle/%s /ca/%s && chmod 0444 /ca/%s", caKeyCert, caFileName, caFileName),
		},
		VolumeMounts: []corev1.VolumeMount{
			{Name: caSharedVolumeName, MountPath: "/ca"},
			{Name: caBundleVolumeName, MountPath: "/bundle", ReadOnly: true},
		},
	})
	pod.Spec.Volumes = append(pod.Spec.Volumes, corev1.Volume{
		Name: caBundleVolumeName,
		VolumeSource: corev1.VolumeSource{
			ConfigMap: &corev1.ConfigMapVolumeSource{
				LocalObjectReference: corev1.LocalObjectReference{Name: names.CABundle},
			},
		},
	})

	// Envoy data plane: consumes the broker-rendered bootstrap.
	pod.Spec.Containers = append(pod.Spec.Containers,
		corev1.Container{
			Name:    envoyContainerName,
			Image:   EnvoyProxyImage,
			Command: []string{"envoy", "-c", envoyConfigPath + "/envoy.yaml"},
			Ports: []corev1.ContainerPort{
				{Name: "egress-proxy", ContainerPort: proxyPort},
			},
			VolumeMounts: []corev1.VolumeMount{
				{Name: envoyConfigVolumeName, MountPath: envoyConfigPath, ReadOnly: true},
			},
		},
	)

	// Credential broker: ext_authz + SDS + cert minting + Envoy bootstrap
	// rendering. The only container that ever holds credential material.
	brokerEnv := []corev1.EnvVar{
		{Name: "THV_EGRESSBROKER_POLICY_FILE", Value: "/etc/thv-policy/policy.yaml"},
		{Name: "THV_EGRESSBROKER_CA_FILE", Value: "/ca-secret/" + caKeyCert},
		{Name: "THV_EGRESSBROKER_CA_KEY_FILE", Value: "/ca-secret/" + caKeyKey},
		{Name: "THV_EGRESSBROKER_LISTEN_ADDRESS", Value: "127.0.0.1"},
		{Name: "THV_EGRESSBROKER_LISTEN_PORT", Value: fmt.Sprintf("%d", brokerListenPort)},
		{Name: "THV_EGRESSBROKER_ENVOY_BOOTSTRAP_OUT", Value: envoyConfigPath + "/envoy.yaml"},
	}
	for _, mapping := range sidecarEnv {
		brokerEnv = append(brokerEnv, corev1.EnvVar{
			Name: mapping.name,
			ValueFrom: &corev1.EnvVarSource{
				FieldRef: &corev1.ObjectFieldSelector{
					FieldPath: egressbroker.AnnotationFieldRef(mapping.annotation),
				},
			},
		})
	}
	// Token-store coordinates (Wave-3 deviation 1). Address/prefix are non-secret
	// literals; the KEK comes from a Secret env reference so the key value never
	// appears in the pod spec or a ConfigMap. A nil TokenStore renders none of
	// these — the broker then fails closed at startup.
	if req.TokenStore != nil {
		brokerEnv = append(brokerEnv,
			corev1.EnvVar{Name: "THV_EGRESSBROKER_REDIS_ADDR", Value: req.TokenStore.RedisAddr},
			corev1.EnvVar{Name: "THV_EGRESSBROKER_REDIS_KEY_PREFIX", Value: req.TokenStore.KeyPrefix},
		)
		if ref := req.TokenStore.KEKSecretRef; ref != nil {
			brokerEnv = append(brokerEnv, corev1.EnvVar{
				Name: "THV_EGRESSBROKER_KEK",
				ValueFrom: &corev1.EnvVarSource{
					SecretKeyRef: &corev1.SecretKeySelector{
						LocalObjectReference: corev1.LocalObjectReference{Name: ref.Name},
						Key:                  ref.Key,
					},
				},
			})
		}
	}
	pod.Spec.Containers = append(pod.Spec.Containers, corev1.Container{
		Name:  brokerContainerName,
		Image: EgressBrokerImage,
		Env:   brokerEnv,
		Ports: []corev1.ContainerPort{
			{Name: "ext-authz", ContainerPort: brokerListenPort},
		},
		VolumeMounts: []corev1.VolumeMount{
			{Name: caSecretVolumeName, MountPath: "/ca-secret", ReadOnly: true},
			{Name: policyVolumeName, MountPath: "/etc/thv-policy", ReadOnly: true},
			{Name: caSharedVolumeName, MountPath: caMountPath, ReadOnly: true},
			{Name: envoyConfigVolumeName, MountPath: envoyConfigPath},
		},
	})

	// Backend container: proxy env (D10) + runtime CA trust matrix (D9).
	// Appended to the mcp container only; env literals are inert config, not
	// credentials.
	for i := range pod.Spec.Containers {
		c := &pod.Spec.Containers[i]
		if c.Name != mcpContainerName {
			continue
		}
		proxyURL := fmt.Sprintf("http://127.0.0.1:%d", proxyPort)
		c.Env = append(c.Env,
			corev1.EnvVar{Name: "HTTP_PROXY", Value: proxyURL},
			corev1.EnvVar{Name: "HTTPS_PROXY", Value: proxyURL},
			corev1.EnvVar{Name: "NO_PROXY", Value: "127.0.0.1,localhost,::1"},
			corev1.EnvVar{Name: "SSL_CERT_FILE", Value: caMountPath + "/" + caFileName},
			corev1.EnvVar{Name: "NODE_EXTRA_CA_CERTS", Value: caMountPath + "/" + caFileName},
			corev1.EnvVar{Name: "REQUESTS_CA_BUNDLE", Value: caMountPath + "/" + caFileName},
			corev1.EnvVar{Name: "CURL_CA_BUNDLE", Value: caMountPath + "/" + caFileName},
		)
		c.VolumeMounts = append(c.VolumeMounts, corev1.VolumeMount{
			Name: caSharedVolumeName, MountPath: caMountPath, ReadOnly: readOnly,
		})
		return nil
	}
	return fmt.Errorf("untrusted egress wiring: pod has no %q container", mcpContainerName)
}

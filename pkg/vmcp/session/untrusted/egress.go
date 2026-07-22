// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package untrusted

import (
	"fmt"
	"sort"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/util/intstr"

	"github.com/stacklok/toolhive/pkg/egressbroker"
	vmcpconfig "github.com/stacklok/toolhive/pkg/vmcp/config"
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

	// DefaultEgressBrokerImage is the default broker sidecar image
	// (ext_authz + SDS). Bump with the ToolHive release that ships the
	// broker binary; overridable per install via THV_UNTRUSTED_BROKER_IMAGE
	// (resolved once in pkg/vmcp/cli/untrusted.go) for air-gapped mirrors.
	DefaultEgressBrokerImage = "ghcr.io/stacklok/toolhive/egressbroker:v0.17.0"
	// DefaultEnvoyProxyImage is the default Envoy data-plane image, pinned by
	// tag AND digest — a floating tag would let the untrusted data plane run
	// whatever upstream publishes next. Bump procedure: update tag+digest
	// together (resolve the digest with `crane digest envoyproxy/envoy:<tag>`),
	// then run `task operator-test`; the untrusted-egress chainsaw scenario
	// must pass. Overridable per install via THV_UNTRUSTED_ENVOY_IMAGE for
	// air-gapped mirrors.
	DefaultEnvoyProxyImage = "envoyproxy/envoy:v1.36.2@sha256:4972515dd9a069b44beb43cebba7851596e72a8c61cd7a7c33d8f48efc5280ba"

	// BrokerHealthPort is the loopback-only health HTTP port (readiness +
	// liveness probes). It is the single source of truth for the contract
	// between the clone-time probes (here) and the broker's health listener:
	// cmd/thv-egressbroker binds it directly (test-only import back into this
	// package pins the two sides).
	BrokerHealthPort = 15083

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

// SidecarImages carries the egress data-plane images for one clone. Resolved
// once at the vMCP composition root (pkg/vmcp/cli/untrusted.go); a nil *Images
// field falls back to the pinned default.
type SidecarImages struct {
	// EnvoyProxy is the Envoy data-plane image. Empty = DefaultEnvoyProxyImage.
	EnvoyProxy string
	// EgressBroker is the broker sidecar image (also the CA-seed init image).
	// Empty = DefaultEgressBrokerImage.
	EgressBroker string
}

// resolved applies the pinned defaults.
func (s SidecarImages) resolved() SidecarImages {
	if s.EnvoyProxy == "" {
		s.EnvoyProxy = DefaultEnvoyProxyImage
	}
	if s.EgressBroker == "" {
		s.EgressBroker = DefaultEgressBrokerImage
	}
	return s
}

// SidecarResourceOverride tunes the envoy/broker sidecar container resources
// (the ca-seed init container stays fixed — it copies one file and exits).
// Multipliers scale the default requests and limits together; resolved once
// at the composition root from THV_UNTRUSTED_SIDECAR_CPU/MEM.
type SidecarResourceOverride struct {
	// CPUMultiplier scales sidecar CPU requests/limits (1.0 = defaults).
	// Must be > 0.
	CPUMultiplier float64
	// MemoryMultiplier scales sidecar memory requests/limits (1.0 = defaults).
	// Must be > 0.
	MemoryMultiplier float64
}

// Default sidecar container resources (spec §3.2). Envoy's bump is
// connection-heavy; the Go broker is light; the CA-seed init copies one file.
var (
	defaultEnvoyResources = corev1.ResourceRequirements{
		Requests: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("50m"),
			corev1.ResourceMemory: resource.MustParse("64Mi"),
		},
		Limits: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("500m"),
			corev1.ResourceMemory: resource.MustParse("256Mi"),
		},
	}
	defaultBrokerResources = corev1.ResourceRequirements{
		Requests: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("25m"),
			corev1.ResourceMemory: resource.MustParse("32Mi"),
		},
		Limits: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("250m"),
			corev1.ResourceMemory: resource.MustParse("128Mi"),
		},
	}
	// caSeedResources are fixed: the init container copies one file and exits.
	caSeedResources = corev1.ResourceRequirements{
		Requests: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("10m"),
			corev1.ResourceMemory: resource.MustParse("16Mi"),
		},
		Limits: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("50m"),
			corev1.ResourceMemory: resource.MustParse("32Mi"),
		},
	}
)

// scaledResources returns a copy of base with CPU quantities multiplied by
// cpuMult and memory quantities by memMult (1.0 = unchanged).
func scaledResources(base corev1.ResourceRequirements, cpuMult, memMult float64) corev1.ResourceRequirements {
	out := *base.DeepCopy()
	scale := func(list corev1.ResourceList, name corev1.ResourceName, mult float64) {
		q, ok := list[name]
		if !ok || mult == 1.0 {
			return
		}
		q.SetMilli(int64(float64(q.MilliValue()) * mult))
		list[name] = q
	}
	for _, list := range []corev1.ResourceList{out.Requests, out.Limits} {
		scale(list, corev1.ResourceCPU, cpuMult)
		scale(list, corev1.ResourceMemory, memMult)
	}
	return out
}

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
// _REDIS_KEY_PREFIX, and the tokenenc KEKs) is injected here from
// req.TokenStore. The address, key prefix, and active key ID are non-secret
// and are rendered as env literals; each KEK, when encryption is enabled, is
// referenced from a Secret via a per-ID SecretKeyRef (never a ConfigMap or env
// literal). A nil TokenStore renders no token-store env at all — the broker
// then refuses to start (fail closed, per cmd/thv-egressbroker).
//
//nolint:gocyclo // one flat pod-assembly walk: validate, volumes, init, envoy, broker, backend.
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

	images := SidecarImages{}
	if req.Images != nil {
		images = *req.Images
	}
	images = images.resolved()
	res := SidecarResourceOverride{CPUMultiplier: 1.0, MemoryMultiplier: 1.0}
	if req.SidecarResources != nil {
		res = *req.SidecarResources
	}
	if res.CPUMultiplier <= 0 || res.MemoryMultiplier <= 0 {
		return fmt.Errorf("untrusted egress wiring: sidecar resource multipliers must be positive")
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
		Image:   images.EgressBroker,
		Command: []string{"/ko-app/thv-egressbroker", "seed-ca"},
		Args: []string{
			fmt.Sprintf("/bundle/%s", caKeyCert),
			fmt.Sprintf("/ca/%s", caFileName),
		},
		Resources: caSeedResources,
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
			Name:  envoyContainerName,
			Image: images.EnvoyProxy,
			// Wait for the broker to render the bootstrap before starting; the
			// broker writes it at startup and Envoy crashes on a missing file.
			Command: []string{"sh", "-c"},
			Args: []string{
				fmt.Sprintf("until [ -f %s/envoy.yaml ]; do sleep 0.2; done; exec envoy -c %s/envoy.yaml", envoyConfigPath, envoyConfigPath),
			},
			Resources: scaledResources(defaultEnvoyResources, res.CPUMultiplier, res.MemoryMultiplier),
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
		// Pod name for the D11 audit trail (metadata context only — the
		// identity contract above stays annotation-mirrored).
		{
			Name: egressbroker.EnvPodName,
			ValueFrom: &corev1.EnvVarSource{
				FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.name"},
			},
		},
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
	// literals; the KEKs come from per-ID Secret env references so the key values
	// never appear in the pod spec or a ConfigMap. The broker keyring gets the
	// FULL key-ID set (active + retired) so a key rotation never orphans rows
	// sealed under the old ID. A nil TokenStore renders none of these — the
	// broker then fails closed at startup.
	if req.TokenStore != nil {
		brokerEnv = append(brokerEnv,
			corev1.EnvVar{Name: "THV_EGRESSBROKER_REDIS_ADDR", Value: req.TokenStore.RedisAddr},
			corev1.EnvVar{Name: "THV_EGRESSBROKER_REDIS_KEY_PREFIX", Value: req.TokenStore.KeyPrefix},
		)
		// The token store's Redis password: SecretKeyRef only, never a literal
		// (KEK pattern). The broker reads it via vmcpconfig.RedisPasswordEnvVar
		// and fails loud at startup when it is absent.
		if req.TokenStore.RedisPasswordSecret != "" {
			brokerEnv = append(brokerEnv, corev1.EnvVar{
				Name: vmcpconfig.RedisPasswordEnvVar,
				ValueFrom: &corev1.EnvVarSource{
					SecretKeyRef: &corev1.SecretKeySelector{
						LocalObjectReference: corev1.LocalObjectReference{Name: req.TokenStore.RedisPasswordSecret},
						Key:                  req.TokenStore.RedisPasswordKey,
					},
				},
			})
		}
		if req.TokenStore.KEKSecret != "" {
			brokerEnv = append(brokerEnv,
				corev1.EnvVar{Name: "THV_EGRESSBROKER_KEK_ID", Value: req.TokenStore.KEKActiveID},
			)
			for _, id := range req.TokenStore.KEKIDs {
				brokerEnv = append(brokerEnv, corev1.EnvVar{
					Name: "THV_EGRESSBROKER_KEK_" + id,
					ValueFrom: &corev1.EnvVarSource{
						SecretKeyRef: &corev1.SecretKeySelector{
							LocalObjectReference: corev1.LocalObjectReference{Name: req.TokenStore.KEKSecret},
							Key:                  id,
						},
					},
				})
			}
		}
	}
	pod.Spec.Containers = append(pod.Spec.Containers, corev1.Container{
		Name:      brokerContainerName,
		Image:     images.EgressBroker,
		Env:       brokerEnv,
		Resources: scaledResources(defaultBrokerResources, res.CPUMultiplier, res.MemoryMultiplier),
		Ports: []corev1.ContainerPort{
			{Name: "ext-authz", ContainerPort: brokerListenPort},
			{Name: "healthz", ContainerPort: BrokerHealthPort},
		},
		// Health contract: /healthz is 200 iff Redis is reachable AND the
		// policy is loaded AND the bump CA is not past rotation-due. Readiness
		// gates the pod; liveness (3 strikes) restarts a wedged broker.
		ReadinessProbe: &corev1.Probe{
			ProbeHandler: corev1.ProbeHandler{
				HTTPGet: &corev1.HTTPGetAction{
					Path: "/healthz",
					Port: intstr.FromInt32(BrokerHealthPort),
				},
			},
			PeriodSeconds: 5,
		},
		LivenessProbe: &corev1.Probe{
			ProbeHandler: corev1.ProbeHandler{
				HTTPGet: &corev1.HTTPGetAction{
					// /livez is dependency-free (process alive), served from
					// process start — a slow Redis/CA init must not let
					// liveness kill a healthy broker mid-startup.
					Path: "/livez",
					Port: intstr.FromInt32(BrokerHealthPort),
				},
			},
			PeriodSeconds:    5,
			FailureThreshold: 3,
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

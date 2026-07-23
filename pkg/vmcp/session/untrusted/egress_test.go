// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package untrusted

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"

	"github.com/stacklok/toolhive/pkg/egressbroker"
)

func TestApplyEgressBrokerSidecar(t *testing.T) {
	t.Parallel()

	newPod := func() *corev1.Pod {
		pod, err := clonePodFromTemplate(validTemplate(), "backend-app", testRequest(), "vmcp-1")
		require.NoError(t, err)
		return pod
	}

	t.Run("sidecar, envoy, and init containers are appended", func(t *testing.T) {
		t.Parallel()
		pod := newPod()

		names := []string{}
		for _, c := range pod.Spec.Containers {
			names = append(names, c.Name)
		}
		assert.Contains(t, names, "mcp")
		assert.Contains(t, names, envoyContainerName)
		assert.Contains(t, names, brokerContainerName)

		initNames := []string{}
		for _, c := range pod.Spec.InitContainers {
			initNames = append(initNames, c.Name)
		}
		assert.Contains(t, initNames, caSeedInitContainerName)
	})

	t.Run("backend gets proxy env and the runtime CA matrix, never a credential", func(t *testing.T) {
		t.Parallel()
		pod := newPod()
		backend := findContainer(t, pod, "mcp")

		env := map[string]corev1.EnvVar{}
		for _, e := range backend.Env {
			env[e.Name] = e
		}
		for _, name := range []string{"HTTP_PROXY", "HTTPS_PROXY"} {
			require.Contains(t, env, name)
			assert.Equal(t, fmt.Sprintf("http://127.0.0.1:%d", proxyPort), env[name].Value)
		}
		assert.Equal(t, "127.0.0.1,localhost,::1", env["NO_PROXY"].Value)
		caFile := caMountPath + "/" + caFileName
		for _, name := range []string{"SSL_CERT_FILE", "NODE_EXTRA_CA_CERTS", "REQUESTS_CA_BUNDLE", "CURL_CA_BUNDLE"} {
			require.Contains(t, env, name)
			assert.Equal(t, caFile, env[name].Value)
		}
		for _, e := range backend.Env {
			if e.ValueFrom != nil {
				assert.NotNil(t, e.ValueFrom.FieldRef,
					"backend env %s must not source from Secret/ConfigMap", e.Name)
			}
		}
	})

	t.Run("backend CA mount is the shared emptyDir, read-only, not a ConfigMap", func(t *testing.T) {
		t.Parallel()
		pod := newPod()
		backend := findContainer(t, pod, "mcp")

		var mount *corev1.VolumeMount
		for i := range backend.VolumeMounts {
			if backend.VolumeMounts[i].Name == caSharedVolumeName {
				mount = &backend.VolumeMounts[i]
			}
		}
		require.NotNil(t, mount, "backend must mount the shared CA emptyDir")
		assert.Equal(t, caMountPath, mount.MountPath)
		assert.True(t, mount.ReadOnly, "backend CA mount must be read-only")

		// The backend must not mount any ConfigMap/Secret volume directly.
		volumesByName := map[string]corev1.Volume{}
		for _, v := range pod.Spec.Volumes {
			volumesByName[v.Name] = v
		}
		for _, m := range backend.VolumeMounts {
			v := volumesByName[m.Name]
			assert.Nil(t, v.ConfigMap, "backend mounts ConfigMap volume %s", m.Name)
			assert.Nil(t, v.Secret, "backend mounts Secret volume %s", m.Name)
		}
	})

	t.Run("broker container gets downward-API identity env mirroring the pod annotations", func(t *testing.T) {
		t.Parallel()
		pod := newPod()
		broker := findContainer(t, pod, brokerContainerName)

		env := map[string]corev1.EnvVar{}
		for _, e := range broker.Env {
			env[e.Name] = e
		}
		for _, mapping := range sidecarEnv {
			require.Contains(t, env, mapping.name)
			e := env[mapping.name]
			require.NotNil(t, e.ValueFrom, "%s must come from the downward API", mapping.name)
			require.NotNil(t, e.ValueFrom.FieldRef)
			expected := fmt.Sprintf("metadata.annotations['%s']", mapping.annotation)
			assert.Equal(t, expected, e.ValueFrom.FieldRef.FieldPath)
			// The annotation the env mirrors must actually be on the pod.
			assert.Contains(t, pod.Annotations, mapping.annotation)
		}
		// Static config env.
		assert.Equal(t, "/etc/thv-policy/policy.yaml", env["THV_EGRESSBROKER_POLICY_FILE"].Value)
	})

	t.Run("bump CA private key mount exists only on the broker container", func(t *testing.T) {
		t.Parallel()
		pod := newPod()

		volumesByName := map[string]corev1.Volume{}
		for _, v := range pod.Spec.Volumes {
			volumesByName[v.Name] = v
		}
		require.NotNil(t, volumesByName[caSecretVolumeName].Secret,
			"CA volume must be the operator-owned Secret")
		wantNames := egressbroker.ResourceNamesFor("github-mcp", "gen-aaa111")
		assert.Equal(t, wantNames.CASecret, volumesByName[caSecretVolumeName].Secret.SecretName)

		for i := range pod.Spec.Containers {
			c := &pod.Spec.Containers[i]
			mountsCA := false
			for _, m := range c.VolumeMounts {
				if m.Name == caSecretVolumeName {
					mountsCA = true
				}
			}
			if c.Name == brokerContainerName {
				assert.True(t, mountsCA, "broker must mount the CA Secret")
			} else {
				assert.False(t, mountsCA, "container %s must not mount the CA Secret", c.Name)
			}
		}
	})

	t.Run("policy and CA resource names match the operator contract", func(t *testing.T) {
		t.Parallel()
		pod := newPod()
		volumesByName := map[string]corev1.Volume{}
		for _, v := range pod.Spec.Volumes {
			volumesByName[v.Name] = v
		}
		wantNames := egressbroker.ResourceNamesFor("github-mcp", "gen-aaa111")
		assert.Equal(t, wantNames.EgressPolicy,
			volumesByName[policyVolumeName].ConfigMap.Name)
		assert.Equal(t, wantNames.CABundle,
			volumesByName[caBundleVolumeName].ConfigMap.Name)
	})

	t.Run("init-container seeds the CA file into the shared emptyDir", func(t *testing.T) {
		t.Parallel()
		pod := newPod()
		var init *corev1.Container
		for i := range pod.Spec.InitContainers {
			if pod.Spec.InitContainers[i].Name == caSeedInitContainerName {
				init = &pod.Spec.InitContainers[i]
			}
		}
		require.NotNil(t, init)
		mounts := map[string]string{}
		for _, m := range init.VolumeMounts {
			mounts[m.Name] = m.MountPath
		}
		assert.Contains(t, mounts, caSharedVolumeName)
		assert.Contains(t, mounts, caBundleVolumeName)
	})

	t.Run("missing CA generation fails closed (no mid-rotation split pair possible)", func(t *testing.T) {
		t.Parallel()
		req := testRequest()
		req.Session.CAGeneration = ""
		_, err := clonePodFromTemplate(validTemplate(), "backend-app", req, "vmcp-1")
		require.Error(t, err)
		assert.Contains(t, err.Error(), AnnotationCAGeneration)
	})

	t.Run("fails loudly on nil pod or missing mcp container", func(t *testing.T) {
		t.Parallel()
		require.Error(t, applyEgressBrokerSidecar(nil, testRequest()))
		require.Error(t, applyEgressBrokerSidecar(&corev1.Pod{}, testRequest()))
		require.Error(t, applyEgressBrokerSidecar(&corev1.Pod{
			Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "other"}}},
		}, testRequest()))
		noName := testRequest()
		noName.MCPServerName = ""
		require.Error(t, applyEgressBrokerSidecar(&corev1.Pod{}, noName))
	})

	t.Run("token store env rendered on the broker from TokenStoreConfig", func(t *testing.T) {
		t.Parallel()
		req := testRequest()
		req.TokenStore = &TokenStoreConfig{
			RedisAddr:           "redis.auth:6379",
			KeyPrefix:           "thv:auth:{toolhive:my-vmcp}:",
			RedisPasswordSecret: "redis-creds",
			RedisPasswordKey:    "password",
			KEKSecret:           "my-vmcp-token-kek",
			KEKActiveID:         "kek-2",
			KEKIDs:              []string{"kek-1", "kek-2"},
		}
		pod, err := clonePodFromTemplate(validTemplate(), "backend-app", req, "vmcp-1")
		require.NoError(t, err)
		broker := findContainer(t, pod, brokerContainerName)

		env := map[string]corev1.EnvVar{}
		for _, e := range broker.Env {
			env[e.Name] = e
		}
		assert.Equal(t, "redis.auth:6379", env["THV_EGRESSBROKER_REDIS_ADDR"].Value)
		assert.Equal(t, "thv:auth:{toolhive:my-vmcp}:", env["THV_EGRESSBROKER_REDIS_KEY_PREFIX"].Value)

		// The Redis password is a SecretKeyRef env (KEK pattern — never a
		// literal); the broker reads it via vmcpconfig.RedisPasswordEnvVar.
		pw := env["THV_SESSION_REDIS_PASSWORD"]
		require.NotNil(t, pw.ValueFrom, "the Redis password must come from a Secret env reference")
		require.NotNil(t, pw.ValueFrom.SecretKeyRef)
		assert.Equal(t, "redis-creds", pw.ValueFrom.SecretKeyRef.Name)
		assert.Equal(t, "password", pw.ValueFrom.SecretKeyRef.Key)
		assert.Empty(t, pw.Value, "the Redis password must never be a pod-spec literal")

		// The active key ID is a non-secret literal.
		assert.Equal(t, "kek-2", env["THV_EGRESSBROKER_KEK_ID"].Value)

		// Every key ID (active + retired) is a per-ID Secret env reference,
		// never a literal or ConfigMap.
		for _, id := range []string{"kek-1", "kek-2"} {
			kek := env["THV_EGRESSBROKER_KEK_"+id]
			require.NotNil(t, kek.ValueFrom, "KEK %s must come from a Secret env reference", id)
			require.NotNil(t, kek.ValueFrom.SecretKeyRef)
			assert.Equal(t, "my-vmcp-token-kek", kek.ValueFrom.SecretKeyRef.Name)
			assert.Equal(t, id, kek.ValueFrom.SecretKeyRef.Key)
			assert.Empty(t, kek.Value, "KEK value must never be a pod-spec literal")
		}
	})

	t.Run("nil TokenStore renders no token-store env (broker fails closed)", func(t *testing.T) {
		t.Parallel()
		pod, err := clonePodFromTemplate(validTemplate(), "backend-app", testRequest(), "vmcp-1")
		require.NoError(t, err)
		broker := findContainer(t, pod, brokerContainerName)
		for _, e := range broker.Env {
			assert.NotContains(t, []string{
				"THV_EGRESSBROKER_REDIS_ADDR",
				"THV_EGRESSBROKER_REDIS_KEY_PREFIX",
				"THV_EGRESSBROKER_KEK_ID",
			}, e.Name, "no token-store env without a TokenStore config")
			assert.NotContains(t, e.Name, "THV_EGRESSBROKER_KEK_",
				"no per-ID KEK env without a TokenStore config")
		}
	})

	t.Run("invalid TokenStore is a clone-time error (fail closed)", func(t *testing.T) {
		t.Parallel()
		for _, tc := range []struct {
			name string
			ts   *TokenStoreConfig
		}{
			{"empty addr", &TokenStoreConfig{KeyPrefix: "thv:auth:{a:b}:"}},
			{"prefix missing colon", &TokenStoreConfig{RedisAddr: "r:6379", KeyPrefix: "thv:auth"}},
			{"kek coords partial", &TokenStoreConfig{
				RedisAddr: "r:6379", KeyPrefix: "thv:auth:{a:b}:",
				KEKSecret: "s",
			}},
			{"active ID not in key set", &TokenStoreConfig{
				RedisAddr: "r:6379", KeyPrefix: "thv:auth:{a:b}:",
				KEKSecret: "s", KEKActiveID: "kek-9", KEKIDs: []string{"kek-1"},
			}},
			{"duplicate key ID", &TokenStoreConfig{
				RedisAddr: "r:6379", KeyPrefix: "thv:auth:{a:b}:",
				KEKSecret: "s", KEKActiveID: "kek-1", KEKIDs: []string{"kek-1", "kek-1"},
			}},
			{"key ID unsafe as env suffix", &TokenStoreConfig{
				RedisAddr: "r:6379", KeyPrefix: "thv:auth:{a:b}:",
				KEKSecret: "s", KEKActiveID: "kek.1", KEKIDs: []string{"kek.1"},
			}},
			{"password coordinates partial", &TokenStoreConfig{
				RedisAddr: "r:6379", KeyPrefix: "thv:auth:{a:b}:",
				RedisPasswordSecret: "redis-creds",
			}},
		} {
			t.Run(tc.name, func(t *testing.T) {
				t.Parallel()
				req := testRequest()
				req.TokenStore = tc.ts
				_, err := clonePodFromTemplate(validTemplate(), "backend-app", req, "vmcp-1")
				require.Error(t, err)
			})
		}
	})

	t.Run("sidecar images are pinned by default and overridable per clone", func(t *testing.T) {
		t.Parallel()
		pod := newPod()
		assert.Equal(t, DefaultEnvoyProxyImage, findContainer(t, pod, envoyContainerName).Image)
		assert.Equal(t, DefaultEgressBrokerImage, findContainer(t, pod, brokerContainerName).Image)
		assert.Contains(t, DefaultEnvoyProxyImage, "@sha256:",
			"the default Envoy image must be digest-pinned (supply chain)")
		var init *corev1.Container
		for i := range pod.Spec.InitContainers {
			if pod.Spec.InitContainers[i].Name == caSeedInitContainerName {
				init = &pod.Spec.InitContainers[i]
			}
		}
		require.NotNil(t, init)
		assert.Equal(t, DefaultEgressBrokerImage, init.Image)

		req := testRequest()
		req.Images = &SidecarImages{EnvoyProxy: "mirror.local/envoy:v1", EgressBroker: "mirror.local/broker:v2"}
		overridden, err := clonePodFromTemplate(validTemplate(), "backend-app", req, "vmcp-1")
		require.NoError(t, err)
		assert.Equal(t, "mirror.local/envoy:v1", findContainer(t, overridden, envoyContainerName).Image)
		assert.Equal(t, "mirror.local/broker:v2", findContainer(t, overridden, brokerContainerName).Image)
	})

	t.Run("sidecar containers carry resource requests and limits", func(t *testing.T) {
		t.Parallel()
		pod := newPod()
		envoy := findContainer(t, pod, envoyContainerName)
		broker := findContainer(t, pod, brokerContainerName)

		assert.Equal(t, "50m", envoy.Resources.Requests.Cpu().String())
		assert.Equal(t, "64Mi", envoy.Resources.Requests.Memory().String())
		assert.Equal(t, "500m", envoy.Resources.Limits.Cpu().String())
		assert.Equal(t, "256Mi", envoy.Resources.Limits.Memory().String())
		assert.Equal(t, "25m", broker.Resources.Requests.Cpu().String())
		assert.Equal(t, "32Mi", broker.Resources.Requests.Memory().String())
		assert.Equal(t, "250m", broker.Resources.Limits.Cpu().String())
		assert.Equal(t, "128Mi", broker.Resources.Limits.Memory().String())

		var init *corev1.Container
		for i := range pod.Spec.InitContainers {
			if pod.Spec.InitContainers[i].Name == caSeedInitContainerName {
				init = &pod.Spec.InitContainers[i]
			}
		}
		require.NotNil(t, init)
		assert.Equal(t, "10m", init.Resources.Requests.Cpu().String())
		assert.Equal(t, "16Mi", init.Resources.Requests.Memory().String())
		assert.Equal(t, "50m", init.Resources.Limits.Cpu().String())
		assert.Equal(t, "32Mi", init.Resources.Limits.Memory().String())
	})

	t.Run("resource multipliers scale envoy+broker, never the ca-seed init", func(t *testing.T) {
		t.Parallel()
		req := testRequest()
		req.SidecarResources = &SidecarResourceOverride{CPUMultiplier: 2.0, MemoryMultiplier: 4.0}
		pod, err := clonePodFromTemplate(validTemplate(), "backend-app", req, "vmcp-1")
		require.NoError(t, err)

		envoy := findContainer(t, pod, envoyContainerName)
		assert.Equal(t, "100m", envoy.Resources.Requests.Cpu().String())
		assert.Equal(t, "256Mi", envoy.Resources.Requests.Memory().String())
		assert.Equal(t, "1", envoy.Resources.Limits.Cpu().String())
		broker := findContainer(t, pod, brokerContainerName)
		assert.Equal(t, "50m", broker.Resources.Requests.Cpu().String())

		var init *corev1.Container
		for i := range pod.Spec.InitContainers {
			if pod.Spec.InitContainers[i].Name == caSeedInitContainerName {
				init = &pod.Spec.InitContainers[i]
			}
		}
		require.NotNil(t, init)
		assert.Equal(t, "10m", init.Resources.Requests.Cpu().String(), "init resources stay fixed")
	})

	t.Run("partial multiplier scales only its dimension", func(t *testing.T) {
		t.Parallel()
		req := testRequest()
		req.SidecarResources = &SidecarResourceOverride{CPUMultiplier: 2.0, MemoryMultiplier: 1.0}
		pod, err := clonePodFromTemplate(validTemplate(), "backend-app", req, "vmcp-1")
		require.NoError(t, err)

		envoy := findContainer(t, pod, envoyContainerName)
		assert.Equal(t, "100m", envoy.Resources.Requests.Cpu().String(), "CPU scaled 2x")
		assert.Equal(t, "64Mi", envoy.Resources.Requests.Memory().String(), "memory untouched")
		assert.Equal(t, "256Mi", envoy.Resources.Limits.Memory().String(), "memory limit untouched")
	})

	t.Run("non-positive resource multipliers fail loudly", func(t *testing.T) {
		t.Parallel()
		for _, mult := range []SidecarResourceOverride{
			{CPUMultiplier: 0, MemoryMultiplier: 1},
			{CPUMultiplier: 1, MemoryMultiplier: -1},
		} {
			req := testRequest()
			m := mult
			req.SidecarResources = &m
			_, err := clonePodFromTemplate(validTemplate(), "backend-app", req, "vmcp-1")
			require.Error(t, err)
		}
	})

	t.Run("broker container gets readiness and liveness probes on the health port", func(t *testing.T) {
		t.Parallel()
		pod := newPod()
		broker := findContainer(t, pod, brokerContainerName)

		// Liveness targets /livez (dependency-free, served from process start)
		// so a slow Redis/CA init cannot kill a healthy broker; readiness
		// targets /healthz (gated on CA/policy/Redis).
		for name, probe := range map[string]*corev1.Probe{
			"readiness": broker.ReadinessProbe,
			"liveness":  broker.LivenessProbe,
		} {
			require.NotNil(t, probe, "%s probe must be set", name)
			require.NotNil(t, probe.HTTPGet, "%s probe must be httpGet", name)
			assert.Equal(t, int32(BrokerHealthPort), probe.HTTPGet.Port.IntVal)
			assert.Equal(t, int32(5), probe.PeriodSeconds, "%s probe period", name)
		}
		assert.Equal(t, "/healthz", broker.ReadinessProbe.HTTPGet.Path)
		assert.Equal(t, "/livez", broker.LivenessProbe.HTTPGet.Path)
		assert.Equal(t, int32(3), broker.LivenessProbe.FailureThreshold)

		// Envoy and the backend container get no probes (the broker's health
		// is the pod's egress data-plane liveness contract).
		assert.Nil(t, findContainer(t, pod, envoyContainerName).ReadinessProbe)
		assert.Nil(t, findContainer(t, pod, "mcp").ReadinessProbe)
	})
}

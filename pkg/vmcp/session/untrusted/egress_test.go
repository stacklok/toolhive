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
			RedisAddr: "redis.auth:6379",
			KeyPrefix: "thv:auth:{toolhive:my-vmcp}:",
			KEKSecretRef: &SecretKeyRef{
				Name: "my-vmcp-token-kek",
				Key:  "kek",
			},
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

		// The KEK is a Secret env reference, never a literal or ConfigMap.
		kek := env["THV_EGRESSBROKER_KEK"]
		require.NotNil(t, kek.ValueFrom, "KEK must come from a Secret env reference")
		require.NotNil(t, kek.ValueFrom.SecretKeyRef)
		assert.Equal(t, "my-vmcp-token-kek", kek.ValueFrom.SecretKeyRef.Name)
		assert.Equal(t, "kek", kek.ValueFrom.SecretKeyRef.Key)
		assert.Empty(t, kek.Value, "KEK value must never be a pod-spec literal")
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
				"THV_EGRESSBROKER_KEK",
			}, e.Name, "no token-store env without a TokenStore config")
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
			{"kek ref missing key", &TokenStoreConfig{
				RedisAddr: "r:6379", KeyPrefix: "thv:auth:{a:b}:",
				KEKSecretRef: &SecretKeyRef{Name: "s"},
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
}

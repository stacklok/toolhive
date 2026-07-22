// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package untrusted

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/stacklok/toolhive/pkg/vmcp/session/binding"
)

func testRequest() EnsurePodRequest {
	return EnsurePodRequest{
		Session: SessionRef{
			SessionID:    "session-abc",
			Issuer:       "https://issuer.example.com",
			Subject:      "user-123",
			Namespace:    "toolhive",
			CAGeneration: "gen-aaa111",
		},
		MCPServerUID:  "uid-1234567890abcdef",
		MCPServerName: "github-mcp",
		Port:          8080,
		OwnerRefUID:   types.UID("uid-1234567890abcdef"),
	}
}

func validTemplate() *corev1.PodTemplateSpec {
	return &corev1.PodTemplateSpec{
		ObjectMeta: metav1.ObjectMeta{
			Annotations: map[string]string{AnnotationCAGeneration: "gen-aaa111"},
		},
		Spec: corev1.PodSpec{
			ServiceAccountName: "backend-sa",
			SecurityContext:    &corev1.PodSecurityContext{RunAsNonRoot: boolPtr(true)},
			ImagePullSecrets:   []corev1.LocalObjectReference{{Name: "pull-secret"}},
			Containers: []corev1.Container{{
				Name:  "mcp",
				Image: "ghcr.io/example/backend:1.0",
				Env: []corev1.EnvVar{
					{Name: "SENTINEL", Value: "literal-ok"},
					{Name: "POD_NAME", ValueFrom: &corev1.EnvVarSource{
						FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.name"},
					}},
				},
			}},
			Volumes: []corev1.Volume{
				{Name: "scratch", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
			},
		},
	}
}

func boolPtr(b bool) *bool { return &b }

// findContainer returns the named container from the pod or fails the test.
func findContainer(t *testing.T, pod *corev1.Pod, name string) *corev1.Container {
	t.Helper()
	for i := range pod.Spec.Containers {
		if pod.Spec.Containers[i].Name == name {
			return &pod.Spec.Containers[i]
		}
	}
	t.Fatalf("pod has no %q container", name)
	return nil
}

func TestClonePodFromTemplate(t *testing.T) {
	t.Parallel()

	t.Run("clones backend container and pod-level settings", func(t *testing.T) {
		t.Parallel()
		pod, err := clonePodFromTemplate(validTemplate(), "backend-app", testRequest(), "vmcp-1")
		require.NoError(t, err)

		backend := findContainer(t, pod, "mcp")
		assert.Equal(t, "ghcr.io/example/backend:1.0", backend.Image)
		assert.Equal(t, "backend-sa", pod.Spec.ServiceAccountName)
		require.NotNil(t, pod.Spec.SecurityContext)
		assert.Equal(t, corev1.RestartPolicyAlways, pod.Spec.RestartPolicy)
		assert.Empty(t, pod.Spec.NodeName)
	})

	t.Run("deterministic name, ownerRef to MCPServer", func(t *testing.T) {
		t.Parallel()
		req := testRequest()
		pod, err := clonePodFromTemplate(validTemplate(), "backend-app", req, "vmcp-1")
		require.NoError(t, err)

		userKey, err := binding.Format(req.Session.Issuer, req.Session.Subject)
		require.NoError(t, err)
		assert.Equal(t, PodNameFor(req.MCPServerUID, userKey, req.Session.SessionID), pod.Name)
		assert.Equal(t, "toolhive", pod.Namespace)

		require.Len(t, pod.OwnerReferences, 1)
		or := pod.OwnerReferences[0]
		assert.Equal(t, "toolhive.stacklok.dev/v1beta1", or.APIVersion)
		assert.Equal(t, "MCPServer", or.Kind)
		assert.Equal(t, "github-mcp", or.Name)
		assert.Equal(t, req.OwnerRefUID, or.UID)
		assert.Nil(t, or.Controller, "ownerRef must be non-controller")
	})

	t.Run("labels carry hashed identity, never raw sub", func(t *testing.T) {
		t.Parallel()
		req := testRequest()
		pod, err := clonePodFromTemplate(validTemplate(), "backend-app", req, "vmcp-1")
		require.NoError(t, err)

		assert.Equal(t, "backend-app", pod.Labels[LabelApp])
		assert.Equal(t, "true", pod.Labels[LabelUntrusted])
		assert.Equal(t, req.MCPServerUID, pod.Labels[LabelMCPServerUID])
		assert.Len(t, pod.Labels[LabelUntrustedUser], 40)
		assert.Len(t, pod.Labels[LabelUntrustedSession], 40)
		for k, v := range pod.Labels {
			assert.NotContains(t, v, "user-123", "label %s leaks raw sub", k)
		}
	})

	t.Run("annotations: raw iss, hashed sub, session id, vmcp uid", func(t *testing.T) {
		t.Parallel()
		req := testRequest()
		pod, err := clonePodFromTemplate(validTemplate(), "backend-app", req, "vmcp-1")
		require.NoError(t, err)

		assert.Equal(t, "https://issuer.example.com", pod.Annotations[AnnotationIssuer])
		assert.Equal(t, subjectHash("user-123"), pod.Annotations[AnnotationSubjectHash])
		assert.NotContains(t, pod.Annotations[AnnotationSubjectHash], "user-123")
		assert.Equal(t, "user-123", pod.Annotations[AnnotationSubjectRaw],
			"raw sub must be carried in the sub-raw annotation (Wave-2/3 contract)")
		assert.Equal(t, "github-mcp", pod.Annotations[AnnotationMCPServerName])
		assert.Equal(t, "session-abc", pod.Annotations[AnnotationSessionID])
		assert.Equal(t, "vmcp-1", pod.Annotations[AnnotationVMCPUid])
	})

	t.Run("does not mutate the source template", func(t *testing.T) {
		t.Parallel()
		tmpl := validTemplate()
		_, err := clonePodFromTemplate(tmpl, "backend-app", testRequest(), "vmcp-1")
		require.NoError(t, err)
		assert.Empty(t, tmpl.Spec.RestartPolicy, "template was mutated")
	})
}

func TestClonePodFromTemplate_FailClosed(t *testing.T) {
	t.Parallel()

	withContainer := func(mut func(c *corev1.Container)) *corev1.PodTemplateSpec {
		tmpl := validTemplate()
		mut(&tmpl.Spec.Containers[0])
		return tmpl
	}
	withVolume := func(v corev1.Volume) *corev1.PodTemplateSpec {
		tmpl := validTemplate()
		tmpl.Spec.Volumes = append(tmpl.Spec.Volumes, v)
		return tmpl
	}

	cases := []struct {
		name     string
		template *corev1.PodTemplateSpec
		wantErr  string
	}{
		{
			name:     "nil template",
			template: nil,
			wantErr:  "pod template is nil",
		},
		{
			name: "secretKeyRef env",
			template: withContainer(func(c *corev1.Container) {
				c.Env = append(c.Env, corev1.EnvVar{Name: "TOKEN", ValueFrom: &corev1.EnvVarSource{
					SecretKeyRef: &corev1.SecretKeySelector{
						LocalObjectReference: corev1.LocalObjectReference{Name: "creds"}, Key: "token",
					},
				}})
			}),
			wantErr: `sources from Secret "creds"`,
		},
		{
			name: "configMapKeyRef env",
			template: withContainer(func(c *corev1.Container) {
				c.Env = append(c.Env, corev1.EnvVar{Name: "CFG", ValueFrom: &corev1.EnvVarSource{
					ConfigMapKeyRef: &corev1.ConfigMapKeySelector{
						LocalObjectReference: corev1.LocalObjectReference{Name: "cfg"}, Key: "k",
					},
				}})
			}),
			wantErr: `sources from ConfigMap "cfg"`,
		},
		{
			name: "envFrom secretRef",
			template: withContainer(func(c *corev1.Container) {
				c.EnvFrom = append(c.EnvFrom, corev1.EnvFromSource{
					SecretRef: &corev1.SecretEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: "creds"}},
				})
			}),
			wantErr: `envFrom on container "mcp" sources from Secret "creds"`,
		},
		{
			name: "envFrom configMapRef",
			template: withContainer(func(c *corev1.Container) {
				c.EnvFrom = append(c.EnvFrom, corev1.EnvFromSource{
					ConfigMapRef: &corev1.ConfigMapEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: "cfg"}},
				})
			}),
			wantErr: `envFrom on container "mcp" sources from ConfigMap "cfg"`,
		},
		{
			name: "secret volume",
			template: withVolume(corev1.Volume{Name: "sec", VolumeSource: corev1.VolumeSource{
				Secret: &corev1.SecretVolumeSource{SecretName: "creds"},
			}}),
			wantErr: `volume "sec" sources from Secret "creds"`,
		},
		{
			name: "configmap volume",
			template: withVolume(corev1.Volume{Name: "cm", VolumeSource: corev1.VolumeSource{
				ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: "cfg"}},
			}}),
			wantErr: `volume "cm" sources from ConfigMap "cfg"`,
		},
		{
			name: "projected secret",
			template: withVolume(corev1.Volume{Name: "proj", VolumeSource: corev1.VolumeSource{
				Projected: &corev1.ProjectedVolumeSource{Sources: []corev1.VolumeProjection{{
					Secret: &corev1.SecretProjection{LocalObjectReference: corev1.LocalObjectReference{Name: "creds"}},
				}}},
			}}),
			wantErr: `volume "proj" projects Secret "creds"`,
		},
		{
			name: "projected configmap",
			template: withVolume(corev1.Volume{Name: "proj", VolumeSource: corev1.VolumeSource{
				Projected: &corev1.ProjectedVolumeSource{Sources: []corev1.VolumeProjection{{
					ConfigMap: &corev1.ConfigMapProjection{LocalObjectReference: corev1.LocalObjectReference{Name: "cfg"}},
				}}},
			}}),
			wantErr: `volume "proj" projects ConfigMap "cfg"`,
		},
		{
			name: "csi secretProviderClass",
			template: withVolume(corev1.Volume{Name: "csi", VolumeSource: corev1.VolumeSource{
				CSI: &corev1.CSIVolumeSource{Driver: "secrets-store.csi.k8s.io",
					VolumeAttributes: map[string]string{"secretProviderClass": "spc"}},
			}}),
			wantErr: `CSI SecretProviderClass "spc"`,
		},
		{
			name: "missing mcp container",
			template: &corev1.PodTemplateSpec{Spec: corev1.PodSpec{
				Containers: []corev1.Container{{Name: "sidecar", Image: "img"}},
			}},
			wantErr: `no "mcp" container`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := clonePodFromTemplate(tc.template, "app", testRequest(), "vmcp-1")
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantErr)
			assert.Contains(t, err.Error(), "untrusted pod clone")
		})
	}
}

func TestReverifyAllowsFieldRefEnv(t *testing.T) {
	t.Parallel()
	// FieldRef exposes pod metadata, not credentials — allowed by the gate.
	tmpl := validTemplate()
	require.NoError(t, reverifyNoSecretMaterial(tmpl))
}

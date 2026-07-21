// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllerutil

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
)

// secretEnvVar builds a SecretKeyRef-sourced EnvVar for test fixtures.
func secretEnvVar(name, secretName, key string) corev1.EnvVar {
	return corev1.EnvVar{
		Name: name,
		ValueFrom: &corev1.EnvVarSource{
			SecretKeyRef: &corev1.SecretKeySelector{
				LocalObjectReference: corev1.LocalObjectReference{Name: secretName},
				Key:                  key,
			},
		},
	}
}

// configMapEnvVar builds a ConfigMapKeyRef-sourced EnvVar for test fixtures.
func configMapEnvVar(name, configMapName, key string) corev1.EnvVar {
	return corev1.EnvVar{
		Name: name,
		ValueFrom: &corev1.EnvVarSource{
			ConfigMapKeyRef: &corev1.ConfigMapKeySelector{
				LocalObjectReference: corev1.LocalObjectReference{Name: configMapName},
				Key:                  key,
			},
		},
	}
}

func TestValidateNoSecretEnvForUntrusted(t *testing.T) {
	t.Parallel()

	const backend = "mcp"

	tests := []struct {
		name        string
		podTemplate *corev1.PodTemplateSpec
		untrusted   bool
		wantErr     string // empty means no error; otherwise a substring of the error
	}{
		{
			name: "secretkeyref env on backend container is rejected",
			podTemplate: &corev1.PodTemplateSpec{Spec: corev1.PodSpec{
				Containers: []corev1.Container{{
					Name: backend,
					Env:  []corev1.EnvVar{secretEnvVar("API_TOKEN", "backend-creds", "token")},
				}},
			}},
			untrusted: true,
			wantErr:   `env var "API_TOKEN" on container "mcp" sources its value from Secret "backend-creds"`,
		},
		{
			name: "secretkeyref env is allowed when workload is trusted",
			podTemplate: &corev1.PodTemplateSpec{Spec: corev1.PodSpec{
				Containers: []corev1.Container{{
					Name: backend,
					Env:  []corev1.EnvVar{secretEnvVar("API_TOKEN", "backend-creds", "token")},
				}},
			}},
			untrusted: false,
		},
		{
			name: "configmapkeyref env on backend container is rejected",
			podTemplate: &corev1.PodTemplateSpec{Spec: corev1.PodSpec{
				Containers: []corev1.Container{{
					Name: backend,
					Env:  []corev1.EnvVar{configMapEnvVar("API_TOKEN", "backend-config", "token")},
				}},
			}},
			untrusted: true,
			wantErr:   `env var "API_TOKEN" on container "mcp" sources its value from ConfigMap "backend-config"`,
		},
		{
			name: "fieldref env on backend container is allowed",
			podTemplate: &corev1.PodTemplateSpec{Spec: corev1.PodSpec{
				Containers: []corev1.Container{{
					Name: backend,
					Env: []corev1.EnvVar{{
						Name: "POD_NAME",
						ValueFrom: &corev1.EnvVarSource{
							FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.name"},
						},
					}},
				}},
			}},
			untrusted: true,
		},
		{
			name: "secretkeyref env on backend init container is rejected",
			podTemplate: &corev1.PodTemplateSpec{Spec: corev1.PodSpec{
				InitContainers: []corev1.Container{{
					Name: backend,
					Env:  []corev1.EnvVar{secretEnvVar("INIT_TOKEN", "init-creds", "token")},
				}},
			}},
			untrusted: true,
			wantErr:   `env var "INIT_TOKEN" on container "mcp" sources its value from Secret "init-creds"`,
		},
		{
			name: "secretkeyref env on a sidecar container is allowed",
			podTemplate: &corev1.PodTemplateSpec{Spec: corev1.PodSpec{
				Containers: []corev1.Container{{
					Name: "sidecar",
					Env:  []corev1.EnvVar{secretEnvVar("SIDECAR_TOKEN", "sidecar-creds", "token")},
				}},
			}},
			untrusted: true,
		},
		{
			name: "literal-only env on backend container is allowed",
			podTemplate: &corev1.PodTemplateSpec{Spec: corev1.PodSpec{
				Containers: []corev1.Container{{
					Name: backend,
					Env:  []corev1.EnvVar{{Name: "SENTINEL", Value: "literal-value"}},
				}},
			}},
			untrusted: true,
		},
		{
			name: "secret volume is rejected",
			podTemplate: &corev1.PodTemplateSpec{Spec: corev1.PodSpec{
				Volumes: []corev1.Volume{{
					Name: "creds",
					VolumeSource: corev1.VolumeSource{
						Secret: &corev1.SecretVolumeSource{SecretName: "backend-creds"},
					},
				}},
			}},
			untrusted: true,
			wantErr:   `volume "creds" sources its content from Secret "backend-creds"`,
		},
		{
			name: "envFrom secretRef on backend container is rejected",
			podTemplate: &corev1.PodTemplateSpec{Spec: corev1.PodSpec{
				Containers: []corev1.Container{{
					Name: backend,
					EnvFrom: []corev1.EnvFromSource{{
						SecretRef: &corev1.SecretEnvSource{
							LocalObjectReference: corev1.LocalObjectReference{Name: "backend-creds"},
						},
					}},
				}},
			}},
			untrusted: true,
			wantErr:   `envFrom on container "mcp" sources values from Secret "backend-creds"`,
		},
		{
			name: "envFrom configMapRef on backend container is rejected",
			podTemplate: &corev1.PodTemplateSpec{Spec: corev1.PodSpec{
				Containers: []corev1.Container{{
					Name: backend,
					EnvFrom: []corev1.EnvFromSource{{
						ConfigMapRef: &corev1.ConfigMapEnvSource{
							LocalObjectReference: corev1.LocalObjectReference{Name: "backend-config"},
						},
					}},
				}},
			}},
			untrusted: true,
			wantErr:   `envFrom on container "mcp" sources values from ConfigMap "backend-config"`,
		},
		{
			name: "envFrom secretRef on backend init container is rejected",
			podTemplate: &corev1.PodTemplateSpec{Spec: corev1.PodSpec{
				InitContainers: []corev1.Container{{
					Name: backend,
					EnvFrom: []corev1.EnvFromSource{{
						SecretRef: &corev1.SecretEnvSource{
							LocalObjectReference: corev1.LocalObjectReference{Name: "init-creds"},
						},
					}},
				}},
			}},
			untrusted: true,
			wantErr:   `envFrom on container "mcp" sources values from Secret "init-creds"`,
		},
		{
			name: "envFrom secretRef on a sidecar container is allowed",
			podTemplate: &corev1.PodTemplateSpec{Spec: corev1.PodSpec{
				Containers: []corev1.Container{{
					Name: "sidecar",
					EnvFrom: []corev1.EnvFromSource{{
						SecretRef: &corev1.SecretEnvSource{
							LocalObjectReference: corev1.LocalObjectReference{Name: "sidecar-creds"},
						},
					}},
				}},
			}},
			untrusted: true,
		},
		{
			name: "configmap volume is rejected",
			podTemplate: &corev1.PodTemplateSpec{Spec: corev1.PodSpec{
				Volumes: []corev1.Volume{{
					Name: "config",
					VolumeSource: corev1.VolumeSource{
						ConfigMap: &corev1.ConfigMapVolumeSource{
							LocalObjectReference: corev1.LocalObjectReference{Name: "backend-config"},
						},
					},
				}},
			}},
			untrusted: true,
			wantErr:   `volume "config" sources its content from ConfigMap "backend-config"`,
		},
		{
			name: "projected volume with secret source is rejected",
			podTemplate: &corev1.PodTemplateSpec{Spec: corev1.PodSpec{
				Volumes: []corev1.Volume{{
					Name: "projected",
					VolumeSource: corev1.VolumeSource{
						Projected: &corev1.ProjectedVolumeSource{
							Sources: []corev1.VolumeProjection{{
								Secret: &corev1.SecretProjection{
									LocalObjectReference: corev1.LocalObjectReference{Name: "backend-creds"},
								},
							}},
						},
					},
				}},
			}},
			untrusted: true,
			wantErr:   `volume "projected" projects Secret "backend-creds"`,
		},
		{
			name: "projected volume with configmap source is rejected",
			podTemplate: &corev1.PodTemplateSpec{Spec: corev1.PodSpec{
				Volumes: []corev1.Volume{{
					Name: "projected",
					VolumeSource: corev1.VolumeSource{
						Projected: &corev1.ProjectedVolumeSource{
							Sources: []corev1.VolumeProjection{{
								ConfigMap: &corev1.ConfigMapProjection{
									LocalObjectReference: corev1.LocalObjectReference{Name: "backend-config"},
								},
							}},
						},
					},
				}},
			}},
			untrusted: true,
			wantErr:   `volume "projected" projects ConfigMap "backend-config"`,
		},
		{
			name: "projected volume with only downward-api sources is allowed",
			podTemplate: &corev1.PodTemplateSpec{Spec: corev1.PodSpec{
				Volumes: []corev1.Volume{{
					Name: "projected",
					VolumeSource: corev1.VolumeSource{
						Projected: &corev1.ProjectedVolumeSource{
							Sources: []corev1.VolumeProjection{{
								DownwardAPI: &corev1.DownwardAPIProjection{
									Items: []corev1.DownwardAPIVolumeFile{{
										Path:     "labels",
										FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.labels"},
									}},
								},
							}},
						},
					},
				}},
			}},
			untrusted: true,
		},
		{
			name: "csi secretproviderclass volume is rejected",
			podTemplate: &corev1.PodTemplateSpec{Spec: corev1.PodSpec{
				Volumes: []corev1.Volume{{
					Name: "csi-creds",
					VolumeSource: corev1.VolumeSource{
						CSI: &corev1.CSIVolumeSource{
							Driver:           "secrets-store.csi.k8s.io",
							VolumeAttributes: map[string]string{"secretProviderClass": "vault-creds"},
						},
					},
				}},
			}},
			untrusted: true,
			wantErr:   `volume "csi-creds" sources its content from CSI SecretProviderClass "vault-creds"`,
		},
		{
			name: "csi volume without secretproviderclass attribute is allowed",
			podTemplate: &corev1.PodTemplateSpec{Spec: corev1.PodSpec{
				Volumes: []corev1.Volume{{
					Name: "csi-data",
					VolumeSource: corev1.VolumeSource{
						CSI: &corev1.CSIVolumeSource{
							Driver:           "ebs.csi.aws.com",
							VolumeAttributes: map[string]string{"foo": "bar"},
						},
					},
				}},
			}},
			untrusted: true,
		},
		{
			name:        "nil pod template is a no-op",
			podTemplate: nil,
			untrusted:   true,
		},
		{
			name: "merged spec.secrets and raw template entries are both caught",
			podTemplate: &corev1.PodTemplateSpec{Spec: corev1.PodSpec{
				Containers: []corev1.Container{{
					Name: backend,
					Env: []corev1.EnvVar{
						// From spec.secrets via WithSecrets.
						secretEnvVar("FROM_SPEC_SECRETS", "declared-secret", "key"),
						// From a raw podTemplateSpec smuggled onto the same container.
						secretEnvVar("FROM_RAW_TEMPLATE", "smuggled-secret", "key"),
					},
				}},
			}},
			untrusted: true,
			wantErr:   `env var "FROM_SPEC_SECRETS" on container "mcp" sources its value from Secret "declared-secret"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := ValidateNoSecretEnvForUntrusted(tt.podTemplate, backend, tt.untrusted)
			if tt.wantErr == "" {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
			}
		})
	}
}

// TestValidateNoSecretEnvForUntrusted_MergedBuilderOutput exercises the gate against
// the actual PodTemplateSpecBuilder output, proving that both seams (declarative
// spec.secrets and raw spec.podTemplateSpec) converge into a patch the gate rejects.
func TestValidateNoSecretEnvForUntrusted_MergedBuilderOutput(t *testing.T) {
	t.Parallel()

	builder, err := NewPodTemplateSpecBuilder(nil, "mcp")
	require.NoError(t, err)

	podTemplate := builder.
		WithSecrets([]mcpv1beta1.SecretRef{
			{Name: "declared-secret", Key: "token", TargetEnvName: "FROM_SPEC_SECRETS"},
		}).
		Build()
	require.NotNil(t, podTemplate)

	// Simulate the merged case: a raw template entry on the same container would
	// land alongside the WithSecrets entry in the same patch.
	podTemplate.Spec.Containers[0].Env = append(
		podTemplate.Spec.Containers[0].Env,
		secretEnvVar("FROM_RAW_TEMPLATE", "smuggled-secret", "token"),
	)

	err = ValidateNoSecretEnvForUntrusted(podTemplate, "mcp", true)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `env var "FROM_SPEC_SECRETS" on container "mcp" sources its value from Secret "declared-secret"`)
}

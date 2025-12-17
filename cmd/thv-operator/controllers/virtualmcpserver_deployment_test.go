// Copyright 2025 Stacklok, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package controllers

import (
	"context"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	ctrlutil "github.com/stacklok/toolhive/cmd/thv-operator/pkg/controllerutil"
	"github.com/stacklok/toolhive/cmd/thv-operator/pkg/runconfig/configmap/checksum"
	"github.com/stacklok/toolhive/pkg/vmcp/workloads"
)

// TestDeploymentForVirtualMCPServer tests Deployment creation
func TestDeploymentForVirtualMCPServer(t *testing.T) {
	t.Parallel()

	vmcp := &mcpv1alpha1.VirtualMCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-vmcp",
			Namespace: "default",
		},
		Spec: mcpv1alpha1.VirtualMCPServerSpec{
			GroupRef: mcpv1alpha1.GroupRef{
				Name: "test-group",
			},
		},
	}

	scheme := runtime.NewScheme()
	_ = mcpv1alpha1.AddToScheme(scheme)

	r := &VirtualMCPServerReconciler{
		Scheme:           scheme,
		PlatformDetector: ctrlutil.NewSharedPlatformDetector(),
	}

	deployment := r.deploymentForVirtualMCPServer(context.Background(), vmcp, "test-checksum", []workloads.TypedWorkload{})

	require.NotNil(t, deployment)
	assert.Equal(t, vmcp.Name, deployment.Name)
	assert.Equal(t, vmcp.Namespace, deployment.Namespace)
	assert.NotNil(t, deployment.Spec.Replicas)
	assert.Equal(t, int32(1), *deployment.Spec.Replicas)

	// Verify labels
	expectedLabels := labelsForVirtualMCPServer(vmcp.Name)
	assert.Equal(t, expectedLabels, deployment.Labels)
	assert.Equal(t, expectedLabels, deployment.Spec.Template.Labels)

	// Verify service account
	assert.Equal(t, vmcpServiceAccountName(vmcp.Name), deployment.Spec.Template.Spec.ServiceAccountName)

	// Verify checksum annotation using standard annotation key
	assert.Equal(t, "test-checksum",
		deployment.Spec.Template.Annotations[checksum.RunConfigChecksumAnnotation])
}

// TestBuildContainerArgsForVmcp tests container argument generation
func TestBuildContainerArgsForVmcp(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		vmcp     *mcpv1alpha1.VirtualMCPServer
		wantArgs []string
	}{
		{
			name: "without log level",
			vmcp: &mcpv1alpha1.VirtualMCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-vmcp",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.VirtualMCPServerSpec{
					GroupRef: mcpv1alpha1.GroupRef{
						Name: "test-group",
					},
				},
			},
			wantArgs: []string{"serve", "--config=/etc/vmcp-config/config.yaml", "--host=0.0.0.0", "--port=4483"},
		},
		{
			name: "with log level debug",
			vmcp: &mcpv1alpha1.VirtualMCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-vmcp",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.VirtualMCPServerSpec{
					GroupRef: mcpv1alpha1.GroupRef{
						Name: "test-group",
					},
					Operational: &mcpv1alpha1.OperationalConfig{
						LogLevel: "debug",
					},
				},
			},
			wantArgs: []string{"serve", "--config=/etc/vmcp-config/config.yaml", "--host=0.0.0.0", "--port=4483", "--debug"},
		},
	}

	for _, tt := range tests {
		tt := tt // capture range variable
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := &VirtualMCPServerReconciler{}
			args := r.buildContainerArgsForVmcp(tt.vmcp)

			assert.Equal(t, tt.wantArgs, args)
		})
	}
}

// TestBuildVolumesForVmcp tests volume and volume mount generation
func TestBuildVolumesForVmcp(t *testing.T) {
	t.Parallel()

	vmcp := &mcpv1alpha1.VirtualMCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-vmcp",
			Namespace: "default",
		},
		Spec: mcpv1alpha1.VirtualMCPServerSpec{
			GroupRef: mcpv1alpha1.GroupRef{
				Name: "test-group",
			},
		},
	}

	r := &VirtualMCPServerReconciler{}
	volumeMounts, volumes := r.buildVolumesForVmcp(vmcp)

	// Verify vmcp config volume
	require.Len(t, volumeMounts, 1)
	assert.Equal(t, "vmcp-config", volumeMounts[0].Name)
	assert.Equal(t, "/etc/vmcp-config", volumeMounts[0].MountPath)
	assert.True(t, volumeMounts[0].ReadOnly)

	require.Len(t, volumes, 1)
	assert.Equal(t, "vmcp-config", volumes[0].Name)
	assert.NotNil(t, volumes[0].ConfigMap)
	assert.Equal(t, "test-vmcp-vmcp-config", volumes[0].ConfigMap.Name)
}

// TestBuildEnvVarsForVmcp tests environment variable generation
func TestBuildEnvVarsForVmcp(t *testing.T) {
	t.Parallel()

	vmcp := &mcpv1alpha1.VirtualMCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-vmcp",
			Namespace: "test-namespace",
		},
		Spec: mcpv1alpha1.VirtualMCPServerSpec{
			GroupRef: mcpv1alpha1.GroupRef{
				Name: "test-group",
			},
		},
	}

	r := &VirtualMCPServerReconciler{}
	env := r.buildEnvVarsForVmcp(context.Background(), vmcp, []workloads.TypedWorkload{})

	// Should have VMCP_NAME and VMCP_NAMESPACE
	foundName := false
	foundNamespace := false

	for _, e := range env {
		if e.Name == "VMCP_NAME" {
			foundName = true
			assert.Equal(t, "test-vmcp", e.Value)
		}
		if e.Name == "VMCP_NAMESPACE" {
			foundNamespace = true
			assert.Equal(t, "test-namespace", e.Value)
		}
	}

	assert.True(t, foundName, "Should have VMCP_NAME env var")
	assert.True(t, foundNamespace, "Should have VMCP_NAMESPACE env var")
}

// TestBuildDeploymentMetadataForVmcp tests deployment metadata generation
func TestBuildDeploymentMetadataForVmcp(t *testing.T) {
	t.Parallel()

	baseLabels := labelsForVirtualMCPServer("test-vmcp")
	vmcp := &mcpv1alpha1.VirtualMCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-vmcp",
			Namespace: "default",
		},
	}

	r := &VirtualMCPServerReconciler{}
	labels, annotations := r.buildDeploymentMetadataForVmcp(baseLabels, vmcp)

	assert.Equal(t, baseLabels, labels)
	assert.NotNil(t, annotations)
}

// TestBuildPodTemplateMetadata tests pod template metadata generation
func TestBuildPodTemplateMetadata(t *testing.T) {
	t.Parallel()

	baseLabels := labelsForVirtualMCPServer("test-vmcp")
	vmcp := &mcpv1alpha1.VirtualMCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-vmcp",
			Namespace: "default",
		},
	}
	checksumValue := "test-checksum-123"

	r := &VirtualMCPServerReconciler{}
	labels, annotations := r.buildPodTemplateMetadata(baseLabels, vmcp, checksumValue)

	assert.Equal(t, baseLabels, labels)
	assert.Equal(t, checksumValue, annotations[checksum.RunConfigChecksumAnnotation])
}

// TestBuildSecurityContextsForVmcp tests security context generation
func TestBuildSecurityContextsForVmcp(t *testing.T) {
	t.Parallel()

	vmcp := &mcpv1alpha1.VirtualMCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-vmcp",
			Namespace: "default",
		},
	}

	r := &VirtualMCPServerReconciler{
		PlatformDetector: ctrlutil.NewSharedPlatformDetector(),
	}

	podSecCtx, containerSecCtx := r.buildSecurityContextsForVmcp(context.Background(), vmcp)

	assert.NotNil(t, podSecCtx)
	assert.NotNil(t, containerSecCtx)
}

// TestBuildContainerPortsForVmcp tests container port generation
func TestBuildContainerPortsForVmcp(t *testing.T) {
	t.Parallel()

	vmcp := &mcpv1alpha1.VirtualMCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-vmcp",
			Namespace: "default",
		},
	}

	r := &VirtualMCPServerReconciler{}
	ports := r.buildContainerPortsForVmcp(vmcp)

	require.Len(t, ports, 1)
	assert.Equal(t, vmcpDefaultPort, ports[0].ContainerPort)
	assert.Equal(t, "http", ports[0].Name)
	assert.Equal(t, corev1.ProtocolTCP, ports[0].Protocol)
}

// TestServiceForVirtualMCPServer tests Service creation
func TestServiceForVirtualMCPServer(t *testing.T) {
	t.Parallel()

	vmcp := &mcpv1alpha1.VirtualMCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-vmcp",
			Namespace: "default",
		},
		Spec: mcpv1alpha1.VirtualMCPServerSpec{
			GroupRef: mcpv1alpha1.GroupRef{
				Name: "test-group",
			},
		},
	}

	scheme := runtime.NewScheme()
	_ = mcpv1alpha1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)

	r := &VirtualMCPServerReconciler{
		Scheme: scheme,
	}

	service := r.serviceForVirtualMCPServer(context.Background(), vmcp)

	require.NotNil(t, service)
	assert.Equal(t, vmcpServiceName(vmcp.Name), service.Name)
	assert.Equal(t, vmcp.Namespace, service.Namespace)
	assert.Equal(t, corev1.ServiceTypeClusterIP, service.Spec.Type)

	// Verify labels
	expectedLabels := labelsForVirtualMCPServer(vmcp.Name)
	assert.Equal(t, expectedLabels, service.Spec.Selector)

	// Verify ports
	require.Len(t, service.Spec.Ports, 1)
	assert.Equal(t, vmcpDefaultPort, service.Spec.Ports[0].Port)
	assert.Equal(t, "http", service.Spec.Ports[0].Name)
}

// TestBuildServiceMetadataForVmcp tests service metadata generation
func TestBuildServiceMetadataForVmcp(t *testing.T) {
	t.Parallel()

	baseLabels := labelsForVirtualMCPServer("test-vmcp")
	vmcp := &mcpv1alpha1.VirtualMCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-vmcp",
			Namespace: "default",
		},
	}

	r := &VirtualMCPServerReconciler{}
	labels, annotations := r.buildServiceMetadataForVmcp(baseLabels, vmcp)

	assert.Equal(t, baseLabels, labels)
	assert.NotNil(t, annotations)
}

// TestGetVmcpImage tests vmcp image retrieval
//
//nolint:paralleltest,tparallel // Cannot run in parallel due to environment variable manipulation
func TestGetVmcpImage(t *testing.T) {
	// Note: Not using t.Parallel() because subtests manipulate environment variables
	tests := []struct {
		name          string
		envValue      string
		expectedImage string
	}{
		{
			name:          "default image",
			envValue:      "",
			expectedImage: "ghcr.io/stacklok/toolhive/vmcp:latest",
		},
		{
			name:          "custom image from env",
			envValue:      "custom-registry/vmcp:v1.0.0",
			expectedImage: "custom-registry/vmcp:v1.0.0",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			// Cannot run subtests in parallel due to environment variable manipulation

			if tt.envValue != "" {
				err := os.Setenv("VMCP_IMAGE", tt.envValue)
				require.NoError(t, err)
				defer os.Unsetenv("VMCP_IMAGE")
			}

			image := getVmcpImage()
			assert.Equal(t, tt.expectedImage, image)
		})
	}
}

// TestDeploymentNeedsUpdate tests deployment update detection
func TestDeploymentNeedsUpdate(t *testing.T) {
	t.Parallel()

	// This is a basic test - full testing would require more setup
	r := &VirtualMCPServerReconciler{
		PlatformDetector: ctrlutil.NewSharedPlatformDetector(),
	}

	// Test nil inputs
	assert.True(t, r.deploymentNeedsUpdate(context.Background(), nil, nil, "", []workloads.TypedWorkload{}))

	vmcp := &mcpv1alpha1.VirtualMCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-vmcp",
			Namespace: "default",
		},
	}

	// Test with nil deployment
	assert.True(t, r.deploymentNeedsUpdate(context.Background(), nil, vmcp, "checksum", []workloads.TypedWorkload{}))
}

// TestServiceNeedsUpdate tests service update detection
func TestServiceNeedsUpdate(t *testing.T) {
	t.Parallel()

	r := &VirtualMCPServerReconciler{}

	// Test nil inputs
	assert.True(t, r.serviceNeedsUpdate(nil, nil))

	vmcp := &mcpv1alpha1.VirtualMCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-vmcp",
			Namespace: "default",
		},
	}

	// Test with nil service
	assert.True(t, r.serviceNeedsUpdate(nil, vmcp))

	// Test with service missing port
	service := &corev1.Service{
		Spec: corev1.ServiceSpec{
			Ports: []corev1.ServicePort{},
		},
	}
	assert.True(t, r.serviceNeedsUpdate(service, vmcp))
}

// TestBuildMergedResourcesForVmcp tests intelligent resource merging
func TestBuildMergedResourcesForVmcp(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                  string
		podTemplateSpecJSON   string
		expectedCPURequest    string
		expectedCPULimit      string
		expectedMemoryRequest string
		expectedMemoryLimit   string
	}{
		{
			name:                  "no PodTemplateSpec - use defaults",
			podTemplateSpecJSON:   "",
			expectedCPURequest:    ctrlutil.DefaultCPURequest,
			expectedCPULimit:      ctrlutil.DefaultCPULimit,
			expectedMemoryRequest: ctrlutil.DefaultMemoryRequest,
			expectedMemoryLimit:   ctrlutil.DefaultMemoryLimit,
		},
		{
			name: "user provides both requests and limits - use user values",
			podTemplateSpecJSON: `{
				"spec": {
					"containers": [{
						"name": "vmcp",
						"resources": {
							"requests": {
								"cpu": "200m",
								"memory": "256Mi"
							},
							"limits": {
								"cpu": "1000m",
								"memory": "1Gi"
							}
						}
					}]
				}
			}`,
			expectedCPURequest:    "200m",
			expectedCPULimit:      "1", // 1000m is normalized to 1 core
			expectedMemoryRequest: "256Mi",
			expectedMemoryLimit:   "1Gi",
		},
		{
			name: "user provides only limits - use default requests + user limits",
			podTemplateSpecJSON: `{
				"spec": {
					"containers": [{
						"name": "vmcp",
						"resources": {
							"limits": {
								"cpu": "800m",
								"memory": "768Mi"
							}
						}
					}]
				}
			}`,
			expectedCPURequest:    ctrlutil.DefaultCPURequest,
			expectedCPULimit:      "800m",
			expectedMemoryRequest: ctrlutil.DefaultMemoryRequest,
			expectedMemoryLimit:   "768Mi",
		},
		{
			name: "user provides only requests (below defaults) - use requests + default limits",
			podTemplateSpecJSON: `{
				"spec": {
					"containers": [{
						"name": "vmcp",
						"resources": {
							"requests": {
								"cpu": "50m",
								"memory": "64Mi"
							}
						}
					}]
				}
			}`,
			expectedCPURequest:    "50m",
			expectedCPULimit:      ctrlutil.DefaultCPULimit,
			expectedMemoryRequest: "64Mi",
			expectedMemoryLimit:   ctrlutil.DefaultMemoryLimit,
		},
		{
			name: "user provides only requests (above defaults) - use requests for both",
			podTemplateSpecJSON: `{
				"spec": {
					"containers": [{
						"name": "vmcp",
						"resources": {
							"requests": {
								"cpu": "2000m",
								"memory": "2Gi"
							}
						}
					}]
				}
			}`,
			expectedCPURequest:    "2", // 2000m is normalized to 2 cores
			expectedCPULimit:      "2", // 2000m is normalized to 2 cores
			expectedMemoryRequest: "2Gi",
			expectedMemoryLimit:   "2Gi",
		},
		{
			name: "user provides empty resources - use defaults",
			podTemplateSpecJSON: `{
				"spec": {
					"containers": [{
						"name": "vmcp",
						"resources": {}
					}]
				}
			}`,
			expectedCPURequest:    ctrlutil.DefaultCPURequest,
			expectedCPULimit:      ctrlutil.DefaultCPULimit,
			expectedMemoryRequest: ctrlutil.DefaultMemoryRequest,
			expectedMemoryLimit:   ctrlutil.DefaultMemoryLimit,
		},
		{
			name: "user provides vmcp container without resources - use defaults",
			podTemplateSpecJSON: `{
				"spec": {
					"containers": [{
						"name": "vmcp"
					}]
				}
			}`,
			expectedCPURequest:    ctrlutil.DefaultCPURequest,
			expectedCPULimit:      ctrlutil.DefaultCPULimit,
			expectedMemoryRequest: ctrlutil.DefaultMemoryRequest,
			expectedMemoryLimit:   ctrlutil.DefaultMemoryLimit,
		},
		{
			name: "user provides other containers - use defaults for vmcp",
			podTemplateSpecJSON: `{
				"spec": {
					"containers": [{
						"name": "sidecar",
						"resources": {
							"requests": {
								"cpu": "100m",
								"memory": "128Mi"
							}
						}
					}]
				}
			}`,
			expectedCPURequest:    ctrlutil.DefaultCPURequest,
			expectedCPULimit:      ctrlutil.DefaultCPULimit,
			expectedMemoryRequest: ctrlutil.DefaultMemoryRequest,
			expectedMemoryLimit:   ctrlutil.DefaultMemoryLimit,
		},
		{
			name: "user provides limit below default request - use limit for both",
			podTemplateSpecJSON: `{
				"spec": {
					"containers": [{
						"name": "vmcp",
						"resources": {
							"limits": {
								"cpu": "50m",
								"memory": "64Mi"
							}
						}
					}]
				}
			}`,
			expectedCPURequest:    "50m",
			expectedCPULimit:      "50m",
			expectedMemoryRequest: "64Mi",
			expectedMemoryLimit:   "64Mi",
		},
		{
			name: "user provides mixed partial resources - CPU limit and Memory request",
			podTemplateSpecJSON: `{
				"spec": {
					"containers": [{
						"name": "vmcp",
						"resources": {
							"requests": {
								"memory": "256Mi"
							},
							"limits": {
								"cpu": "800m"
							}
						}
					}]
				}
			}`,
			expectedCPURequest:    ctrlutil.DefaultCPURequest,
			expectedCPULimit:      "800m",
			expectedMemoryRequest: "256Mi",
			expectedMemoryLimit:   ctrlutil.DefaultMemoryLimit,
		},
		{
			name: "user provides limit equal to default request - use default request and user limit",
			podTemplateSpecJSON: `{
				"spec": {
					"containers": [{
						"name": "vmcp",
						"resources": {
							"limits": {
								"cpu": "100m",
								"memory": "128Mi"
							}
						}
					}]
				}
			}`,
			expectedCPURequest:    ctrlutil.DefaultCPURequest,
			expectedCPULimit:      "100m",
			expectedMemoryRequest: ctrlutil.DefaultMemoryRequest,
			expectedMemoryLimit:   "128Mi",
		},
		{
			name: "user provides request equal to default limit - use request for both",
			podTemplateSpecJSON: `{
				"spec": {
					"containers": [{
						"name": "vmcp",
						"resources": {
							"requests": {
								"cpu": "500m",
								"memory": "512Mi"
							}
						}
					}]
				}
			}`,
			expectedCPURequest:    "500m",
			expectedCPULimit:      "500m",
			expectedMemoryRequest: "512Mi",
			expectedMemoryLimit:   "512Mi",
		},
		{
			name: "user provides only CPU request above default limit - CPU uses request for both, Memory uses all defaults",
			podTemplateSpecJSON: `{
				"spec": {
					"containers": [{
						"name": "vmcp",
						"resources": {
							"requests": {
								"cpu": "1000m"
							}
						}
					}]
				}
			}`,
			expectedCPURequest:    "1",
			expectedCPULimit:      "1",
			expectedMemoryRequest: ctrlutil.DefaultMemoryRequest,
			expectedMemoryLimit:   ctrlutil.DefaultMemoryLimit,
		},
		{
			name: "user provides only CPU limit below default request - CPU uses limit for both, Memory uses all defaults",
			podTemplateSpecJSON: `{
				"spec": {
					"containers": [{
						"name": "vmcp",
						"resources": {
							"limits": {
								"cpu": "50m"
							}
						}
					}]
				}
			}`,
			expectedCPURequest:    "50m",
			expectedCPULimit:      "50m",
			expectedMemoryRequest: ctrlutil.DefaultMemoryRequest,
			expectedMemoryLimit:   ctrlutil.DefaultMemoryLimit,
		},
		{
			name: "user provides only Memory request above default limit - Memory uses request for both, CPU uses all defaults",
			podTemplateSpecJSON: `{
				"spec": {
					"containers": [{
						"name": "vmcp",
						"resources": {
							"requests": {
								"memory": "1Gi"
							}
						}
					}]
				}
			}`,
			expectedCPURequest:    ctrlutil.DefaultCPURequest,
			expectedCPULimit:      ctrlutil.DefaultCPULimit,
			expectedMemoryRequest: "1Gi",
			expectedMemoryLimit:   "1Gi",
		},
		{
			name: "user provides only Memory limit below default request - Memory uses limit for both, CPU uses all defaults",
			podTemplateSpecJSON: `{
				"spec": {
					"containers": [{
						"name": "vmcp",
						"resources": {
							"limits": {
								"memory": "64Mi"
							}
						}
					}]
				}
			}`,
			expectedCPURequest:    ctrlutil.DefaultCPURequest,
			expectedCPULimit:      ctrlutil.DefaultCPULimit,
			expectedMemoryRequest: "64Mi",
			expectedMemoryLimit:   "64Mi",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			vmcp := &mcpv1alpha1.VirtualMCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-vmcp",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.VirtualMCPServerSpec{
					GroupRef: mcpv1alpha1.GroupRef{
						Name: "test-group",
					},
				},
			}

			if tt.podTemplateSpecJSON != "" {
				vmcp.Spec.PodTemplateSpec = &runtime.RawExtension{
					Raw: []byte(tt.podTemplateSpecJSON),
				}
			}

			r := &VirtualMCPServerReconciler{}
			resources := r.buildMergedResourcesForVmcp(vmcp)

			assert.Equal(t, tt.expectedCPURequest, resources.Requests.Cpu().String())
			assert.Equal(t, tt.expectedCPULimit, resources.Limits.Cpu().String())
			assert.Equal(t, tt.expectedMemoryRequest, resources.Requests.Memory().String())
			assert.Equal(t, tt.expectedMemoryLimit, resources.Limits.Memory().String())
		})
	}
}

// TestDeploymentUsesIntelligentResourceMerging tests that deployments use merged resources
func TestDeploymentUsesIntelligentResourceMerging(t *testing.T) {
	t.Parallel()

	vmcp := &mcpv1alpha1.VirtualMCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-vmcp",
			Namespace: "default",
		},
		Spec: mcpv1alpha1.VirtualMCPServerSpec{
			GroupRef: mcpv1alpha1.GroupRef{
				Name: "test-group",
			},
			PodTemplateSpec: &runtime.RawExtension{
				Raw: []byte(`{
					"spec": {
						"containers": [{
							"name": "vmcp",
							"resources": {
								"requests": {
									"cpu": "200m",
									"memory": "256Mi"
								},
								"limits": {
									"cpu": "1000m",
									"memory": "1Gi"
								}
							}
						}]
					}
				}`),
			},
		},
	}

	scheme := runtime.NewScheme()
	_ = mcpv1alpha1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)

	r := &VirtualMCPServerReconciler{
		Scheme:           scheme,
		PlatformDetector: ctrlutil.NewSharedPlatformDetector(),
	}

	deployment := r.deploymentForVirtualMCPServer(context.Background(), vmcp, "test-checksum", []workloads.TypedWorkload{})

	require.NotNil(t, deployment)
	require.Len(t, deployment.Spec.Template.Spec.Containers, 1)

	container := deployment.Spec.Template.Spec.Containers[0]
	assert.Equal(t, "200m", container.Resources.Requests.Cpu().String())
	assert.Equal(t, "1", container.Resources.Limits.Cpu().String()) // 1000m normalized to 1
	assert.Equal(t, "256Mi", container.Resources.Requests.Memory().String())
	assert.Equal(t, "1Gi", container.Resources.Limits.Memory().String())
}

// TestApplyPodTemplateSpecStripsResources tests that resources are stripped before strategic merge
func TestApplyPodTemplateSpecStripsResources(t *testing.T) {
	t.Parallel()

	vmcp := &mcpv1alpha1.VirtualMCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-vmcp",
			Namespace: "default",
		},
		Spec: mcpv1alpha1.VirtualMCPServerSpec{
			GroupRef: mcpv1alpha1.GroupRef{
				Name: "test-group",
			},
			PodTemplateSpec: &runtime.RawExtension{
				Raw: []byte(`{
					"spec": {
						"containers": [{
							"name": "vmcp",
							"resources": {
								"requests": {
									"cpu": "300m"
								}
							}
						}],
						"nodeSelector": {
							"disk": "ssd"
						}
					}
				}`),
			},
		},
	}

	scheme := runtime.NewScheme()
	_ = mcpv1alpha1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)

	r := &VirtualMCPServerReconciler{
		Scheme:           scheme,
		PlatformDetector: ctrlutil.NewSharedPlatformDetector(),
	}

	deployment := r.deploymentForVirtualMCPServer(context.Background(), vmcp, "test-checksum", []workloads.TypedWorkload{})

	require.NotNil(t, deployment)

	// Verify nodeSelector was applied (from strategic merge)
	assert.Equal(t, "ssd", deployment.Spec.Template.Spec.NodeSelector["disk"])

	// Verify resources use intelligent merging, not raw user values
	// User only provided 300m CPU request, so intelligent merge should add default limit
	container := deployment.Spec.Template.Spec.Containers[0]
	assert.Equal(t, "300m", container.Resources.Requests.Cpu().String())
	assert.Equal(t, ctrlutil.DefaultCPULimit, container.Resources.Limits.Cpu().String())
	// Memory should be all defaults since user didn't specify
	assert.Equal(t, ctrlutil.DefaultMemoryRequest, container.Resources.Requests.Memory().String())
	assert.Equal(t, ctrlutil.DefaultMemoryLimit, container.Resources.Limits.Memory().String())
}

// TestDeploymentWithEmptyJSONPodTemplateSpec tests that defaults are applied when PodTemplateSpec is empty JSON "{}"
func TestDeploymentWithEmptyJSONPodTemplateSpec(t *testing.T) {
	t.Parallel()

	vmcp := &mcpv1alpha1.VirtualMCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-vmcp",
			Namespace: "default",
		},
		Spec: mcpv1alpha1.VirtualMCPServerSpec{
			GroupRef: mcpv1alpha1.GroupRef{
				Name: "test-group",
			},
			// Empty JSON object - valid JSON but no meaningful fields
			PodTemplateSpec: &runtime.RawExtension{
				Raw: []byte(`{}`),
			},
		},
	}

	scheme := runtime.NewScheme()
	_ = mcpv1alpha1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)

	r := &VirtualMCPServerReconciler{
		Scheme:           scheme,
		PlatformDetector: ctrlutil.NewSharedPlatformDetector(),
	}

	deployment := r.deploymentForVirtualMCPServer(context.Background(), vmcp, "test-checksum", []workloads.TypedWorkload{})

	require.NotNil(t, deployment)
	require.Len(t, deployment.Spec.Template.Spec.Containers, 1)

	container := deployment.Spec.Template.Spec.Containers[0]
	assert.Equal(t, ctrlutil.DefaultCPURequest, container.Resources.Requests.Cpu().String(),
		"CPU request should be default when PodTemplateSpec is empty JSON")
	assert.Equal(t, ctrlutil.DefaultCPULimit, container.Resources.Limits.Cpu().String(),
		"CPU limit should be default when PodTemplateSpec is empty JSON")
	assert.Equal(t, ctrlutil.DefaultMemoryRequest, container.Resources.Requests.Memory().String(),
		"Memory request should be default when PodTemplateSpec is empty JSON")
	assert.Equal(t, ctrlutil.DefaultMemoryLimit, container.Resources.Limits.Memory().String(),
		"Memory limit should be default when PodTemplateSpec is empty JSON")
}

// TestDeploymentWithEmptyRawExtension tests that defaults are applied when RawExtension has empty Raw
func TestDeploymentWithEmptyRawExtension(t *testing.T) {
	t.Parallel()

	vmcp := &mcpv1alpha1.VirtualMCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-vmcp",
			Namespace: "default",
		},
		Spec: mcpv1alpha1.VirtualMCPServerSpec{
			GroupRef: mcpv1alpha1.GroupRef{
				Name: "test-group",
			},
			// RawExtension exists but Raw is empty slice
			PodTemplateSpec: &runtime.RawExtension{
				Raw: []byte{},
			},
		},
	}

	scheme := runtime.NewScheme()
	_ = mcpv1alpha1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)

	r := &VirtualMCPServerReconciler{
		Scheme:           scheme,
		PlatformDetector: ctrlutil.NewSharedPlatformDetector(),
	}

	deployment := r.deploymentForVirtualMCPServer(context.Background(), vmcp, "test-checksum", []workloads.TypedWorkload{})

	require.NotNil(t, deployment)
	require.Len(t, deployment.Spec.Template.Spec.Containers, 1)

	container := deployment.Spec.Template.Spec.Containers[0]
	assert.Equal(t, ctrlutil.DefaultCPURequest, container.Resources.Requests.Cpu().String(),
		"CPU request should be default when Raw is empty slice")
	assert.Equal(t, ctrlutil.DefaultCPULimit, container.Resources.Limits.Cpu().String(),
		"CPU limit should be default when Raw is empty slice")
	assert.Equal(t, ctrlutil.DefaultMemoryRequest, container.Resources.Requests.Memory().String(),
		"Memory request should be default when Raw is empty slice")
	assert.Equal(t, ctrlutil.DefaultMemoryLimit, container.Resources.Limits.Memory().String(),
		"Memory limit should be default when Raw is empty slice")
}

// TestDeploymentWithEmptySpecFields tests that defaults are applied when PodTemplateSpec
// has structure but all fields are empty (triggers builder.Build() == nil)
func TestDeploymentWithEmptySpecFields(t *testing.T) {
	t.Parallel()

	vmcp := &mcpv1alpha1.VirtualMCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-vmcp",
			Namespace: "default",
		},
		Spec: mcpv1alpha1.VirtualMCPServerSpec{
			GroupRef: mcpv1alpha1.GroupRef{
				Name: "test-group",
			},
			// Valid JSON structure with empty spec - triggers builder.Build() == nil
			PodTemplateSpec: &runtime.RawExtension{
				Raw: []byte(`{"spec": {}}`),
			},
		},
	}

	scheme := runtime.NewScheme()
	_ = mcpv1alpha1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)

	r := &VirtualMCPServerReconciler{
		Scheme:           scheme,
		PlatformDetector: ctrlutil.NewSharedPlatformDetector(),
	}

	deployment := r.deploymentForVirtualMCPServer(context.Background(), vmcp, "test-checksum", []workloads.TypedWorkload{})

	require.NotNil(t, deployment)
	require.Len(t, deployment.Spec.Template.Spec.Containers, 1)

	container := deployment.Spec.Template.Spec.Containers[0]
	assert.Equal(t, ctrlutil.DefaultCPURequest, container.Resources.Requests.Cpu().String(),
		"CPU request should be default when spec fields are empty")
	assert.Equal(t, ctrlutil.DefaultCPULimit, container.Resources.Limits.Cpu().String(),
		"CPU limit should be default when spec fields are empty")
	assert.Equal(t, ctrlutil.DefaultMemoryRequest, container.Resources.Requests.Memory().String(),
		"Memory request should be default when spec fields are empty")
	assert.Equal(t, ctrlutil.DefaultMemoryLimit, container.Resources.Limits.Memory().String(),
		"Memory limit should be default when spec fields are empty")
}

// TestContainerNeedsUpdateDetectsResourceChanges tests that existing deployments without resources
// will trigger an update when the operator is upgraded to include default resources
func TestContainerNeedsUpdateDetectsResourceChanges(t *testing.T) {
	t.Parallel()

	vmcp := &mcpv1alpha1.VirtualMCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-vmcp",
			Namespace: "default",
		},
		Spec: mcpv1alpha1.VirtualMCPServerSpec{
			GroupRef: mcpv1alpha1.GroupRef{
				Name: "test-group",
			},
			// No PodTemplateSpec - simulates pre-upgrade deployment
		},
	}

	scheme := runtime.NewScheme()
	_ = mcpv1alpha1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)

	r := &VirtualMCPServerReconciler{
		Scheme:           scheme,
		PlatformDetector: ctrlutil.NewSharedPlatformDetector(),
	}

	// Simulate a pre-upgrade deployment with no resources set
	deploymentWithoutResources := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      vmcp.Name,
			Namespace: vmcp.Namespace,
		},
		Spec: appsv1.DeploymentSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					ServiceAccountName: vmcpServiceAccountName(vmcp.Name),
					Containers: []corev1.Container{{
						Name:  "vmcp",
						Image: getVmcpImage(),
						Ports: []corev1.ContainerPort{{ContainerPort: vmcpDefaultPort}},
						Env:   r.buildEnvVarsForVmcp(context.Background(), vmcp, []workloads.TypedWorkload{}),
						// No Resources set - simulates pre-upgrade state
					}},
				},
			},
		},
	}

	// containerNeedsUpdate should return true because resources are missing
	needsUpdate := r.containerNeedsUpdate(context.Background(), deploymentWithoutResources, vmcp, []workloads.TypedWorkload{})
	assert.True(t, needsUpdate, "containerNeedsUpdate should detect missing resources")
}

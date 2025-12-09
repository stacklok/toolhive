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
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	ctrlutil "github.com/stacklok/toolhive/cmd/thv-operator/pkg/controllerutil"
	"github.com/stacklok/toolhive/cmd/thv-operator/pkg/runconfig/configmap/checksum"
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

	deployment := r.deploymentForVirtualMCPServer(context.Background(), vmcp, "test-checksum", []string{})

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
	env := r.buildEnvVarsForVmcp(context.Background(), vmcp, []string{})

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
	assert.True(t, r.deploymentNeedsUpdate(context.Background(), nil, nil, "", []string{}))

	vmcp := &mcpv1alpha1.VirtualMCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-vmcp",
			Namespace: "default",
		},
	}

	// Test with nil deployment
	assert.True(t, r.deploymentNeedsUpdate(context.Background(), nil, vmcp, "checksum", []string{}))
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

package registryapi

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
)

func TestBuildRegistryAPIService(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		mcpRegistry    *mcpv1alpha1.MCPRegistry
		validateResult func(*testing.T, *corev1.Service)
	}{
		{
			name: "basic service creation",
			mcpRegistry: &mcpv1alpha1.MCPRegistry{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-registry",
					Namespace: "test-namespace",
				},
			},
			validateResult: func(t *testing.T, service *corev1.Service) {
				t.Helper()
				require.NotNil(t, service)

				// Verify basic metadata
				assert.Equal(t, "test-registry-api", service.Name)
				assert.Equal(t, "test-namespace", service.Namespace)

				// Verify labels
				expectedLabels := map[string]string{
					"app.kubernetes.io/name":             "test-registry-api",
					"app.kubernetes.io/component":        "registry-api",
					"app.kubernetes.io/managed-by":       "toolhive-operator",
					"toolhive.stacklok.io/registry-name": "test-registry",
				}
				assert.Equal(t, expectedLabels, service.Labels)

				// Verify service type
				assert.Equal(t, corev1.ServiceTypeClusterIP, service.Spec.Type)

				// Verify selector
				expectedSelector := map[string]string{
					"app.kubernetes.io/name":      "test-registry-api",
					"app.kubernetes.io/component": "registry-api",
				}
				assert.Equal(t, expectedSelector, service.Spec.Selector)

				// Verify ports
				require.Len(t, service.Spec.Ports, 1)
				port := service.Spec.Ports[0]
				assert.Equal(t, RegistryAPIPortName, port.Name)
				assert.Equal(t, int32(RegistryAPIPort), port.Port)
				assert.Equal(t, intstr.FromInt32(RegistryAPIPort), port.TargetPort)
				assert.Equal(t, corev1.ProtocolTCP, port.Protocol)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			service := buildRegistryAPIService(tt.mcpRegistry)

			if tt.validateResult != nil {
				tt.validateResult(t, service)
			}
		})
	}
}

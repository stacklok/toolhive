package accessors

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
)

func TestNewMCPServerFieldAccessor(t *testing.T) {
	accessor := NewMCPServerFieldAccessor()
	require.NotNil(t, accessor)
	_, ok := accessor.(*mcpServerFieldAccessor)
	assert.True(t, ok, "NewMCPServerFieldAccessor should return *mcpServerFieldAccessor")
}

func TestGetProxyDeploymentLabelsAndAnnotations(t *testing.T) {
	tests := []struct {
		name                string
		mcpServer           *mcpv1alpha1.MCPServer
		expectedLabels      map[string]string
		expectedAnnotations map[string]string
	}{
		{
			name: "nil resource overrides",
			mcpServer: &mcpv1alpha1.MCPServer{
				Spec: mcpv1alpha1.MCPServerSpec{
					ResourceOverrides: nil,
				},
			},
			expectedLabels:      map[string]string{},
			expectedAnnotations: map[string]string{},
		},
		{
			name: "nil proxy deployment overrides",
			mcpServer: &mcpv1alpha1.MCPServer{
				Spec: mcpv1alpha1.MCPServerSpec{
					ResourceOverrides: &mcpv1alpha1.ResourceOverrides{
						ProxyDeployment: nil,
					},
				},
			},
			expectedLabels:      map[string]string{},
			expectedAnnotations: map[string]string{},
		},
		{
			name: "with labels only",
			mcpServer: &mcpv1alpha1.MCPServer{
				Spec: mcpv1alpha1.MCPServerSpec{
					ResourceOverrides: &mcpv1alpha1.ResourceOverrides{
						ProxyDeployment: &mcpv1alpha1.ProxyDeploymentOverrides{
							ResourceMetadataOverrides: mcpv1alpha1.ResourceMetadataOverrides{
								Labels: map[string]string{
									"app":     "my-app",
									"version": "v1",
								},
							},
						},
					},
				},
			},
			expectedLabels: map[string]string{
				"app":     "my-app",
				"version": "v1",
			},
			expectedAnnotations: map[string]string{},
		},
		{
			name: "with annotations only",
			mcpServer: &mcpv1alpha1.MCPServer{
				Spec: mcpv1alpha1.MCPServerSpec{
					ResourceOverrides: &mcpv1alpha1.ResourceOverrides{
						ProxyDeployment: &mcpv1alpha1.ProxyDeploymentOverrides{
							ResourceMetadataOverrides: mcpv1alpha1.ResourceMetadataOverrides{
								Annotations: map[string]string{
									"prometheus.io/scrape": "true",
									"prometheus.io/port":   "9090",
								},
							},
						},
					},
				},
			},
			expectedLabels: map[string]string{},
			expectedAnnotations: map[string]string{
				"prometheus.io/scrape": "true",
				"prometheus.io/port":   "9090",
			},
		},
		{
			name: "with both labels and annotations",
			mcpServer: &mcpv1alpha1.MCPServer{
				Spec: mcpv1alpha1.MCPServerSpec{
					ResourceOverrides: &mcpv1alpha1.ResourceOverrides{
						ProxyDeployment: &mcpv1alpha1.ProxyDeploymentOverrides{
							ResourceMetadataOverrides: mcpv1alpha1.ResourceMetadataOverrides{
								Labels: map[string]string{
									"env":                    "production",
									"team":                   "platform",
									"app.kubernetes.io/name": "toolhive",
								},
								Annotations: map[string]string{
									"description":             "MCP Server Proxy",
									"owner":                   "platform-team",
									"sidecar.istio.io/inject": "false",
								},
							},
						},
					},
				},
			},
			expectedLabels: map[string]string{
				"env":                    "production",
				"team":                   "platform",
				"app.kubernetes.io/name": "toolhive",
			},
			expectedAnnotations: map[string]string{
				"description":             "MCP Server Proxy",
				"owner":                   "platform-team",
				"sidecar.istio.io/inject": "false",
			},
		},
		{
			name: "nil labels and annotations maps",
			mcpServer: &mcpv1alpha1.MCPServer{
				Spec: mcpv1alpha1.MCPServerSpec{
					ResourceOverrides: &mcpv1alpha1.ResourceOverrides{
						ProxyDeployment: &mcpv1alpha1.ProxyDeploymentOverrides{
							ResourceMetadataOverrides: mcpv1alpha1.ResourceMetadataOverrides{
								Labels:      nil,
								Annotations: nil,
							},
						},
					},
				},
			},
			expectedLabels:      map[string]string{},
			expectedAnnotations: map[string]string{},
		},
		{
			name: "empty labels and annotations maps",
			mcpServer: &mcpv1alpha1.MCPServer{
				Spec: mcpv1alpha1.MCPServerSpec{
					ResourceOverrides: &mcpv1alpha1.ResourceOverrides{
						ProxyDeployment: &mcpv1alpha1.ProxyDeploymentOverrides{
							ResourceMetadataOverrides: mcpv1alpha1.ResourceMetadataOverrides{
								Labels:      map[string]string{},
								Annotations: map[string]string{},
							},
						},
					},
				},
			},
			expectedLabels:      map[string]string{},
			expectedAnnotations: map[string]string{},
		},
	}

	accessor := NewMCPServerFieldAccessor()

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			labels, annotations := accessor.GetProxyDeploymentLabelsAndAnnotations(tt.mcpServer)
			assert.Equal(t, tt.expectedLabels, labels)
			assert.Equal(t, tt.expectedAnnotations, annotations)
		})
	}
}

func TestGetProxyDeploymentTemplateLabelsAndAnnotations(t *testing.T) {
	tests := []struct {
		name                string
		mcpServer           *mcpv1alpha1.MCPServer
		expectedLabels      map[string]string
		expectedAnnotations map[string]string
	}{
		{
			name: "nil resource overrides",
			mcpServer: &mcpv1alpha1.MCPServer{
				Spec: mcpv1alpha1.MCPServerSpec{
					ResourceOverrides: nil,
				},
			},
			expectedLabels:      map[string]string{},
			expectedAnnotations: map[string]string{},
		},
		{
			name: "nil proxy deployment overrides",
			mcpServer: &mcpv1alpha1.MCPServer{
				Spec: mcpv1alpha1.MCPServerSpec{
					ResourceOverrides: &mcpv1alpha1.ResourceOverrides{
						ProxyDeployment: nil,
					},
				},
			},
			expectedLabels:      map[string]string{},
			expectedAnnotations: map[string]string{},
		},
		{
			name: "nil pod template metadata overrides",
			mcpServer: &mcpv1alpha1.MCPServer{
				Spec: mcpv1alpha1.MCPServerSpec{
					ResourceOverrides: &mcpv1alpha1.ResourceOverrides{
						ProxyDeployment: &mcpv1alpha1.ProxyDeploymentOverrides{
							PodTemplateMetadataOverrides: nil,
						},
					},
				},
			},
			expectedLabels:      map[string]string{},
			expectedAnnotations: map[string]string{},
		},
		{
			name: "with pod template labels only",
			mcpServer: &mcpv1alpha1.MCPServer{
				Spec: mcpv1alpha1.MCPServerSpec{
					ResourceOverrides: &mcpv1alpha1.ResourceOverrides{
						ProxyDeployment: &mcpv1alpha1.ProxyDeploymentOverrides{
							PodTemplateMetadataOverrides: &mcpv1alpha1.ResourceMetadataOverrides{
								Labels: map[string]string{
									"pod-label-1": "value1",
									"pod-label-2": "value2",
								},
							},
						},
					},
				},
			},
			expectedLabels: map[string]string{
				"pod-label-1": "value1",
				"pod-label-2": "value2",
			},
			expectedAnnotations: map[string]string{},
		},
		{
			name: "with pod template annotations only",
			mcpServer: &mcpv1alpha1.MCPServer{
				Spec: mcpv1alpha1.MCPServerSpec{
					ResourceOverrides: &mcpv1alpha1.ResourceOverrides{
						ProxyDeployment: &mcpv1alpha1.ProxyDeploymentOverrides{
							PodTemplateMetadataOverrides: &mcpv1alpha1.ResourceMetadataOverrides{
								Annotations: map[string]string{
									"pod-annotation-1": "value1",
									"pod-annotation-2": "value2",
								},
							},
						},
					},
				},
			},
			expectedLabels: map[string]string{},
			expectedAnnotations: map[string]string{
				"pod-annotation-1": "value1",
				"pod-annotation-2": "value2",
			},
		},
		{
			name: "with both pod template labels and annotations",
			mcpServer: &mcpv1alpha1.MCPServer{
				Spec: mcpv1alpha1.MCPServerSpec{
					ResourceOverrides: &mcpv1alpha1.ResourceOverrides{
						ProxyDeployment: &mcpv1alpha1.ProxyDeploymentOverrides{
							PodTemplateMetadataOverrides: &mcpv1alpha1.ResourceMetadataOverrides{
								Labels: map[string]string{
									"app.kubernetes.io/component": "proxy",
									"app.kubernetes.io/instance":  "server-1",
								},
								Annotations: map[string]string{
									"co.elastic.logs/enabled": "true",
									"fluentbit.io/parser":     "json",
								},
							},
						},
					},
				},
			},
			expectedLabels: map[string]string{
				"app.kubernetes.io/component": "proxy",
				"app.kubernetes.io/instance":  "server-1",
			},
			expectedAnnotations: map[string]string{
				"co.elastic.logs/enabled": "true",
				"fluentbit.io/parser":     "json",
			},
		},
		{
			name: "deployment overrides don't affect pod template",
			mcpServer: &mcpv1alpha1.MCPServer{
				Spec: mcpv1alpha1.MCPServerSpec{
					ResourceOverrides: &mcpv1alpha1.ResourceOverrides{
						ProxyDeployment: &mcpv1alpha1.ProxyDeploymentOverrides{
							ResourceMetadataOverrides: mcpv1alpha1.ResourceMetadataOverrides{
								Labels: map[string]string{
									"deployment-label": "should-not-appear",
								},
								Annotations: map[string]string{
									"deployment-annotation": "should-not-appear",
								},
							},
							PodTemplateMetadataOverrides: &mcpv1alpha1.ResourceMetadataOverrides{
								Labels: map[string]string{
									"pod-label": "should-appear",
								},
								Annotations: map[string]string{
									"pod-annotation": "should-appear",
								},
							},
						},
					},
				},
			},
			expectedLabels: map[string]string{
				"pod-label": "should-appear",
			},
			expectedAnnotations: map[string]string{
				"pod-annotation": "should-appear",
			},
		},
	}

	accessor := NewMCPServerFieldAccessor()

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			labels, annotations := accessor.GetProxyDeploymentTemplateLabelsAndAnnotations(tt.mcpServer)
			assert.Equal(t, tt.expectedLabels, labels)
			assert.Equal(t, tt.expectedAnnotations, annotations)
		})
	}
}

func TestInterfaceContract(t *testing.T) {
	// Test that the concrete type implements the interface
	var _ MCPServerFieldAccessor = (*mcpServerFieldAccessor)(nil)
	var _ MCPServerFieldAccessor = NewMCPServerFieldAccessor()
}

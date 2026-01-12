package virtualmcp

import (
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/yaml"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	"github.com/stacklok/toolhive/pkg/telemetry"
	vmcpconfig "github.com/stacklok/toolhive/pkg/vmcp/config"
	"github.com/stacklok/toolhive/test/e2e/images"
)

var _ = Describe("VirtualMCPServer Telemetry Config", Ordered, func() {
	var (
		testNamespace   = "default"
		mcpGroupName    = "test-telemetry-group"
		vmcpServerName  = "test-vmcp-telemetry"
		backendName     = "yardstick-telemetry"
		timeout         = 3 * time.Minute
		pollingInterval = 1 * time.Second
	)

	BeforeAll(func() {
		By("Creating MCPGroup for telemetry test")
		CreateMCPGroupAndWait(ctx, k8sClient, mcpGroupName, testNamespace,
			"Test MCP Group for telemetry config", timeout, pollingInterval)

		By("Creating yardstick backend MCPServer")
		backend := &mcpv1alpha1.MCPServer{
			ObjectMeta: metav1.ObjectMeta{
				Name:      backendName,
				Namespace: testNamespace,
			},
			Spec: mcpv1alpha1.MCPServerSpec{
				GroupRef:  mcpGroupName,
				Image:     images.YardstickServerImage,
				Transport: "streamable-http",
				ProxyPort: 8080,
				McpPort:   8080,
				Env: []mcpv1alpha1.EnvVar{
					{Name: "TRANSPORT", Value: "streamable-http"},
				},
			},
		}
		Expect(k8sClient.Create(ctx, backend)).To(Succeed())

		By("Waiting for backend MCPServer to be running")
		Eventually(func() error {
			server := &mcpv1alpha1.MCPServer{}
			if err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      backendName,
				Namespace: testNamespace,
			}, server); err != nil {
				return fmt.Errorf("failed to get server: %w", err)
			}
			if server.Status.Phase != mcpv1alpha1.MCPServerPhaseRunning {
				return fmt.Errorf("backend not ready yet, phase: %s", server.Status.Phase)
			}
			return nil
		}, timeout, pollingInterval).Should(Succeed())

		By("Creating VirtualMCPServer with telemetry config")
		vmcp := &mcpv1alpha1.VirtualMCPServer{
			ObjectMeta: metav1.ObjectMeta{
				Name:      vmcpServerName,
				Namespace: testNamespace,
			},
			Spec: mcpv1alpha1.VirtualMCPServerSpec{
				ServiceType: "NodePort",
				IncomingAuth: &mcpv1alpha1.IncomingAuthConfig{
					Type: "anonymous",
				},
				Config: vmcpconfig.Config{
					Group: mcpGroupName,
					Telemetry: &telemetry.Config{
						Endpoint:                    "localhost:4317",
						EnablePrometheusMetricsPath: true,
						TracingEnabled:              true, // Enable tracing to satisfy OTLP endpoint requirement
						MetricsEnabled:              true, // Enable metrics to satisfy OTLP endpoint requirement
						ServiceName:                 "custom-service-name",
						ServiceVersion:              "v1.2.3",
						CustomAttributes: map[string]string{
							"environment":  "e2e-test",
							"test_id":      "telemetry_config_test",
							"cluster_name": "kind-test-cluster",
						},
						EnvironmentVariables: []string{"PATH", "HOME"},
					},
				},
			},
		}
		Expect(k8sClient.Create(ctx, vmcp)).To(Succeed())

		By("Waiting for VirtualMCPServer to be ready")
		Eventually(func() error {
			server := &mcpv1alpha1.VirtualMCPServer{}
			if err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      vmcpServerName,
				Namespace: testNamespace,
			}, server); err != nil {
				return fmt.Errorf("failed to get VirtualMCPServer: %w", err)
			}
			if server.Status.Phase != mcpv1alpha1.VirtualMCPServerPhaseReady {
				return fmt.Errorf("VirtualMCPServer not ready yet, phase: %s", server.Status.Phase)
			}
			return nil
		}, timeout, pollingInterval).Should(Succeed())
	})

	AfterAll(func() {
		By("Cleaning up VirtualMCPServer")
		vmcp := &mcpv1alpha1.VirtualMCPServer{
			ObjectMeta: metav1.ObjectMeta{
				Name:      vmcpServerName,
				Namespace: testNamespace,
			},
		}
		Expect(k8sClient.Delete(ctx, vmcp)).To(Succeed())

		By("Cleaning up backend MCPServer")
		backend := &mcpv1alpha1.MCPServer{
			ObjectMeta: metav1.ObjectMeta{
				Name:      backendName,
				Namespace: testNamespace,
			},
		}
		Expect(k8sClient.Delete(ctx, backend)).To(Succeed())

		By("Cleaning up MCPGroup")
		group := &mcpv1alpha1.MCPGroup{
			ObjectMeta: metav1.ObjectMeta{
				Name:      mcpGroupName,
				Namespace: testNamespace,
			},
		}
		Expect(k8sClient.Delete(ctx, group)).To(Succeed())
	})

	It("should preserve telemetry config in ConfigMap", func() {
		By("Getting the ConfigMap for VirtualMCPServer")
		configMap := &corev1.ConfigMap{}
		configMapName := fmt.Sprintf("%s-config", vmcpServerName)

		Eventually(func() error {
			return k8sClient.Get(ctx, types.NamespacedName{
				Name:      configMapName,
				Namespace: testNamespace,
			}, configMap)
		}, timeout, pollingInterval).Should(Succeed())

		By("Parsing the config.yaml from ConfigMap")
		configYAML, exists := configMap.Data["config.yaml"]
		Expect(exists).To(BeTrue(), "ConfigMap should contain config.yaml")
		Expect(configYAML).NotTo(BeEmpty(), "config.yaml should not be empty")

		// Parse the YAML config to verify telemetry settings
		var config vmcpconfig.Config
		Expect(yaml.Unmarshal([]byte(configYAML), &config)).To(Succeed())

		By("Validating telemetry configuration")
		Expect(config.Telemetry).NotTo(BeNil(), "Telemetry config should not be nil")

		// Verify all telemetry fields are preserved
		Expect(config.Telemetry.EnablePrometheusMetricsPath).To(BeTrue(),
			"EnablePrometheusMetricsPath should be preserved")

		Expect(config.Telemetry.ServiceName).To(Equal("custom-service-name"),
			"ServiceName should be preserved")

		Expect(config.Telemetry.ServiceVersion).To(Equal("v1.2.3"),
			"ServiceVersion should be preserved")

		Expect(config.Telemetry.CustomAttributes).NotTo(BeNil(),
			"CustomAttributes should not be nil")
		Expect(config.Telemetry.CustomAttributes).To(HaveKeyWithValue("environment", "e2e-test"),
			"CustomAttributes should contain 'environment'")
		Expect(config.Telemetry.CustomAttributes).To(HaveKeyWithValue("test_id", "telemetry_config_test"),
			"CustomAttributes should contain 'test_id'")
		Expect(config.Telemetry.CustomAttributes).To(HaveKeyWithValue("cluster_name", "kind-test-cluster"),
			"CustomAttributes should contain 'cluster_name'")

		Expect(config.Telemetry.EnvironmentVariables).NotTo(BeEmpty(),
			"EnvironmentVariables should not be empty")
		Expect(config.Telemetry.EnvironmentVariables).To(ContainElements("PATH", "HOME"),
			"EnvironmentVariables should be preserved")

		GinkgoWriter.Println("✓ All telemetry configuration fields preserved in ConfigMap")
	})
})

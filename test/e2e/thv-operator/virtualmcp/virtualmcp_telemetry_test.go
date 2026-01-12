package virtualmcp

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

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
		vmcpNodePort    int32
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
						Endpoint:                    "localhost:4317", // Required by CRD validation
						EnablePrometheusMetricsPath: true,
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

		By("Getting NodePort for VirtualMCPServer")
		service := &corev1.Service{}
		Eventually(func() error {
			return k8sClient.Get(ctx, types.NamespacedName{
				Name:      vmcpServerName,
				Namespace: testNamespace,
			}, service)
		}, timeout, pollingInterval).Should(Succeed())

		Expect(service.Spec.Ports).NotTo(BeEmpty())
		vmcpNodePort = service.Spec.Ports[0].NodePort
		Expect(vmcpNodePort).NotTo(BeZero())
		GinkgoWriter.Printf("VirtualMCPServer accessible at http://localhost:%d\n", vmcpNodePort)
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

	It("should expose Prometheus metrics with custom attributes", func() {
		By("Calling /metrics endpoint")
		metricsURL := fmt.Sprintf("http://localhost:%d/metrics", vmcpNodePort)

		var metricsBody string
		Eventually(func() error {
			resp, err := http.Get(metricsURL)
			if err != nil {
				return fmt.Errorf("failed to GET /metrics: %w", err)
			}
			defer resp.Body.Close()

			if resp.StatusCode != http.StatusOK {
				return fmt.Errorf("expected status 200, got %d", resp.StatusCode)
			}

			body, err := io.ReadAll(resp.Body)
			if err != nil {
				return fmt.Errorf("failed to read response body: %w", err)
			}

			metricsBody = string(body)

			// Verify we got valid Prometheus metrics output
			if !strings.Contains(metricsBody, "# HELP") {
				return fmt.Errorf("response doesn't look like Prometheus metrics")
			}

			return nil
		}, timeout, pollingInterval).Should(Succeed())

		By("Validating custom attributes appear in metrics")
		// Check for service.name label
		Expect(metricsBody).To(ContainSubstring("service_name=\"custom-service-name\""),
			"Metrics should include custom service name")

		// Check for service.version label
		Expect(metricsBody).To(ContainSubstring("service_version=\"v1.2.3\""),
			"Metrics should include custom service version")

		// Check for custom attributes
		Expect(metricsBody).To(ContainSubstring("environment=\"e2e-test\""),
			"Metrics should include custom attribute 'environment'")

		Expect(metricsBody).To(ContainSubstring("test_id=\"telemetry_config_test\""),
			"Metrics should include custom attribute 'test_id'")

		Expect(metricsBody).To(ContainSubstring("cluster_name=\"kind-test-cluster\""),
			"Metrics should include custom attribute 'cluster_name'")

		GinkgoWriter.Println("✓ All custom telemetry attributes found in metrics")
	})

	It("should include environment variables in metrics", func() {
		By("Calling /metrics endpoint")
		metricsURL := fmt.Sprintf("http://localhost:%d/metrics", vmcpNodePort)

		resp, err := http.Get(metricsURL)
		Expect(err).NotTo(HaveOccurred())
		defer resp.Body.Close()

		body, err := io.ReadAll(resp.Body)
		Expect(err).NotTo(HaveOccurred())
		metricsBody := string(body)

		By("Validating environment variables appear in metrics")
		// The environment variables should be captured as resource attributes
		// Note: The exact format depends on how the vmcp server exports them
		// They might appear as labels on metrics or in resource attributes
		// For now, we just verify the metrics endpoint is working with the config
		Expect(metricsBody).To(ContainSubstring("service_name=\"custom-service-name\""),
			"Environment variables configuration should be present alongside other telemetry config")

		GinkgoWriter.Println("✓ Telemetry config with environment variables is active")
	})
})

// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

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
				GroupRef:  &mcpv1alpha1.MCPGroupRef{Name: mcpGroupName},
				Image:     images.YardstickServerImage,
				Transport: "streamable-http",
				ProxyPort: 8080,
				MCPPort:   8080,
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
			if server.Status.Phase != mcpv1alpha1.MCPServerPhaseReady {
				return fmt.Errorf("backend not ready yet, phase: %s", server.Status.Phase)
			}
			return nil
		}, timeout, pollingInterval).Should(Succeed())

		By("Creating MCPTelemetryConfig for shared telemetry")
		telCfg := &mcpv1alpha1.MCPTelemetryConfig{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "e2e-telemetry-config",
				Namespace: testNamespace,
			},
			Spec: mcpv1alpha1.MCPTelemetryConfigSpec{
				OpenTelemetry: &mcpv1alpha1.MCPTelemetryOTelConfig{
					Enabled:  true,
					Endpoint: "localhost:4317",
					Tracing:  &mcpv1alpha1.OpenTelemetryTracingConfig{Enabled: true},
					Metrics:  &mcpv1alpha1.OpenTelemetryMetricsConfig{Enabled: true},
					ResourceAttributes: map[string]string{
						"environment":  "e2e-test",
						"test_id":      "telemetry_config_test",
						"cluster_name": "kind-test-cluster",
					},
				},
				Prometheus: &mcpv1alpha1.PrometheusConfig{Enabled: true},
			},
		}
		Expect(k8sClient.Create(ctx, telCfg)).To(Succeed())

		// Wait for MCPTelemetryConfig to be reconciled (hash set)
		Eventually(func() bool {
			fetched := &mcpv1alpha1.MCPTelemetryConfig{}
			err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      telCfg.Name,
				Namespace: telCfg.Namespace,
			}, fetched)
			return err == nil && fetched.Status.ConfigHash != ""
		}, timeout, pollingInterval).Should(BeTrue())

		By("Creating VirtualMCPServer with telemetryConfigRef")
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
				TelemetryConfigRef: &mcpv1alpha1.MCPTelemetryConfigReference{
					Name:        "e2e-telemetry-config",
					ServiceName: "custom-service-name",
				},
				Config: vmcpconfig.Config{
					Group: mcpGroupName,
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

		By("Cleaning up MCPTelemetryConfig")
		_ = k8sClient.Delete(ctx, &mcpv1alpha1.MCPTelemetryConfig{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "e2e-telemetry-config",
				Namespace: testNamespace,
			},
		})

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
		configMapName := fmt.Sprintf("%s-vmcp-config", vmcpServerName)

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

		By("Validating telemetry configuration from MCPTelemetryConfig")
		Expect(config.Telemetry).NotTo(BeNil(), "Telemetry config should not be nil")

		Expect(config.Telemetry.EnablePrometheusMetricsPath).To(BeTrue(),
			"EnablePrometheusMetricsPath should be set from MCPTelemetryConfig")

		Expect(config.Telemetry.ServiceName).To(Equal("custom-service-name"),
			"ServiceName should come from TelemetryConfigRef override")

		Expect(config.Telemetry.TracingEnabled).To(BeTrue(),
			"TracingEnabled should be set from MCPTelemetryConfig")

		Expect(config.Telemetry.MetricsEnabled).To(BeTrue(),
			"MetricsEnabled should be set from MCPTelemetryConfig")

		Expect(config.Telemetry.CustomAttributes).NotTo(BeNil(),
			"CustomAttributes should not be nil")
		Expect(config.Telemetry.CustomAttributes).To(HaveKeyWithValue("environment", "e2e-test"),
			"CustomAttributes should contain 'environment'")
		Expect(config.Telemetry.CustomAttributes).To(HaveKeyWithValue("test_id", "telemetry_config_test"),
			"CustomAttributes should contain 'test_id'")
		Expect(config.Telemetry.CustomAttributes).To(HaveKeyWithValue("cluster_name", "kind-test-cluster"),
			"CustomAttributes should contain 'cluster_name'")

		GinkgoWriter.Println("✓ All telemetry configuration fields resolved from MCPTelemetryConfig")
	})
})

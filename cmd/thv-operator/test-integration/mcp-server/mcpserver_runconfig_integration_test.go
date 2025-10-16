// Package controllers contains integration tests for the RunConfig ConfigMap management
package controllers

import (
	"encoding/json"
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	"github.com/stacklok/toolhive/cmd/thv-operator/pkg/runconfig/configmap/checksum"
	"github.com/stacklok/toolhive/pkg/authz"
	"github.com/stacklok/toolhive/pkg/runner"
	transporttypes "github.com/stacklok/toolhive/pkg/transport/types"
)

var _ = Describe("RunConfig ConfigMap Integration Tests", func() {
	const (
		timeout  = time.Second * 30
		interval = time.Millisecond * 250
	)

	Context("When creating an MCPServer with RunConfig ConfigMap", Ordered, func() {
		var (
			namespace        string
			mcpServerName    string
			mcpServer        *mcpv1alpha1.MCPServer
			createdMCPServer *mcpv1alpha1.MCPServer
			configMapName    string
		)

		BeforeAll(func() {
			namespace = "runconfig-test-ns"
			mcpServerName = "test-runconfig-server"
			configMapName = mcpServerName + "-runconfig"

			// Create namespace if it doesn't exist
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: namespace,
				},
			}
			_ = k8sClient.Create(ctx, ns)

			// Define the MCPServer resource with comprehensive configuration
			mcpServer = &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      mcpServerName,
					Namespace: namespace,
				},
				Spec: mcpv1alpha1.MCPServerSpec{
					Image:       "example/mcp-server:v1.0.0",
					Transport:   "stdio",
					ProxyMode:   "sse",
					Port:        8080,
					TargetPort:  8081,
					Args:        []string{"--verbose", "--debug"},
					ToolsFilter: []string{"tool1", "tool2"},
					Env: []mcpv1alpha1.EnvVar{
						{
							Name:  "DEBUG",
							Value: "true",
						},
						{
							Name:  "LOG_LEVEL",
							Value: "debug",
						},
					},
					Volumes: []mcpv1alpha1.Volume{
						{
							Name:      "config",
							HostPath:  "/host/config",
							MountPath: "/app/config",
							ReadOnly:  true,
						},
					},
					Resources: mcpv1alpha1.ResourceRequirements{
						Limits: mcpv1alpha1.ResourceList{
							CPU:    "500m",
							Memory: "1Gi",
						},
						Requests: mcpv1alpha1.ResourceList{
							CPU:    "100m",
							Memory: "128Mi",
						},
					},
				},
			}

			// Create the MCPServer
			Expect(k8sClient.Create(ctx, mcpServer)).Should(Succeed())

			createdMCPServer = &mcpv1alpha1.MCPServer{}
			k8sClient.Get(ctx, types.NamespacedName{
				Name:      mcpServerName,
				Namespace: namespace,
			}, createdMCPServer)
		})

		AfterAll(func() {
			// Clean up the MCPServer (ConfigMap should be cleaned up by owner reference)
			Expect(k8sClient.Delete(ctx, mcpServer)).Should(Succeed())

			// Wait for ConfigMap to be deleted due to owner reference
			Eventually(func() bool {
				cm := &corev1.ConfigMap{}
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      configMapName,
					Namespace: namespace,
				}, cm)
				return err != nil // Should eventually return NotFound error
			}, timeout, interval).Should(BeTrue())
		})

		It("Should create a RunConfig ConfigMap with correct content", func() {
			// Wait for ConfigMap to be created
			configMap := &corev1.ConfigMap{}
			Eventually(func() error {
				return k8sClient.Get(ctx, types.NamespacedName{
					Name:      configMapName,
					Namespace: namespace,
				}, configMap)
			}, timeout, interval).Should(Succeed())

			// Verify ConfigMap metadata
			Expect(configMap.Name).To(Equal(configMapName))
			Expect(configMap.Namespace).To(Equal(namespace))

			// Verify owner reference is set correctly
			verifyOwnerReference(configMap.OwnerReferences, createdMCPServer, "RunConfig ConfigMap")

			// Verify ConfigMap labels
			expectedLabels := map[string]string{
				"toolhive.stacklok.io/component":  "run-config",
				"toolhive.stacklok.io/mcp-server": mcpServerName,
				"toolhive.stacklok.io/managed-by": "toolhive-operator",
			}
			for key, value := range expectedLabels {
				Expect(configMap.Labels).To(HaveKeyWithValue(key, value))
			}

			// Verify ConfigMap has checksum annotation
			Expect(configMap.Annotations).To(HaveKey(checksum.ContentChecksumAnnotation))
			initialChecksum := configMap.Annotations[checksum.ContentChecksumAnnotation]
			Expect(initialChecksum).NotTo(BeEmpty())

			// Verify ConfigMap data contains runconfig.json
			Expect(configMap.Data).To(HaveKey("runconfig.json"))
			runConfigJSON := configMap.Data["runconfig.json"]
			Expect(runConfigJSON).NotTo(BeEmpty())

			// Parse and verify RunConfig content
			var runConfig runner.RunConfig
			err := json.Unmarshal([]byte(runConfigJSON), &runConfig)
			Expect(err).NotTo(HaveOccurred())

			// Verify RunConfig fields match MCPServer spec
			Expect(runConfig.Name).To(Equal(mcpServerName))
			Expect(runConfig.Image).To(Equal("example/mcp-server:v1.0.0"))
			Expect(runConfig.Transport).To(Equal(transporttypes.TransportTypeStdio))
			Expect(runConfig.ProxyMode).To(Equal(transporttypes.ProxyModeSSE))
			Expect(runConfig.Port).To(Equal(8080))
			Expect(runConfig.TargetPort).To(Equal(8081))
			Expect(runConfig.CmdArgs).To(Equal([]string{"--verbose", "--debug"}))
			Expect(runConfig.ToolsFilter).To(Equal([]string{"tool1", "tool2"}))

			// Verify environment variables
			Expect(runConfig.EnvVars).To(HaveKeyWithValue("DEBUG", "true"))
			Expect(runConfig.EnvVars).To(HaveKeyWithValue("LOG_LEVEL", "debug"))
			Expect(runConfig.EnvVars).To(HaveKeyWithValue("MCP_TRANSPORT", "stdio"))

			// Verify volumes
			Expect(runConfig.Volumes).To(HaveLen(1))
			Expect(runConfig.Volumes[0]).To(Equal("/host/config:/app/config:ro"))

			// Verify schema version
			Expect(runConfig.SchemaVersion).To(Equal(runner.CurrentSchemaVersion))
		})

		It("Should not update ConfigMap when MCPServer spec is unchanged", func() {
			// Get initial ConfigMap state
			initialConfigMap := &corev1.ConfigMap{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      configMapName,
				Namespace: namespace,
			}, initialConfigMap)).To(Succeed())

			initialChecksum := initialConfigMap.Annotations[checksum.ContentChecksumAnnotation]
			initialResourceVersion := initialConfigMap.ResourceVersion

			// Trigger a reconciliation by updating an annotation on MCPServer (not affecting RunConfig)
			Eventually(func() error {
				if err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      mcpServerName,
					Namespace: namespace,
				}, mcpServer); err != nil {
					return err
				}
				if mcpServer.Annotations == nil {
					mcpServer.Annotations = make(map[string]string)
				}
				mcpServer.Annotations["test-annotation"] = "test-value"
				return k8sClient.Update(ctx, mcpServer)
			}, timeout, interval).Should(Succeed())

			// Give time for potential reconciliation
			time.Sleep(2 * time.Second)

			// Verify ConfigMap was not updated
			unchangedConfigMap := &corev1.ConfigMap{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      configMapName,
				Namespace: namespace,
			}, unchangedConfigMap)).To(Succeed())

			// Checksum should remain the same
			Expect(unchangedConfigMap.Annotations[checksum.ContentChecksumAnnotation]).To(Equal(initialChecksum))

			// ResourceVersion should remain the same (no update occurred)
			Expect(unchangedConfigMap.ResourceVersion).To(Equal(initialResourceVersion))
		})

		It("Should update ConfigMap when MCPServer spec changes", func() {
			// Get initial ConfigMap state
			initialConfigMap := &corev1.ConfigMap{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      configMapName,
				Namespace: namespace,
			}, initialConfigMap)).To(Succeed())

			initialChecksum := initialConfigMap.Annotations[checksum.ContentChecksumAnnotation]
			initialResourceVersion := initialConfigMap.ResourceVersion

			// Update MCPServer spec with changes that affect RunConfig
			Eventually(func() error {
				if err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      mcpServerName,
					Namespace: namespace,
				}, mcpServer); err != nil {
					return err
				}
				// Update multiple fields
				mcpServer.Spec.Image = "example/mcp-server:v2.0.0"
				mcpServer.Spec.Port = 9090
				mcpServer.Spec.Env = append(mcpServer.Spec.Env, mcpv1alpha1.EnvVar{
					Name:  "NEW_VAR",
					Value: "new_value",
				})
				mcpServer.Spec.Args = []string{"--production"}
				return k8sClient.Update(ctx, mcpServer)
			}, timeout, interval).Should(Succeed())

			// Wait for ConfigMap to be updated
			Eventually(func() bool {
				cm := &corev1.ConfigMap{}
				if err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      configMapName,
					Namespace: namespace,
				}, cm); err != nil {
					return false
				}
				// Check if checksum has changed
				return cm.Annotations[checksum.ContentChecksumAnnotation] != initialChecksum
			}, timeout, interval).Should(BeTrue())

			// Get updated ConfigMap
			updatedConfigMap := &corev1.ConfigMap{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      configMapName,
				Namespace: namespace,
			}, updatedConfigMap)).To(Succeed())

			// Verify checksum has changed
			newChecksum := updatedConfigMap.Annotations[checksum.ContentChecksumAnnotation]
			Expect(newChecksum).NotTo(Equal(initialChecksum))
			Expect(newChecksum).NotTo(BeEmpty())

			// Verify ResourceVersion has changed (update occurred)
			Expect(updatedConfigMap.ResourceVersion).NotTo(Equal(initialResourceVersion))

			// Parse and verify updated RunConfig content
			var updatedRunConfig runner.RunConfig
			err := json.Unmarshal([]byte(updatedConfigMap.Data["runconfig.json"]), &updatedRunConfig)
			Expect(err).NotTo(HaveOccurred())

			// Verify updated fields
			Expect(updatedRunConfig.Image).To(Equal("example/mcp-server:v2.0.0"))
			Expect(updatedRunConfig.Port).To(Equal(9090))
			Expect(updatedRunConfig.CmdArgs).To(Equal([]string{"--production"}))
			Expect(updatedRunConfig.EnvVars).To(HaveKeyWithValue("NEW_VAR", "new_value"))
			Expect(updatedRunConfig.EnvVars).To(HaveKeyWithValue("DEBUG", "true"))
			Expect(updatedRunConfig.EnvVars).To(HaveKeyWithValue("LOG_LEVEL", "debug"))

			// Owner reference should still be set
			verifyOwnerReference(updatedConfigMap.OwnerReferences, createdMCPServer, "Updated RunConfig ConfigMap")
		})
	})

	Context("When creating MCPServer with complex configurations", func() {
		It("Should handle MCPServer with telemetry configuration", func() {
			namespace := "telemetry-test-ns"
			mcpServerName := "telemetry-server"
			configMapName := mcpServerName + "-runconfig"

			// Create namespace
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: namespace,
				},
			}
			_ = k8sClient.Create(ctx, ns)

			// Create MCPServer with telemetry configuration
			mcpServer := &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      mcpServerName,
					Namespace: namespace,
				},
				Spec: mcpv1alpha1.MCPServerSpec{
					Image:     "telemetry/mcp-server:latest",
					Transport: "stdio",
					Port:      8080,
					Telemetry: &mcpv1alpha1.TelemetryConfig{
						OpenTelemetry: &mcpv1alpha1.OpenTelemetryConfig{
							Enabled:     true,
							Endpoint:    "http://otel-collector:4317",
							ServiceName: "test-service",
							Insecure:    true,
							Tracing: &mcpv1alpha1.OpenTelemetryTracingConfig{
								Enabled:      true,
								SamplingRate: "0.1",
							},
							Metrics: &mcpv1alpha1.OpenTelemetryMetricsConfig{
								Enabled: true,
							},
						},
						Prometheus: &mcpv1alpha1.PrometheusConfig{
							Enabled: true,
						},
					},
				},
			}

			Expect(k8sClient.Create(ctx, mcpServer)).Should(Succeed())
			defer k8sClient.Delete(ctx, mcpServer)

			// Wait for ConfigMap to be created
			configMap := &corev1.ConfigMap{}
			Eventually(func() error {
				return k8sClient.Get(ctx, types.NamespacedName{
					Name:      configMapName,
					Namespace: namespace,
				}, configMap)
			}, timeout, interval).Should(Succeed())

			// Parse RunConfig and verify telemetry configuration
			var runConfig runner.RunConfig
			err := json.Unmarshal([]byte(configMap.Data["runconfig.json"]), &runConfig)
			Expect(err).NotTo(HaveOccurred())

			// Verify telemetry configuration
			Expect(runConfig.TelemetryConfig).NotTo(BeNil())
			Expect(runConfig.TelemetryConfig.Endpoint).To(Equal("otel-collector:4317"))
			Expect(runConfig.TelemetryConfig.ServiceName).To(Equal("test-service"))
			Expect(runConfig.TelemetryConfig.Insecure).To(BeTrue())
			Expect(runConfig.TelemetryConfig.TracingEnabled).To(BeTrue())
			Expect(runConfig.TelemetryConfig.MetricsEnabled).To(BeTrue())
			Expect(runConfig.TelemetryConfig.SamplingRate).To(Equal(0.1))
			Expect(runConfig.TelemetryConfig.EnablePrometheusMetricsPath).To(BeTrue())
		})

		It("Should handle MCPServer with inline authorization configuration", func() {
			namespace := "authz-test-ns"
			mcpServerName := "authz-server"
			configMapName := mcpServerName + "-runconfig"

			// Create namespace
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: namespace,
				},
			}
			_ = k8sClient.Create(ctx, ns)

			// Create MCPServer with inline authorization
			mcpServer := &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      mcpServerName,
					Namespace: namespace,
				},
				Spec: mcpv1alpha1.MCPServerSpec{
					Image:     "authz/mcp-server:latest",
					Transport: "stdio",
					Port:      8080,
					AuthzConfig: &mcpv1alpha1.AuthzConfigRef{
						Type: mcpv1alpha1.AuthzConfigTypeInline,
						Inline: &mcpv1alpha1.InlineAuthzConfig{
							Policies: []string{
								`permit(principal, action == Action::"call_tool", resource == Tool::"weather");`,
								`permit(principal, action == Action::"get_prompt", resource == Prompt::"greeting");`,
							},
							EntitiesJSON: `[{"uid": {"type": "User", "id": "user1"}, "attrs": {}}]`,
						},
					},
				},
			}

			Expect(k8sClient.Create(ctx, mcpServer)).Should(Succeed())
			defer k8sClient.Delete(ctx, mcpServer)

			// Wait for ConfigMap to be created
			configMap := &corev1.ConfigMap{}
			Eventually(func() error {
				return k8sClient.Get(ctx, types.NamespacedName{
					Name:      configMapName,
					Namespace: namespace,
				}, configMap)
			}, timeout, interval).Should(Succeed())

			// Parse RunConfig and verify authorization configuration
			var runConfig runner.RunConfig
			err := json.Unmarshal([]byte(configMap.Data["runconfig.json"]), &runConfig)
			Expect(err).NotTo(HaveOccurred())

			// Verify authorization configuration
			Expect(runConfig.AuthzConfig).NotTo(BeNil())
			Expect(runConfig.AuthzConfig.Version).To(Equal("v1"))
			Expect(runConfig.AuthzConfig.Type).To(Equal(authz.ConfigTypeCedarV1))
			Expect(runConfig.AuthzConfig.Cedar).NotTo(BeNil())
			Expect(runConfig.AuthzConfig.Cedar.Policies).To(HaveLen(2))
			Expect(runConfig.AuthzConfig.Cedar.Policies[0]).To(ContainSubstring("call_tool"))
			Expect(runConfig.AuthzConfig.Cedar.Policies[1]).To(ContainSubstring("get_prompt"))
			Expect(runConfig.AuthzConfig.Cedar.EntitiesJSON).To(ContainSubstring("user1"))
		})

		It("Should handle deterministic ConfigMap generation", func() {
			namespace := "deterministic-test-ns"
			mcpServerName := "deterministic-server"
			configMapName := mcpServerName + "-runconfig"

			// Create namespace
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: namespace,
				},
			}
			_ = k8sClient.Create(ctx, ns)

			// Create MCPServer with comprehensive configuration
			mcpServer := &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      mcpServerName,
					Namespace: namespace,
				},
				Spec: mcpv1alpha1.MCPServerSpec{
					Image:       "deterministic/mcp-server:v1.0.0",
					Transport:   "sse",
					Port:        9090,
					TargetPort:  8080,
					Args:        []string{"--arg1", "--arg2", "--arg3"},
					ToolsFilter: []string{"tool3", "tool1", "tool2"},
					Env: []mcpv1alpha1.EnvVar{
						{Name: "VAR_C", Value: "value_c"},
						{Name: "VAR_A", Value: "value_a"},
						{Name: "VAR_B", Value: "value_b"},
					},
					Volumes: []mcpv1alpha1.Volume{
						{Name: "vol2", HostPath: "/host2", MountPath: "/mount2", ReadOnly: true},
						{Name: "vol1", HostPath: "/host1", MountPath: "/mount1", ReadOnly: false},
					},
				},
			}

			Expect(k8sClient.Create(ctx, mcpServer)).Should(Succeed())
			defer k8sClient.Delete(ctx, mcpServer)

			// Wait for ConfigMap to be created
			configMap := &corev1.ConfigMap{}
			Eventually(func() error {
				return k8sClient.Get(ctx, types.NamespacedName{
					Name:      configMapName,
					Namespace: namespace,
				}, configMap)
			}, timeout, interval).Should(Succeed())

			// Store initial checksum
			initialChecksum := configMap.Annotations[checksum.ContentChecksumAnnotation]
			Expect(initialChecksum).NotTo(BeEmpty())

			// Delete the ConfigMap
			Expect(k8sClient.Delete(ctx, configMap)).Should(Succeed())

			// Wait for ConfigMap to be deleted
			Eventually(func() bool {
				cm := &corev1.ConfigMap{}
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      configMapName,
					Namespace: namespace,
				}, cm)
				return err != nil
			}, timeout, interval).Should(BeTrue())

			// Trigger reconciliation by updating MCPServer annotation
			Eventually(func() error {
				if err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      mcpServerName,
					Namespace: namespace,
				}, mcpServer); err != nil {
					return err
				}
				if mcpServer.Annotations == nil {
					mcpServer.Annotations = make(map[string]string)
				}
				mcpServer.Annotations["trigger-recreate"] = fmt.Sprint(time.Now().Unix())
				return k8sClient.Update(ctx, mcpServer)
			}, timeout, interval).Should(Succeed())

			// Wait for ConfigMap to be recreated
			recreatedConfigMap := &corev1.ConfigMap{}
			Eventually(func() error {
				return k8sClient.Get(ctx, types.NamespacedName{
					Name:      configMapName,
					Namespace: namespace,
				}, recreatedConfigMap)
			}, timeout, interval).Should(Succeed())

			// Verify checksum is identical (deterministic generation)
			recreatedChecksum := recreatedConfigMap.Annotations[checksum.ContentChecksumAnnotation]
			Expect(recreatedChecksum).To(Equal(initialChecksum), "Checksum should be identical for same configuration")

			// Parse and verify content structure is consistent
			var runConfig runner.RunConfig
			err := json.Unmarshal([]byte(recreatedConfigMap.Data["runconfig.json"]), &runConfig)
			Expect(err).NotTo(HaveOccurred())

			// Verify fields maintain their values
			Expect(runConfig.Name).To(Equal(mcpServerName))
			Expect(runConfig.Image).To(Equal("deterministic/mcp-server:v1.0.0"))
			Expect(runConfig.Transport).To(Equal(transporttypes.TransportTypeSSE))
			Expect(runConfig.Port).To(Equal(9090))
			Expect(runConfig.TargetPort).To(Equal(8080))
			Expect(runConfig.CmdArgs).To(Equal([]string{"--arg1", "--arg2", "--arg3"}))
			Expect(runConfig.ToolsFilter).To(Equal([]string{"tool3", "tool1", "tool2"}))
		})
	})
})

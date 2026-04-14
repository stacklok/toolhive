// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package controllers contains integration tests for the VirtualMCPServer controller
package controllers

import (
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/yaml"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	vmcpconfig "github.com/stacklok/toolhive/pkg/vmcp/config"
)

var _ = Describe("VirtualMCPServer TelemetryConfig Integration",
	Label("k8s", "telemetry"), func() {
		const (
			timeout   = time.Second * 30
			interval  = time.Millisecond * 250
			namespace = "default"
		)

		Context("VirtualMCPServer with TelemetryConfigRef should track config hash in status", Ordered, func() {
			var (
				mcpGroup         *mcpv1alpha1.MCPGroup
				telemetryConfig  *mcpv1alpha1.MCPTelemetryConfig
				virtualMCPServer *mcpv1alpha1.VirtualMCPServer
			)

			BeforeAll(func() {
				ns := &corev1.Namespace{
					ObjectMeta: metav1.ObjectMeta{Name: namespace},
				}
				err := k8sClient.Create(ctx, ns)
				if err != nil && !apierrors.IsAlreadyExists(err) {
					Expect(err).NotTo(HaveOccurred())
				}

				mcpGroup = &mcpv1alpha1.MCPGroup{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-group-telemetry-hash",
						Namespace: namespace,
					},
					Spec: mcpv1alpha1.MCPGroupSpec{
						Description: "Test group for telemetry config hash test",
					},
				}
				Expect(k8sClient.Create(ctx, mcpGroup)).Should(Succeed())

				telemetryConfig = &mcpv1alpha1.MCPTelemetryConfig{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-telemetry-vmcp-hash",
						Namespace: namespace,
					},
				}
				telemetryConfig.Spec.OpenTelemetry = &mcpv1alpha1.MCPTelemetryOTelConfig{
					Enabled:  true,
					Endpoint: "https://otel-collector:4317",
					Tracing:  &mcpv1alpha1.OpenTelemetryTracingConfig{Enabled: true},
					Metrics:  &mcpv1alpha1.OpenTelemetryMetricsConfig{Enabled: true},
				}
				Expect(k8sClient.Create(ctx, telemetryConfig)).Should(Succeed())

				// Wait for the MCPTelemetryConfig controller to set ConfigHash
				Eventually(func() bool {
					fetched := &mcpv1alpha1.MCPTelemetryConfig{}
					err := k8sClient.Get(ctx, types.NamespacedName{
						Name:      telemetryConfig.Name,
						Namespace: namespace,
					}, fetched)
					return err == nil && fetched.Status.ConfigHash != ""
				}, timeout, interval).Should(BeTrue())

				virtualMCPServer = &mcpv1alpha1.VirtualMCPServer{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-vmcp-telemetry-hash",
						Namespace: namespace,
					},
					Spec: mcpv1alpha1.VirtualMCPServerSpec{
						Config: vmcpconfig.Config{Group: "test-group-telemetry-hash"},
						IncomingAuth: &mcpv1alpha1.IncomingAuthConfig{
							Type: "anonymous",
						},
						TelemetryConfigRef: &mcpv1alpha1.MCPTelemetryConfigReference{
							Name: "test-telemetry-vmcp-hash",
						},
					},
				}
				Expect(k8sClient.Create(ctx, virtualMCPServer)).Should(Succeed())
			})

			AfterAll(func() {
				Expect(k8sClient.Delete(ctx, virtualMCPServer)).Should(Succeed())
				Expect(k8sClient.Delete(ctx, mcpGroup)).Should(Succeed())
				// MCPTelemetryConfig may be blocked by finalizer until references are removed;
				// the VirtualMCPServer deletion above clears the reference.
				Eventually(func() bool {
					err := k8sClient.Delete(ctx, telemetryConfig)
					return err == nil || apierrors.IsNotFound(err)
				}, timeout, interval).Should(BeTrue())
			})

			It("should set status.telemetryConfigHash to a non-empty value", func() {
				Eventually(func() string {
					fetched := &mcpv1alpha1.VirtualMCPServer{}
					err := k8sClient.Get(ctx, types.NamespacedName{
						Name:      virtualMCPServer.Name,
						Namespace: namespace,
					}, fetched)
					if err != nil {
						return ""
					}
					return fetched.Status.TelemetryConfigHash
				}, timeout, interval).ShouldNot(BeEmpty())
			})

			It("should set TelemetryConfigRefValidated condition to True", func() {
				Eventually(func() bool {
					fetched := &mcpv1alpha1.VirtualMCPServer{}
					err := k8sClient.Get(ctx, types.NamespacedName{
						Name:      virtualMCPServer.Name,
						Namespace: namespace,
					}, fetched)
					if err != nil {
						return false
					}
					for _, cond := range fetched.Status.Conditions {
						if cond.Type == mcpv1alpha1.ConditionTypeVirtualMCPServerTelemetryConfigRefValidated {
							return cond.Status == metav1.ConditionTrue &&
								cond.Reason == mcpv1alpha1.ConditionReasonVirtualMCPServerTelemetryConfigRefValid
						}
					}
					return false
				}, timeout, interval).Should(BeTrue())
			})

			It("should produce a ConfigMap with telemetry config from the MCPTelemetryConfig", func() {
				configMapName := fmt.Sprintf("%s-vmcp-config", virtualMCPServer.Name)
				Eventually(func() bool {
					cm := &corev1.ConfigMap{}
					err := k8sClient.Get(ctx, types.NamespacedName{
						Name:      configMapName,
						Namespace: namespace,
					}, cm)
					if err != nil {
						return false
					}
					configYAML, ok := cm.Data["config.yaml"]
					if !ok || configYAML == "" {
						return false
					}
					// Parse the config and verify telemetry fields match the MCPTelemetryConfig
					var config vmcpconfig.Config
					if err := yaml.Unmarshal([]byte(configYAML), &config); err != nil {
						return false
					}
					return config.Telemetry != nil &&
						config.Telemetry.Endpoint == "otel-collector:4317" && // NormalizeTelemetryConfig strips https://
						config.Telemetry.TracingEnabled &&
						config.Telemetry.MetricsEnabled
				}, timeout, interval).Should(BeTrue())
			})
		})

		Context("VirtualMCPServer should update when MCPTelemetryConfig spec changes", Ordered, func() {
			var (
				mcpGroup         *mcpv1alpha1.MCPGroup
				telemetryConfig  *mcpv1alpha1.MCPTelemetryConfig
				virtualMCPServer *mcpv1alpha1.VirtualMCPServer
				initialHash      string
			)

			BeforeAll(func() {
				mcpGroup = &mcpv1alpha1.MCPGroup{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-group-telemetry-update",
						Namespace: namespace,
					},
					Spec: mcpv1alpha1.MCPGroupSpec{
						Description: "Test group for telemetry config update test",
					},
				}
				Expect(k8sClient.Create(ctx, mcpGroup)).Should(Succeed())

				telemetryConfig = &mcpv1alpha1.MCPTelemetryConfig{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-telemetry-vmcp-update",
						Namespace: namespace,
					},
				}
				telemetryConfig.Spec.OpenTelemetry = &mcpv1alpha1.MCPTelemetryOTelConfig{
					Enabled:  true,
					Endpoint: "https://otel-collector:4317",
					Tracing:  &mcpv1alpha1.OpenTelemetryTracingConfig{Enabled: true},
				}
				Expect(k8sClient.Create(ctx, telemetryConfig)).Should(Succeed())

				// Wait for the MCPTelemetryConfig controller to set ConfigHash
				Eventually(func() bool {
					fetched := &mcpv1alpha1.MCPTelemetryConfig{}
					err := k8sClient.Get(ctx, types.NamespacedName{
						Name:      telemetryConfig.Name,
						Namespace: namespace,
					}, fetched)
					return err == nil && fetched.Status.ConfigHash != ""
				}, timeout, interval).Should(BeTrue())

				virtualMCPServer = &mcpv1alpha1.VirtualMCPServer{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-vmcp-telemetry-update",
						Namespace: namespace,
					},
					Spec: mcpv1alpha1.VirtualMCPServerSpec{
						Config: vmcpconfig.Config{Group: "test-group-telemetry-update"},
						IncomingAuth: &mcpv1alpha1.IncomingAuthConfig{
							Type: "anonymous",
						},
						TelemetryConfigRef: &mcpv1alpha1.MCPTelemetryConfigReference{
							Name: "test-telemetry-vmcp-update",
						},
					},
				}
				Expect(k8sClient.Create(ctx, virtualMCPServer)).Should(Succeed())

				// Wait for the initial hash to be propagated to the VirtualMCPServer
				Eventually(func() bool {
					fetched := &mcpv1alpha1.VirtualMCPServer{}
					err := k8sClient.Get(ctx, types.NamespacedName{
						Name:      virtualMCPServer.Name,
						Namespace: namespace,
					}, fetched)
					if err != nil || fetched.Status.TelemetryConfigHash == "" {
						return false
					}
					initialHash = fetched.Status.TelemetryConfigHash
					return true
				}, timeout, interval).Should(BeTrue())
			})

			AfterAll(func() {
				Expect(k8sClient.Delete(ctx, virtualMCPServer)).Should(Succeed())
				Expect(k8sClient.Delete(ctx, mcpGroup)).Should(Succeed())
				Eventually(func() bool {
					err := k8sClient.Delete(ctx, telemetryConfig)
					return err == nil || apierrors.IsNotFound(err)
				}, timeout, interval).Should(BeTrue())
			})

			It("should update telemetryConfigHash when MCPTelemetryConfig spec changes", func() {
				// Update the MCPTelemetryConfig endpoint to trigger a hash change
				Eventually(func() error {
					fetched := &mcpv1alpha1.MCPTelemetryConfig{}
					if err := k8sClient.Get(ctx, types.NamespacedName{
						Name:      telemetryConfig.Name,
						Namespace: namespace,
					}, fetched); err != nil {
						return err
					}
					fetched.Spec.OpenTelemetry.Endpoint = "https://new-collector:4317"
					return k8sClient.Update(ctx, fetched)
				}, timeout, interval).Should(Succeed())

				// Verify the VirtualMCPServer's telemetryConfigHash changes
				Eventually(func() bool {
					fetched := &mcpv1alpha1.VirtualMCPServer{}
					err := k8sClient.Get(ctx, types.NamespacedName{
						Name:      virtualMCPServer.Name,
						Namespace: namespace,
					}, fetched)
					if err != nil {
						return false
					}
					return fetched.Status.TelemetryConfigHash != "" &&
						fetched.Status.TelemetryConfigHash != initialHash
				}, timeout, interval).Should(BeTrue())

				// Verify the ConfigMap reflects the new endpoint
				configMapName := fmt.Sprintf("%s-vmcp-config", virtualMCPServer.Name)
				Eventually(func() bool {
					cm := &corev1.ConfigMap{}
					err := k8sClient.Get(ctx, types.NamespacedName{
						Name:      configMapName,
						Namespace: namespace,
					}, cm)
					if err != nil {
						return false
					}
					var config vmcpconfig.Config
					if err := yaml.Unmarshal([]byte(cm.Data["config.yaml"]), &config); err != nil {
						return false
					}
					// NormalizeTelemetryConfig strips https:// prefix
					return config.Telemetry != nil &&
						config.Telemetry.Endpoint == "new-collector:4317"
				}, timeout, interval).Should(BeTrue())
			})
		})

		Context("VirtualMCPServer referencing non-existent MCPTelemetryConfig", Ordered, func() {
			var (
				mcpGroup         *mcpv1alpha1.MCPGroup
				virtualMCPServer *mcpv1alpha1.VirtualMCPServer
			)

			BeforeAll(func() {
				mcpGroup = &mcpv1alpha1.MCPGroup{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-group-telemetry-notfound",
						Namespace: namespace,
					},
					Spec: mcpv1alpha1.MCPGroupSpec{
						Description: "Test group for telemetry config not found test",
					},
				}
				Expect(k8sClient.Create(ctx, mcpGroup)).Should(Succeed())

				virtualMCPServer = &mcpv1alpha1.VirtualMCPServer{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-vmcp-telemetry-notfound",
						Namespace: namespace,
					},
					Spec: mcpv1alpha1.VirtualMCPServerSpec{
						Config: vmcpconfig.Config{Group: "test-group-telemetry-notfound"},
						IncomingAuth: &mcpv1alpha1.IncomingAuthConfig{
							Type: "anonymous",
						},
						TelemetryConfigRef: &mcpv1alpha1.MCPTelemetryConfigReference{
							Name: "nonexistent-telemetry-config",
						},
					},
				}
				Expect(k8sClient.Create(ctx, virtualMCPServer)).Should(Succeed())
			})

			AfterAll(func() {
				Expect(k8sClient.Delete(ctx, virtualMCPServer)).Should(Succeed())
				Expect(k8sClient.Delete(ctx, mcpGroup)).Should(Succeed())
			})

			It("should set TelemetryConfigRefValidated condition to False with reason TelemetryConfigRefNotFound", func() {
				Eventually(func() bool {
					fetched := &mcpv1alpha1.VirtualMCPServer{}
					err := k8sClient.Get(ctx, types.NamespacedName{
						Name:      virtualMCPServer.Name,
						Namespace: namespace,
					}, fetched)
					if err != nil {
						return false
					}
					for _, cond := range fetched.Status.Conditions {
						if cond.Type == mcpv1alpha1.ConditionTypeVirtualMCPServerTelemetryConfigRefValidated {
							return cond.Status == metav1.ConditionFalse &&
								cond.Reason == mcpv1alpha1.ConditionReasonVirtualMCPServerTelemetryConfigRefNotFound
						}
					}
					return false
				}, timeout, interval).Should(BeTrue())
			})
		})

	})

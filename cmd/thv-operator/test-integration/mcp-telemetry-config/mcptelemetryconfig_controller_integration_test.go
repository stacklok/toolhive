// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
)

const (
	testEndpoint           = "https://otel-collector:4317"
	telemetryFinalizerName = "mcptelemetryconfig.toolhive.stacklok.dev/finalizer"
	timeout                = time.Second * 30
	interval               = time.Millisecond * 250
)

var _ = Describe("MCPTelemetryConfig Controller", func() {
	It("should set Valid condition and config hash on creation", func() {
		telemetryConfig := &mcpv1alpha1.MCPTelemetryConfig{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-telemetry-creation",
				Namespace: "default",
			},
		}
		telemetryConfig.Spec.OpenTelemetry = &mcpv1alpha1.MCPTelemetryOTelConfig{
			Enabled:  true,
			Endpoint: testEndpoint,
			Tracing:  &mcpv1alpha1.OpenTelemetryTracingConfig{Enabled: true},
			Metrics:  &mcpv1alpha1.OpenTelemetryMetricsConfig{Enabled: true},
		}

		Expect(k8sClient.Create(ctx, telemetryConfig)).To(Succeed())

		// Verify config hash is set
		Eventually(func() bool {
			fetched := &mcpv1alpha1.MCPTelemetryConfig{}
			err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      telemetryConfig.Name,
				Namespace: telemetryConfig.Namespace,
			}, fetched)
			if err != nil {
				return false
			}
			return fetched.Status.ConfigHash != ""
		}, timeout, interval).Should(BeTrue())

		// Verify Valid condition is set to True
		Eventually(func() bool {
			fetched := &mcpv1alpha1.MCPTelemetryConfig{}
			err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      telemetryConfig.Name,
				Namespace: telemetryConfig.Namespace,
			}, fetched)
			if err != nil {
				return false
			}
			for _, cond := range fetched.Status.Conditions {
				if cond.Type == "Valid" && cond.Status == metav1.ConditionTrue {
					return true
				}
			}
			return false
		}, timeout, interval).Should(BeTrue())
	})

	It("should update config hash when spec changes", func() {
		telemetryConfig := &mcpv1alpha1.MCPTelemetryConfig{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-telemetry-hash-change",
				Namespace: "default",
			},
		}
		telemetryConfig.Spec.OpenTelemetry = &mcpv1alpha1.MCPTelemetryOTelConfig{
			Enabled:  true,
			Endpoint: testEndpoint,
			Tracing:  &mcpv1alpha1.OpenTelemetryTracingConfig{Enabled: true},
		}

		Expect(k8sClient.Create(ctx, telemetryConfig)).To(Succeed())

		// Wait for initial hash
		var firstHash string
		Eventually(func() bool {
			fetched := &mcpv1alpha1.MCPTelemetryConfig{}
			err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      telemetryConfig.Name,
				Namespace: telemetryConfig.Namespace,
			}, fetched)
			if err != nil || fetched.Status.ConfigHash == "" {
				return false
			}
			firstHash = fetched.Status.ConfigHash
			return true
		}, timeout, interval).Should(BeTrue())

		// Update the spec
		fetched := &mcpv1alpha1.MCPTelemetryConfig{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{
			Name:      telemetryConfig.Name,
			Namespace: telemetryConfig.Namespace,
		}, fetched)).To(Succeed())

		fetched.Spec.OpenTelemetry.Endpoint = "https://new-collector:4317"
		Expect(k8sClient.Update(ctx, fetched)).To(Succeed())

		// Verify hash changed
		Eventually(func() bool {
			updated := &mcpv1alpha1.MCPTelemetryConfig{}
			err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      telemetryConfig.Name,
				Namespace: telemetryConfig.Namespace,
			}, updated)
			if err != nil {
				return false
			}
			return updated.Status.ConfigHash != "" && updated.Status.ConfigHash != firstHash
		}, timeout, interval).Should(BeTrue())
	})

	It("should allow deletion by removing finalizer", func() {
		telemetryConfig := &mcpv1alpha1.MCPTelemetryConfig{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-telemetry-deletion",
				Namespace: "default",
			},
		}
		telemetryConfig.Spec.OpenTelemetry = &mcpv1alpha1.MCPTelemetryOTelConfig{
			Enabled:  true,
			Endpoint: testEndpoint,
			Tracing:  &mcpv1alpha1.OpenTelemetryTracingConfig{Enabled: true},
		}

		Expect(k8sClient.Create(ctx, telemetryConfig)).To(Succeed())

		// Wait for finalizer to be added
		Eventually(func() bool {
			fetched := &mcpv1alpha1.MCPTelemetryConfig{}
			err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      telemetryConfig.Name,
				Namespace: telemetryConfig.Namespace,
			}, fetched)
			if err != nil {
				return false
			}
			for _, f := range fetched.Finalizers {
				if f == telemetryFinalizerName {
					return true
				}
			}
			return false
		}, timeout, interval).Should(BeTrue())

		// Delete the config
		Expect(k8sClient.Delete(ctx, telemetryConfig)).To(Succeed())

		// Verify it's actually deleted (finalizer removed, object gone)
		Eventually(func() bool {
			fetched := &mcpv1alpha1.MCPTelemetryConfig{}
			err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      telemetryConfig.Name,
				Namespace: telemetryConfig.Namespace,
			}, fetched)
			return err != nil // Should be NotFound
		}, timeout, interval).Should(BeTrue())
	})

	It("should track referencing MCPServers in status", func() {
		// Create a telemetry config
		telemetryConfig := &mcpv1alpha1.MCPTelemetryConfig{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-ref-tracking",
				Namespace: "default",
			},
		}
		telemetryConfig.Spec.OpenTelemetry = &mcpv1alpha1.MCPTelemetryOTelConfig{
			Enabled:  true,
			Endpoint: testEndpoint,
			Tracing:  &mcpv1alpha1.OpenTelemetryTracingConfig{Enabled: true},
		}

		Expect(k8sClient.Create(ctx, telemetryConfig)).To(Succeed())

		// Wait for initial reconciliation (finalizer + hash)
		Eventually(func() bool {
			fetched := &mcpv1alpha1.MCPTelemetryConfig{}
			err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      telemetryConfig.Name,
				Namespace: telemetryConfig.Namespace,
			}, fetched)
			return err == nil && fetched.Status.ConfigHash != ""
		}, timeout, interval).Should(BeTrue())

		// Create an MCPServer that references this config
		server := &mcpv1alpha1.MCPServer{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "server-ref-tracking",
				Namespace: "default",
			},
			Spec: mcpv1alpha1.MCPServerSpec{
				Image: "example/mcp-server:latest",
				TelemetryConfigRef: &mcpv1alpha1.MCPTelemetryConfigReference{
					Name: "test-ref-tracking",
				},
			},
		}
		Expect(k8sClient.Create(ctx, server)).To(Succeed())

		// The MCPServer watch should trigger a reconciliation of the MCPTelemetryConfig.
		// Verify ReferencingWorkloads is updated to include our server.
		Eventually(func() []string {
			fetched := &mcpv1alpha1.MCPTelemetryConfig{}
			err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      telemetryConfig.Name,
				Namespace: telemetryConfig.Namespace,
			}, fetched)
			if err != nil {
				return nil
			}
			names := make([]string, 0, len(fetched.Status.ReferencingWorkloads))
			for _, ref := range fetched.Status.ReferencingWorkloads {
				names = append(names, ref.Name)
			}
			return names
		}, timeout, interval).Should(ContainElement("server-ref-tracking"))
	})

	It("should block deletion when MCPServers reference the config", func() {
		// Create a telemetry config
		telemetryConfig := &mcpv1alpha1.MCPTelemetryConfig{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-deletion-protection",
				Namespace: "default",
			},
		}
		telemetryConfig.Spec.OpenTelemetry = &mcpv1alpha1.MCPTelemetryOTelConfig{
			Enabled:  true,
			Endpoint: testEndpoint,
			Tracing:  &mcpv1alpha1.OpenTelemetryTracingConfig{Enabled: true},
		}

		Expect(k8sClient.Create(ctx, telemetryConfig)).To(Succeed())

		// Wait for finalizer
		Eventually(func() bool {
			fetched := &mcpv1alpha1.MCPTelemetryConfig{}
			err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      telemetryConfig.Name,
				Namespace: telemetryConfig.Namespace,
			}, fetched)
			if err != nil {
				return false
			}
			for _, f := range fetched.Finalizers {
				if f == telemetryFinalizerName {
					return true
				}
			}
			return false
		}, timeout, interval).Should(BeTrue())

		// Create an MCPServer that references this config
		server := &mcpv1alpha1.MCPServer{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "server-deletion-blocker",
				Namespace: "default",
			},
			Spec: mcpv1alpha1.MCPServerSpec{
				Image: "example/mcp-server:latest",
				TelemetryConfigRef: &mcpv1alpha1.MCPTelemetryConfigReference{
					Name: "test-deletion-protection",
				},
			},
		}
		Expect(k8sClient.Create(ctx, server)).To(Succeed())

		// Wait for ReferencingWorkloads to be populated
		Eventually(func() []string {
			fetched := &mcpv1alpha1.MCPTelemetryConfig{}
			err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      telemetryConfig.Name,
				Namespace: telemetryConfig.Namespace,
			}, fetched)
			if err != nil {
				return nil
			}
			names := make([]string, 0, len(fetched.Status.ReferencingWorkloads))
			for _, ref := range fetched.Status.ReferencingWorkloads {
				names = append(names, ref.Name)
			}
			return names
		}, timeout, interval).Should(ContainElement("server-deletion-blocker"))

		// Attempt to delete the config — the API call succeeds (sets DeletionTimestamp)
		// but the finalizer blocks actual removal
		Expect(k8sClient.Delete(ctx, telemetryConfig)).To(Succeed())

		// Verify the object still exists (finalizer prevents deletion)
		Consistently(func() bool {
			fetched := &mcpv1alpha1.MCPTelemetryConfig{}
			err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      telemetryConfig.Name,
				Namespace: telemetryConfig.Namespace,
			}, fetched)
			return err == nil
		}, 3*time.Second, interval).Should(BeTrue(), "Config should not be deleted while referenced")

		// Now remove the referencing MCPServer
		Expect(k8sClient.Delete(ctx, server)).To(Succeed())

		// The config should now be deleted (finalizer removed after reference is gone)
		Eventually(func() bool {
			fetched := &mcpv1alpha1.MCPTelemetryConfig{}
			err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      telemetryConfig.Name,
				Namespace: telemetryConfig.Namespace,
			}, fetched)
			return err != nil // Should be NotFound
		}, timeout, interval).Should(BeTrue(), "Config should be deleted after references are removed")
	})

	It("should track MCPRemoteProxy in ReferencingWorkloads", func() {
		telemetryConfig := &mcpv1alpha1.MCPTelemetryConfig{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-proxy-ref-tracking",
				Namespace: "default",
			},
		}
		telemetryConfig.Spec.OpenTelemetry = &mcpv1alpha1.MCPTelemetryOTelConfig{
			Enabled:  true,
			Endpoint: testEndpoint,
			Tracing:  &mcpv1alpha1.OpenTelemetryTracingConfig{Enabled: true},
		}

		Expect(k8sClient.Create(ctx, telemetryConfig)).To(Succeed())

		// Wait for config to be ready
		Eventually(func() bool {
			fetched := &mcpv1alpha1.MCPTelemetryConfig{}
			err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      telemetryConfig.Name,
				Namespace: telemetryConfig.Namespace,
			}, fetched)
			return err == nil && fetched.Status.ConfigHash != ""
		}, timeout, interval).Should(BeTrue())

		// Create an MCPRemoteProxy that references this config
		proxy := &mcpv1alpha1.MCPRemoteProxy{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "proxy-ref-tracking",
				Namespace: "default",
			},
			Spec: mcpv1alpha1.MCPRemoteProxySpec{
				RemoteURL: "https://example.com/mcp",
				TelemetryConfigRef: &mcpv1alpha1.MCPTelemetryConfigReference{
					Name: "test-proxy-ref-tracking",
				},
			},
		}
		Expect(k8sClient.Create(ctx, proxy)).To(Succeed())

		// The MCPRemoteProxy watch should trigger reconciliation of MCPTelemetryConfig.
		// Verify ReferencingWorkloads includes the proxy.
		Eventually(func() []string {
			fetched := &mcpv1alpha1.MCPTelemetryConfig{}
			err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      telemetryConfig.Name,
				Namespace: telemetryConfig.Namespace,
			}, fetched)
			if err != nil {
				return nil
			}
			names := make([]string, 0, len(fetched.Status.ReferencingWorkloads))
			for _, ref := range fetched.Status.ReferencingWorkloads {
				names = append(names, ref.Kind+"/"+ref.Name)
			}
			return names
		}, timeout, interval).Should(ContainElement("MCPRemoteProxy/proxy-ref-tracking"))
	})

	It("should block deletion when MCPRemoteProxy references the config", func() {
		telemetryConfig := &mcpv1alpha1.MCPTelemetryConfig{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-proxy-deletion-protection",
				Namespace: "default",
			},
		}
		telemetryConfig.Spec.OpenTelemetry = &mcpv1alpha1.MCPTelemetryOTelConfig{
			Enabled:  true,
			Endpoint: testEndpoint,
			Tracing:  &mcpv1alpha1.OpenTelemetryTracingConfig{Enabled: true},
		}

		Expect(k8sClient.Create(ctx, telemetryConfig)).To(Succeed())

		// Wait for finalizer
		Eventually(func() bool {
			fetched := &mcpv1alpha1.MCPTelemetryConfig{}
			err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      telemetryConfig.Name,
				Namespace: telemetryConfig.Namespace,
			}, fetched)
			if err != nil {
				return false
			}
			for _, f := range fetched.Finalizers {
				if f == telemetryFinalizerName {
					return true
				}
			}
			return false
		}, timeout, interval).Should(BeTrue())

		// Create an MCPRemoteProxy that references this config
		proxy := &mcpv1alpha1.MCPRemoteProxy{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "proxy-deletion-blocker",
				Namespace: "default",
			},
			Spec: mcpv1alpha1.MCPRemoteProxySpec{
				RemoteURL: "https://example.com/mcp",
				TelemetryConfigRef: &mcpv1alpha1.MCPTelemetryConfigReference{
					Name: "test-proxy-deletion-protection",
				},
			},
		}
		Expect(k8sClient.Create(ctx, proxy)).To(Succeed())

		// Wait for ReferencingWorkloads to include the proxy
		Eventually(func() []string {
			fetched := &mcpv1alpha1.MCPTelemetryConfig{}
			err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      telemetryConfig.Name,
				Namespace: telemetryConfig.Namespace,
			}, fetched)
			if err != nil {
				return nil
			}
			names := make([]string, 0, len(fetched.Status.ReferencingWorkloads))
			for _, ref := range fetched.Status.ReferencingWorkloads {
				names = append(names, ref.Name)
			}
			return names
		}, timeout, interval).Should(ContainElement("proxy-deletion-blocker"))

		// Attempt to delete — finalizer blocks removal
		Expect(k8sClient.Delete(ctx, telemetryConfig)).To(Succeed())

		// Verify object still exists
		Consistently(func() bool {
			fetched := &mcpv1alpha1.MCPTelemetryConfig{}
			err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      telemetryConfig.Name,
				Namespace: telemetryConfig.Namespace,
			}, fetched)
			return err == nil
		}, 3*time.Second, interval).Should(BeTrue(), "Config should not be deleted while proxy references it")

		// Remove the referencing proxy
		Expect(k8sClient.Delete(ctx, proxy)).To(Succeed())

		// Config should now be deleted
		Eventually(func() bool {
			fetched := &mcpv1alpha1.MCPTelemetryConfig{}
			err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      telemetryConfig.Name,
				Namespace: telemetryConfig.Namespace,
			}, fetched)
			return err != nil // Should be NotFound
		}, timeout, interval).Should(BeTrue(), "Config should be deleted after proxy reference is removed")
	})
})

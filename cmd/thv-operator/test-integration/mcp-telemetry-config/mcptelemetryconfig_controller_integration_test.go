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
	testEndpoint = "https://otel-collector:4317"
	timeout      = time.Second * 30
	interval     = time.Millisecond * 250
)

var _ = Describe("MCPTelemetryConfig Controller", func() {
	It("should set Valid condition and config hash on creation", func() {
		telemetryConfig := &mcpv1alpha1.MCPTelemetryConfig{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-telemetry-creation",
				Namespace: "default",
			},
		}
		telemetryConfig.Spec.Endpoint = testEndpoint
		telemetryConfig.Spec.TracingEnabled = true
		telemetryConfig.Spec.MetricsEnabled = true

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
		telemetryConfig.Spec.Endpoint = testEndpoint
		telemetryConfig.Spec.TracingEnabled = true

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

		fetched.Spec.Endpoint = "https://new-collector:4317"
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
		telemetryConfig.Spec.Endpoint = testEndpoint
		telemetryConfig.Spec.TracingEnabled = true

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
				if f == "mcptelemetryconfig.toolhive.stacklok.dev/finalizer" {
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
})

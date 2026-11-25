// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package controllers contains integration tests for the per-pod
// MCPServer-generation freeze (#5360).
//
// These tests cover the operator side of the contract — that the proxy
// Deployment's pod template carries the mcpserver-generation annotation and
// that the proxyrunner container declares the THV_MCPSERVER_GENERATION env
// var via the downward API pointing at that annotation. The proxyrunner-side
// override of the file value is covered by
// TestTryLoadConfigFromFile_MCPServerGenerationEnvOverride in
// cmd/thv-proxyrunner/app/run_test.go.
package controllers

import (
	"strconv"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
	"github.com/stacklok/toolhive/pkg/container/kubernetes"
)

var _ = Describe("MCPServer generation freeze (#5360)", func() {
	const (
		timeout    = time.Second * 30
		interval   = time.Millisecond * 250
		freezeNS   = "generation-freeze-test-ns"
		annotation = kubernetes.RunConfigMCPServerGenerationAnnotation
	)

	envVarName := kubernetes.EnvVarMCPServerGeneration
	expectedFieldPath := "metadata.annotations['" + annotation + "']"

	// findGenerationEnvVar returns the proxyrunner container's
	// THV_MCPSERVER_GENERATION env var, or nil if absent.
	findGenerationEnvVar := func(deployment *appsv1.Deployment) *corev1.EnvVar {
		if len(deployment.Spec.Template.Spec.Containers) == 0 {
			return nil
		}
		for i := range deployment.Spec.Template.Spec.Containers[0].Env {
			ev := &deployment.Spec.Template.Spec.Containers[0].Env[i]
			if ev.Name == envVarName {
				return ev
			}
		}
		return nil
	}

	BeforeEach(func() {
		ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: freezeNS}}
		_ = k8sClient.Create(ctx, ns)
	})

	cleanupServer := func(key types.NamespacedName) {
		fresh := &mcpv1beta1.MCPServer{}
		if err := k8sClient.Get(ctx, key, fresh); err != nil {
			return
		}
		if len(fresh.Finalizers) > 0 {
			original := fresh.DeepCopy()
			fresh.Finalizers = nil
			// Test-only teardown: no concurrent writers, so plain MergeFrom is
			// fine. Do not copy into reconciler code (see operator rules).
			_ = k8sClient.Patch(ctx, fresh, client.MergeFrom(original))
		}
		_ = k8sClient.Delete(ctx, fresh)
	}

	Context("When the proxy Deployment is created", func() {
		It("Stamps the mcpserver-generation annotation and projects it via the downward API", func() {
			name := "freeze-initial-reconcile"
			server := &mcpv1beta1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: freezeNS},
				Spec: mcpv1beta1.MCPServerSpec{
					Image:     "example/mcp-server:v1.0.0",
					Transport: "stdio",
					ProxyMode: "sse",
					ProxyPort: 8080,
					MCPPort:   8081,
				},
			}
			Expect(k8sClient.Create(ctx, server)).To(Succeed())
			key := types.NamespacedName{Name: name, Namespace: freezeNS}
			DeferCleanup(func() { cleanupServer(key) })

			deployment := &appsv1.Deployment{}
			Eventually(func(g Gomega) {
				g.Expect(k8sClient.Get(ctx, key, deployment)).To(Succeed())
				// Wait until the pod template has both annotations the
				// controller stamps; otherwise we may sample the deployment
				// mid-build.
				g.Expect(deployment.Spec.Template.Annotations).
					To(HaveKey(annotation))
			}, timeout, interval).Should(Succeed())

			// The annotation value must equal the live MCPServer.metadata.generation.
			fresh := &mcpv1beta1.MCPServer{}
			Expect(k8sClient.Get(ctx, key, fresh)).To(Succeed())
			Expect(deployment.Spec.Template.Annotations[annotation]).
				To(Equal(strconv.FormatInt(fresh.Generation, 10)),
					"pod-template annotation must mirror MCPServer.metadata.generation")

			// The proxyrunner container must carry the downward-API env var
			// pointing at that annotation. Field references must use the
			// exact annotation key — a typo here silently produces an empty
			// env value and the override at run.go falls through to the file
			// (defeating the fix).
			ev := findGenerationEnvVar(deployment)
			Expect(ev).NotTo(BeNil(),
				"proxyrunner container must declare the %s env var", envVarName)
			Expect(ev.Value).To(BeEmpty(),
				"env var must use ValueFrom (downward API), not a literal Value")
			Expect(ev.ValueFrom).NotTo(BeNil())
			Expect(ev.ValueFrom.FieldRef).NotTo(BeNil())
			Expect(ev.ValueFrom.FieldRef.FieldPath).To(Equal(expectedFieldPath),
				"FieldRef must point at the mcpserver-generation pod annotation")
		})
	})

	Context("After the initial reconcile settles", func() {
		It("Does not flag spurious drift on a no-op reconcile (#5360 regression)", func() {
			// Regression test for a defaulting trap: ObjectFieldSelector.APIVersion
			// is defaulted to "v1" by the API server on persistence. If the
			// drift-check code rebuilds the env var with an empty APIVersion,
			// equality.Semantic.DeepEqual returns false on every reconcile and
			// the controller perpetually re-applies the Deployment, defeating
			// the rolling-update freeze the env var is supposed to provide.
			name := "freeze-no-drift"
			server := &mcpv1beta1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: freezeNS},
				Spec: mcpv1beta1.MCPServerSpec{
					Image:     "example/mcp-server:v1.0.0",
					Transport: "stdio",
					ProxyMode: "sse",
					ProxyPort: 8080,
					MCPPort:   8081,
				},
			}
			Expect(k8sClient.Create(ctx, server)).To(Succeed())
			key := types.NamespacedName{Name: name, Namespace: freezeNS}
			DeferCleanup(func() { cleanupServer(key) })

			deployment := &appsv1.Deployment{}
			Eventually(func(g Gomega) {
				g.Expect(k8sClient.Get(ctx, key, deployment)).To(Succeed())
				g.Expect(deployment.Spec.Template.Annotations).To(HaveKey(annotation))
			}, timeout, interval).Should(Succeed())

			// Capture the post-reconcile resourceVersion.
			settledRV := deployment.ResourceVersion

			// Give the controller a few extra reconciles to run. If drift
			// detection is broken, each reconcile would re-Update the
			// Deployment and bump its resourceVersion.
			Consistently(func(g Gomega) {
				latest := &appsv1.Deployment{}
				g.Expect(k8sClient.Get(ctx, key, latest)).To(Succeed())
				g.Expect(latest.ResourceVersion).To(Equal(settledRV),
					"Deployment must not be rewritten on no-op reconciles; "+
						"check ObjectFieldSelector.APIVersion defaulting against the drift comparator")
			}, time.Second*5, interval).Should(Succeed())

			// Sanity-check that the persisted env var has the API-server-defaulted
			// APIVersion. This is the value the drift comparator must match.
			ev := findGenerationEnvVar(deployment)
			Expect(ev).NotTo(BeNil())
			Expect(ev.ValueFrom).NotTo(BeNil())
			Expect(ev.ValueFrom.FieldRef).NotTo(BeNil())
			Expect(ev.ValueFrom.FieldRef.APIVersion).To(Equal("v1"),
				"API server defaults ObjectFieldSelector.APIVersion to v1; "+
					"the operator must construct the env var with the same value to avoid false drift")
		})
	})

	Context("When MCPServer.Spec.Image changes (rolling update path)", func() {
		It("Bumps the pod-template annotation to the new MCPServer generation", func() {
			name := "freeze-spec-bump"
			server := &mcpv1beta1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: freezeNS},
				Spec: mcpv1beta1.MCPServerSpec{
					Image:     "example/mcp-server:v1",
					Transport: "stdio",
					ProxyMode: "sse",
					ProxyPort: 8080,
					MCPPort:   8081,
				},
			}
			Expect(k8sClient.Create(ctx, server)).To(Succeed())
			key := types.NamespacedName{Name: name, Namespace: freezeNS}
			DeferCleanup(func() { cleanupServer(key) })

			// Wait for the first reconcile to stamp the deployment.
			deployment := &appsv1.Deployment{}
			var initialGenStr string
			Eventually(func(g Gomega) {
				g.Expect(k8sClient.Get(ctx, key, deployment)).To(Succeed())
				g.Expect(deployment.Spec.Template.Annotations).To(HaveKey(annotation))
				initialGenStr = deployment.Spec.Template.Annotations[annotation]
				g.Expect(initialGenStr).NotTo(BeEmpty())
			}, timeout, interval).Should(Succeed())

			fresh := &mcpv1beta1.MCPServer{}
			Expect(k8sClient.Get(ctx, key, fresh)).To(Succeed())
			initialGeneration := fresh.Generation
			Expect(initialGenStr).To(Equal(strconv.FormatInt(initialGeneration, 10)))

			// Patch Spec.Image. Use a merge patch so other fields aren't
			// touched and resourceVersion isn't precondition-checked — we
			// expect this to bump .metadata.generation.
			Eventually(func() error {
				toPatch := &mcpv1beta1.MCPServer{}
				if err := k8sClient.Get(ctx, key, toPatch); err != nil {
					return err
				}
				original := toPatch.DeepCopy()
				toPatch.Spec.Image = "example/mcp-server:v2"
				return k8sClient.Patch(ctx, toPatch, client.MergeFrom(original))
			}, timeout, interval).Should(Succeed())

			// The controller should re-render the deployment with the new
			// generation in the pod-template annotation. This is the value
			// that, in production, downward-API-projects into new pods so
			// they carry a frozen-per-pod generation strictly greater than
			// any restarted old-RS pod.
			Eventually(func(g Gomega) {
				bumped := &mcpv1beta1.MCPServer{}
				g.Expect(k8sClient.Get(ctx, key, bumped)).To(Succeed())
				g.Expect(bumped.Generation).To(BeNumerically(">", initialGeneration),
					"patching Spec.Image must bump MCPServer.metadata.generation")

				latest := &appsv1.Deployment{}
				g.Expect(k8sClient.Get(ctx, key, latest)).To(Succeed())
				g.Expect(latest.Spec.Template.Annotations[annotation]).
					To(Equal(strconv.FormatInt(bumped.Generation, 10)),
						"pod-template annotation must track the new MCPServer generation")

				// The downward-API env var must still be wired correctly
				// after the rolling-update reconcile.
				ev := findGenerationEnvVar(latest)
				g.Expect(ev).NotTo(BeNil())
				g.Expect(ev.ValueFrom).NotTo(BeNil())
				g.Expect(ev.ValueFrom.FieldRef).NotTo(BeNil())
				g.Expect(ev.ValueFrom.FieldRef.FieldPath).To(Equal(expectedFieldPath))
			}, timeout, interval).Should(Succeed())
		})
	})
})

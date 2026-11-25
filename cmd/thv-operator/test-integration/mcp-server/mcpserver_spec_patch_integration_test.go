// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package controllers contains integration tests for the MCPServer controller.
//
// This file covers regression tests for the spec-Patch migration (#4767): the
// controller must not silently clobber MCPServer spec fields owned by another
// controller (e.g. an external authorization controller writing
// spec.authzConfig via its own merge-patch). The controller now uses an
// optimistic-lock merge patch when mutating finalizers or annotations, so
// concurrent writes to disjoint spec fields survive a reconcile.
//
// The finalizer add/remove paths are not tested separately here. They use
// the same optimistic-lock merge patch pattern and are covered
// deterministically by the unit test TestMCPServerSpecPatchesAreOptimisticLock
// (AddFinalizer / RemoveFinalizer table rows), which asserts the wire-level
// resourceVersion precondition via a patch-recording client. Testing
// deletion in envtest is also awkward: the controller removes the finalizer
// and the object disappears, leaving nothing to Get for the survival
// assertion.
package controllers

import (
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
	"github.com/stacklok/toolhive/cmd/thv-operator/controllers"
)

var _ = Describe("MCPServer spec Patch survival (issue #4767)", func() {
	const (
		// Keep the timeout short: we are asserting that a single reconcile has
		// completed, not waiting for a Deployment to become ready.
		survivalTimeout  = time.Second * 10
		survivalInterval = time.Millisecond * 250
		survivalNS       = "default"
	)

	// authzConfigFixture returns a minimal valid AuthzConfigRef for this test.
	// The controller does not need to resolve the referenced ConfigMap — we only
	// assert the field survives a reconcile that mutates metadata.
	authzConfigFixture := func(cmName string) *mcpv1beta1.AuthzConfigRef {
		return &mcpv1beta1.AuthzConfigRef{
			Type: mcpv1beta1.AuthzConfigTypeConfigMap,
			ConfigMap: &mcpv1beta1.ConfigMapAuthzRef{
				Name: cmName,
				Key:  "authz.json",
			},
		}
	}

	// newMCPServer returns a minimal stdio MCPServer used as a starting point
	// for survival tests. Keep the spec small — we only care about the
	// reconcile triggering the finalizer-add / restart-annotation paths.
	newMCPServer := func(name string) *mcpv1beta1.MCPServer {
		return &mcpv1beta1.MCPServer{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: survivalNS,
			},
			Spec: mcpv1beta1.MCPServerSpec{
				Image:     "example/mcp-server:latest",
				Transport: "stdio",
				ProxyMode: "sse",
				ProxyPort: 8080,
				MCPPort:   8080,
			},
		}
	}

	BeforeEach(func() {
		ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: survivalNS}}
		_ = k8sClient.Create(ctx, ns)
	})

	// cleanupServer strips the controller finalizer and deletes the MCPServer.
	// Relying on the controller to drive its own delete reconcile makes test
	// teardown order-dependent; explicitly removing the finalizer ensures the
	// object is GC'd before the next spec runs, so we do not leak objects
	// between specs or test runs.
	cleanupServer := func(key types.NamespacedName) {
		fresh := &mcpv1beta1.MCPServer{}
		if err := k8sClient.Get(ctx, key, fresh); err != nil {
			return
		}
		if len(fresh.Finalizers) > 0 {
			original := fresh.DeepCopy()
			fresh.Finalizers = nil
			// Test-only teardown: no concurrent writers, so a plain MergeFrom
			// is sufficient. Do not copy this pattern into reconciler code —
			// see .claude/rules/operator.md "Spec / metadata patching".
			if err := k8sClient.Patch(ctx, fresh, client.MergeFrom(original)); err != nil {
				GinkgoWriter.Printf("cleanupServer: failed to strip finalizer from %s: %v\n", key, err)
			}
		}
		if err := k8sClient.Delete(ctx, fresh); err != nil {
			GinkgoWriter.Printf("cleanupServer: failed to delete %s: %v\n", key, err)
		}
	}

	Context("When a second actor writes spec.authzConfig out-of-band", func() {
		It("Should preserve spec.authzConfig across the restart-annotation reconcile", func() {
			// Step 1: create the MCPServer and wait for the controller to
			// settle (finalizer added).
			name := "spec-patch-authz-restart"
			server := newMCPServer(name)
			Expect(k8sClient.Create(ctx, server)).Should(Succeed())
			key := types.NamespacedName{Name: name, Namespace: survivalNS}
			DeferCleanup(func() { cleanupServer(key) })
			Eventually(func(g Gomega) {
				got := &mcpv1beta1.MCPServer{}
				g.Expect(k8sClient.Get(ctx, key, got)).To(Succeed())
				g.Expect(got.Finalizers).To(ContainElement(controllers.MCPServerFinalizerName))
			}, survivalTimeout, survivalInterval).Should(Succeed())

			// Step 2: second actor writes spec.authzConfig, then we trigger
			// the restart-annotation reconcile path by setting the
			// restarted-at annotation. Both edits go through merge patches
			// so they do not collide on resourceVersion unnecessarily.
			Eventually(func() error {
				fresh := &mcpv1beta1.MCPServer{}
				if err := k8sClient.Get(ctx, key, fresh); err != nil {
					return err
				}
				original := fresh.DeepCopy()
				fresh.Spec.AuthzConfig = authzConfigFixture("external-authz-cm-restart")
				return k8sClient.Patch(ctx, fresh, client.MergeFrom(original))
			}, survivalTimeout, survivalInterval).Should(Succeed())

			restartedAt := time.Now().UTC().Format(time.RFC3339)
			Eventually(func() error {
				fresh := &mcpv1beta1.MCPServer{}
				if err := k8sClient.Get(ctx, key, fresh); err != nil {
					return err
				}
				original := fresh.DeepCopy()
				if fresh.Annotations == nil {
					fresh.Annotations = map[string]string{}
				}
				fresh.Annotations[controllers.RestartedAtAnnotationKey] = restartedAt
				return k8sClient.Patch(ctx, fresh, client.MergeFrom(original))
			}, survivalTimeout, survivalInterval).Should(Succeed())

			// Step 3: wait for the controller to process the restart (the
			// last-processed-restart annotation will be set to the value we
			// wrote) and assert spec.authzConfig still matches the
			// out-of-band write.
			Eventually(func(g Gomega) {
				got := &mcpv1beta1.MCPServer{}
				g.Expect(k8sClient.Get(ctx, key, got)).To(Succeed())
				g.Expect(got.Annotations).To(HaveKeyWithValue(
					controllers.LastProcessedRestartAnnotationKey, restartedAt),
					"controller should have processed the restart annotation")
				g.Expect(got.Spec.AuthzConfig).NotTo(BeNil(),
					"spec.authzConfig was clobbered by the restart-annotation reconcile")
				g.Expect(got.Spec.AuthzConfig.ConfigMap).NotTo(BeNil())
				g.Expect(got.Spec.AuthzConfig.ConfigMap.Name).To(Equal("external-authz-cm-restart"))
			}, survivalTimeout, survivalInterval).Should(Succeed())
		})
	})
})

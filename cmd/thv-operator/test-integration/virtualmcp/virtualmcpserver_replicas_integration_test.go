// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package controllers contains integration tests for the VirtualMCPServer controller
package controllers

import (
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	vmcpconfig "github.com/stacklok/toolhive/pkg/vmcp/config"
)

var _ = Describe("VirtualMCPServer Replicas Integration Tests",
	Label("k8s", "replicas"), func() {
		const (
			timeout   = time.Second * 30
			interval  = time.Millisecond * 250
			namespace = "default"
		)

		Context("When spec.replicas is set", Ordered, func() {
			var (
				mcpGroup         *mcpv1alpha1.MCPGroup
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
						Name:      "test-group-replicas",
						Namespace: namespace,
					},
					Spec: mcpv1alpha1.MCPGroupSpec{
						Description: "Test group for replicas integration test",
					},
				}
				Expect(k8sClient.Create(ctx, mcpGroup)).Should(Succeed())

				replicas := int32(3)
				virtualMCPServer = &mcpv1alpha1.VirtualMCPServer{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "vmcp-replicas-test",
						Namespace: namespace,
					},
					Spec: mcpv1alpha1.VirtualMCPServerSpec{
						GroupRef: &mcpv1alpha1.MCPGroupRef{Name: "test-group-replicas"},
						Config:   vmcpconfig.Config{Group: "test-group-replicas"},
						IncomingAuth: &mcpv1alpha1.IncomingAuthConfig{
							Type: "anonymous",
						},
						Replicas: &replicas,
					},
				}
				Expect(k8sClient.Create(ctx, virtualMCPServer)).Should(Succeed())
			})

			AfterAll(func() {
				Expect(k8sClient.Delete(ctx, virtualMCPServer)).Should(Succeed())
				Expect(k8sClient.Delete(ctx, mcpGroup)).Should(Succeed())
			})

			It("Should create a Deployment with the specified replica count", func() {
				deployment := &appsv1.Deployment{}
				Eventually(func() error {
					return k8sClient.Get(ctx, types.NamespacedName{
						Name:      virtualMCPServer.Name,
						Namespace: namespace,
					}, deployment)
				}, timeout, interval).Should(Succeed())

				Expect(deployment.Spec.Replicas).NotTo(BeNil())
				Expect(*deployment.Spec.Replicas).To(Equal(int32(3)))
			})
		})

		Context("When spec.replicas is nil", Ordered, func() {
			var (
				mcpGroup         *mcpv1alpha1.MCPGroup
				virtualMCPServer *mcpv1alpha1.VirtualMCPServer
			)

			BeforeAll(func() {
				mcpGroup = &mcpv1alpha1.MCPGroup{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-group-nil-replicas",
						Namespace: namespace,
					},
					Spec: mcpv1alpha1.MCPGroupSpec{
						Description: "Test group for nil replicas integration test",
					},
				}
				Expect(k8sClient.Create(ctx, mcpGroup)).Should(Succeed())

				virtualMCPServer = &mcpv1alpha1.VirtualMCPServer{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "vmcp-nil-replicas-test",
						Namespace: namespace,
					},
					Spec: mcpv1alpha1.VirtualMCPServerSpec{
						GroupRef: &mcpv1alpha1.MCPGroupRef{Name: "test-group-nil-replicas"},
						Config:   vmcpconfig.Config{Group: "test-group-nil-replicas"},
						IncomingAuth: &mcpv1alpha1.IncomingAuthConfig{
							Type: "anonymous",
						},
					},
				}
				Expect(k8sClient.Create(ctx, virtualMCPServer)).Should(Succeed())
			})

			AfterAll(func() {
				Expect(k8sClient.Delete(ctx, virtualMCPServer)).Should(Succeed())
				Expect(k8sClient.Delete(ctx, mcpGroup)).Should(Succeed())
			})

			// Kubernetes defaults spec.replicas to 1 when nil is submitted, so we cannot
			// assert BeNil() on the stored Deployment. Instead we verify the HPA-compatible
			// contract: the operator must not override a replica count set externally.
			It("Should not override externally-set replicas on reconcile (HPA compatible)", func() {
				// Wait for the Deployment to be created.
				Eventually(func() error {
					dep := &appsv1.Deployment{}
					return k8sClient.Get(ctx, types.NamespacedName{
						Name:      virtualMCPServer.Name,
						Namespace: namespace,
					}, dep)
				}, timeout, interval).Should(Succeed())

				// Simulate HPA: scale the Deployment to 5 replicas externally.
				externalReplicas := int32(5)
				Eventually(func() error {
					dep := &appsv1.Deployment{}
					if err := k8sClient.Get(ctx, types.NamespacedName{
						Name:      virtualMCPServer.Name,
						Namespace: namespace,
					}, dep); err != nil {
						return err
					}
					dep.Spec.Replicas = &externalReplicas
					return k8sClient.Update(ctx, dep)
				}, timeout, interval).Should(Succeed())

				// Trigger a reconciliation via a spec change (ServiceType=ClusterIP,
				// which is the default). Unlike annotation changes, spec changes increment
				// metadata.generation, so we can gate on status.observedGeneration to
				// confirm the reconcile completed after the external scale.
				var triggerGeneration int64
				Eventually(func() error {
					vmcp := &mcpv1alpha1.VirtualMCPServer{}
					if err := k8sClient.Get(ctx, types.NamespacedName{
						Name:      virtualMCPServer.Name,
						Namespace: namespace,
					}, vmcp); err != nil {
						return err
					}
					vmcp.Spec.ServiceType = "ClusterIP"
					if err := k8sClient.Update(ctx, vmcp); err != nil {
						return err
					}
					// controller-runtime Update mutates the object in-place with the server
					// response, so vmcp.Generation already holds the post-increment value.
					triggerGeneration = vmcp.Generation
					return nil
				}, timeout, interval).Should(Succeed())

				// Wait until the controller has processed at least triggerGeneration,
				// confirming a reconciliation ran after the spec change.
				Eventually(func() (int64, error) {
					vmcp := &mcpv1alpha1.VirtualMCPServer{}
					if err := k8sClient.Get(ctx, types.NamespacedName{
						Name:      virtualMCPServer.Name,
						Namespace: namespace,
					}, vmcp); err != nil {
						return 0, err
					}
					return vmcp.Status.ObservedGeneration, nil
				}, timeout, interval).Should(BeNumerically(">=", triggerGeneration))

				// Now assert the operator preserved the externally-set replica count.
				Consistently(func() (int32, error) {
					dep := &appsv1.Deployment{}
					if err := k8sClient.Get(ctx, types.NamespacedName{
						Name:      virtualMCPServer.Name,
						Namespace: namespace,
					}, dep); err != nil {
						return 0, err
					}
					if dep.Spec.Replicas == nil {
						return 0, nil
					}
					return *dep.Spec.Replicas, nil
				}, 3*time.Second, interval).Should(Equal(int32(5)))
			})
		})
	})

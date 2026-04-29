// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package controllers contains integration tests for the VirtualMCPServer controller
package controllers

import (
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
	vmcpconfig "github.com/stacklok/toolhive/pkg/vmcp/config"
)

// extractSecretNames returns just the Name fields from a list of LocalObjectReferences,
// which is what assertions usually care about (order is not guaranteed by strategic merge).
func extractSecretNames(refs []corev1.LocalObjectReference) []string {
	names := make([]string, 0, len(refs))
	for _, r := range refs {
		names = append(names, r.Name)
	}
	return names
}

var _ = Describe("VirtualMCPServer ImagePullSecrets Integration Tests",
	Label("k8s", "imagepullsecrets"), func() {
		const (
			timeout          = time.Second * 30
			interval         = time.Millisecond * 250
			defaultNamespace = "default"
		)

		ensureNamespace := func() {
			ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: defaultNamespace}}
			err := k8sClient.Create(ctx, ns)
			if err != nil && !apierrors.IsAlreadyExists(err) {
				Expect(err).NotTo(HaveOccurred())
			}
		}

		// vmcpServiceAccountName mirrors the controller's helper. We duplicate it here
		// rather than importing it because the controllers package's helper is unexported
		// and the integration test only needs the SA name format ("<vmcp-name>-vmcp").
		saName := func(vmcpName string) string { return fmt.Sprintf("%s-vmcp", vmcpName) }

		Context("When spec.imagePullSecrets is set", Ordered, func() {
			var (
				mcpGroupName     = "test-group-ips-create"
				virtualMCPName   = "test-vmcp-ips-create"
				mcpGroup         *mcpv1beta1.MCPGroup
				virtualMCPServer *mcpv1beta1.VirtualMCPServer
			)

			BeforeAll(func() {
				ensureNamespace()

				mcpGroup = &mcpv1beta1.MCPGroup{
					ObjectMeta: metav1.ObjectMeta{Name: mcpGroupName, Namespace: defaultNamespace},
					Spec:       mcpv1beta1.MCPGroupSpec{Description: "Test group for imagePullSecrets create test"},
				}
				Expect(k8sClient.Create(ctx, mcpGroup)).Should(Succeed())

				virtualMCPServer = &mcpv1beta1.VirtualMCPServer{
					ObjectMeta: metav1.ObjectMeta{Name: virtualMCPName, Namespace: defaultNamespace},
					Spec: mcpv1beta1.VirtualMCPServerSpec{
						GroupRef:     &mcpv1beta1.MCPGroupRef{Name: mcpGroupName},
						Config:       vmcpconfig.Config{Group: mcpGroupName},
						IncomingAuth: &mcpv1beta1.IncomingAuthConfig{Type: "anonymous"},
						ImagePullSecrets: []corev1.LocalObjectReference{
							{Name: "registry-creds-1"},
							{Name: "registry-creds-2"},
						},
					},
				}
				Expect(k8sClient.Create(ctx, virtualMCPServer)).Should(Succeed())
			})

			AfterAll(func() {
				_ = k8sClient.Delete(ctx, virtualMCPServer)
				_ = k8sClient.Delete(ctx, mcpGroup)
			})

			It("Should propagate imagePullSecrets to the Deployment PodSpec", func() {
				deployment := &appsv1.Deployment{}
				Eventually(func() error {
					return k8sClient.Get(ctx, types.NamespacedName{
						Name:      virtualMCPName,
						Namespace: defaultNamespace,
					}, deployment)
				}, timeout, interval).Should(Succeed())

				Expect(extractSecretNames(deployment.Spec.Template.Spec.ImagePullSecrets)).
					To(ConsistOf("registry-creds-1", "registry-creds-2"))
			})

			It("Should propagate imagePullSecrets to the operator-managed ServiceAccount", func() {
				sa := &corev1.ServiceAccount{}
				Eventually(func() error {
					return k8sClient.Get(ctx, types.NamespacedName{
						Name:      saName(virtualMCPName),
						Namespace: defaultNamespace,
					}, sa)
				}, timeout, interval).Should(Succeed())

				Eventually(func() []string {
					if err := k8sClient.Get(ctx, types.NamespacedName{
						Name:      saName(virtualMCPName),
						Namespace: defaultNamespace,
					}, sa); err != nil {
						return nil
					}
					return extractSecretNames(sa.ImagePullSecrets)
				}, timeout, interval).Should(ConsistOf("registry-creds-1", "registry-creds-2"))
			})
		})

		// Regression test for the drift-detection gap fixed alongside this test:
		// edits to spec.imagePullSecrets on an existing CR must roll out to the
		// running Deployment.
		Context("When spec.imagePullSecrets is updated on an existing CR", Ordered, func() {
			var (
				mcpGroupName     = "test-group-ips-update"
				virtualMCPName   = "test-vmcp-ips-update"
				mcpGroup         *mcpv1beta1.MCPGroup
				virtualMCPServer *mcpv1beta1.VirtualMCPServer
			)

			BeforeAll(func() {
				ensureNamespace()

				mcpGroup = &mcpv1beta1.MCPGroup{
					ObjectMeta: metav1.ObjectMeta{Name: mcpGroupName, Namespace: defaultNamespace},
					Spec:       mcpv1beta1.MCPGroupSpec{Description: "Test group for imagePullSecrets update test"},
				}
				Expect(k8sClient.Create(ctx, mcpGroup)).Should(Succeed())

				virtualMCPServer = &mcpv1beta1.VirtualMCPServer{
					ObjectMeta: metav1.ObjectMeta{Name: virtualMCPName, Namespace: defaultNamespace},
					Spec: mcpv1beta1.VirtualMCPServerSpec{
						GroupRef:     &mcpv1beta1.MCPGroupRef{Name: mcpGroupName},
						Config:       vmcpconfig.Config{Group: mcpGroupName},
						IncomingAuth: &mcpv1beta1.IncomingAuthConfig{Type: "anonymous"},
						ImagePullSecrets: []corev1.LocalObjectReference{
							{Name: "secret-a"},
						},
					},
				}
				Expect(k8sClient.Create(ctx, virtualMCPServer)).Should(Succeed())
			})

			AfterAll(func() {
				_ = k8sClient.Delete(ctx, virtualMCPServer)
				_ = k8sClient.Delete(ctx, mcpGroup)
			})

			It("Should roll out the new imagePullSecrets to the Deployment", func() {
				// Wait for the initial Deployment.
				Eventually(func() []string {
					dep := &appsv1.Deployment{}
					if err := k8sClient.Get(ctx, types.NamespacedName{
						Name:      virtualMCPName,
						Namespace: defaultNamespace,
					}, dep); err != nil {
						return nil
					}
					return extractSecretNames(dep.Spec.Template.Spec.ImagePullSecrets)
				}, timeout, interval).Should(ConsistOf("secret-a"))

				// Update the CR's imagePullSecrets to a different value.
				Eventually(func() error {
					vmcp := &mcpv1beta1.VirtualMCPServer{}
					if err := k8sClient.Get(ctx, types.NamespacedName{
						Name:      virtualMCPName,
						Namespace: defaultNamespace,
					}, vmcp); err != nil {
						return err
					}
					vmcp.Spec.ImagePullSecrets = []corev1.LocalObjectReference{{Name: "secret-b"}}
					return k8sClient.Update(ctx, vmcp)
				}, timeout, interval).Should(Succeed())

				// The Deployment must converge to the new list.
				Eventually(func() []string {
					dep := &appsv1.Deployment{}
					if err := k8sClient.Get(ctx, types.NamespacedName{
						Name:      virtualMCPName,
						Namespace: defaultNamespace,
					}, dep); err != nil {
						return nil
					}
					return extractSecretNames(dep.Spec.Template.Spec.ImagePullSecrets)
				}, timeout, interval).Should(ConsistOf("secret-b"))

				// And the SA must follow.
				Eventually(func() []string {
					sa := &corev1.ServiceAccount{}
					if err := k8sClient.Get(ctx, types.NamespacedName{
						Name:      saName(virtualMCPName),
						Namespace: defaultNamespace,
					}, sa); err != nil {
						return nil
					}
					return extractSecretNames(sa.ImagePullSecrets)
				}, timeout, interval).Should(ConsistOf("secret-b"))
			})
		})

		// Verifies the documented contract: PodSpec.ImagePullSecrets is the
		// strategic-merge union of spec.imagePullSecrets and
		// spec.podTemplateSpec.spec.imagePullSecrets, while the SA reflects
		// only spec.imagePullSecrets.
		Context("When both spec.imagePullSecrets and spec.podTemplateSpec carry imagePullSecrets", Ordered, func() {
			var (
				mcpGroupName     = "test-group-ips-union"
				virtualMCPName   = "test-vmcp-ips-union"
				mcpGroup         *mcpv1beta1.MCPGroup
				virtualMCPServer *mcpv1beta1.VirtualMCPServer
			)

			BeforeAll(func() {
				ensureNamespace()

				mcpGroup = &mcpv1beta1.MCPGroup{
					ObjectMeta: metav1.ObjectMeta{Name: mcpGroupName, Namespace: defaultNamespace},
					Spec:       mcpv1beta1.MCPGroupSpec{Description: "Test group for imagePullSecrets union test"},
				}
				Expect(k8sClient.Create(ctx, mcpGroup)).Should(Succeed())

				virtualMCPServer = &mcpv1beta1.VirtualMCPServer{
					ObjectMeta: metav1.ObjectMeta{Name: virtualMCPName, Namespace: defaultNamespace},
					Spec: mcpv1beta1.VirtualMCPServerSpec{
						GroupRef:     &mcpv1beta1.MCPGroupRef{Name: mcpGroupName},
						Config:       vmcpconfig.Config{Group: mcpGroupName},
						IncomingAuth: &mcpv1beta1.IncomingAuthConfig{Type: "anonymous"},
						// "shared" appears in both sources to exercise overlap;
						// "explicit-only" is unique to spec.imagePullSecrets;
						// "podtemplate-only" is unique to PodTemplateSpec.
						ImagePullSecrets: []corev1.LocalObjectReference{
							{Name: "shared"},
							{Name: "explicit-only"},
						},
						PodTemplateSpec: &runtime.RawExtension{
							Raw: []byte(`{"spec":{"imagePullSecrets":[{"name":"shared"},{"name":"podtemplate-only"}]}}`),
						},
					},
				}
				Expect(k8sClient.Create(ctx, virtualMCPServer)).Should(Succeed())
			})

			AfterAll(func() {
				_ = k8sClient.Delete(ctx, virtualMCPServer)
				_ = k8sClient.Delete(ctx, mcpGroup)
			})

			It("Should union the two sources on the Deployment by name", func() {
				Eventually(func() []string {
					dep := &appsv1.Deployment{}
					if err := k8sClient.Get(ctx, types.NamespacedName{
						Name:      virtualMCPName,
						Namespace: defaultNamespace,
					}, dep); err != nil {
						return nil
					}
					return extractSecretNames(dep.Spec.Template.Spec.ImagePullSecrets)
				}, timeout, interval).Should(ConsistOf("shared", "explicit-only", "podtemplate-only"))
			})

			It("Should reflect ONLY spec.imagePullSecrets on the ServiceAccount", func() {
				Eventually(func() []string {
					sa := &corev1.ServiceAccount{}
					if err := k8sClient.Get(ctx, types.NamespacedName{
						Name:      saName(virtualMCPName),
						Namespace: defaultNamespace,
					}, sa); err != nil {
						return nil
					}
					return extractSecretNames(sa.ImagePullSecrets)
				}, timeout, interval).Should(ConsistOf("shared", "explicit-only"))
			})
		})
	})

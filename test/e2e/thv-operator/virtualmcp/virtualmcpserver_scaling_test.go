// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package virtualmcp contains e2e tests for VirtualMCPServer against a real Kubernetes cluster
package virtualmcp

import (
	"fmt"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	vmcpconfig "github.com/stacklok/toolhive/pkg/vmcp/config"
	"github.com/stacklok/toolhive/test/e2e/images"
)

// countReadyPods returns the number of Running+Ready pods for a VirtualMCPServer.
func countReadyPods(vmcpName, namespace string) (int, error) {
	podList, err := GetVirtualMCPServerPods(ctx, k8sClient, vmcpName, namespace)
	if err != nil {
		return 0, err
	}
	ready := 0
	for _, pod := range podList.Items {
		if pod.Status.Phase != corev1.PodRunning {
			continue
		}
		for _, c := range pod.Status.Conditions {
			if c.Type == corev1.PodReady && c.Status == corev1.ConditionTrue {
				ready++
			}
		}
	}
	return ready, nil
}

var _ = ginkgo.Describe("VirtualMCPServer Horizontal Scaling", func() {
	const (
		timeout          = time.Minute * 5
		pollInterval     = time.Second * 2
		defaultNamespace = "default"
	)

	// -------------------------------------------------------------------------
	// Context 1: Deploy with replicas=2, verify warning and pods
	// -------------------------------------------------------------------------

	ginkgo.Context("When VirtualMCPServer is created with replicas=2 and no Redis", ginkgo.Ordered, func() {
		var (
			mcpGroupName string
			backendName  string
			vmcpName     string
		)

		ginkgo.BeforeAll(func() {
			ts := time.Now().UnixNano()
			mcpGroupName = fmt.Sprintf("e2e-scale-static-%d", ts)
			backendName = fmt.Sprintf("e2e-scale-backend-%d", ts)
			vmcpName = fmt.Sprintf("e2e-scale-vmcp-%d", ts)

			ginkgo.By("Creating MCPGroup")
			gomega.Expect(k8sClient.Create(ctx, &mcpv1alpha1.MCPGroup{
				ObjectMeta: metav1.ObjectMeta{Name: mcpGroupName, Namespace: defaultNamespace},
				Spec:       mcpv1alpha1.MCPGroupSpec{Description: "E2E scaling group"},
			})).To(gomega.Succeed())

			ginkgo.By("Creating backend MCPServer")
			gomega.Expect(k8sClient.Create(ctx, &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{Name: backendName, Namespace: defaultNamespace},
				Spec: mcpv1alpha1.MCPServerSpec{
					GroupRef:  mcpGroupName,
					Image:     images.YardstickServerImage,
					Transport: "streamable-http",
					ProxyPort: 8080,
					McpPort:   8080,
				},
			})).To(gomega.Succeed())

			replicas := int32(2)
			ginkgo.By("Creating VirtualMCPServer with replicas=2")
			gomega.Expect(k8sClient.Create(ctx, &mcpv1alpha1.VirtualMCPServer{
				ObjectMeta: metav1.ObjectMeta{Name: vmcpName, Namespace: defaultNamespace},
				Spec: mcpv1alpha1.VirtualMCPServerSpec{
					Config:       vmcpconfig.Config{Group: mcpGroupName},
					IncomingAuth: &mcpv1alpha1.IncomingAuthConfig{Type: "anonymous"},
					Replicas:     &replicas,
				},
			})).To(gomega.Succeed())
		})

		ginkgo.AfterAll(func() {
			_ = k8sClient.Delete(ctx, &mcpv1alpha1.VirtualMCPServer{
				ObjectMeta: metav1.ObjectMeta{Name: vmcpName, Namespace: defaultNamespace},
			})
			_ = k8sClient.Delete(ctx, &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{Name: backendName, Namespace: defaultNamespace},
			})
			_ = k8sClient.Delete(ctx, &mcpv1alpha1.MCPGroup{
				ObjectMeta: metav1.ObjectMeta{Name: mcpGroupName, Namespace: defaultNamespace},
			})
			gomega.Eventually(func() bool {
				err := k8sClient.Get(ctx, types.NamespacedName{Name: vmcpName, Namespace: defaultNamespace}, &mcpv1alpha1.VirtualMCPServer{})
				return apierrors.IsNotFound(err)
			}, timeout, pollInterval).Should(gomega.BeTrue())
		})

		ginkgo.It("Should create a Deployment with spec.replicas=2", func() {
			deployment := &appsv1.Deployment{}
			gomega.Eventually(func() error {
				return k8sClient.Get(ctx, types.NamespacedName{Name: vmcpName, Namespace: defaultNamespace}, deployment)
			}, timeout, pollInterval).Should(gomega.Succeed())

			gomega.Expect(deployment.Spec.Replicas).NotTo(gomega.BeNil())
			gomega.Expect(*deployment.Spec.Replicas).To(gomega.Equal(int32(2)))
		})

		ginkgo.It("Should eventually run 2 ready pods", func() {
			ginkgo.By("Waiting for 2 pods to become Running+Ready")
			gomega.Eventually(func() (int, error) {
				return countReadyPods(vmcpName, defaultNamespace)
			}, timeout, pollInterval).Should(gomega.Equal(2))
		})

		ginkgo.It("Should set SessionStorageWarning condition when Redis is not configured", func() {
			WaitForCondition(ctx, k8sClient, vmcpName, defaultNamespace,
				mcpv1alpha1.ConditionSessionStorageWarning, "True",
				timeout, pollInterval)

			vmcp := &mcpv1alpha1.VirtualMCPServer{}
			gomega.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: vmcpName, Namespace: defaultNamespace}, vmcp)).To(gomega.Succeed())

			var warningCond *metav1.Condition
			for i, cond := range vmcp.Status.Conditions {
				if cond.Type == mcpv1alpha1.ConditionSessionStorageWarning {
					warningCond = &vmcp.Status.Conditions[i]
					break
				}
			}
			gomega.Expect(warningCond).NotTo(gomega.BeNil())
			gomega.Expect(warningCond.Reason).To(gomega.Equal(mcpv1alpha1.ConditionReasonSessionStorageMissing))
		})
	})

	// -------------------------------------------------------------------------
	// Context 2: Scale from 1 to 2 replicas (lifecycle transition)
	// -------------------------------------------------------------------------

	ginkgo.Context("When VirtualMCPServer is scaled from 1 to 2 replicas", ginkgo.Ordered, func() {
		var (
			mcpGroupName string
			backendName  string
			vmcpName     string
		)

		ginkgo.BeforeAll(func() {
			ts := time.Now().UnixNano()
			mcpGroupName = fmt.Sprintf("e2e-scale-lifecycle-%d", ts)
			backendName = fmt.Sprintf("e2e-scale-lc-backend-%d", ts)
			vmcpName = fmt.Sprintf("e2e-scale-lc-vmcp-%d", ts)

			ginkgo.By("Creating MCPGroup")
			gomega.Expect(k8sClient.Create(ctx, &mcpv1alpha1.MCPGroup{
				ObjectMeta: metav1.ObjectMeta{Name: mcpGroupName, Namespace: defaultNamespace},
				Spec:       mcpv1alpha1.MCPGroupSpec{Description: "E2E scaling lifecycle group"},
			})).To(gomega.Succeed())

			ginkgo.By("Creating backend MCPServer")
			gomega.Expect(k8sClient.Create(ctx, &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{Name: backendName, Namespace: defaultNamespace},
				Spec: mcpv1alpha1.MCPServerSpec{
					GroupRef:  mcpGroupName,
					Image:     images.YardstickServerImage,
					Transport: "streamable-http",
					ProxyPort: 8080,
					McpPort:   8080,
				},
			})).To(gomega.Succeed())

			replicas := int32(1)
			ginkgo.By("Creating VirtualMCPServer with replicas=1")
			gomega.Expect(k8sClient.Create(ctx, &mcpv1alpha1.VirtualMCPServer{
				ObjectMeta: metav1.ObjectMeta{Name: vmcpName, Namespace: defaultNamespace},
				Spec: mcpv1alpha1.VirtualMCPServerSpec{
					Config:       vmcpconfig.Config{Group: mcpGroupName},
					IncomingAuth: &mcpv1alpha1.IncomingAuthConfig{Type: "anonymous"},
					Replicas:     &replicas,
					ServiceType:  "NodePort",
				},
			})).To(gomega.Succeed())

			ginkgo.By("Waiting for VirtualMCPServer to be ready with 1 replica")
			WaitForVirtualMCPServerReady(ctx, k8sClient, vmcpName, defaultNamespace, timeout, pollInterval)
		})

		ginkgo.AfterAll(func() {
			_ = k8sClient.Delete(ctx, &mcpv1alpha1.VirtualMCPServer{
				ObjectMeta: metav1.ObjectMeta{Name: vmcpName, Namespace: defaultNamespace},
			})
			_ = k8sClient.Delete(ctx, &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{Name: backendName, Namespace: defaultNamespace},
			})
			_ = k8sClient.Delete(ctx, &mcpv1alpha1.MCPGroup{
				ObjectMeta: metav1.ObjectMeta{Name: mcpGroupName, Namespace: defaultNamespace},
			})
			gomega.Eventually(func() bool {
				err := k8sClient.Get(ctx, types.NamespacedName{Name: vmcpName, Namespace: defaultNamespace}, &mcpv1alpha1.VirtualMCPServer{})
				return apierrors.IsNotFound(err)
			}, timeout, pollInterval).Should(gomega.BeTrue())
		})

		ginkgo.It("Should initially have 1 running pod and no SessionStorageWarning", func() {
			gomega.Eventually(func() (int, error) {
				return countReadyPods(vmcpName, defaultNamespace)
			}, timeout, pollInterval).Should(gomega.Equal(1))

			WaitForCondition(ctx, k8sClient, vmcpName, defaultNamespace,
				mcpv1alpha1.ConditionSessionStorageWarning, "False",
				timeout, pollInterval)
		})

		ginkgo.It("Should update Deployment replicas and set SessionStorageWarning after scaling to 2", func() {
			ginkgo.By("Scaling VirtualMCPServer to 2 replicas")
			gomega.Eventually(func() error {
				vmcp := &mcpv1alpha1.VirtualMCPServer{}
				if err := k8sClient.Get(ctx, types.NamespacedName{Name: vmcpName, Namespace: defaultNamespace}, vmcp); err != nil {
					return err
				}
				newReplicas := int32(2)
				vmcp.Spec.Replicas = &newReplicas
				return k8sClient.Update(ctx, vmcp)
			}, timeout, pollInterval).Should(gomega.Succeed())

			ginkgo.By("Verifying Deployment spec.replicas becomes 2")
			gomega.Eventually(func() (int32, error) {
				deployment := &appsv1.Deployment{}
				if err := k8sClient.Get(ctx, types.NamespacedName{Name: vmcpName, Namespace: defaultNamespace}, deployment); err != nil {
					return 0, err
				}
				if deployment.Spec.Replicas == nil {
					return 0, fmt.Errorf("replicas is nil")
				}
				return *deployment.Spec.Replicas, nil
			}, timeout, pollInterval).Should(gomega.Equal(int32(2)))

			ginkgo.By("Verifying 2 pods become ready")
			gomega.Eventually(func() (int, error) {
				return countReadyPods(vmcpName, defaultNamespace)
			}, timeout, pollInterval).Should(gomega.Equal(2))

			ginkgo.By("Verifying SessionStorageWarning is now set")
			WaitForCondition(ctx, k8sClient, vmcpName, defaultNamespace,
				mcpv1alpha1.ConditionSessionStorageWarning, "True",
				timeout, pollInterval)
		})

		ginkgo.It("Should clear SessionStorageWarning when scaled back to 1", func() {
			ginkgo.By("Scaling VirtualMCPServer back to 1 replica")
			gomega.Eventually(func() error {
				vmcp := &mcpv1alpha1.VirtualMCPServer{}
				if err := k8sClient.Get(ctx, types.NamespacedName{Name: vmcpName, Namespace: defaultNamespace}, vmcp); err != nil {
					return err
				}
				newReplicas := int32(1)
				vmcp.Spec.Replicas = &newReplicas
				return k8sClient.Update(ctx, vmcp)
			}, timeout, pollInterval).Should(gomega.Succeed())

			ginkgo.By("Verifying SessionStorageWarning is cleared")
			WaitForCondition(ctx, k8sClient, vmcpName, defaultNamespace,
				mcpv1alpha1.ConditionSessionStorageWarning, "False",
				timeout, pollInterval)
		})
	})
})

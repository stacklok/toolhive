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

// countReadyPods returns the number of Running+Ready pods for a VirtualMCPServer
// in the default namespace.
func countReadyPods(vmcpName string) (int, error) {
	podList, err := GetVirtualMCPServerPods(ctx, k8sClient, vmcpName, testNamespace)
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

// scaleVMCP updates a VirtualMCPServer's replica count, retrying on conflicts.
func scaleVMCP(name, namespace string, replicas int32, timeout, pollInterval time.Duration) {
	ginkgo.GinkgoHelper()
	gomega.Eventually(func() error {
		vmcp := &mcpv1alpha1.VirtualMCPServer{}
		if err := k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: namespace}, vmcp); err != nil {
			return err
		}
		vmcp.Spec.Replicas = &replicas
		return k8sClient.Update(ctx, vmcp)
	}, timeout, pollInterval).Should(gomega.Succeed())
}

var _ = ginkgo.Describe("VirtualMCPServer Horizontal Scaling", func() {
	const (
		timeout      = time.Minute * 5
		pollInterval = time.Second * 2
	)

	// Single ordered context covers the full scaling lifecycle:
	//   create@1 → verify no warning → delete Deployment → verify reconciled →
	//   scale@2 → verify warning → scale@0 → verify 0 pods → scale@1 → verify cleared
	ginkgo.Context("Scaling lifecycle", ginkgo.Ordered, func() {
		var (
			mcpGroupName string
			backendName  string
			vmcpName     string
		)

		ginkgo.BeforeAll(func() {
			ts := time.Now().UnixNano()
			mcpGroupName = fmt.Sprintf("e2e-scale-%d", ts)
			backendName = fmt.Sprintf("e2e-scale-backend-%d", ts)
			vmcpName = fmt.Sprintf("e2e-scale-vmcp-%d", ts)

			ginkgo.By("Creating MCPGroup")
			gomega.Expect(k8sClient.Create(ctx, &mcpv1alpha1.MCPGroup{
				ObjectMeta: metav1.ObjectMeta{Name: mcpGroupName, Namespace: testNamespace},
				Spec:       mcpv1alpha1.MCPGroupSpec{Description: "E2E scaling group"},
			})).To(gomega.Succeed())

			ginkgo.By("Creating backend MCPServer")
			gomega.Expect(k8sClient.Create(ctx, &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{Name: backendName, Namespace: testNamespace},
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
				ObjectMeta: metav1.ObjectMeta{Name: vmcpName, Namespace: testNamespace},
				Spec: mcpv1alpha1.VirtualMCPServerSpec{
					Config:       vmcpconfig.Config{Group: mcpGroupName},
					IncomingAuth: &mcpv1alpha1.IncomingAuthConfig{Type: "anonymous"},
					Replicas:     &replicas,
				},
			})).To(gomega.Succeed())
		})

		ginkgo.AfterAll(func() {
			_ = k8sClient.Delete(ctx, &mcpv1alpha1.VirtualMCPServer{
				ObjectMeta: metav1.ObjectMeta{Name: vmcpName, Namespace: testNamespace},
			})
			_ = k8sClient.Delete(ctx, &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{Name: backendName, Namespace: testNamespace},
			})
			_ = k8sClient.Delete(ctx, &mcpv1alpha1.MCPGroup{
				ObjectMeta: metav1.ObjectMeta{Name: mcpGroupName, Namespace: testNamespace},
			})
			gomega.Eventually(func() bool {
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name: vmcpName, Namespace: testNamespace,
				}, &mcpv1alpha1.VirtualMCPServer{})
				return apierrors.IsNotFound(err)
			}, timeout, pollInterval).Should(gomega.BeTrue())
		})

		ginkgo.It("Should create a Deployment with spec.replicas=1, 1 ready pod, and no SessionStorageWarning", func() {
			ginkgo.By("Waiting for Deployment with spec.replicas=1")
			deployment := &appsv1.Deployment{}
			gomega.Eventually(func() error {
				return k8sClient.Get(ctx,
					types.NamespacedName{Name: vmcpName, Namespace: testNamespace}, deployment)
			}, timeout, pollInterval).Should(gomega.Succeed())
			gomega.Expect(deployment.Spec.Replicas).NotTo(gomega.BeNil())
			gomega.Expect(*deployment.Spec.Replicas).To(gomega.Equal(int32(1)))

			ginkgo.By("Waiting for 1 pod to become Running+Ready")
			gomega.Eventually(func() (int, error) {
				return countReadyPods(vmcpName)
			}, timeout, pollInterval).Should(gomega.Equal(1))

			ginkgo.By("Verifying no SessionStorageWarning at 1 replica without Redis")
			WaitForCondition(ctx, k8sClient, vmcpName, testNamespace,
				mcpv1alpha1.ConditionSessionStorageWarning, "False",
				timeout, pollInterval)
		})

		ginkgo.It("Should recreate the Deployment if it is deleted externally", func() {
			ginkgo.By("Deleting the Deployment out-of-band")
			deployment := &appsv1.Deployment{}
			gomega.Expect(k8sClient.Get(ctx,
				types.NamespacedName{Name: vmcpName, Namespace: testNamespace}, deployment)).To(gomega.Succeed())
			gomega.Expect(k8sClient.Delete(ctx, deployment)).To(gomega.Succeed())

			ginkgo.By("Waiting for Deployment to be gone")
			gomega.Eventually(func() bool {
				err := k8sClient.Get(ctx,
					types.NamespacedName{Name: vmcpName, Namespace: testNamespace}, &appsv1.Deployment{})
				return apierrors.IsNotFound(err)
			}, timeout, pollInterval).Should(gomega.BeTrue())

			ginkgo.By("Waiting for operator to recreate the Deployment")
			recreated := &appsv1.Deployment{}
			gomega.Eventually(func() error {
				return k8sClient.Get(ctx,
					types.NamespacedName{Name: vmcpName, Namespace: testNamespace}, recreated)
			}, timeout, pollInterval).Should(gomega.Succeed())
			gomega.Expect(recreated.Spec.Replicas).NotTo(gomega.BeNil())
			gomega.Expect(*recreated.Spec.Replicas).To(gomega.Equal(int32(1)))
		})

		ginkgo.It("Should set SessionStorageWarning and have 2 ready pods when scaled to 2 replicas", func() {
			ginkgo.By("Scaling to 2 replicas")
			scaleVMCP(vmcpName, testNamespace, 2, timeout, pollInterval)

			ginkgo.By("Waiting for Deployment spec.replicas=2")
			gomega.Eventually(func() (int32, error) {
				dep := &appsv1.Deployment{}
				if err := k8sClient.Get(ctx,
					types.NamespacedName{Name: vmcpName, Namespace: testNamespace}, dep); err != nil {
					return 0, err
				}
				if dep.Spec.Replicas == nil {
					return 0, fmt.Errorf("replicas is nil")
				}
				return *dep.Spec.Replicas, nil
			}, timeout, pollInterval).Should(gomega.Equal(int32(2)))

			ginkgo.By("Waiting for 2 pods to become Running+Ready")
			gomega.Eventually(func() (int, error) {
				return countReadyPods(vmcpName)
			}, timeout, pollInterval).Should(gomega.Equal(2))

			ginkgo.By("Verifying SessionStorageWarning is set")
			WaitForCondition(ctx, k8sClient, vmcpName, testNamespace,
				mcpv1alpha1.ConditionSessionStorageWarning, "True",
				timeout, pollInterval)

			vmcp := &mcpv1alpha1.VirtualMCPServer{}
			gomega.Expect(k8sClient.Get(ctx,
				types.NamespacedName{Name: vmcpName, Namespace: testNamespace}, vmcp)).To(gomega.Succeed())
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

		ginkgo.It("Should scale to 0 replicas and have no running pods", func() {
			ginkgo.By("Scaling to 0 replicas")
			scaleVMCP(vmcpName, testNamespace, 0, timeout, pollInterval)

			ginkgo.By("Waiting for Deployment spec.replicas=0")
			gomega.Eventually(func() (int32, error) {
				dep := &appsv1.Deployment{}
				if err := k8sClient.Get(ctx,
					types.NamespacedName{Name: vmcpName, Namespace: testNamespace}, dep); err != nil {
					return 0, err
				}
				if dep.Spec.Replicas == nil {
					return 0, fmt.Errorf("replicas is nil")
				}
				return *dep.Spec.Replicas, nil
			}, timeout, pollInterval).Should(gomega.Equal(int32(0)))

			ginkgo.By("Waiting for all pods to terminate")
			gomega.Eventually(func() (int, error) {
				return countReadyPods(vmcpName)
			}, timeout, pollInterval).Should(gomega.Equal(0))
		})

		ginkgo.It("Should clear SessionStorageWarning when scaled back to 1 replica", func() {
			ginkgo.By("Scaling to 1 replica")
			scaleVMCP(vmcpName, testNamespace, 1, timeout, pollInterval)

			ginkgo.By("Waiting for exactly 1 ready pod")
			gomega.Eventually(func() (int, error) {
				return countReadyPods(vmcpName)
			}, timeout, pollInterval).Should(gomega.Equal(1))

			ginkgo.By("Verifying SessionStorageWarning is cleared")
			WaitForCondition(ctx, k8sClient, vmcpName, testNamespace,
				mcpv1alpha1.ConditionSessionStorageWarning, "False",
				timeout, pollInterval)
		})
	})
})

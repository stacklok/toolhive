// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package virtualmcp contains e2e tests for VirtualMCPServer against a real Kubernetes cluster
package virtualmcp

import (
	"encoding/base64"
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
)

var _ = ginkgo.Describe("VirtualMCPServer HMAC Secret E2E Tests", func() {
	const (
		timeout          = time.Minute * 2
		pollInterval     = time.Second * 2
		defaultNamespace = "default"
	)

	ginkgo.Context("When creating VirtualMCPServer with SessionManagementV2 enabled", ginkgo.Ordered, func() {
		var (
			mcpGroupName       string
			virtualMCPName     string
			expectedSecretName string
		)

		ginkgo.BeforeAll(func() {
			// Use timestamp to ensure unique names and avoid collisions with parallel runs
			timestamp := time.Now().Unix()
			mcpGroupName = fmt.Sprintf("e2e-test-group-hmac-%d", timestamp)
			virtualMCPName = fmt.Sprintf("e2e-test-vmcp-hmac-%d", timestamp)
			expectedSecretName = virtualMCPName + "-hmac-secret"

			ginkgo.By("Creating MCPGroup")
			mcpGroup := &mcpv1alpha1.MCPGroup{
				ObjectMeta: metav1.ObjectMeta{
					Name:      mcpGroupName,
					Namespace: defaultNamespace,
				},
				Spec: mcpv1alpha1.MCPGroupSpec{
					Description: "E2E test group for HMAC secret validation",
				},
			}
			gomega.Expect(k8sClient.Create(ctx, mcpGroup)).Should(gomega.Succeed())

			ginkgo.By("Creating VirtualMCPServer with SessionManagementV2 enabled")
			virtualMCPServer := &mcpv1alpha1.VirtualMCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      virtualMCPName,
					Namespace: defaultNamespace,
				},
				Spec: mcpv1alpha1.VirtualMCPServerSpec{
					Config: vmcpconfig.Config{
						Group: mcpGroupName,
						Operational: &vmcpconfig.OperationalConfig{
							SessionManagementV2: true, // Enable Session Management V2
						},
					},
					IncomingAuth: &mcpv1alpha1.IncomingAuthConfig{
						Type: "anonymous",
					},
				},
			}
			gomega.Expect(k8sClient.Create(ctx, virtualMCPServer)).Should(gomega.Succeed())
		})

		ginkgo.AfterAll(func() {
			ginkgo.By("Cleaning up VirtualMCPServer")
			virtualMCPServer := &mcpv1alpha1.VirtualMCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      virtualMCPName,
					Namespace: defaultNamespace,
				},
			}
			_ = k8sClient.Delete(ctx, virtualMCPServer)

			ginkgo.By("Cleaning up MCPGroup")
			mcpGroup := &mcpv1alpha1.MCPGroup{
				ObjectMeta: metav1.ObjectMeta{
					Name:      mcpGroupName,
					Namespace: defaultNamespace,
				},
			}
			_ = k8sClient.Delete(ctx, mcpGroup)

			ginkgo.By("Waiting for resources to be deleted")
			gomega.Eventually(func() bool {
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      virtualMCPName,
					Namespace: defaultNamespace,
				}, &mcpv1alpha1.VirtualMCPServer{})
				return apierrors.IsNotFound(err)
			}, timeout, pollInterval).Should(gomega.BeTrue())
		})

		ginkgo.It("Should automatically create HMAC secret", func() {
			ginkgo.By("Waiting for HMAC secret to be created by operator")
			secret := &corev1.Secret{}
			gomega.Eventually(func() error {
				return k8sClient.Get(ctx, types.NamespacedName{
					Name:      expectedSecretName,
					Namespace: defaultNamespace,
				}, secret)
			}, timeout, pollInterval).Should(gomega.Succeed())

			ginkgo.By("Verifying secret was created")
			gomega.Expect(secret.Name).To(gomega.Equal(expectedSecretName))
		})

		ginkgo.It("Should have correct secret structure and metadata", func() {
			secret := &corev1.Secret{}
			gomega.Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      expectedSecretName,
				Namespace: defaultNamespace,
			}, secret)).Should(gomega.Succeed())

			ginkgo.By("Verifying secret type")
			gomega.Expect(secret.Type).To(gomega.Equal(corev1.SecretTypeOpaque))

			ginkgo.By("Verifying labels")
			gomega.Expect(secret.Labels).To(gomega.HaveKeyWithValue("app.kubernetes.io/name", "virtualmcpserver"))
			gomega.Expect(secret.Labels).To(gomega.HaveKeyWithValue("app.kubernetes.io/instance", virtualMCPName))
			gomega.Expect(secret.Labels).To(gomega.HaveKeyWithValue("app.kubernetes.io/component", "session-security"))
			gomega.Expect(secret.Labels).To(gomega.HaveKeyWithValue("app.kubernetes.io/managed-by", "toolhive-operator"))

			ginkgo.By("Verifying annotations")
			gomega.Expect(secret.Annotations).To(gomega.HaveKeyWithValue("toolhive.stacklok.dev/purpose", "hmac-secret-for-session-token-binding"))

			ginkgo.By("Verifying owner reference for cascade deletion")
			gomega.Expect(secret.OwnerReferences).To(gomega.HaveLen(1))
			gomega.Expect(secret.OwnerReferences[0].Name).To(gomega.Equal(virtualMCPName))
			gomega.Expect(secret.OwnerReferences[0].Kind).To(gomega.Equal("VirtualMCPServer"))
		})

		ginkgo.It("Should contain a valid 32-byte base64-encoded HMAC secret", func() {
			secret := &corev1.Secret{}
			gomega.Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      expectedSecretName,
				Namespace: defaultNamespace,
			}, secret)).Should(gomega.Succeed())

			ginkgo.By("Verifying secret has hmac-secret key")
			gomega.Expect(secret.Data).To(gomega.HaveKey("hmac-secret"))

			hmacSecretBase64 := string(secret.Data["hmac-secret"])
			gomega.Expect(hmacSecretBase64).NotTo(gomega.BeEmpty())

			ginkgo.By("Verifying secret is valid base64")
			decoded, err := base64.StdEncoding.DecodeString(hmacSecretBase64)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())

			ginkgo.By("Verifying decoded secret is exactly 32 bytes")
			gomega.Expect(decoded).To(gomega.HaveLen(32))

			ginkgo.By("Verifying secret is not all zeros (proper random generation)")
			allZeros := make([]byte, 32)
			gomega.Expect(decoded).NotTo(gomega.Equal(allZeros))
		})

		ginkgo.It("Should inject HMAC secret into deployment as environment variable", func() {
			deployment := &appsv1.Deployment{}

			ginkgo.By("Waiting for deployment to be created")
			gomega.Eventually(func() error {
				return k8sClient.Get(ctx, types.NamespacedName{
					Name:      virtualMCPName,
					Namespace: defaultNamespace,
				}, deployment)
			}, timeout, pollInterval).Should(gomega.Succeed())

			ginkgo.By("Finding vmcp container in deployment")
			gomega.Expect(deployment.Spec.Template.Spec.Containers).NotTo(gomega.BeEmpty())

			var vmcpContainer *corev1.Container
			for i, container := range deployment.Spec.Template.Spec.Containers {
				if container.Name == "vmcp" {
					vmcpContainer = &deployment.Spec.Template.Spec.Containers[i]
					break
				}
			}
			gomega.Expect(vmcpContainer).NotTo(gomega.BeNil())

			ginkgo.By("Verifying VMCP_SESSION_HMAC_SECRET environment variable exists")
			var hmacSecretEnvVar *corev1.EnvVar
			for i, env := range vmcpContainer.Env {
				if env.Name == "VMCP_SESSION_HMAC_SECRET" {
					hmacSecretEnvVar = &vmcpContainer.Env[i]
					break
				}
			}
			gomega.Expect(hmacSecretEnvVar).NotTo(gomega.BeNil())

			ginkgo.By("Verifying env var is sourced from the secret")
			gomega.Expect(hmacSecretEnvVar.ValueFrom).NotTo(gomega.BeNil())
			gomega.Expect(hmacSecretEnvVar.ValueFrom.SecretKeyRef).NotTo(gomega.BeNil())
			gomega.Expect(hmacSecretEnvVar.ValueFrom.SecretKeyRef.Name).To(gomega.Equal(expectedSecretName))
			gomega.Expect(hmacSecretEnvVar.ValueFrom.SecretKeyRef.Key).To(gomega.Equal("hmac-secret"))
		})
	})

	ginkgo.Context("When creating VirtualMCPServer WITHOUT SessionManagementV2", ginkgo.Ordered, func() {
		var (
			mcpGroupName       string
			virtualMCPName     string
			expectedSecretName string
		)

		ginkgo.BeforeAll(func() {
			// Use timestamp to ensure unique names and avoid collisions with parallel runs
			timestamp := time.Now().Unix()
			mcpGroupName = fmt.Sprintf("e2e-test-group-no-hmac-%d", timestamp)
			virtualMCPName = fmt.Sprintf("e2e-test-vmcp-no-hmac-%d", timestamp)
			expectedSecretName = virtualMCPName + "-hmac-secret"

			ginkgo.By("Creating MCPGroup")
			mcpGroup := &mcpv1alpha1.MCPGroup{
				ObjectMeta: metav1.ObjectMeta{
					Name:      mcpGroupName,
					Namespace: defaultNamespace,
				},
				Spec: mcpv1alpha1.MCPGroupSpec{
					Description: "E2E test group for no HMAC secret validation",
				},
			}
			gomega.Expect(k8sClient.Create(ctx, mcpGroup)).Should(gomega.Succeed())

			ginkgo.By("Creating VirtualMCPServer WITHOUT SessionManagementV2")
			virtualMCPServer := &mcpv1alpha1.VirtualMCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      virtualMCPName,
					Namespace: defaultNamespace,
				},
				Spec: mcpv1alpha1.VirtualMCPServerSpec{
					Config: vmcpconfig.Config{
						Group: mcpGroupName,
						// No Operational config - SessionManagementV2 defaults to false
					},
					IncomingAuth: &mcpv1alpha1.IncomingAuthConfig{
						Type: "anonymous",
					},
				},
			}
			gomega.Expect(k8sClient.Create(ctx, virtualMCPServer)).Should(gomega.Succeed())
		})

		ginkgo.AfterAll(func() {
			ginkgo.By("Cleaning up VirtualMCPServer")
			virtualMCPServer := &mcpv1alpha1.VirtualMCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      virtualMCPName,
					Namespace: defaultNamespace,
				},
			}
			_ = k8sClient.Delete(ctx, virtualMCPServer)

			ginkgo.By("Cleaning up MCPGroup")
			mcpGroup := &mcpv1alpha1.MCPGroup{
				ObjectMeta: metav1.ObjectMeta{
					Name:      mcpGroupName,
					Namespace: defaultNamespace,
				},
			}
			_ = k8sClient.Delete(ctx, mcpGroup)
		})

		ginkgo.It("Should NOT create HMAC secret when SessionManagementV2 is disabled", func() {
			ginkgo.By("Verifying secret consistently does not exist over time")
			// Use Consistently to verify the secret is NOT created over a sustained period
			// This is more robust than a fixed sleep + single check
			gomega.Consistently(func() bool {
				secret := &corev1.Secret{}
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      expectedSecretName,
					Namespace: defaultNamespace,
				}, secret)
				return apierrors.IsNotFound(err)
			}, time.Second*15, pollInterval).Should(gomega.BeTrue(), "HMAC secret should NOT be created when SessionManagementV2 is disabled")
		})

		ginkgo.It("Should NOT inject HMAC secret env var when SessionManagementV2 is disabled", func() {
			deployment := &appsv1.Deployment{}

			ginkgo.By("Waiting for deployment to be created")
			gomega.Eventually(func() error {
				return k8sClient.Get(ctx, types.NamespacedName{
					Name:      virtualMCPName,
					Namespace: defaultNamespace,
				}, deployment)
			}, timeout, pollInterval).Should(gomega.Succeed())

			ginkgo.By("Finding vmcp container")
			var vmcpContainer *corev1.Container
			for i, container := range deployment.Spec.Template.Spec.Containers {
				if container.Name == "vmcp" {
					vmcpContainer = &deployment.Spec.Template.Spec.Containers[i]
					break
				}
			}
			gomega.Expect(vmcpContainer).NotTo(gomega.BeNil())

			ginkgo.By("Verifying VMCP_SESSION_HMAC_SECRET env var does NOT exist")
			for _, env := range vmcpContainer.Env {
				gomega.Expect(env.Name).NotTo(gomega.Equal("VMCP_SESSION_HMAC_SECRET"))
			}
		})
	})
})

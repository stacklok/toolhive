// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"encoding/json"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
)

// When spec.serviceAccount names an existing account, the operator must NOT
// create the auto-managed "<name>-sa" backend ServiceAccount, must still create
// the proxy-runner ServiceAccount (which is always operator-managed), and must
// inject the custom account into the backend pod via the --k8s-pod-patch arg.
// Only the default-creation path was covered before this test.
var _ = Describe("MCPServer with a custom ServiceAccount", Ordered, func() {
	const (
		timeout              = time.Second * 30
		interval             = time.Millisecond * 250
		customServiceAccount = "my-existing-sa"
	)

	var (
		namespace     string
		mcpServerName string
		mcpServer     *mcpv1beta1.MCPServer
		ns            *corev1.Namespace
	)

	BeforeAll(func() {
		ns = &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{GenerateName: "test-mcpserver-customsa-"},
		}
		Expect(k8sClient.Create(ctx, ns)).Should(Succeed())
		namespace = ns.Name
		mcpServerName = "test-mcpserver-customsa"

		mcpServer = &mcpv1beta1.MCPServer{
			ObjectMeta: metav1.ObjectMeta{
				Name:      mcpServerName,
				Namespace: namespace,
			},
			Spec: mcpv1beta1.MCPServerSpec{
				Image:          "example/mcp-server:latest",
				Transport:      "stdio",
				ProxyPort:      8080,
				ServiceAccount: ptr.To(customServiceAccount),
			},
		}
		Expect(k8sClient.Create(ctx, mcpServer)).Should(Succeed())
	})

	AfterAll(func() {
		_ = k8sClient.Delete(ctx, mcpServer)
		Expect(k8sClient.Delete(ctx, ns)).Should(Succeed())
	})

	It("still creates the operator-managed proxy-runner ServiceAccount", func() {
		Eventually(func() error {
			return k8sClient.Get(ctx, types.NamespacedName{
				Name:      mcpServerName + "-proxy-runner",
				Namespace: namespace,
			}, &corev1.ServiceAccount{})
		}, timeout, interval).Should(Succeed())
	})

	It("does not create the auto-managed backend ServiceAccount", func() {
		// The "<name>-sa" account is only created when spec.serviceAccount is
		// unset; with a custom account it must never appear.
		Consistently(func() bool {
			err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      mcpServerName + "-sa",
				Namespace: namespace,
			}, &corev1.ServiceAccount{})
			return apierrors.IsNotFound(err)
		}, 3*time.Second, interval).Should(BeTrue())
	})

	It("injects the custom ServiceAccount into the backend pod patch", func() {
		deployment := &appsv1.Deployment{}
		Eventually(func() error {
			return k8sClient.Get(ctx, types.NamespacedName{
				Name:      mcpServerName,
				Namespace: namespace,
			}, deployment)
		}, timeout, interval).Should(Succeed())

		Expect(deployment.Spec.Template.Spec.Containers).NotTo(BeEmpty())
		var podPatchJSON string
		for _, arg := range deployment.Spec.Template.Spec.Containers[0].Args {
			if strings.HasPrefix(arg, "--k8s-pod-patch=") {
				podPatchJSON = strings.TrimPrefix(arg, "--k8s-pod-patch=")
				break
			}
		}
		Expect(podPatchJSON).NotTo(BeEmpty(), "Deployment should have --k8s-pod-patch argument")

		var patch map[string]any
		Expect(json.Unmarshal([]byte(podPatchJSON), &patch)).Should(Succeed())
		spec, ok := patch["spec"].(map[string]any)
		Expect(ok).To(BeTrue(), "pod patch should have a spec")
		Expect(spec["serviceAccountName"]).To(Equal(customServiceAccount))
	})
})

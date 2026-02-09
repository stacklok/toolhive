// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"fmt"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
)

// WaitForDeployment waits for a Deployment to be created and returns it.
func (h *MCPRemoteProxyTestHelper) WaitForDeployment(name string, timeout time.Duration) *appsv1.Deployment {
	ginkgo.By(fmt.Sprintf("waiting for Deployment %s to be created", name))

	deployment := &appsv1.Deployment{}
	gomega.Eventually(func() error {
		return h.Client.Get(h.Context, types.NamespacedName{
			Name:      name,
			Namespace: h.Namespace,
		}, deployment)
	}, timeout, DefaultPollingInterval).Should(gomega.Succeed())

	return deployment
}

// WaitForService waits for a Service to be created and returns it.
func (h *MCPRemoteProxyTestHelper) WaitForService(name string, timeout time.Duration) *corev1.Service {
	ginkgo.By(fmt.Sprintf("waiting for Service %s to be created", name))

	service := &corev1.Service{}
	gomega.Eventually(func() error {
		return h.Client.Get(h.Context, types.NamespacedName{
			Name:      name,
			Namespace: h.Namespace,
		}, service)
	}, timeout, DefaultPollingInterval).Should(gomega.Succeed())

	return service
}

// WaitForConfigMap waits for a ConfigMap to be created and returns it.
func (h *MCPRemoteProxyTestHelper) WaitForConfigMap(name string, timeout time.Duration) *corev1.ConfigMap {
	ginkgo.By(fmt.Sprintf("waiting for ConfigMap %s to be created", name))

	configMap := &corev1.ConfigMap{}
	gomega.Eventually(func() error {
		return h.Client.Get(h.Context, types.NamespacedName{
			Name:      name,
			Namespace: h.Namespace,
		}, configMap)
	}, timeout, DefaultPollingInterval).Should(gomega.Succeed())

	return configMap
}

// WaitForServiceAccount waits for a ServiceAccount to be created and returns it.
func (h *MCPRemoteProxyTestHelper) WaitForServiceAccount(name string, timeout time.Duration) *corev1.ServiceAccount {
	ginkgo.By(fmt.Sprintf("waiting for ServiceAccount %s to be created", name))

	serviceAccount := &corev1.ServiceAccount{}
	gomega.Eventually(func() error {
		return h.Client.Get(h.Context, types.NamespacedName{
			Name:      name,
			Namespace: h.Namespace,
		}, serviceAccount)
	}, timeout, DefaultPollingInterval).Should(gomega.Succeed())

	return serviceAccount
}

// WaitForRole waits for a Role to be created and returns it.
func (h *MCPRemoteProxyTestHelper) WaitForRole(name string, timeout time.Duration) *rbacv1.Role {
	ginkgo.By(fmt.Sprintf("waiting for Role %s to be created", name))

	role := &rbacv1.Role{}
	gomega.Eventually(func() error {
		return h.Client.Get(h.Context, types.NamespacedName{
			Name:      name,
			Namespace: h.Namespace,
		}, role)
	}, timeout, DefaultPollingInterval).Should(gomega.Succeed())

	return role
}

// WaitForRoleBinding waits for a RoleBinding to be created and returns it.
func (h *MCPRemoteProxyTestHelper) WaitForRoleBinding(name string, timeout time.Duration) *rbacv1.RoleBinding {
	ginkgo.By(fmt.Sprintf("waiting for RoleBinding %s to be created", name))

	roleBinding := &rbacv1.RoleBinding{}
	gomega.Eventually(func() error {
		return h.Client.Get(h.Context, types.NamespacedName{
			Name:      name,
			Namespace: h.Namespace,
		}, roleBinding)
	}, timeout, DefaultPollingInterval).Should(gomega.Succeed())

	return roleBinding
}

// WaitForExternalAuthConfigHash waits for the proxy to have a non-empty ExternalAuthConfigHash and returns it.
func (h *MCPRemoteProxyTestHelper) WaitForExternalAuthConfigHash(name string, timeout time.Duration) string {
	var hash string
	gomega.Eventually(func() string {
		p, err := h.GetRemoteProxy(name)
		if err != nil {
			return ""
		}
		hash = p.Status.ExternalAuthConfigHash
		return hash
	}, timeout, DefaultPollingInterval).ShouldNot(gomega.BeEmpty(),
		"MCPRemoteProxy %s should have ExternalAuthConfigHash set", name)
	return hash
}

// WaitForExternalAuthConfigHashChange waits for the proxy's ExternalAuthConfigHash to change from the previous value.
func (h *MCPRemoteProxyTestHelper) WaitForExternalAuthConfigHashChange(
	name, previousHash string, timeout time.Duration,
) {
	gomega.Eventually(func() bool {
		p, err := h.GetRemoteProxy(name)
		if err != nil {
			return false
		}
		return p.Status.ExternalAuthConfigHash != previousHash &&
			p.Status.ExternalAuthConfigHash != ""
	}, timeout, DefaultPollingInterval).Should(gomega.BeTrue(),
		"MCPRemoteProxy %s ExternalAuthConfigHash should change from %s", name, previousHash)
}

// WaitForToolConfigHash waits for the proxy to have a non-empty ToolConfigHash and returns it.
func (h *MCPRemoteProxyTestHelper) WaitForToolConfigHash(name string, timeout time.Duration) string {
	var hash string
	gomega.Eventually(func() string {
		p, err := h.GetRemoteProxy(name)
		if err != nil {
			return ""
		}
		hash = p.Status.ToolConfigHash
		return hash
	}, timeout, DefaultPollingInterval).ShouldNot(gomega.BeEmpty(),
		"MCPRemoteProxy %s should have ToolConfigHash set", name)
	return hash
}

// WaitForToolConfigHashChange waits for the proxy's ToolConfigHash to change from the previous value.
func (h *MCPRemoteProxyTestHelper) WaitForToolConfigHashChange(
	name, previousHash string, timeout time.Duration,
) {
	gomega.Eventually(func() bool {
		p, err := h.GetRemoteProxy(name)
		if err != nil {
			return false
		}
		return p.Status.ToolConfigHash != previousHash &&
			p.Status.ToolConfigHash != ""
	}, timeout, DefaultPollingInterval).Should(gomega.BeTrue(),
		"MCPRemoteProxy %s ToolConfigHash should change from %s", name, previousHash)
}

// verifyRemoteProxyOwnerReference verifies that the owner reference matches the expected MCPRemoteProxy.
func verifyRemoteProxyOwnerReference(ownerRefs []metav1.OwnerReference, proxy *mcpv1alpha1.MCPRemoteProxy, resourceType string) {
	gomega.ExpectWithOffset(1, ownerRefs).To(gomega.HaveLen(1),
		fmt.Sprintf("%s should have exactly one owner reference", resourceType))

	ownerRef := ownerRefs[0]
	gomega.ExpectWithOffset(1, ownerRef.APIVersion).To(gomega.Equal("toolhive.stacklok.dev/v1alpha1"),
		fmt.Sprintf("%s owner reference should have correct APIVersion", resourceType))
	gomega.ExpectWithOffset(1, ownerRef.Kind).To(gomega.Equal("MCPRemoteProxy"),
		fmt.Sprintf("%s owner reference should have correct Kind", resourceType))
	gomega.ExpectWithOffset(1, ownerRef.Name).To(gomega.Equal(proxy.Name),
		fmt.Sprintf("%s owner reference should have correct Name", resourceType))
	gomega.ExpectWithOffset(1, ownerRef.UID).To(gomega.Equal(proxy.UID),
		fmt.Sprintf("%s owner reference should have correct UID", resourceType))
	gomega.ExpectWithOffset(1, ownerRef.Controller).ToNot(gomega.BeNil(),
		fmt.Sprintf("%s owner reference Controller should not be nil", resourceType))
	gomega.ExpectWithOffset(1, *ownerRef.Controller).To(gomega.BeTrue(),
		fmt.Sprintf("%s owner reference Controller should be true", resourceType))
	gomega.ExpectWithOffset(1, ownerRef.BlockOwnerDeletion).ToNot(gomega.BeNil(),
		fmt.Sprintf("%s owner reference BlockOwnerDeletion should not be nil", resourceType))
	gomega.ExpectWithOffset(1, *ownerRef.BlockOwnerDeletion).To(gomega.BeTrue(),
		fmt.Sprintf("%s owner reference BlockOwnerDeletion should be true", resourceType))
}

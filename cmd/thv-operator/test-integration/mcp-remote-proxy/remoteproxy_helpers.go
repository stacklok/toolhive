// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"context"
	"fmt"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
)

// Common timeout values for different types of operations
const (
	// MediumTimeout for operations that may take some time (e.g., controller reconciliation)
	MediumTimeout = 30 * time.Second

	// LongTimeout for operations that may take a while (e.g., sync operations)
	LongTimeout = 2 * time.Minute

	// DefaultPollingInterval for Eventually/Consistently checks
	DefaultPollingInterval = 1 * time.Second
)

// MCPRemoteProxyTestHelper provides specialized utilities for MCPRemoteProxy testing
type MCPRemoteProxyTestHelper struct {
	Client    client.Client
	Context   context.Context
	Namespace string
}

// NewMCPRemoteProxyTestHelper creates a new test helper for MCPRemoteProxy operations
func NewMCPRemoteProxyTestHelper(
	ctx context.Context, k8sClient client.Client, namespace string,
) *MCPRemoteProxyTestHelper {
	return &MCPRemoteProxyTestHelper{
		Client:    k8sClient,
		Context:   ctx,
		Namespace: namespace,
	}
}

// RemoteProxyBuilder provides a fluent interface for building MCPRemoteProxy objects
type RemoteProxyBuilder struct {
	proxy *mcpv1alpha1.MCPRemoteProxy
}

// NewRemoteProxyBuilder creates a new MCPRemoteProxy builder with sensible defaults
// for required fields (RemoteURL, OIDCConfig) so tests only need to override what they're testing
func (h *MCPRemoteProxyTestHelper) NewRemoteProxyBuilder(name string) *RemoteProxyBuilder {
	return &RemoteProxyBuilder{
		proxy: &mcpv1alpha1.MCPRemoteProxy{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: h.Namespace,
				Labels: map[string]string{
					"test.toolhive.io/suite": "operator-e2e",
				},
			},
			Spec: mcpv1alpha1.MCPRemoteProxySpec{
				RemoteURL: "https://remote.example.com/mcp",
				Port:      8080,
				Transport: "streamable-http",
				// Default OIDC config for tests - override with WithInlineOIDCConfig if testing OIDC
				OIDCConfig: mcpv1alpha1.OIDCConfigRef{
					Type: "inline",
					Inline: &mcpv1alpha1.InlineOIDCConfig{
						Issuer:   "https://auth.example.com",
						Audience: "test-audience",
						ClientID: "test-client",
					},
				},
			},
		},
	}
}

// WithPort sets the port for the proxy
func (rb *RemoteProxyBuilder) WithPort(port int32) *RemoteProxyBuilder {
	rb.proxy.Spec.Port = port
	return rb
}

// Create builds and creates the MCPRemoteProxy in the cluster
func (rb *RemoteProxyBuilder) Create(h *MCPRemoteProxyTestHelper) *mcpv1alpha1.MCPRemoteProxy {
	proxy := rb.proxy.DeepCopy()
	err := h.Client.Create(h.Context, proxy)
	gomega.Expect(err).NotTo(gomega.HaveOccurred(), "Failed to create MCPRemoteProxy")
	return proxy
}

// GetRemoteProxy retrieves an MCPRemoteProxy by name
func (h *MCPRemoteProxyTestHelper) GetRemoteProxy(name string) (*mcpv1alpha1.MCPRemoteProxy, error) {
	proxy := &mcpv1alpha1.MCPRemoteProxy{}
	err := h.Client.Get(h.Context, types.NamespacedName{
		Namespace: h.Namespace,
		Name:      name,
	}, proxy)
	return proxy, err
}

// GetRemoteProxyStatus returns the current status of an MCPRemoteProxy
func (h *MCPRemoteProxyTestHelper) GetRemoteProxyStatus(
	name string,
) (*mcpv1alpha1.MCPRemoteProxyStatus, error) {
	proxy, err := h.GetRemoteProxy(name)
	if err != nil {
		return nil, err
	}
	return &proxy.Status, nil
}

// GetRemoteProxyPhase returns the current phase of an MCPRemoteProxy
func (h *MCPRemoteProxyTestHelper) GetRemoteProxyPhase(
	name string,
) (mcpv1alpha1.MCPRemoteProxyPhase, error) {
	status, err := h.GetRemoteProxyStatus(name)
	if err != nil {
		return "", err
	}
	return status.Phase, nil
}

// GetRemoteProxyCondition returns a specific condition from the proxy status
func (h *MCPRemoteProxyTestHelper) GetRemoteProxyCondition(
	name, conditionType string,
) (*metav1.Condition, error) {
	status, err := h.GetRemoteProxyStatus(name)
	if err != nil {
		return nil, err
	}

	for _, condition := range status.Conditions {
		if condition.Type == conditionType {
			return &condition, nil
		}
	}
	return nil, fmt.Errorf("condition %s not found", conditionType)
}

// CleanupRemoteProxies deletes all MCPRemoteProxies in the namespace
func (h *MCPRemoteProxyTestHelper) CleanupRemoteProxies() error {
	proxyList := &mcpv1alpha1.MCPRemoteProxyList{}
	err := h.Client.List(h.Context, proxyList, client.InNamespace(h.Namespace))
	if err != nil {
		return err
	}

	for _, proxy := range proxyList.Items {
		if err := h.Client.Delete(h.Context, &proxy); err != nil && !errors.IsNotFound(err) {
			return err
		}

		// Wait for proxy to be actually deleted
		ginkgo.By(fmt.Sprintf("waiting for remote proxy %s to be deleted", proxy.Name))
		gomega.Eventually(func() bool {
			_, err := h.GetRemoteProxy(proxy.Name)
			return err != nil && errors.IsNotFound(err)
		}, LongTimeout, DefaultPollingInterval).Should(gomega.BeTrue())
	}
	return nil
}

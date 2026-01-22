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
	"sigs.k8s.io/controller-runtime/pkg/client"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
)

// RemoteProxyStatusTestHelper provides utilities for MCPRemoteProxy status testing and validation
type RemoteProxyStatusTestHelper struct {
	proxyHelper *MCPRemoteProxyTestHelper
}

// NewRemoteProxyStatusTestHelper creates a new test helper for status operations
func NewRemoteProxyStatusTestHelper(
	ctx context.Context, k8sClient client.Client, namespace string,
) *RemoteProxyStatusTestHelper {
	return &RemoteProxyStatusTestHelper{
		proxyHelper: NewMCPRemoteProxyTestHelper(ctx, k8sClient, namespace),
	}
}

// WaitForPhaseAny waits for an MCPRemoteProxy to reach any of the specified phases
func (h *RemoteProxyStatusTestHelper) WaitForPhaseAny(
	proxyName string, expectedPhases []mcpv1alpha1.MCPRemoteProxyPhase, timeout time.Duration,
) {
	gomega.Eventually(func() mcpv1alpha1.MCPRemoteProxyPhase {
		ginkgo.By(fmt.Sprintf("waiting for remote proxy %s to reach one of phases %v", proxyName, expectedPhases))
		proxy, err := h.proxyHelper.GetRemoteProxy(proxyName)
		if err != nil {
			if errors.IsNotFound(err) {
				ginkgo.By(fmt.Sprintf("remote proxy %s not found", proxyName))
				return mcpv1alpha1.MCPRemoteProxyPhaseTerminating
			}
			return ""
		}
		return proxy.Status.Phase
	}, timeout, time.Second).Should(gomega.BeElementOf(expectedPhases),
		"MCPRemoteProxy %s should reach one of phases %v", proxyName, expectedPhases)
}

// WaitForURL waits for the URL to be set in the status
func (h *RemoteProxyStatusTestHelper) WaitForURL(proxyName string, timeout time.Duration) {
	gomega.Eventually(func() string {
		status, err := h.proxyHelper.GetRemoteProxyStatus(proxyName)
		if err != nil {
			return ""
		}
		return status.URL
	}, timeout, time.Second).ShouldNot(gomega.BeEmpty(),
		"MCPRemoteProxy %s should have a URL set", proxyName)
}

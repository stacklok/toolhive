// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"fmt"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
)

// RemoteProxyStatusTestHelper provides utilities for MCPRemoteProxy status testing and validation
type RemoteProxyStatusTestHelper struct {
	proxyHelper *MCPRemoteProxyTestHelper
}

// NewRemoteProxyStatusTestHelper creates a new test helper for status operations
func NewRemoteProxyStatusTestHelper(
	proxyHelper *MCPRemoteProxyTestHelper,
) *RemoteProxyStatusTestHelper {
	return &RemoteProxyStatusTestHelper{
		proxyHelper: proxyHelper,
	}
}

// WaitForPhaseAny waits for an MCPRemoteProxy to reach any of the specified phases
func (h *RemoteProxyStatusTestHelper) WaitForPhaseAny(
	proxyName string, expectedPhases []mcpv1alpha1.MCPRemoteProxyPhase, timeout time.Duration,
) {
	ginkgo.By(fmt.Sprintf("waiting for remote proxy %s to reach one of phases %v", proxyName, expectedPhases))
	gomega.Eventually(func() mcpv1alpha1.MCPRemoteProxyPhase {
		proxy, err := h.proxyHelper.GetRemoteProxy(proxyName)
		if err != nil {
			if errors.IsNotFound(err) {
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

// WaitForPhase waits for an MCPRemoteProxy to reach the specified phase
func (h *RemoteProxyStatusTestHelper) WaitForPhase(
	proxyName string, expectedPhase mcpv1alpha1.MCPRemoteProxyPhase, timeout time.Duration,
) {
	gomega.Eventually(func() mcpv1alpha1.MCPRemoteProxyPhase {
		proxy, err := h.proxyHelper.GetRemoteProxy(proxyName)
		if err != nil {
			return ""
		}
		return proxy.Status.Phase
	}, timeout, time.Second).Should(gomega.Equal(expectedPhase),
		"MCPRemoteProxy %s should reach phase %s", proxyName, expectedPhase)
}

// WaitForCondition waits for a specific condition to have the expected status
func (h *RemoteProxyStatusTestHelper) WaitForCondition(
	proxyName, conditionType string, expectedStatus metav1.ConditionStatus, timeout time.Duration,
) {
	gomega.Eventually(func() metav1.ConditionStatus {
		condition, err := h.proxyHelper.GetRemoteProxyCondition(proxyName, conditionType)
		if err != nil {
			return metav1.ConditionUnknown
		}
		return condition.Status
	}, timeout, time.Second).Should(gomega.Equal(expectedStatus),
		"MCPRemoteProxy %s should have condition %s with status %s", proxyName, conditionType, expectedStatus)
}

// WaitForConditionReason waits for a condition to have a specific reason
func (h *RemoteProxyStatusTestHelper) WaitForConditionReason(
	proxyName, conditionType, expectedReason string, timeout time.Duration,
) {
	gomega.Eventually(func() string {
		condition, err := h.proxyHelper.GetRemoteProxyCondition(proxyName, conditionType)
		if err != nil {
			return ""
		}
		return condition.Reason
	}, timeout, time.Second).Should(gomega.Equal(expectedReason),
		"MCPRemoteProxy %s condition %s should have reason %s", proxyName, conditionType, expectedReason)
}

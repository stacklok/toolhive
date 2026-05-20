// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package operator_test

import (
	"context"
	"fmt"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
)

// StatusTestHelper provides utilities for MCPRegistry status testing and validation
type StatusTestHelper struct {
	registryHelper *MCPRegistryTestHelper
}

// NewStatusTestHelper creates a new test helper for status operations
func NewStatusTestHelper(ctx context.Context, k8sClient client.Client, namespace string) *StatusTestHelper {
	return &StatusTestHelper{
		registryHelper: NewMCPRegistryTestHelper(ctx, k8sClient, namespace),
	}
}

// WaitForPhase waits for an MCPRegistry to reach the specified phase
func (h *StatusTestHelper) WaitForPhase(registryName string, expectedPhase mcpv1beta1.MCPRegistryPhase, timeout time.Duration) {
	h.WaitForPhaseAny(registryName, []mcpv1beta1.MCPRegistryPhase{expectedPhase}, timeout)
}

// WaitForPhaseAny waits for an MCPRegistry to reach any of the specified phases
func (h *StatusTestHelper) WaitForPhaseAny(registryName string,
	expectedPhases []mcpv1beta1.MCPRegistryPhase, timeout time.Duration) {
	gomega.Eventually(func() mcpv1beta1.MCPRegistryPhase {
		ginkgo.By(fmt.Sprintf("waiting for registry %s to reach one of phases %v", registryName, expectedPhases))
		registry, err := h.registryHelper.GetRegistry(registryName)
		if err != nil {
			if errors.IsNotFound(err) {
				ginkgo.By(fmt.Sprintf("registry %s not found", registryName))
				return mcpv1beta1.MCPRegistryPhaseTerminating
			}
			return ""
		}
		return registry.Status.Phase
	}, timeout, time.Second).Should(gomega.BeElementOf(expectedPhases),
		"MCPRegistry %s should reach one of phases %v", registryName, expectedPhases)
}


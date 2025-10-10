package operator_test

import (
	"context"
	"fmt"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
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
func (h *StatusTestHelper) WaitForPhase(registryName string, expectedPhase mcpv1alpha1.MCPRegistryPhase, timeout time.Duration) {
	h.WaitForPhaseAny(registryName, []mcpv1alpha1.MCPRegistryPhase{expectedPhase}, timeout)
}

// WaitForPhaseAny waits for an MCPRegistry to reach any of the specified phases
func (h *StatusTestHelper) WaitForPhaseAny(registryName string,
	expectedPhases []mcpv1alpha1.MCPRegistryPhase, timeout time.Duration) {
	gomega.Eventually(func() mcpv1alpha1.MCPRegistryPhase {
		ginkgo.By(fmt.Sprintf("waiting for registry %s to reach one of phases %v", registryName, expectedPhases))
		registry, err := h.registryHelper.GetRegistry(registryName)
		if err != nil {
			if errors.IsNotFound(err) {
				ginkgo.By(fmt.Sprintf("registry %s not found", registryName))
				return mcpv1alpha1.MCPRegistryPhaseTerminating
			}
			return ""
		}
		return registry.Status.Phase
	}, timeout, time.Second).Should(gomega.BeElementOf(expectedPhases),
		"MCPRegistry %s should reach one of phases %v", registryName, expectedPhases)
}

// WaitForCondition waits for a specific condition to have the expected status
func (h *StatusTestHelper) WaitForCondition(registryName, conditionType string,
	expectedStatus metav1.ConditionStatus, timeout time.Duration) {
	gomega.Eventually(func() metav1.ConditionStatus {
		condition, err := h.registryHelper.GetRegistryCondition(registryName, conditionType)
		if err != nil {
			return metav1.ConditionUnknown
		}
		return condition.Status
	}, timeout, time.Second).Should(gomega.Equal(expectedStatus),
		"MCPRegistry %s should have condition %s with status %s", registryName, conditionType, expectedStatus)
}

// WaitForConditionReason waits for a condition to have a specific reason
func (h *StatusTestHelper) WaitForConditionReason(registryName, conditionType, expectedReason string, timeout time.Duration) {
	gomega.Eventually(func() string {
		condition, err := h.registryHelper.GetRegistryCondition(registryName, conditionType)
		if err != nil {
			return ""
		}
		return condition.Reason
	}, timeout, time.Second).Should(gomega.Equal(expectedReason),
		"MCPRegistry %s condition %s should have reason %s", registryName, conditionType, expectedReason)
}

// WaitForServerCount waits for the registry to report a specific server count
func (h *StatusTestHelper) WaitForServerCount(registryName string, expectedCount int, timeout time.Duration) {
	gomega.Eventually(func() int {
		status, err := h.registryHelper.GetRegistryStatus(registryName)
		if err != nil {
			return -1
		}
		return status.SyncStatus.ServerCount
	}, timeout, time.Second).Should(gomega.Equal(expectedCount),
		"MCPRegistry %s should have server count %d", registryName, expectedCount)
}

// WaitForLastSyncTime waits for the registry to update its last sync time
func (h *StatusTestHelper) WaitForLastSyncTime(registryName string, afterTime time.Time, timeout time.Duration) {
	gomega.Eventually(func() bool {
		status, err := h.registryHelper.GetRegistryStatus(registryName)
		if err != nil || status.SyncStatus.LastSyncTime == nil {
			return false
		}
		return status.SyncStatus.LastSyncTime.After(afterTime)
	}, timeout, time.Second).Should(gomega.BeTrue(),
		"MCPRegistry %s should update last sync time after %s", registryName, afterTime)
}

// WaitForLastSyncHash waits for the registry to have a non-empty last sync hash
func (h *StatusTestHelper) WaitForLastSyncHash(registryName string, timeout time.Duration) {
	gomega.Eventually(func() string {
		status, err := h.registryHelper.GetRegistryStatus(registryName)
		if err != nil {
			return ""
		}
		return status.SyncStatus.LastSyncHash
	}, timeout, time.Second).ShouldNot(gomega.BeEmpty(),
		"MCPRegistry %s should have a last sync hash", registryName)
}

// WaitForSyncCompletion waits for a sync operation to complete (either success or failure)
func (h *StatusTestHelper) WaitForSyncCompletion(registryName string, timeout time.Duration) {
	gomega.Eventually(func() bool {
		registry, err := h.registryHelper.GetRegistry(registryName)
		if err != nil {
			return false
		}

		// Check if sync is no longer in progress
		phase := registry.Status.Phase
		return phase == mcpv1alpha1.MCPRegistryPhaseReady ||
			phase == mcpv1alpha1.MCPRegistryPhaseFailed
	}, timeout, time.Second).Should(gomega.BeTrue(),
		"MCPRegistry %s sync operation should complete", registryName)
}

// WaitForManualSyncProcessed waits for a manual sync annotation to be processed
func (h *StatusTestHelper) WaitForManualSyncProcessed(registryName, triggerValue string, timeout time.Duration) {
	gomega.Eventually(func() string {
		status, err := h.registryHelper.GetRegistryStatus(registryName)
		if err != nil {
			return ""
		}
		return status.LastManualSyncTrigger
	}, timeout, time.Second).Should(gomega.Equal(triggerValue),
		"MCPRegistry %s should process manual sync trigger %s", registryName, triggerValue)
}

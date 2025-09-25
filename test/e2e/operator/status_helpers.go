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
	gomega.Eventually(func() mcpv1alpha1.MCPRegistryPhase {
		ginkgo.By(fmt.Sprintf("waiting for registry %s to reach phase %s", registryName, expectedPhase))
		registry, err := h.registryHelper.GetRegistry(registryName)
		if err != nil {
			if errors.IsNotFound(err) {
				ginkgo.By(fmt.Sprintf("registry %s not found", registryName))
				return mcpv1alpha1.MCPRegistryPhaseTerminating
			}
			return ""
		}
		return registry.Status.Phase
	}, timeout, time.Second).Should(gomega.Equal(expectedPhase),
		"MCPRegistry %s should reach phase %s", registryName, expectedPhase)
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

// AssertPhase asserts that an MCPRegistry is currently in the specified phase
func (h *StatusTestHelper) AssertPhase(registryName string, expectedPhase mcpv1alpha1.MCPRegistryPhase) {
	phase, err := h.registryHelper.GetRegistryPhase(registryName)
	gomega.Expect(err).NotTo(gomega.HaveOccurred(), "Failed to get registry phase")
	gomega.Expect(phase).To(gomega.Equal(expectedPhase),
		"MCPRegistry %s should be in phase %s", registryName, expectedPhase)
}

// AssertCondition asserts that a condition has the expected status
func (h *StatusTestHelper) AssertCondition(registryName, conditionType string, expectedStatus metav1.ConditionStatus) {
	condition, err := h.registryHelper.GetRegistryCondition(registryName, conditionType)
	gomega.Expect(err).NotTo(gomega.HaveOccurred(), "Failed to get condition %s", conditionType)
	gomega.Expect(condition.Status).To(gomega.Equal(expectedStatus),
		"Condition %s should have status %s", conditionType, expectedStatus)
}

// AssertConditionReason asserts that a condition has the expected reason
func (h *StatusTestHelper) AssertConditionReason(registryName, conditionType, expectedReason string) {
	condition, err := h.registryHelper.GetRegistryCondition(registryName, conditionType)
	gomega.Expect(err).NotTo(gomega.HaveOccurred(), "Failed to get condition %s", conditionType)
	gomega.Expect(condition.Reason).To(gomega.Equal(expectedReason),
		"Condition %s should have reason %s", conditionType, expectedReason)
}

// AssertServerCount asserts that the registry has the expected server count
func (h *StatusTestHelper) AssertServerCount(registryName string, expectedCount int) {
	status, err := h.registryHelper.GetRegistryStatus(registryName)
	gomega.Expect(err).NotTo(gomega.HaveOccurred(), "Failed to get registry status")
	gomega.Expect(status.SyncStatus.ServerCount).To(gomega.Equal(expectedCount),
		"MCPRegistry %s should have server count %d", registryName, expectedCount)
}

// AssertHasConditions asserts that the registry has all expected condition types
func (h *StatusTestHelper) AssertHasConditions(registryName string, expectedConditions []string) {
	status, err := h.registryHelper.GetRegistryStatus(registryName)
	gomega.Expect(err).NotTo(gomega.HaveOccurred(), "Failed to get registry status")

	actualConditions := make(map[string]bool)
	for _, condition := range status.Conditions {
		actualConditions[condition.Type] = true
	}

	for _, expectedCondition := range expectedConditions {
		gomega.Expect(actualConditions[expectedCondition]).To(gomega.BeTrue(),
			"MCPRegistry %s should have condition %s", registryName, expectedCondition)
	}
}

// AssertStorageRef asserts that the registry has a storage reference configured
func (h *StatusTestHelper) AssertStorageRef(registryName, expectedType string) {
	status, err := h.registryHelper.GetRegistryStatus(registryName)
	gomega.Expect(err).NotTo(gomega.HaveOccurred(), "Failed to get registry status")
	gomega.Expect(status.StorageRef).NotTo(gomega.BeNil(), "Storage reference should be set")
	gomega.Expect(status.StorageRef.Type).To(gomega.Equal(expectedType),
		"Storage reference type should be %s", expectedType)
}

// AssertAPIEndpoint asserts that the registry has an API endpoint configured
func (h *StatusTestHelper) AssertAPIEndpoint(registryName string) {
	status, err := h.registryHelper.GetRegistryStatus(registryName)
	gomega.Expect(err).NotTo(gomega.HaveOccurred(), "Failed to get registry status")
	gomega.Expect(status.APIStatus.Endpoint).NotTo(gomega.BeEmpty(), "API endpoint should be set")
}

// GetConditionMessage returns the message of a specific condition
func (h *StatusTestHelper) GetConditionMessage(registryName, conditionType string) (string, error) {
	condition, err := h.registryHelper.GetRegistryCondition(registryName, conditionType)
	if err != nil {
		return "", err
	}
	return condition.Message, nil
}

// GetStatusMessage returns the current status message
func (h *StatusTestHelper) GetStatusMessage(registryName string) (string, error) {
	status, err := h.registryHelper.GetRegistryStatus(registryName)
	if err != nil {
		return "", err
	}
	return status.Message, nil
}

// PrintStatus prints the current status for debugging purposes
func (h *StatusTestHelper) PrintStatus(registryName string) {
	registry, err := h.registryHelper.GetRegistry(registryName)
	if err != nil {
		fmt.Printf("Failed to get registry %s: %v\n", registryName, err)
		return
	}

	fmt.Printf("=== MCPRegistry %s Status ===\n", registryName)
	fmt.Printf("Phase: %s\n", registry.Status.Phase)
	fmt.Printf("Message: %s\n", registry.Status.Message)
	fmt.Printf("Server Count: %d\n", registry.Status.SyncStatus.ServerCount)
	if registry.Status.SyncStatus.LastSyncTime != nil {
		fmt.Printf("Last Sync Time: %s\n", registry.Status.SyncStatus.LastSyncTime.Format(time.RFC3339))
	}
	fmt.Printf("Last Sync Hash: %s\n", registry.Status.SyncStatus.LastSyncHash)
	fmt.Printf("Sync Attempts: %d\n", registry.Status.SyncStatus.AttemptCount)

	if len(registry.Status.Conditions) > 0 {
		fmt.Printf("Conditions:\n")
		for _, condition := range registry.Status.Conditions {
			fmt.Printf("  - Type: %s, Status: %s, Reason: %s\n",
				condition.Type, condition.Status, condition.Reason)
			if condition.Message != "" {
				fmt.Printf("    Message: %s\n", condition.Message)
			}
		}
	}
	fmt.Printf("==============================\n")
}

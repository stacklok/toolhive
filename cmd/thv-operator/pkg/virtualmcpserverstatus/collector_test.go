// Copyright 2025 Stacklok, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package virtualmcpserverstatus

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
)

func TestStatusCollector_SetPhase(t *testing.T) {
	t.Parallel()

	vmcp := &mcpv1alpha1.VirtualMCPServer{}
	collector := NewStatusManager(vmcp)

	collector.SetPhase(mcpv1alpha1.VirtualMCPServerPhaseReady)

	status := &mcpv1alpha1.VirtualMCPServerStatus{}
	hasUpdates := collector.UpdateStatus(context.Background(), status)

	assert.True(t, hasUpdates)
	assert.Equal(t, mcpv1alpha1.VirtualMCPServerPhaseReady, status.Phase)
}

func TestStatusCollector_SetMessage(t *testing.T) {
	t.Parallel()

	vmcp := &mcpv1alpha1.VirtualMCPServer{}
	collector := NewStatusManager(vmcp)

	collector.SetMessage("test message")

	status := &mcpv1alpha1.VirtualMCPServerStatus{}
	hasUpdates := collector.UpdateStatus(context.Background(), status)

	assert.True(t, hasUpdates)
	assert.Equal(t, "test message", status.Message)
}

func TestStatusCollector_SetURL(t *testing.T) {
	t.Parallel()

	vmcp := &mcpv1alpha1.VirtualMCPServer{}
	collector := NewStatusManager(vmcp)

	collector.SetURL("http://test.example.com")

	status := &mcpv1alpha1.VirtualMCPServerStatus{}
	hasUpdates := collector.UpdateStatus(context.Background(), status)

	assert.True(t, hasUpdates)
	assert.Equal(t, "http://test.example.com", status.URL)
}

func TestStatusCollector_SetObservedGeneration(t *testing.T) {
	t.Parallel()

	vmcp := &mcpv1alpha1.VirtualMCPServer{}
	collector := NewStatusManager(vmcp)

	collector.SetObservedGeneration(42)

	status := &mcpv1alpha1.VirtualMCPServerStatus{}
	hasUpdates := collector.UpdateStatus(context.Background(), status)

	assert.True(t, hasUpdates)
	assert.Equal(t, int64(42), status.ObservedGeneration)
}

func TestStatusCollector_SetGroupRefValidatedCondition(t *testing.T) {
	t.Parallel()

	vmcp := &mcpv1alpha1.VirtualMCPServer{}
	collector := NewStatusManager(vmcp)

	collector.SetGroupRefValidatedCondition("TestReason", "test message", metav1.ConditionTrue)

	status := &mcpv1alpha1.VirtualMCPServerStatus{}
	hasUpdates := collector.UpdateStatus(context.Background(), status)

	assert.True(t, hasUpdates)
	assert.Len(t, status.Conditions, 1)
	assert.Equal(t, mcpv1alpha1.ConditionTypeVirtualMCPServerGroupRefValidated, status.Conditions[0].Type)
	assert.Equal(t, metav1.ConditionTrue, status.Conditions[0].Status)
	assert.Equal(t, "TestReason", status.Conditions[0].Reason)
	assert.Equal(t, "test message", status.Conditions[0].Message)
}

func TestStatusCollector_SetReadyCondition(t *testing.T) {
	t.Parallel()

	vmcp := &mcpv1alpha1.VirtualMCPServer{}
	collector := NewStatusManager(vmcp)

	collector.SetReadyCondition("DeploymentReady", "deployment is ready", metav1.ConditionTrue)

	status := &mcpv1alpha1.VirtualMCPServerStatus{}
	hasUpdates := collector.UpdateStatus(context.Background(), status)

	assert.True(t, hasUpdates)
	assert.Len(t, status.Conditions, 1)
	assert.Equal(t, mcpv1alpha1.ConditionTypeVirtualMCPServerReady, status.Conditions[0].Type)
	assert.Equal(t, metav1.ConditionTrue, status.Conditions[0].Status)
	assert.Equal(t, "DeploymentReady", status.Conditions[0].Reason)
	assert.Equal(t, "deployment is ready", status.Conditions[0].Message)
}

func TestStatusCollector_BatchedUpdates(t *testing.T) {
	t.Parallel()

	vmcp := &mcpv1alpha1.VirtualMCPServer{}
	collector := NewStatusManager(vmcp)

	// Collect multiple changes
	collector.SetPhase(mcpv1alpha1.VirtualMCPServerPhaseReady)
	collector.SetMessage("test message")
	collector.SetURL("http://test.example.com")
	collector.SetObservedGeneration(42)
	collector.SetGroupRefValidatedCondition("TestReason", "group is valid", metav1.ConditionTrue)
	collector.SetReadyCondition("DeploymentReady", "deployment is ready", metav1.ConditionTrue)

	// Apply all at once
	status := &mcpv1alpha1.VirtualMCPServerStatus{}
	hasUpdates := collector.UpdateStatus(context.Background(), status)

	assert.True(t, hasUpdates)
	assert.Equal(t, mcpv1alpha1.VirtualMCPServerPhaseReady, status.Phase)
	assert.Equal(t, "test message", status.Message)
	assert.Equal(t, "http://test.example.com", status.URL)
	assert.Equal(t, int64(42), status.ObservedGeneration)
	assert.Len(t, status.Conditions, 2)
}

func TestStatusCollector_NoChanges(t *testing.T) {
	t.Parallel()

	vmcp := &mcpv1alpha1.VirtualMCPServer{}
	collector := NewStatusManager(vmcp)

	// Don't set any changes
	status := &mcpv1alpha1.VirtualMCPServerStatus{}
	hasUpdates := collector.UpdateStatus(context.Background(), status)

	assert.False(t, hasUpdates)
}

func TestStatusCollector_MultipleConditions(t *testing.T) {
	t.Parallel()

	vmcp := &mcpv1alpha1.VirtualMCPServer{}
	collector := NewStatusManager(vmcp)

	collector.SetGroupRefValidatedCondition("GroupValid", "group is valid", metav1.ConditionTrue)
	collector.SetReadyCondition("DeploymentReady", "deployment is ready", metav1.ConditionTrue)

	status := &mcpv1alpha1.VirtualMCPServerStatus{}
	hasUpdates := collector.UpdateStatus(context.Background(), status)

	assert.True(t, hasUpdates)
	assert.Len(t, status.Conditions, 2)

	// Verify both conditions are present
	conditionTypes := make(map[string]bool)
	for _, cond := range status.Conditions {
		conditionTypes[cond.Type] = true
	}
	assert.True(t, conditionTypes[mcpv1alpha1.ConditionTypeVirtualMCPServerGroupRefValidated])
	assert.True(t, conditionTypes[mcpv1alpha1.ConditionTypeVirtualMCPServerReady])
}

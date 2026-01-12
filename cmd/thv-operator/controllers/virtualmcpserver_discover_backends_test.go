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

package controllers

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	"github.com/stacklok/toolhive/cmd/thv-operator/pkg/virtualmcpserverstatus"
	vmcpconfig "github.com/stacklok/toolhive/pkg/vmcp/config"
)

// TestVirtualMCPServerStatusManagerDiscoveredBackends tests that StatusManager
// correctly sets discovered backends and backend count
func TestVirtualMCPServerStatusManagerDiscoveredBackends(t *testing.T) {
	t.Parallel()

	vmcp := &mcpv1alpha1.VirtualMCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "test-vmcp",
			Namespace:  "default",
			Generation: 1,
		},
		Spec: mcpv1alpha1.VirtualMCPServerSpec{
			Config: vmcpconfig.Config{Group: "test-group"},
		},
	}

	statusManager := virtualmcpserverstatus.NewStatusManager(vmcp)

	discoveredBackends := []mcpv1alpha1.DiscoveredBackend{
		{
			Name:            "backend-1",
			Status:          "ready",
			URL:             "http://backend-1.default.svc.cluster.local:8080",
			LastHealthCheck: metav1.Now(),
		},
		{
			Name:            "backend-2",
			Status:          "ready",
			URL:             "http://backend-2.default.svc.cluster.local:8080",
			LastHealthCheck: metav1.Now(),
		},
		{
			Name:            "backend-3",
			Status:          "unavailable",
			LastHealthCheck: metav1.Now(),
		},
	}

	statusManager.SetDiscoveredBackends(discoveredBackends)

	// Create a fresh status object to update
	vmcpStatus := &mcpv1alpha1.VirtualMCPServerStatus{}

	ctx := context.Background()
	updated := statusManager.UpdateStatus(ctx, vmcpStatus)

	assert.True(t, updated, "status should be updated")
	assert.Len(t, vmcpStatus.DiscoveredBackends, 3, "should have 3 discovered backends")
	assert.Equal(t, 2, vmcpStatus.BackendCount, "backendCount should be 2 (only ready backends)")

	// Verify backend details
	assert.Equal(t, "backend-1", vmcpStatus.DiscoveredBackends[0].Name)
	assert.Equal(t, "ready", vmcpStatus.DiscoveredBackends[0].Status)
	assert.Equal(t, "http://backend-1.default.svc.cluster.local:8080", vmcpStatus.DiscoveredBackends[0].URL)

	assert.Equal(t, "backend-2", vmcpStatus.DiscoveredBackends[1].Name)
	assert.Equal(t, "ready", vmcpStatus.DiscoveredBackends[1].Status)

	assert.Equal(t, "backend-3", vmcpStatus.DiscoveredBackends[2].Name)
	assert.Equal(t, "unavailable", vmcpStatus.DiscoveredBackends[2].Status)
	assert.Empty(t, vmcpStatus.DiscoveredBackends[2].URL, "unavailable backend should not have URL")
}

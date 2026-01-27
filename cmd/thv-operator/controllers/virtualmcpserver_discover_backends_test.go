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
// correctly sets discovered backends in the status
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
	}

	statusManager.SetDiscoveredBackends(discoveredBackends)

	// Apply status changes to a status object
	status := &mcpv1alpha1.VirtualMCPServerStatus{}
	updated := statusManager.UpdateStatus(context.Background(), status)

	assert.True(t, updated, "UpdateStatus should return true when backends are set")
	assert.Equal(t, 2, status.BackendCount, "BackendCount should be set to 2")
	assert.Equal(t, discoveredBackends, status.DiscoveredBackends, "DiscoveredBackends should match")
}

// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package status provides abstractions for vMCP runtime status reporting.
package status

import (
	"context"
	"fmt"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	"github.com/stacklok/toolhive/pkg/logger"
	vmcptypes "github.com/stacklok/toolhive/pkg/vmcp"
)

// K8sReporter implements Reporter for Kubernetes environments.
// It updates the VirtualMCPServer/status subresource with runtime status information.
type K8sReporter struct {
	client    client.Client
	name      string
	namespace string
}

// NewK8sReporter creates a new K8sReporter instance.
//
// Parameters:
//   - restConfig: Kubernetes REST config for creating the client
//   - name: Name of the VirtualMCPServer resource
//   - namespace: Namespace of the VirtualMCPServer resource
//
// Returns a K8sReporter and any error encountered during client creation.
func NewK8sReporter(restConfig *rest.Config, name, namespace string) (*K8sReporter, error) {
	if restConfig == nil {
		return nil, fmt.Errorf("restConfig cannot be nil")
	}
	if name == "" {
		return nil, fmt.Errorf("name cannot be empty")
	}
	if namespace == "" {
		return nil, fmt.Errorf("namespace cannot be empty")
	}

	// Create scheme and register VirtualMCPServer types
	scheme := runtime.NewScheme()
	if err := mcpv1alpha1.AddToScheme(scheme); err != nil {
		return nil, fmt.Errorf("failed to add VirtualMCPServer types to scheme: %w", err)
	}

	// Create Kubernetes client
	k8sClient, err := client.New(restConfig, client.Options{
		Scheme: scheme,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create Kubernetes client: %w", err)
	}

	return &K8sReporter{
		client:    k8sClient,
		name:      name,
		namespace: namespace,
	}, nil
}

// ReportStatus sends a status update to the VirtualMCPServer/status subresource.
// This method uses optimistic concurrency control with automatic retries on conflicts.
func (r *K8sReporter) ReportStatus(ctx context.Context, status *vmcptypes.Status) error {
	if validateStatus(status) {
		return nil
	}

	namespacedName := types.NamespacedName{
		Name:      r.name,
		Namespace: r.namespace,
	}

	// Use retry logic to handle concurrent updates gracefully.
	// If the resource is modified between Get() and Update(), Kubernetes will reject
	// the update with a conflict error, and retry.RetryOnConflict will automatically retry.
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		// Get the latest version of the VirtualMCPServer resource
		vmcpServer := &mcpv1alpha1.VirtualMCPServer{}
		if err := r.client.Get(ctx, namespacedName, vmcpServer); err != nil {
			return fmt.Errorf("failed to get VirtualMCPServer: %w", err)
		}

		// Convert vmcp.Status to VirtualMCPServerStatus
		r.updateStatus(vmcpServer, status)

		// Update the status subresource (may return conflict error if resource was modified)
		return r.client.Status().Update(ctx, vmcpServer)
	})

	if err != nil {
		logger.Errorf("Failed to update VirtualMCPServer status for %s/%s after retries: %v",
			r.namespace, r.name, err)
		return fmt.Errorf("failed to update status: %w", err)
	}

	logger.Debugw("updated VirtualMCPServer status",
		"namespace", r.namespace,
		"name", r.name,
		"phase", status.Phase)
	return nil
}

// Start initializes the reporter.
// Returns a shutdown function for cleanup (no-op for K8sReporter since it's stateless).
func (*K8sReporter) Start(_ context.Context) (func(context.Context) error, error) {
	logReporterStart("K8s", "updates VirtualMCPServer/status")
	return noOpShutdown("K8s"), nil
}

// updateStatus converts vmcp.Status to VirtualMCPServerStatus and updates the resource.
// Note: This method does NOT update the URL field, as that is infrastructure-level
// status owned by the operator (the external service URL). The vMCP runtime only
// reports operational status (phase, backends, conditions).
func (*K8sReporter) updateStatus(vmcpServer *mcpv1alpha1.VirtualMCPServer, status *vmcptypes.Status) {
	// Update phase
	vmcpServer.Status.Phase = convertPhase(status.Phase)

	// Update message
	vmcpServer.Status.Message = status.Message

	// Update backend count (only counts healthy/ready backends)
	vmcpServer.Status.BackendCount = status.BackendCount

	// Update discovered backends
	vmcpServer.Status.DiscoveredBackends = make([]mcpv1alpha1.DiscoveredBackend, 0, len(status.DiscoveredBackends))
	for _, backend := range status.DiscoveredBackends {
		// Convert vmcp.DiscoveredBackend to mcpv1alpha1.DiscoveredBackend
		// Both types have identical fields, so we can use type conversion
		vmcpServer.Status.DiscoveredBackends = append(vmcpServer.Status.DiscoveredBackends,
			mcpv1alpha1.DiscoveredBackend(backend))
	}

	// Update conditions
	vmcpServer.Status.Conditions = status.Conditions

	// Update observed generation
	vmcpServer.Status.ObservedGeneration = vmcpServer.Generation
}

// convertPhase converts vmcp.Phase to VirtualMCPServerPhase.
func convertPhase(phase vmcptypes.Phase) mcpv1alpha1.VirtualMCPServerPhase {
	switch phase {
	case vmcptypes.PhaseReady:
		return mcpv1alpha1.VirtualMCPServerPhaseReady
	case vmcptypes.PhaseDegraded:
		return mcpv1alpha1.VirtualMCPServerPhaseDegraded
	case vmcptypes.PhaseFailed:
		return mcpv1alpha1.VirtualMCPServerPhaseFailed
	case vmcptypes.PhasePending:
		return mcpv1alpha1.VirtualMCPServerPhasePending
	default:
		return mcpv1alpha1.VirtualMCPServerPhasePending
	}
}

// Verify K8sReporter implements Reporter interface
var _ Reporter = (*K8sReporter)(nil)

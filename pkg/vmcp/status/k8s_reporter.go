// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package status provides abstractions for vMCP runtime status reporting.
package status

import (
	"context"
	"fmt"
	"log/slog"

	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
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

	// Create scheme and register Kubernetes core types and custom CRD types
	runtimeScheme := runtime.NewScheme()

	// Register standard Kubernetes types (Pods, Services, etc.)
	if err := clientgoscheme.AddToScheme(runtimeScheme); err != nil {
		return nil, fmt.Errorf("failed to add client-go scheme: %w", err)
	}

	// Register VirtualMCPServer CRD types
	if err := mcpv1beta1.AddToScheme(runtimeScheme); err != nil {
		return nil, fmt.Errorf("failed to add VirtualMCPServer types to scheme: %w", err)
	}

	// Create Kubernetes client
	k8sClient, err := client.New(restConfig, client.Options{
		Scheme: runtimeScheme,
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
	if shouldSkipStatus(status) {
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
		vmcpServer := &mcpv1beta1.VirtualMCPServer{}
		if err := r.client.Get(ctx, namespacedName, vmcpServer); err != nil {
			return fmt.Errorf("failed to get VirtualMCPServer: %w", err)
		}

		// Convert vmcp.Status to VirtualMCPServerStatus
		r.updateStatus(vmcpServer, status)

		// Update the status subresource (may return conflict error if resource was modified)
		return r.client.Status().Update(ctx, vmcpServer)
	})

	if err != nil {
		slog.Error("failed to update VirtualMCPServer status after retries", "namespace", r.namespace, "name", r.name, "error", err)
		return fmt.Errorf("failed to update status: %w", err)
	}

	slog.Debug("updated VirtualMCPServer status",
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

// updateStatus converts vmcp.Status into the runtime-owned status snapshot.
// The operator projects this snapshot into the public top-level fields, keeping
// the top-level Conditions array under a single writer.
func (*K8sReporter) updateStatus(vmcpServer *mcpv1beta1.VirtualMCPServer, status *vmcptypes.Status) {
	if vmcpServer.Status.Runtime == nil {
		vmcpServer.Status.Runtime = &mcpv1beta1.VirtualMCPServerRuntimeStatus{}
	}
	runtimeStatus := vmcpServer.Status.Runtime
	runtimeStatus.Phase = convertPhase(status.Phase)
	runtimeStatus.Message = status.Message
	runtimeStatus.BackendCount = status.BackendCount
	runtimeStatus.DiscoveredBackends = make([]mcpv1beta1.DiscoveredBackend, 0, len(status.DiscoveredBackends))
	for _, backend := range status.DiscoveredBackends {
		runtimeStatus.DiscoveredBackends = append(runtimeStatus.DiscoveredBackends,
			mcpv1beta1.DiscoveredBackend(backend))
	}

	newConditionTypes := make(map[string]bool, len(status.Conditions))
	for _, condition := range status.Conditions {
		newConditionTypes[condition.Type] = true
	}
	for _, conditionType := range []string{"Ready", "Degraded", "BackendsDiscovered"} {
		if !newConditionTypes[conditionType] {
			if conditionType == "Ready" || conditionType == "BackendsDiscovered" {
				slog.Warn("core condition missing from new status - this may indicate a bug in status building", "condition", conditionType)
			}
			meta.RemoveStatusCondition(&runtimeStatus.Conditions, conditionType)
		}
	}
	for _, condition := range status.Conditions {
		meta.SetStatusCondition(&runtimeStatus.Conditions, condition)
	}
}

// convertPhase converts vmcp.Phase to VirtualMCPServerPhase.
func convertPhase(phase vmcptypes.Phase) mcpv1beta1.VirtualMCPServerPhase {
	switch phase {
	case vmcptypes.PhaseReady:
		return mcpv1beta1.VirtualMCPServerPhaseReady
	case vmcptypes.PhaseDegraded:
		return mcpv1beta1.VirtualMCPServerPhaseDegraded
	case vmcptypes.PhaseFailed:
		return mcpv1beta1.VirtualMCPServerPhaseFailed
	case vmcptypes.PhasePending:
		return mcpv1beta1.VirtualMCPServerPhasePending
	default:
		return mcpv1beta1.VirtualMCPServerPhasePending
	}
}

// Verify K8sReporter implements Reporter interface
var _ Reporter = (*K8sReporter)(nil)

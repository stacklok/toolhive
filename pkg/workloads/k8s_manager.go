// Package workloads provides a Kubernetes-based implementation of the Manager interface.
// This file contains the Kubernetes implementation for operator environments.
package workloads

import (
	"context"
	"fmt"
	"time"

	"golang.org/x/sync/errgroup"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/selection"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	rt "github.com/stacklok/toolhive/pkg/container/runtime"
	"github.com/stacklok/toolhive/pkg/core"
	"github.com/stacklok/toolhive/pkg/logger"
	"github.com/stacklok/toolhive/pkg/runner"
	"github.com/stacklok/toolhive/pkg/transport"
	transporttypes "github.com/stacklok/toolhive/pkg/transport/types"
	workloadtypes "github.com/stacklok/toolhive/pkg/workloads/types"
)

// k8sManager implements the Manager interface for Kubernetes environments.
// In Kubernetes, the operator manages workload lifecycle via MCPServer CRDs.
// This manager provides read-only operations and CRD-based storage.
type k8sManager struct {
	k8sClient client.Client
	namespace string
}

// NewK8SManager creates a new Kubernetes-based workload manager.
func NewK8SManager(k8sClient client.Client, namespace string) (Manager, error) {
	return &k8sManager{
		k8sClient: k8sClient,
		namespace: namespace,
	}, nil
}

func (k *k8sManager) GetWorkload(ctx context.Context, workloadName string) (core.Workload, error) {
	mcpServer := &mcpv1alpha1.MCPServer{}
	key := types.NamespacedName{Name: workloadName, Namespace: k.namespace}
	if err := k.k8sClient.Get(ctx, key, mcpServer); err != nil {
		if errors.IsNotFound(err) {
			return core.Workload{}, fmt.Errorf("%w: %s", rt.ErrWorkloadNotFound, workloadName)
		}
		return core.Workload{}, fmt.Errorf("failed to get MCPServer: %w", err)
	}

	return k.mcpServerToWorkload(mcpServer)
}

func (k *k8sManager) DoesWorkloadExist(ctx context.Context, workloadName string) (bool, error) {
	mcpServer := &mcpv1alpha1.MCPServer{}
	key := types.NamespacedName{Name: workloadName, Namespace: k.namespace}
	if err := k.k8sClient.Get(ctx, key, mcpServer); err != nil {
		if errors.IsNotFound(err) {
			return false, nil
		}
		return false, fmt.Errorf("failed to check if workload exists: %w", err)
	}
	return true, nil
}

func (k *k8sManager) ListWorkloads(ctx context.Context, listAll bool, labelFilters ...string) ([]core.Workload, error) {
	mcpServerList := &mcpv1alpha1.MCPServerList{}
	listOpts := []client.ListOption{
		client.InNamespace(k.namespace),
	}

	// Parse label filters if provided
	if len(labelFilters) > 0 {
		parsedFilters, err := workloadtypes.ParseLabelFilters(labelFilters)
		if err != nil {
			return nil, fmt.Errorf("failed to parse label filters: %w", err)
		}

		// Build label selector from filters (equality matching)
		labelSelector := labels.NewSelector()
		for key, value := range parsedFilters {
			requirement, err := labels.NewRequirement(key, selection.Equals, []string{value})
			if err != nil {
				return nil, fmt.Errorf("failed to create label requirement: %w", err)
			}
			labelSelector = labelSelector.Add(*requirement)
		}
		listOpts = append(listOpts, client.MatchingLabelsSelector{Selector: labelSelector})
	}

	if err := k.k8sClient.List(ctx, mcpServerList, listOpts...); err != nil {
		return nil, fmt.Errorf("failed to list MCPServers: %w", err)
	}

	var workloads []core.Workload
	for i := range mcpServerList.Items {
		mcpServer := &mcpServerList.Items[i]

		// Filter by status if listAll is false
		if !listAll {
			phase := mcpServer.Status.Phase
			if phase != mcpv1alpha1.MCPServerPhaseRunning {
				continue
			}
		}

		workload, err := k.mcpServerToWorkload(mcpServer)
		if err != nil {
			logger.Warnf("Failed to convert MCPServer %s to workload: %v", mcpServer.Name, err)
			continue
		}

		workloads = append(workloads, workload)
	}

	return workloads, nil
}

// StopWorkloads is a no-op in Kubernetes mode.
// The operator manages workload lifecycle via MCPServer CRDs.
func (*k8sManager) StopWorkloads(_ context.Context, _ []string) (*errgroup.Group, error) {
	logger.Warnf("StopWorkloads is not supported in Kubernetes mode. Use kubectl to manage MCPServer CRDs.")
	group := &errgroup.Group{}
	// Return empty group - no operations to perform
	return group, nil
}

// RunWorkload is a no-op in Kubernetes mode.
// Workloads are created via MCPServer CRDs managed by the operator.
func (*k8sManager) RunWorkload(_ context.Context, _ *runner.RunConfig) error {
	return fmt.Errorf("RunWorkload is not supported in Kubernetes mode. Create MCPServer CRD instead")
}

// RunWorkloadDetached is a no-op in Kubernetes mode.
// Workloads are created via MCPServer CRDs managed by the operator.
func (*k8sManager) RunWorkloadDetached(_ context.Context, _ *runner.RunConfig) error {
	return fmt.Errorf("RunWorkloadDetached is not supported in Kubernetes mode. Create MCPServer CRD instead")
}

// DeleteWorkloads is a no-op in Kubernetes mode.
// The operator manages workload lifecycle via MCPServer CRDs.
func (*k8sManager) DeleteWorkloads(_ context.Context, _ []string) (*errgroup.Group, error) {
	logger.Warnf("DeleteWorkloads is not supported in Kubernetes mode. Use kubectl to delete MCPServer CRDs.")
	group := &errgroup.Group{}
	// Return empty group - no operations to perform
	return group, nil
}

// RestartWorkloads is a no-op in Kubernetes mode.
// The operator manages workload lifecycle via MCPServer CRDs.
func (*k8sManager) RestartWorkloads(_ context.Context, _ []string, _ bool) (*errgroup.Group, error) {
	logger.Warnf("RestartWorkloads is not supported in Kubernetes mode. Use kubectl to restart MCPServer CRDs.")
	group := &errgroup.Group{}
	// Return empty group - no operations to perform
	return group, nil
}

// UpdateWorkload is a no-op in Kubernetes mode.
// The operator manages workload lifecycle via MCPServer CRDs.
func (*k8sManager) UpdateWorkload(_ context.Context, _ string, _ *runner.RunConfig) (*errgroup.Group, error) {
	logger.Warnf("UpdateWorkload is not supported in Kubernetes mode. Update MCPServer CRD instead.")
	group := &errgroup.Group{}
	// Return empty group - no operations to perform
	return group, nil
}

// GetLogs retrieves logs from the pod associated with the MCPServer.
// Note: This requires a Kubernetes clientset for log streaming.
// For now, this returns an error indicating logs should be retrieved via kubectl.
// TODO: Implement proper log retrieval using clientset or REST client.
func (k *k8sManager) GetLogs(_ context.Context, _ string, follow bool) (string, error) {
	if follow {
		return "", fmt.Errorf("follow mode is not supported. Use 'kubectl logs -f <pod-name> -n %s' to stream logs", k.namespace)
	}
	return "", fmt.Errorf(
		"GetLogs is not fully implemented in Kubernetes mode. Use 'kubectl logs <pod-name> -n %s' to retrieve logs",
		k.namespace)
}

// GetProxyLogs retrieves logs from the proxy container in the pod associated with the MCPServer.
// Note: This requires a Kubernetes clientset for log streaming.
// For now, this returns an error indicating logs should be retrieved via kubectl.
// TODO: Implement proper log retrieval using clientset or REST client.
func (k *k8sManager) GetProxyLogs(_ context.Context, _ string) (string, error) {
	return "", fmt.Errorf(
		"GetProxyLogs is not fully implemented in Kubernetes mode. Use 'kubectl logs <pod-name> -c proxy -n %s' to retrieve proxy logs",
		k.namespace)
}

// MoveToGroup moves the specified workloads from one group to another.
func (k *k8sManager) MoveToGroup(ctx context.Context, workloadNames []string, groupFrom string, groupTo string) error {
	for _, name := range workloadNames {
		mcpServer := &mcpv1alpha1.MCPServer{}
		key := types.NamespacedName{Name: name, Namespace: k.namespace}
		if err := k.k8sClient.Get(ctx, key, mcpServer); err != nil {
			if errors.IsNotFound(err) {
				return fmt.Errorf("MCPServer %s not found", name)
			}
			return fmt.Errorf("failed to get MCPServer: %w", err)
		}

		// Verify the workload is in the expected group
		if mcpServer.Spec.GroupRef != groupFrom {
			return fmt.Errorf("workload %s is not in group %s (current group: %s)", name, groupFrom, mcpServer.Spec.GroupRef)
		}

		// Update the group
		mcpServer.Spec.GroupRef = groupTo

		// Update the MCPServer
		if err := k.k8sClient.Update(ctx, mcpServer); err != nil {
			return fmt.Errorf("failed to update MCPServer %s: %w", name, err)
		}
	}

	return nil
}

// ListWorkloadsInGroup returns all workload names that belong to the specified group.
func (k *k8sManager) ListWorkloadsInGroup(ctx context.Context, groupName string) ([]string, error) {
	mcpServerList := &mcpv1alpha1.MCPServerList{}
	listOpts := []client.ListOption{
		client.InNamespace(k.namespace),
	}

	if err := k.k8sClient.List(ctx, mcpServerList, listOpts...); err != nil {
		return nil, fmt.Errorf("failed to list MCPServers: %w", err)
	}

	var groupWorkloads []string
	for i := range mcpServerList.Items {
		mcpServer := &mcpServerList.Items[i]
		if mcpServer.Spec.GroupRef == groupName {
			groupWorkloads = append(groupWorkloads, mcpServer.Name)
		}
	}

	return groupWorkloads, nil
}

// mcpServerToWorkload converts an MCPServer CRD to a core.Workload.
func (k *k8sManager) mcpServerToWorkload(mcpServer *mcpv1alpha1.MCPServer) (core.Workload, error) {
	// Map MCPServerPhase to runtime.WorkloadStatus
	status := k.mcpServerPhaseToWorkloadStatus(mcpServer.Status.Phase)

	// Parse transport type
	transportType, err := transporttypes.ParseTransportType(mcpServer.Spec.Transport)
	if err != nil {
		logger.Warnf("Failed to parse transport type %s for MCPServer %s: %v", mcpServer.Spec.Transport, mcpServer.Name, err)
		transportType = transporttypes.TransportTypeSSE
	}

	// Calculate effective proxy mode
	effectiveProxyMode := workloadtypes.GetEffectiveProxyMode(transportType, mcpServer.Spec.ProxyMode)

	// Generate URL from status or reconstruct from spec
	url := mcpServer.Status.URL
	if url == "" {
		port := int(mcpServer.Spec.ProxyPort)
		if port == 0 {
			port = int(mcpServer.Spec.Port) // Fallback to deprecated Port field
		}
		if port > 0 {
			url = transport.GenerateMCPServerURL(mcpServer.Spec.Transport, transport.LocalhostIPv4, port, mcpServer.Name, "")
		}
	}

	port := int(mcpServer.Spec.ProxyPort)
	if port == 0 {
		port = int(mcpServer.Spec.Port) // Fallback to deprecated Port field
	}

	// Get tools filter from spec
	toolsFilter := mcpServer.Spec.ToolsFilter
	if mcpServer.Spec.ToolConfigRef != nil {
		// If ToolConfigRef is set, we can't reconstruct the tools filter here
		// The tools filter would be resolved by the operator
		toolsFilter = []string{}
	}

	// Extract user labels from annotations (Kubernetes doesn't have container labels like Docker)
	userLabels := make(map[string]string)
	if mcpServer.Annotations != nil {
		// Filter out standard Kubernetes annotations
		for key, value := range mcpServer.Annotations {
			if !k.isStandardK8sAnnotation(key) {
				userLabels[key] = value
			}
		}
	}

	// Get creation timestamp
	createdAt := mcpServer.CreationTimestamp.Time
	if createdAt.IsZero() {
		createdAt = time.Now()
	}

	return core.Workload{
		Name:          mcpServer.Name,
		Package:       mcpServer.Spec.Image,
		URL:           url,
		ToolType:      "mcp",
		TransportType: transportType,
		ProxyMode:     effectiveProxyMode,
		Status:        status,
		StatusContext: mcpServer.Status.Message,
		CreatedAt:     createdAt,
		Port:          port,
		Labels:        userLabels,
		Group:         mcpServer.Spec.GroupRef,
		ToolsFilter:   toolsFilter,
		Remote:        false, // MCPServers are always container workloads in Kubernetes
	}, nil
}

// mcpServerPhaseToWorkloadStatus maps MCPServerPhase to runtime.WorkloadStatus.
func (*k8sManager) mcpServerPhaseToWorkloadStatus(phase mcpv1alpha1.MCPServerPhase) rt.WorkloadStatus {
	switch phase {
	case mcpv1alpha1.MCPServerPhaseRunning:
		return rt.WorkloadStatusRunning
	case mcpv1alpha1.MCPServerPhasePending:
		return rt.WorkloadStatusStarting
	case mcpv1alpha1.MCPServerPhaseFailed:
		return rt.WorkloadStatusError
	case mcpv1alpha1.MCPServerPhaseTerminating:
		return rt.WorkloadStatusStopping
	default:
		return rt.WorkloadStatusUnknown
	}
}

// isStandardK8sAnnotation checks if an annotation key is a standard Kubernetes annotation.
func (*k8sManager) isStandardK8sAnnotation(key string) bool {
	// Common Kubernetes annotation prefixes
	standardPrefixes := []string{
		"kubectl.kubernetes.io/",
		"kubernetes.io/",
		"deployment.kubernetes.io/",
		"k8s.io/",
	}

	for _, prefix := range standardPrefixes {
		if len(key) >= len(prefix) && key[:len(prefix)] == prefix {
			return true
		}
	}

	return false
}

// Package k8s provides Kubernetes-specific workload management.
// This file contains the Kubernetes implementation for operator environments.
package k8s

import (
	"context"
	"fmt"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/selection"
	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	"github.com/stacklok/toolhive/pkg/container/kubernetes"
	"github.com/stacklok/toolhive/pkg/logger"
	"github.com/stacklok/toolhive/pkg/transport"
	transporttypes "github.com/stacklok/toolhive/pkg/transport/types"
	workloadtypes "github.com/stacklok/toolhive/pkg/workloads/types"
)

// manager implements the Manager interface for Kubernetes environments.
// In Kubernetes, the operator manages workload lifecycle via MCPServer CRDs.
// This manager provides read-only operations and CRD-based storage.
type manager struct {
	k8sClient client.Client
	namespace string
}

// NewManager creates a new Kubernetes-based workload manager.
func NewManager(k8sClient client.Client, namespace string) (Manager, error) {
	return &manager{
		k8sClient: k8sClient,
		namespace: namespace,
	}, nil
}

// NewManagerFromContext creates a Kubernetes-based workload manager from context.
// It automatically sets up the Kubernetes client and detects the namespace.
func NewManagerFromContext(_ context.Context) (Manager, error) {
	// Create a scheme for controller-runtime client
	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(mcpv1alpha1.AddToScheme(scheme))

	// Get Kubernetes config
	cfg, err := ctrl.GetConfig()
	if err != nil {
		return nil, fmt.Errorf("failed to get Kubernetes config: %w", err)
	}

	// Create controller-runtime client
	k8sClient, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		return nil, fmt.Errorf("failed to create Kubernetes client: %w", err)
	}

	// Detect namespace
	namespace := kubernetes.GetCurrentNamespace()

	return NewManager(k8sClient, namespace)
}

func (k *manager) GetWorkload(ctx context.Context, workloadName string) (Workload, error) {
	mcpServer := &mcpv1alpha1.MCPServer{}
	key := types.NamespacedName{Name: workloadName, Namespace: k.namespace}
	if err := k.k8sClient.Get(ctx, key, mcpServer); err != nil {
		if errors.IsNotFound(err) {
			return Workload{}, fmt.Errorf("MCPServer %s not found", workloadName)
		}
		return Workload{}, fmt.Errorf("failed to get MCPServer: %w", err)
	}

	return k.mcpServerToWorkload(mcpServer)
}

func (k *manager) DoesWorkloadExist(ctx context.Context, workloadName string) (bool, error) {
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

func (k *manager) ListWorkloads(ctx context.Context, listAll bool, labelFilters ...string) ([]Workload, error) {
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

	var workloads []Workload
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

// Note: The following operations are not part of Manager interface:
// - StopWorkloads: Use kubectl to manage MCPServer CRDs
// - RunWorkload: Create MCPServer CRD instead
// - RunWorkloadDetached: Create MCPServer CRD instead
// - DeleteWorkloads: Use kubectl to delete MCPServer CRDs
// - RestartWorkloads: Use kubectl to restart MCPServer CRDs
// - UpdateWorkload: Update MCPServer CRD directly
// - GetLogs: Use 'kubectl logs <pod-name> -n <namespace>' to retrieve logs
// - GetProxyLogs: Use 'kubectl logs <pod-name> -c proxy -n <namespace>' to retrieve proxy logs

// MoveToGroup moves the specified workloads from one group to another.
func (k *manager) MoveToGroup(ctx context.Context, workloadNames []string, groupFrom string, groupTo string) error {
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
func (k *manager) ListWorkloadsInGroup(ctx context.Context, groupName string) ([]string, error) {
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

// mcpServerToWorkload converts an MCPServer CRD to a Workload.
func (k *manager) mcpServerToWorkload(mcpServer *mcpv1alpha1.MCPServer) (Workload, error) {
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

	return Workload{
		Name:          mcpServer.Name,
		Namespace:     mcpServer.Namespace,
		Package:       mcpServer.Spec.Image,
		URL:           url,
		ToolType:      "mcp",
		TransportType: transportType,
		ProxyMode:     effectiveProxyMode,
		Phase:         mcpServer.Status.Phase,
		StatusContext: mcpServer.Status.Message,
		CreatedAt:     createdAt,
		Port:          port,
		Labels:        userLabels,
		Group:         mcpServer.Spec.GroupRef,
		GroupRef:      mcpServer.Spec.GroupRef,
		ToolsFilter:   toolsFilter,
	}, nil
}

// isStandardK8sAnnotation checks if an annotation key is a standard Kubernetes annotation.
func (*manager) isStandardK8sAnnotation(key string) bool {
	// Common Kubernetes annotation prefixes
	standardPrefixes := []string{
		"kubectl.kubernetes.io/",
		"kubernetes.io/",
		"deployment.kubernetes.io/",
		"k8s.io/",
	}

	for _, prefix := range standardPrefixes {
		if strings.HasPrefix(key, prefix) {
			return true
		}
	}
	return false
}

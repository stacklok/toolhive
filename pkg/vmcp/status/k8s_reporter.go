// Package status provides abstractions for vMCP runtime status reporting.
package status

import (
	"context"
	"fmt"
	"sync"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	"github.com/stacklok/toolhive/pkg/logger"
	"github.com/stacklok/toolhive/pkg/vmcp"
)

// K8sReporter implements Reporter for Kubernetes environments.
// It updates the VirtualMCPServer/status subresource with runtime status information.
type K8sReporter struct {
	client    client.Client
	name      string
	namespace string

	// Ticker and context for periodic reporting
	ticker   *time.Ticker
	stopChan chan struct{}
	wg       sync.WaitGroup
	mu       sync.Mutex
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
		stopChan:  make(chan struct{}),
	}, nil
}

// Report sends a status update to the VirtualMCPServer/status subresource.
// This method is non-blocking and returns quickly.
func (r *K8sReporter) Report(ctx context.Context, runtimeStatus *RuntimeStatus) error {
	if runtimeStatus == nil {
		return fmt.Errorf("runtimeStatus cannot be nil")
	}

	// Get the VirtualMCPServer resource
	vmcpServer := &mcpv1alpha1.VirtualMCPServer{}
	namespacedName := types.NamespacedName{
		Name:      r.name,
		Namespace: r.namespace,
	}

	if err := r.client.Get(ctx, namespacedName, vmcpServer); err != nil {
		logger.Errorf("Failed to get VirtualMCPServer %s/%s: %v", r.namespace, r.name, err)
		return fmt.Errorf("failed to get VirtualMCPServer: %w", err)
	}

	// Convert RuntimeStatus to VirtualMCPServerStatus
	r.updateStatus(vmcpServer, runtimeStatus)

	// Update the status subresource
	if err := r.client.Status().Update(ctx, vmcpServer); err != nil {
		logger.Errorf("Failed to update VirtualMCPServer status for %s/%s: %v", r.namespace, r.name, err)
		return fmt.Errorf("failed to update status: %w", err)
	}

	logger.Debugf("Successfully updated VirtualMCPServer status for %s/%s (phase: %s)",
		r.namespace, r.name, runtimeStatus.Phase)
	return nil
}

// Start begins periodic status reporting.
// The reporter will call statusFunc at the specified interval to retrieve
// the current status and report it to Kubernetes.
func (r *K8sReporter) Start(ctx context.Context, interval time.Duration, statusFunc func() *RuntimeStatus) error {
	if statusFunc == nil {
		return fmt.Errorf("statusFunc cannot be nil")
	}
	if interval <= 0 {
		return fmt.Errorf("interval must be positive")
	}

	r.mu.Lock()
	if r.ticker != nil {
		r.mu.Unlock()
		return fmt.Errorf("reporter already started")
	}

	r.ticker = time.NewTicker(interval)
	r.mu.Unlock()

	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		logger.Infof("K8sReporter started for %s/%s (interval: %s)", r.namespace, r.name, interval)

		for {
			select {
			case <-r.stopChan:
				logger.Infof("K8sReporter stopped for %s/%s", r.namespace, r.name)
				return
			case <-ctx.Done():
				logger.Infof("K8sReporter context cancelled for %s/%s", r.namespace, r.name)
				return
			case <-r.ticker.C:
				// Get current status
				status := statusFunc()
				if status == nil {
					logger.Warnf("statusFunc returned nil for %s/%s, skipping update", r.namespace, r.name)
					continue
				}

				// Report status (use background context to avoid cancellation during update)
				reportCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				if err := r.Report(reportCtx, status); err != nil {
					logger.Errorf("Failed to report status for %s/%s: %v", r.namespace, r.name, err)
				}
				cancel()
			}
		}
	}()

	return nil
}

// Stop stops periodic status reporting and cleans up resources.
// This method blocks until the reporter goroutine has fully stopped.
func (r *K8sReporter) Stop() {
	r.mu.Lock()
	if r.ticker != nil {
		r.ticker.Stop()
		r.ticker = nil
	}
	r.mu.Unlock()

	close(r.stopChan)
	r.wg.Wait()
	logger.Debugf("K8sReporter cleanup complete for %s/%s", r.namespace, r.name)
}

// updateStatus converts RuntimeStatus to VirtualMCPServerStatus and updates the resource.
func (*K8sReporter) updateStatus(vmcpServer *mcpv1alpha1.VirtualMCPServer, runtimeStatus *RuntimeStatus) {
	// Update phase
	vmcpServer.Status.Phase = convertPhase(runtimeStatus.Phase)

	// Update message
	vmcpServer.Status.Message = runtimeStatus.Message

	// Update backend count
	vmcpServer.Status.BackendCount = len(runtimeStatus.DiscoveredBackends)

	// Update discovered backends
	vmcpServer.Status.DiscoveredBackends = make([]mcpv1alpha1.DiscoveredBackend, 0, len(runtimeStatus.DiscoveredBackends))
	for _, backend := range runtimeStatus.DiscoveredBackends {
		vmcpServer.Status.DiscoveredBackends = append(vmcpServer.Status.DiscoveredBackends, mcpv1alpha1.DiscoveredBackend{
			Name:            backend.Name,
			Status:          convertBackendHealthStatus(backend.HealthStatus),
			URL:             backend.BaseURL,
			LastHealthCheck: metav1.NewTime(backend.LastCheckTime),
			// AuthType and AuthConfigRef would be populated if we had that information
		})
	}

	// Update conditions
	vmcpServer.Status.Conditions = convertConditions(runtimeStatus.Conditions)

	// Update observed generation
	vmcpServer.Status.ObservedGeneration = vmcpServer.Generation
}

// convertPhase converts RuntimeStatus Phase to VirtualMCPServerPhase.
func convertPhase(phase Phase) mcpv1alpha1.VirtualMCPServerPhase {
	switch phase {
	case PhaseReady:
		return mcpv1alpha1.VirtualMCPServerPhaseReady
	case PhaseDegraded:
		return mcpv1alpha1.VirtualMCPServerPhaseDegraded
	case PhaseFailed:
		return mcpv1alpha1.VirtualMCPServerPhaseFailed
	case PhaseUnknown, PhaseStarting:
		return mcpv1alpha1.VirtualMCPServerPhasePending
	default:
		return mcpv1alpha1.VirtualMCPServerPhasePending
	}
}

// convertBackendHealthStatus converts vmcp.BackendHealthStatus to CRD status string.
func convertBackendHealthStatus(healthStatus vmcp.BackendHealthStatus) string {
	switch healthStatus {
	case vmcp.BackendHealthy:
		return mcpv1alpha1.BackendStatusReady
	case vmcp.BackendDegraded:
		return mcpv1alpha1.BackendStatusDegraded
	case vmcp.BackendUnhealthy, vmcp.BackendUnauthenticated:
		return mcpv1alpha1.BackendStatusUnavailable
	case vmcp.BackendUnknown:
		return mcpv1alpha1.BackendStatusUnknown
	default:
		return mcpv1alpha1.BackendStatusUnknown
	}
}

// convertConditions converts status Conditions to metav1.Condition format.
func convertConditions(conditions []Condition) []metav1.Condition {
	result := make([]metav1.Condition, 0, len(conditions))

	for _, cond := range conditions {
		result = append(result, metav1.Condition{
			Type:               string(cond.Type),
			Status:             convertConditionStatus(cond.Status),
			LastTransitionTime: metav1.NewTime(cond.LastTransitionTime),
			Reason:             cond.Reason,
			Message:            cond.Message,
			ObservedGeneration: 0, // Will be set by the status update
		})
	}

	return result
}

// convertConditionStatus converts ConditionStatus to metav1.ConditionStatus.
func convertConditionStatus(status ConditionStatus) metav1.ConditionStatus {
	switch status {
	case ConditionTrue:
		return metav1.ConditionTrue
	case ConditionFalse:
		return metav1.ConditionFalse
	case ConditionUnknown:
		return metav1.ConditionUnknown
	default:
		return metav1.ConditionUnknown
	}
}

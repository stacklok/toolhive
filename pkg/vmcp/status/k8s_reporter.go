package status

import (
	"context"
	"fmt"
	"time"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	"github.com/stacklok/toolhive/pkg/logger"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// K8sReporter implements Reporter by updating VirtualMCPServer status in Kubernetes
type K8sReporter struct {
	name      string
	namespace string
	client    client.Client
	stopCh    chan struct{}
	doneCh    chan struct{}
	getStatus func() *RuntimeStatus // Callback to get server status
}

// NewK8sReporter creates a new K8sReporter instance, using in-cluster configuration
func NewK8sReporter(name, namespace string) (*K8sReporter, error) {
	// Register VirtualMCPServer type with scheme
	if err := mcpv1alpha1.AddToScheme(scheme.Scheme); err != nil {
		return nil, fmt.Errorf("failed to register scheme: %w", err)
	}

	config, err := rest.InClusterConfig()
	if err != nil {
		return nil, fmt.Errorf("failed to get in-cluster config: %w", err)
	}

	// Create Kubernetes client
	k8sClient, err := client.New(config, client.Options{Scheme: scheme.Scheme})
	if err != nil {
		return nil, fmt.Errorf("failed to create kubernetes client: %w", err)
	}

	return &K8sReporter{
		name:      name,
		namespace: namespace,
		client:    k8sClient,
		stopCh:    make(chan struct{}),
		doneCh:    make(chan struct{}),
	}, nil
}

// SetStatusCallback sets the function to retrieve current server status
func (k *K8sReporter) SetStatusCallback(fn func() *RuntimeStatus) {
	k.getStatus = fn
}

// Report updates the VirtualMCPServer status in Kubernetes
func (k *K8sReporter) Report(ctx context.Context, status *RuntimeStatus) error {
	logger.Debugf("[%s/%s] Reporting status to Kubernetes", k.namespace, k.name)

	// Get current VirtualMCPServer
	vmcp := &mcpv1alpha1.VirtualMCPServer{}
	key := client.ObjectKey{
		Name:      k.name,
		Namespace: k.namespace,
	}

	if err := k.client.Get(ctx, key, vmcp); err != nil {
		return fmt.Errorf("failed to get VirtualMCPServer: %w", err)
	}

	// Update status fields
	vmcp.Status.Phase = mcpv1alpha1.VirtualMCPServerPhase(status.Phase)
	vmcp.Status.Message = status.Message
	vmcp.Status.BackendCount = status.HealthyBackends + status.UnhealthyBackends

	// Update status subresource
	if err := k.client.Status().Update(ctx, vmcp); err != nil {
		return fmt.Errorf("failed to update status: %w", err)
	}

	logger.Infof("[%s/%s] Updated K8s status: phase=%s, backends=%d/%d",
		k.namespace, k.name, status.Phase,
		status.HealthyBackends, status.HealthyBackends+status.UnhealthyBackends)
	return nil
}

// Start begins periodic status reporting in a background goroutine
func (k *K8sReporter) Start(ctx context.Context, interval time.Duration) error {
	logger.Infof("[%s/%s] Starting K8s status reporter (interval: %v)",
		k.namespace, k.name, interval)
	go k.reportLoop(ctx, interval)
	return nil
}

// Stop gracefully stops the periodic reporter
func (k *K8sReporter) Stop() {
	select {
	case <-k.stopCh:
		// Already stopped
		return
	default:
		close(k.stopCh)
		<-k.doneCh
	}
}

// reportLoop runs in a background goroutine and reports status periodically
func (k *K8sReporter) reportLoop(ctx context.Context, interval time.Duration) {
	defer close(k.doneCh)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			logger.Debugf("[%s/%s] Periodic report tick", k.namespace, k.name)

			// Get status from server if callback is set
			var status *RuntimeStatus
			if k.getStatus != nil {
				status = k.getStatus()
			} else {
				// Fallback: create basic status
				status = &RuntimeStatus{
					Phase:             PhaseReady,
					Message:           "No status callback configured",
					TotalToolCount:    0,
					HealthyBackends:   0,
					UnhealthyBackends: 0,
					Backends:          []BackendHealthReport{},
					LastDiscoveryTime: time.Now(),
				}
			}

			// Report the status
			if err := k.Report(ctx, status); err != nil {
				logger.Errorf("[%s/%s] Failed to report status: %v", k.namespace, k.name, err)
			}

		case <-k.stopCh:
			logger.Infof("[%s/%s] K8s status reporter stopping", k.namespace, k.name)
			return

		case <-ctx.Done():
			logger.Infof("[%s/%s] K8s status reporter context cancelled", k.namespace, k.name)
			return
		}
	}
}

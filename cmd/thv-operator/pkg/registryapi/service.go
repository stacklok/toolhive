package registryapi

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
)

// ensureService creates or updates the registry-api Service for the MCPRegistry.
// This function handles the Kubernetes API operations (Get, Create, Update) and delegates
// service configuration to buildRegistryAPIService.
func (m *manager) ensureService(
	ctx context.Context,
	mcpRegistry *mcpv1alpha1.MCPRegistry,
) (*corev1.Service, error) {
	ctxLogger := log.FromContext(ctx).WithValues("mcpregistry", mcpRegistry.Name)

	// Build the desired service configuration
	service := buildRegistryAPIService(mcpRegistry)
	serviceName := service.Name

	// Set owner reference for automatic garbage collection
	if err := controllerutil.SetControllerReference(mcpRegistry, service, m.scheme); err != nil {
		ctxLogger.Error(err, "Failed to set controller reference for service")
		return nil, fmt.Errorf("failed to set controller reference for service: %w", err)
	}

	// Check if service already exists
	existing := &corev1.Service{}
	err := m.client.Get(ctx, types.NamespacedName{
		Name:      serviceName,
		Namespace: mcpRegistry.Namespace,
	}, existing)

	if err != nil {
		if errors.IsNotFound(err) {
			// Service doesn't exist, create it
			ctxLogger.Info("Creating registry-api service", "service", serviceName)
			if err := m.client.Create(ctx, service); err != nil {
				ctxLogger.Error(err, "Failed to create service")
				return nil, fmt.Errorf("failed to create service %s: %w", serviceName, err)
			}
			ctxLogger.Info("Successfully created registry-api service", "service", serviceName)
			return service, nil
		}
		// Unexpected error
		ctxLogger.Error(err, "Failed to get service")
		return nil, fmt.Errorf("failed to get service %s: %w", serviceName, err)
	}

	// Service exists, update it if necessary
	ctxLogger.V(1).Info("Service already exists, checking for updates", "service", serviceName)

	// Update the existing service with our desired state
	existing.Spec.Type = service.Spec.Type
	existing.Spec.Selector = service.Spec.Selector
	existing.Spec.Ports = service.Spec.Ports
	existing.Labels = service.Labels

	// Ensure owner reference is set
	if err := controllerutil.SetControllerReference(mcpRegistry, existing, m.scheme); err != nil {
		ctxLogger.Error(err, "Failed to set controller reference for existing service")
		return nil, fmt.Errorf("failed to set controller reference for existing service: %w", err)
	}

	if err := m.client.Update(ctx, existing); err != nil {
		ctxLogger.Error(err, "Failed to update service")
		return nil, fmt.Errorf("failed to update service %s: %w", serviceName, err)
	}

	ctxLogger.Info("Successfully updated registry-api service", "service", serviceName)
	return existing, nil
}

// buildRegistryAPIService creates and configures a Service object for the registry API.
// This function handles all service configuration including labels, ports, and selector.
// It returns a fully configured ClusterIP service ready for Kubernetes API operations.
func buildRegistryAPIService(mcpRegistry *mcpv1alpha1.MCPRegistry) *corev1.Service {
	// Generate service name using the established pattern
	serviceName := mcpRegistry.GetAPIResourceName()

	// Define labels using common function
	labels := labelsForRegistryAPI(mcpRegistry, serviceName)

	// Define selector to match deployment pod labels
	selector := map[string]string{
		"app.kubernetes.io/name":      serviceName,
		"app.kubernetes.io/component": "registry-api",
	}

	// Create service specification
	service := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      serviceName,
			Namespace: mcpRegistry.Namespace,
			Labels:    labels,
		},
		Spec: corev1.ServiceSpec{
			Type:     corev1.ServiceTypeClusterIP,
			Selector: selector,
			Ports: []corev1.ServicePort{
				{
					Name:       "http",
					Port:       8080,
					TargetPort: intstr.FromInt32(8080),
					Protocol:   corev1.ProtocolTCP,
				},
			},
		},
	}

	return service
}

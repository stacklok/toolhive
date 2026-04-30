// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package registryapi

import (
	"context"
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
	"github.com/stacklok/toolhive/cmd/thv-operator/pkg/imagepullsecrets"
	"github.com/stacklok/toolhive/cmd/thv-operator/pkg/kubernetes"
	"github.com/stacklok/toolhive/cmd/thv-operator/pkg/kubernetes/configmaps"
	"github.com/stacklok/toolhive/cmd/thv-operator/pkg/registryapi/config"
)

// manager implements the Manager interface
type manager struct {
	client     client.Client
	scheme     *runtime.Scheme
	kubeHelper *kubernetes.Client
	// imagePullSecretsDefaults are cluster-wide defaults sourced from the
	// operator chart that are merged with the per-CR imagePullSecrets when
	// constructing the registry-api workload. The zero value is a usable
	// empty Defaults.
	imagePullSecretsDefaults imagepullsecrets.Defaults
}

// NewManager creates a new registry API manager. imagePullSecretsDefaults are
// cluster-wide pull-secret defaults from the operator chart; passing the zero
// value disables the merge and the registry-api uses only the per-CR list.
func NewManager(
	k8sClient client.Client,
	scheme *runtime.Scheme,
	imagePullSecretsDefaults imagepullsecrets.Defaults,
) Manager {
	return &manager{
		client:                   k8sClient,
		scheme:                   scheme,
		kubeHelper:               kubernetes.NewClient(k8sClient, scheme),
		imagePullSecretsDefaults: imagePullSecretsDefaults,
	}
}

// ReconcileAPIService orchestrates the deployment, service creation, and readiness checking for the registry API.
// This method coordinates all aspects of API service including creating/updating the deployment and service,
// checking readiness, and updating the MCPRegistry status with deployment references and endpoint information.
//
// It creates a ConfigMap from the raw ConfigYAML string and mounts user-provided volumes directly,
// without parsing or transforming config.
func (m *manager) ReconcileAPIService(
	ctx context.Context, mcpRegistry *mcpv1beta1.MCPRegistry,
) *Error {
	ctxLogger := log.FromContext(ctx).WithValues("mcpregistry", mcpRegistry.Name)
	ctxLogger.Info("Reconciling API service")

	// Create config ConfigMap from raw YAML
	configMap, err := config.RawConfigToConfigMap(mcpRegistry.Name, mcpRegistry.Namespace, mcpRegistry.Spec.ConfigYAML)
	if err != nil {
		ctxLogger.Error(err, "Failed to create config map from raw YAML")
		return &Error{
			Err:             err,
			Message:         fmt.Sprintf("Failed to create config map from raw YAML: %v", err),
			ConditionReason: "ConfigMapFailed",
		}
	}

	// Upsert the ConfigMap with owner reference
	configMapsClient := configmaps.NewClient(m.client, m.scheme)
	if _, err := configMapsClient.UpsertWithOwnerReference(ctx, configMap, mcpRegistry); err != nil {
		ctxLogger.Error(err, "Failed to upsert registry server config config map")
		return &Error{
			Err:             err,
			Message:         fmt.Sprintf("Failed to upsert registry server config config map: %v", err),
			ConditionReason: "ConfigMapFailed",
		}
	}

	configMapName := configMap.Name

	// Ensure RBAC resources (ServiceAccount, Role, RoleBinding) before deployment
	if err := m.ensureRBACResources(ctx, mcpRegistry); err != nil {
		ctxLogger.Error(err, "Failed to ensure RBAC resources")
		return &Error{
			Err:             err,
			Message:         fmt.Sprintf("Failed to ensure RBAC resources: %v", err),
			ConditionReason: "RBACFailed",
		}
	}

	// Ensure deployment exists and is configured correctly
	deployment, err := m.ensureDeployment(ctx, mcpRegistry, configMapName)
	if err != nil {
		ctxLogger.Error(err, "Failed to ensure deployment")
		return &Error{
			Err:             err,
			Message:         fmt.Sprintf("Failed to ensure deployment: %v", err),
			ConditionReason: "DeploymentFailed",
		}
	}

	// Ensure service exists and is configured correctly
	if err := m.ensureService(ctx, mcpRegistry); err != nil {
		ctxLogger.Error(err, "Failed to ensure service")
		return &Error{
			Err:             err,
			Message:         fmt.Sprintf("Failed to ensure service: %v", err),
			ConditionReason: "ServiceFailed",
		}
	}

	// Check API readiness
	isReady := m.CheckAPIReadiness(ctx, deployment)

	if isReady {
		ctxLogger.Info("API service reconciliation completed successfully - API is ready")
	} else {
		ctxLogger.Info("API service reconciliation completed - API is not ready yet")
	}

	return nil
}

// IsAPIReady checks if the registry API deployment is ready and serving requests
func (m *manager) IsAPIReady(ctx context.Context, mcpRegistry *mcpv1beta1.MCPRegistry) bool {
	ctxLogger := log.FromContext(ctx).WithValues("mcpregistry", mcpRegistry.Name)

	deploymentName := mcpRegistry.GetAPIResourceName()
	deployment := &appsv1.Deployment{}

	err := m.client.Get(ctx, client.ObjectKey{
		Name:      deploymentName,
		Namespace: mcpRegistry.Namespace,
	}, deployment)

	if err != nil {
		ctxLogger.Info("API deployment not found, considering not ready", "error", err)
		return false
	}

	// Delegate to the existing CheckAPIReadiness method for consistency
	return m.CheckAPIReadiness(ctx, deployment)
}

// GetReadyReplicas returns the number of ready replicas for the registry API deployment.
// Returns 0 if the deployment is not found or an error occurs.
func (m *manager) GetReadyReplicas(ctx context.Context, mcpRegistry *mcpv1beta1.MCPRegistry) int32 {
	ctxLogger := log.FromContext(ctx).WithValues("mcpregistry", mcpRegistry.Name)

	deploymentName := mcpRegistry.GetAPIResourceName()
	deployment := &appsv1.Deployment{}

	err := m.client.Get(ctx, client.ObjectKey{
		Name:      deploymentName,
		Namespace: mcpRegistry.Namespace,
	}, deployment)

	if err != nil {
		ctxLogger.V(1).Info("API deployment not found for ready replicas check", "error", err)
		return 0
	}

	return deployment.Status.ReadyReplicas
}

// GetAPIStatus returns the readiness state and ready replica count from a single Deployment fetch.
func (m *manager) GetAPIStatus(ctx context.Context, mcpRegistry *mcpv1beta1.MCPRegistry) (bool, int32) {
	ctxLogger := log.FromContext(ctx).WithValues("mcpregistry", mcpRegistry.Name)

	deploymentName := mcpRegistry.GetAPIResourceName()
	deployment := &appsv1.Deployment{}

	err := m.client.Get(ctx, client.ObjectKey{
		Name:      deploymentName,
		Namespace: mcpRegistry.Namespace,
	}, deployment)
	if err != nil {
		ctxLogger.V(1).Info("API deployment not found", "error", err)
		return false, 0
	}

	return m.CheckAPIReadiness(ctx, deployment), deployment.Status.ReadyReplicas
}

// labelsForRegistryAPI generates standard labels for registry API resources
func labelsForRegistryAPI(mcpRegistry *mcpv1beta1.MCPRegistry, resourceName string) map[string]string {
	return map[string]string{
		"app.kubernetes.io/name":             resourceName,
		"app.kubernetes.io/component":        "registry-api",
		"app.kubernetes.io/managed-by":       "toolhive-operator",
		"toolhive.stacklok.io/registry-name": mcpRegistry.Name,
	}
}

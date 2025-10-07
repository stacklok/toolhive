package operator_test

import (
	"context"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// K8sResourceTestHelper provides utilities for testing Kubernetes resources
type K8sResourceTestHelper struct {
	ctx       context.Context
	k8sClient client.Client
	namespace string
}

// NewK8sResourceTestHelper creates a new test helper for Kubernetes resources
func NewK8sResourceTestHelper(ctx context.Context, k8sClient client.Client, namespace string) *K8sResourceTestHelper {
	return &K8sResourceTestHelper{
		ctx:       ctx,
		k8sClient: k8sClient,
		namespace: namespace,
	}
}

// GetDeployment retrieves a deployment by name
func (h *K8sResourceTestHelper) GetDeployment(name string) (*appsv1.Deployment, error) {
	deployment := &appsv1.Deployment{}
	err := h.k8sClient.Get(h.ctx, types.NamespacedName{
		Namespace: h.namespace,
		Name:      name,
	}, deployment)
	return deployment, err
}

// GetService retrieves a service by name
func (h *K8sResourceTestHelper) GetService(name string) (*corev1.Service, error) {
	service := &corev1.Service{}
	err := h.k8sClient.Get(h.ctx, types.NamespacedName{
		Namespace: h.namespace,
		Name:      name,
	}, service)
	return service, err
}

// GetConfigMap retrieves a configmap by name
func (h *K8sResourceTestHelper) GetConfigMap(name string) (*corev1.ConfigMap, error) {
	configMap := &corev1.ConfigMap{}
	err := h.k8sClient.Get(h.ctx, types.NamespacedName{
		Namespace: h.namespace,
		Name:      name,
	}, configMap)
	return configMap, err
}

// DeploymentExists checks if a deployment exists
func (h *K8sResourceTestHelper) DeploymentExists(name string) bool {
	_, err := h.GetDeployment(name)
	return err == nil
}

// ServiceExists checks if a service exists
func (h *K8sResourceTestHelper) ServiceExists(name string) bool {
	_, err := h.GetService(name)
	return err == nil
}

// IsDeploymentReady checks if a deployment is ready (all replicas available)
func (h *K8sResourceTestHelper) IsDeploymentReady(name string) bool {
	deployment, err := h.GetDeployment(name)
	if err != nil {
		return false
	}

	// Check if deployment has at least one replica and all are available
	if deployment.Spec.Replicas == nil || *deployment.Spec.Replicas == 0 {
		return false
	}

	return deployment.Status.ReadyReplicas == *deployment.Spec.Replicas
}

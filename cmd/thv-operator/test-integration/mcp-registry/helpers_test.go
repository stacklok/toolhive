package operator_test

import (
	"context"
	"fmt"
	"time"

	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
)

// KubernetesTestHelper provides utilities for Kubernetes testing
type KubernetesTestHelper struct {
	Client    client.Client
	Context   context.Context
	Namespace string
}

// NewKubernetesTestHelper creates a new test helper for the given namespace
func NewKubernetesTestHelper(namespace string) *KubernetesTestHelper {
	return &KubernetesTestHelper{
		Client:    k8sClient,
		Context:   ctx,
		Namespace: namespace,
	}
}

// CreateMCPRegistry creates an MCPRegistry with the given spec
func (h *KubernetesTestHelper) CreateMCPRegistry(name string, spec mcpv1alpha1.MCPRegistrySpec) *mcpv1alpha1.MCPRegistry {
	registry := &mcpv1alpha1.MCPRegistry{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: h.Namespace,
		},
		Spec: spec,
	}

	err := h.Client.Create(h.Context, registry)
	gomega.Expect(err).NotTo(gomega.HaveOccurred(), "Failed to create MCPRegistry")

	return registry
}

// GetMCPRegistry retrieves an MCPRegistry by name
func (h *KubernetesTestHelper) GetMCPRegistry(name string) (*mcpv1alpha1.MCPRegistry, error) {
	registry := &mcpv1alpha1.MCPRegistry{}
	err := h.Client.Get(h.Context, types.NamespacedName{
		Namespace: h.Namespace,
		Name:      name,
	}, registry)
	return registry, err
}

// UpdateMCPRegistry updates an existing MCPRegistry
func (h *KubernetesTestHelper) UpdateMCPRegistry(registry *mcpv1alpha1.MCPRegistry) error {
	return h.Client.Update(h.Context, registry)
}

// DeleteMCPRegistry deletes an MCPRegistry by name
func (h *KubernetesTestHelper) DeleteMCPRegistry(name string) error {
	registry := &mcpv1alpha1.MCPRegistry{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: h.Namespace,
		},
	}
	return h.Client.Delete(h.Context, registry)
}

// WaitForMCPRegistryPhase waits for the MCPRegistry to reach the specified phase
func (h *KubernetesTestHelper) WaitForMCPRegistryPhase(name string, phase mcpv1alpha1.MCPRegistryPhase, timeout time.Duration) {
	gomega.Eventually(func() mcpv1alpha1.MCPRegistryPhase {
		registry, err := h.GetMCPRegistry(name)
		if err != nil {
			return ""
		}
		return registry.Status.Phase
	}, timeout, time.Second).Should(gomega.Equal(phase), "MCPRegistry should reach phase %s", phase)
}

// WaitForMCPRegistryCondition waits for a specific condition to be true
func (h *KubernetesTestHelper) WaitForMCPRegistryCondition(
	name string, conditionType string, status metav1.ConditionStatus, timeout time.Duration) {
	gomega.Eventually(func() metav1.ConditionStatus {
		registry, err := h.GetMCPRegistry(name)
		if err != nil {
			return metav1.ConditionUnknown
		}

		for _, condition := range registry.Status.Conditions {
			if condition.Type == conditionType {
				return condition.Status
			}
		}
		return metav1.ConditionUnknown
	},
		timeout, time.Second).Should(
		gomega.Equal(status), "MCPRegistry should have condition %s with status %s", conditionType, status)
}

// WaitForMCPRegistryDeletion waits for the MCPRegistry to be deleted
func (h *KubernetesTestHelper) WaitForMCPRegistryDeletion(name string, timeout time.Duration) {
	gomega.Eventually(func() bool {
		_, err := h.GetMCPRegistry(name)
		return apierrors.IsNotFound(err)
	}, timeout, time.Second).Should(gomega.BeTrue(), "MCPRegistry should be deleted")
}

// CreateConfigMap creates a ConfigMap for testing
func (h *KubernetesTestHelper) CreateConfigMap(name string, data map[string]string) *corev1.ConfigMap {
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: h.Namespace,
		},
		Data: data,
	}

	err := h.Client.Create(h.Context, cm)
	gomega.Expect(err).NotTo(gomega.HaveOccurred(), "Failed to create ConfigMap")

	return cm
}

// GetConfigMap retrieves a ConfigMap by name
func (h *KubernetesTestHelper) GetConfigMap(name string) (*corev1.ConfigMap, error) {
	cm := &corev1.ConfigMap{}
	err := h.Client.Get(h.Context, types.NamespacedName{
		Namespace: h.Namespace,
		Name:      name,
	}, cm)
	return cm, err
}

// WaitForConfigMap waits for a ConfigMap to exist
func (h *KubernetesTestHelper) WaitForConfigMap(name string, timeout time.Duration) *corev1.ConfigMap {
	var cm *corev1.ConfigMap
	gomega.Eventually(func() error {
		var err error
		cm, err = h.GetConfigMap(name)
		return err
	}, timeout, time.Second).Should(gomega.Succeed(), "ConfigMap should be created")
	return cm
}

// WaitForConfigMapData waits for a ConfigMap to contain specific data
func (h *KubernetesTestHelper) WaitForConfigMapData(name, key, expectedValue string, timeout time.Duration) {
	gomega.Eventually(func() string {
		cm, err := h.GetConfigMap(name)
		if err != nil {
			return ""
		}
		return cm.Data[key]
	}, timeout, time.Second).Should(gomega.Equal(expectedValue), "ConfigMap should contain expected data")
}

// CreateSecret creates a Secret for testing
func (h *KubernetesTestHelper) CreateSecret(name string, data map[string][]byte) *corev1.Secret {
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: h.Namespace,
		},
		Data: data,
	}

	err := h.Client.Create(h.Context, secret)
	gomega.Expect(err).NotTo(gomega.HaveOccurred(), "Failed to create Secret")

	return secret
}

// CleanupResources removes all test resources in the namespace
func (h *KubernetesTestHelper) CleanupResources() {
	// Delete all MCPRegistries
	registryList := &mcpv1alpha1.MCPRegistryList{}
	err := h.Client.List(h.Context, registryList, client.InNamespace(h.Namespace))
	if err == nil {
		for _, registry := range registryList.Items {
			_ = h.Client.Delete(h.Context, &registry)
		}
	}

	// Delete all ConfigMaps
	cmList := &corev1.ConfigMapList{}
	err = h.Client.List(h.Context, cmList, client.InNamespace(h.Namespace))
	if err == nil {
		for _, cm := range cmList.Items {
			_ = h.Client.Delete(h.Context, &cm)
		}
	}

	// Delete all Secrets
	secretList := &corev1.SecretList{}
	err = h.Client.List(h.Context, secretList, client.InNamespace(h.Namespace))
	if err == nil {
		for _, secret := range secretList.Items {
			_ = h.Client.Delete(h.Context, &secret)
		}
	}
}

// TriggerManualSync adds an annotation to trigger a manual sync
func (h *KubernetesTestHelper) TriggerManualSync(registryName string) error {
	registry, err := h.GetMCPRegistry(registryName)
	if err != nil {
		return err
	}

	if registry.Annotations == nil {
		registry.Annotations = make(map[string]string)
	}
	registry.Annotations["toolhive.stacklok.dev/manual-sync"] = fmt.Sprintf("%d", time.Now().Unix())

	return h.UpdateMCPRegistry(registry)
}

// WaitForSyncCompletion waits for a sync operation to complete
func (h *KubernetesTestHelper) WaitForSyncCompletion(registryName string, timeout time.Duration) {
	gomega.Eventually(func() bool {
		registry, err := h.GetMCPRegistry(registryName)
		if err != nil {
			return false
		}

		// Check if sync is in progress
		for _, condition := range registry.Status.Conditions {
			if condition.Type == "Syncing" && condition.Status == metav1.ConditionTrue {
				return false // Still syncing
			}
		}

		// Sync should be complete (either success or failure)
		return registry.Status.Phase == mcpv1alpha1.MCPRegistryPhaseReady ||
			registry.Status.Phase == mcpv1alpha1.MCPRegistryPhaseFailed
	}, timeout, time.Second).Should(gomega.BeTrue(), "Sync operation should complete")
}

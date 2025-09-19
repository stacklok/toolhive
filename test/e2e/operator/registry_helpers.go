package operator_test

import (
	"context"
	"fmt"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
)

// MCPRegistryTestHelper provides specialized utilities for MCPRegistry testing
type MCPRegistryTestHelper struct {
	Client    client.Client
	Context   context.Context
	Namespace string
}

// NewMCPRegistryTestHelper creates a new test helper for MCPRegistry operations
func NewMCPRegistryTestHelper(ctx context.Context, k8sClient client.Client, namespace string) *MCPRegistryTestHelper {
	return &MCPRegistryTestHelper{
		Client:    k8sClient,
		Context:   ctx,
		Namespace: namespace,
	}
}

// RegistryBuilder provides a fluent interface for building MCPRegistry objects
type RegistryBuilder struct {
	registry *mcpv1alpha1.MCPRegistry
}

// NewRegistryBuilder creates a new MCPRegistry builder
func (h *MCPRegistryTestHelper) NewRegistryBuilder(name string) *RegistryBuilder {
	return &RegistryBuilder{
		registry: &mcpv1alpha1.MCPRegistry{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: h.Namespace,
				Labels: map[string]string{
					"test.toolhive.io/suite": "operator-e2e",
				},
			},
			Spec: mcpv1alpha1.MCPRegistrySpec{},
		},
	}
}

// WithConfigMapSource configures the registry with a ConfigMap source
func (rb *RegistryBuilder) WithConfigMapSource(configMapName, key string) *RegistryBuilder {
	rb.registry.Spec.Source = mcpv1alpha1.MCPRegistrySource{
		Type:   mcpv1alpha1.RegistrySourceTypeConfigMap,
		Format: mcpv1alpha1.RegistryFormatToolHive,
		ConfigMap: &mcpv1alpha1.ConfigMapSource{
			Name: configMapName,
			Key:  key,
		},
	}
	return rb
}

// WithUpstreamFormat configures the registry to use upstream MCP format
func (rb *RegistryBuilder) WithUpstreamFormat() *RegistryBuilder {
	rb.registry.Spec.Source.Format = mcpv1alpha1.RegistryFormatUpstream
	return rb
}

// WithSyncPolicy configures the sync policy
func (rb *RegistryBuilder) WithSyncPolicy(interval string) *RegistryBuilder {
	rb.registry.Spec.SyncPolicy = &mcpv1alpha1.SyncPolicy{
		Interval: interval,
	}
	return rb
}

// WithAnnotation adds an annotation to the registry
func (rb *RegistryBuilder) WithAnnotation(key, value string) *RegistryBuilder {
	if rb.registry.Annotations == nil {
		rb.registry.Annotations = make(map[string]string)
	}
	rb.registry.Annotations[key] = value
	return rb
}

// WithLabel adds a label to the registry
func (rb *RegistryBuilder) WithLabel(key, value string) *RegistryBuilder {
	if rb.registry.Labels == nil {
		rb.registry.Labels = make(map[string]string)
	}
	rb.registry.Labels[key] = value
	return rb
}

// Build returns the constructed MCPRegistry
func (rb *RegistryBuilder) Build() *mcpv1alpha1.MCPRegistry {
	return rb.registry.DeepCopy()
}

// Create builds and creates the MCPRegistry in the cluster
func (rb *RegistryBuilder) Create(h *MCPRegistryTestHelper) *mcpv1alpha1.MCPRegistry {
	registry := rb.Build()
	err := h.Client.Create(h.Context, registry)
	gomega.Expect(err).NotTo(gomega.HaveOccurred(), "Failed to create MCPRegistry")
	return registry
}

// CreateBasicConfigMapRegistry creates a simple MCPRegistry with ConfigMap source
func (h *MCPRegistryTestHelper) CreateBasicConfigMapRegistry(name, configMapName string) *mcpv1alpha1.MCPRegistry {
	return h.NewRegistryBuilder(name).
		WithConfigMapSource(configMapName, "registry.json").
		WithSyncPolicy("1h").
		Create(h)
}

// CreateManualSyncRegistry creates an MCPRegistry with manual sync only
func (h *MCPRegistryTestHelper) CreateManualSyncRegistry(name, configMapName string) *mcpv1alpha1.MCPRegistry {
	return h.NewRegistryBuilder(name).
		WithConfigMapSource(configMapName, "registry.json").
		Create(h)
}

// CreateUpstreamFormatRegistry creates an MCPRegistry with upstream format
func (h *MCPRegistryTestHelper) CreateUpstreamFormatRegistry(name, configMapName string) *mcpv1alpha1.MCPRegistry {
	return h.NewRegistryBuilder(name).
		WithConfigMapSource(configMapName, "registry.json").
		WithUpstreamFormat().
		WithSyncPolicy("30m").
		Create(h)
}

// GetRegistry retrieves an MCPRegistry by name
func (h *MCPRegistryTestHelper) GetRegistry(name string) (*mcpv1alpha1.MCPRegistry, error) {
	registry := &mcpv1alpha1.MCPRegistry{}
	err := h.Client.Get(h.Context, types.NamespacedName{
		Namespace: h.Namespace,
		Name:      name,
	}, registry)
	return registry, err
}

// UpdateRegistry updates an existing MCPRegistry
func (h *MCPRegistryTestHelper) UpdateRegistry(registry *mcpv1alpha1.MCPRegistry) error {
	return h.Client.Update(h.Context, registry)
}

// PatchRegistry patches an MCPRegistry with the given patch
func (h *MCPRegistryTestHelper) PatchRegistry(name string, patch client.Patch) error {
	registry := &mcpv1alpha1.MCPRegistry{}
	registry.Name = name
	registry.Namespace = h.Namespace
	return h.Client.Patch(h.Context, registry, patch)
}

// DeleteRegistry deletes an MCPRegistry by name
func (h *MCPRegistryTestHelper) DeleteRegistry(name string) error {
	registry := &mcpv1alpha1.MCPRegistry{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: h.Namespace,
		},
	}
	return h.Client.Delete(h.Context, registry)
}

// TriggerManualSync adds the manual sync annotation to trigger a sync
func (h *MCPRegistryTestHelper) TriggerManualSync(name string) error {
	registry, err := h.GetRegistry(name)
	if err != nil {
		return err
	}

	if registry.Annotations == nil {
		registry.Annotations = make(map[string]string)
	}
	registry.Annotations["toolhive.stacklok.dev/manual-sync"] = fmt.Sprintf("%d", time.Now().Unix())

	return h.UpdateRegistry(registry)
}

// GetRegistryStatus returns the current status of an MCPRegistry
func (h *MCPRegistryTestHelper) GetRegistryStatus(name string) (*mcpv1alpha1.MCPRegistryStatus, error) {
	registry, err := h.GetRegistry(name)
	if err != nil {
		return nil, err
	}
	return &registry.Status, nil
}

// GetRegistryPhase returns the current phase of an MCPRegistry
func (h *MCPRegistryTestHelper) GetRegistryPhase(name string) (mcpv1alpha1.MCPRegistryPhase, error) {
	status, err := h.GetRegistryStatus(name)
	if err != nil {
		return "", err
	}
	return status.Phase, nil
}

// GetRegistryCondition returns a specific condition from the registry status
func (h *MCPRegistryTestHelper) GetRegistryCondition(name, conditionType string) (*metav1.Condition, error) {
	status, err := h.GetRegistryStatus(name)
	if err != nil {
		return nil, err
	}

	for _, condition := range status.Conditions {
		if condition.Type == conditionType {
			return &condition, nil
		}
	}
	return nil, fmt.Errorf("condition %s not found", conditionType)
}

// ListRegistries returns all MCPRegistries in the namespace
func (h *MCPRegistryTestHelper) ListRegistries() (*mcpv1alpha1.MCPRegistryList, error) {
	registryList := &mcpv1alpha1.MCPRegistryList{}
	err := h.Client.List(h.Context, registryList, client.InNamespace(h.Namespace))
	return registryList, err
}

// CleanupRegistries deletes all MCPRegistries in the namespace
func (h *MCPRegistryTestHelper) CleanupRegistries() error {
	registryList, err := h.ListRegistries()
	if err != nil {
		return err
	}

	for _, registry := range registryList.Items {
		if err := h.Client.Delete(h.Context, &registry); err != nil {
			return err
		}

		// Wait for registry to be actually deleted
		ginkgo.By(fmt.Sprintf("waiting for registry %s to be deleted", registry.Name))
		gomega.Eventually(func() bool {
			_, err := h.GetRegistry(registry.Name)
			return err != nil && errors.IsNotFound(err)
		}, LongTimeout, DefaultPollingInterval).Should(gomega.BeTrue())
	}
	return nil
}

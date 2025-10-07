package operator_test

import (
	"context"
	"encoding/json"
	"fmt"

	ginkgo "github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// ConfigMapTestHelper provides utilities for ConfigMap testing and validation
type ConfigMapTestHelper struct {
	Client    client.Client
	Context   context.Context
	Namespace string
}

// NewConfigMapTestHelper creates a new test helper for ConfigMap operations
func NewConfigMapTestHelper(ctx context.Context, k8sClient client.Client, namespace string) *ConfigMapTestHelper {
	return &ConfigMapTestHelper{
		Client:    k8sClient,
		Context:   ctx,
		Namespace: namespace,
	}
}

// RegistryServer represents a server definition in the registry
type RegistryServer struct {
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	Tier        string   `json:"tier"`
	Status      string   `json:"status"`
	Transport   string   `json:"transport"`
	Tools       []string `json:"tools"`
	Image       string   `json:"image"`
	Tags        []string `json:"tags,omitempty"`
}

// ToolHiveRegistryData represents the ToolHive registry format
type ToolHiveRegistryData struct {
	Version       string                    `json:"version"`
	LastUpdated   string                    `json:"last_updated"`
	Servers       map[string]RegistryServer `json:"servers"`
	RemoteServers map[string]RegistryServer `json:"remoteServers"`
}

// UpstreamRegistryData represents the upstream MCP registry format
type UpstreamRegistryData struct {
	Servers map[string]RegistryServer `json:"servers"`
}

// ConfigMapBuilder provides a fluent interface for building ConfigMaps
type ConfigMapBuilder struct {
	configMap *corev1.ConfigMap
}

// NewConfigMapBuilder creates a new ConfigMap builder
func (h *ConfigMapTestHelper) NewConfigMapBuilder(name string) *ConfigMapBuilder {
	return &ConfigMapBuilder{
		configMap: &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: h.Namespace,
				Labels: map[string]string{
					"test.toolhive.io/suite": "operator-e2e",
				},
			},
			Data: make(map[string]string),
		},
	}
}

// WithLabel adds a label to the ConfigMap
func (cb *ConfigMapBuilder) WithLabel(key, value string) *ConfigMapBuilder {
	if cb.configMap.Labels == nil {
		cb.configMap.Labels = make(map[string]string)
	}
	cb.configMap.Labels[key] = value
	return cb
}

// WithData adds arbitrary data to the ConfigMap
func (cb *ConfigMapBuilder) WithData(key, value string) *ConfigMapBuilder {
	cb.configMap.Data[key] = value
	return cb
}

// WithToolHiveRegistry adds ToolHive format registry data
func (cb *ConfigMapBuilder) WithToolHiveRegistry(key string, servers []RegistryServer) *ConfigMapBuilder {
	// Convert slice to map using server names as keys
	serverMap := make(map[string]RegistryServer)
	for _, server := range servers {
		serverMap[server.Name] = server
	}

	registryData := ToolHiveRegistryData{
		Version:       "1.0.0",
		LastUpdated:   "2025-01-15T10:30:00Z",
		Servers:       serverMap,
		RemoteServers: make(map[string]RegistryServer),
	}
	jsonData, err := json.MarshalIndent(registryData, "", "  ")
	gomega.Expect(err).NotTo(gomega.HaveOccurred(), "Failed to marshal ToolHive registry data")
	cb.configMap.Data[key] = string(jsonData)
	return cb
}

// WithUpstreamRegistry adds upstream MCP format registry data
func (cb *ConfigMapBuilder) WithUpstreamRegistry(key string, servers map[string]RegistryServer) *ConfigMapBuilder {
	registryData := UpstreamRegistryData{Servers: servers}
	jsonData, err := json.MarshalIndent(registryData, "", "  ")
	gomega.Expect(err).NotTo(gomega.HaveOccurred(), "Failed to marshal upstream registry data")
	cb.configMap.Data[key] = string(jsonData)
	return cb
}

// Build returns the constructed ConfigMap
func (cb *ConfigMapBuilder) Build() *corev1.ConfigMap {
	return cb.configMap.DeepCopy()
}

// Create builds and creates the ConfigMap in the cluster
func (cb *ConfigMapBuilder) Create(h *ConfigMapTestHelper) *corev1.ConfigMap {
	configMap := cb.Build()
	err := h.Client.Create(h.Context, configMap)
	gomega.Expect(err).NotTo(gomega.HaveOccurred(), "Failed to create ConfigMap")
	return configMap
}

// CreateSampleToolHiveRegistry creates a ConfigMap with sample ToolHive registry data
func (h *ConfigMapTestHelper) CreateSampleToolHiveRegistry(name string) (*corev1.ConfigMap, int) {
	servers := []RegistryServer{
		{
			Name:        "filesystem",
			Description: "File system operations for secure file access",
			Tier:        "Community",
			Status:      "Active",
			Transport:   "stdio",
			Tools:       []string{"filesystem_tool"},
			Image:       "filesystem/server:latest",
			Tags:        []string{"filesystem", "files"},
		},
		{
			Name:        "fetch",
			Description: "Web content fetching with readability processing",
			Tier:        "Community",
			Status:      "Active",
			Transport:   "stdio",
			Tools:       []string{"fetch_tool"},
			Image:       "fetch/server:latest",
			Tags:        []string{"web", "fetch", "readability"},
		},
	}

	return h.NewConfigMapBuilder(name).
		WithToolHiveRegistry("registry.json", servers).
		Create(h), len(servers)
}

// GetConfigMap retrieves a ConfigMap by name
func (h *ConfigMapTestHelper) GetConfigMap(name string) (*corev1.ConfigMap, error) {
	cm := &corev1.ConfigMap{}
	err := h.Client.Get(h.Context, types.NamespacedName{
		Namespace: h.Namespace,
		Name:      name,
	}, cm)
	return cm, err
}

// UpdateConfigMap updates an existing ConfigMap
func (h *ConfigMapTestHelper) UpdateConfigMap(configMap *corev1.ConfigMap) error {
	return h.Client.Update(h.Context, configMap)
}

// DeleteConfigMap deletes a ConfigMap by name
func (h *ConfigMapTestHelper) DeleteConfigMap(name string) error {
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: h.Namespace,
		},
	}
	return h.Client.Delete(h.Context, cm)
}

// ListConfigMaps returns all ConfigMaps in the namespace
func (h *ConfigMapTestHelper) ListConfigMaps() (*corev1.ConfigMapList, error) {
	cmList := &corev1.ConfigMapList{}
	err := h.Client.List(h.Context, cmList, client.InNamespace(h.Namespace))
	return cmList, err
}

// CleanupConfigMaps deletes all test ConfigMaps in the namespace
func (h *ConfigMapTestHelper) CleanupConfigMaps() error {
	cmList, err := h.ListConfigMaps()
	if err != nil {
		return err
	}

	for _, cm := range cmList.Items {
		// Only delete ConfigMaps with our test label
		if cm.Labels != nil && cm.Labels["test.toolhive.io/suite"] == "operator-e2e" {
			ginkgo.By(fmt.Sprintf("deleting ConfigMap %s", cm.Name))
			if err := h.Client.Delete(h.Context, &cm); err != nil {
				return err
			}
		}
	}
	return nil
}

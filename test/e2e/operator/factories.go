package operator_test

import (
	"context"
	"crypto/rand"
	"fmt"
	"math/big"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
)

// TestDataFactory provides utilities for generating test data and resources
type TestDataFactory struct {
	Client    client.Client
	Context   context.Context
	Namespace string
}

// NewTestDataFactory creates a new test data factory
func NewTestDataFactory(ctx context.Context, k8sClient client.Client, namespace string) *TestDataFactory {
	return &TestDataFactory{
		Client:    k8sClient,
		Context:   ctx,
		Namespace: namespace,
	}
}

// MCPRegistryTemplate represents a template for creating MCPRegistry instances
type MCPRegistryTemplate struct {
	NamePrefix      string
	ConfigMapPrefix string
	SyncInterval    string
	Format          string
	Labels          map[string]string
	Annotations     map[string]string
	ServerCount     int
	WithSyncPolicy  bool
	WithFilter      bool
}

// DefaultMCPRegistryTemplate returns a default template for MCPRegistry creation
func (*TestDataFactory) DefaultMCPRegistryTemplate() MCPRegistryTemplate {
	return MCPRegistryTemplate{
		NamePrefix:      "test-registry",
		ConfigMapPrefix: "test-data",
		SyncInterval:    "1h",
		Format:          mcpv1alpha1.RegistryFormatToolHive,
		Labels: map[string]string{
			"test.toolhive.io/suite": "operator-e2e",
		},
		Annotations:    make(map[string]string),
		ServerCount:    2,
		WithSyncPolicy: true,
		WithFilter:     false,
	}
}

// CreateMCPRegistryFromTemplate creates an MCPRegistry based on a template
func (f *TestDataFactory) CreateMCPRegistryFromTemplate(template MCPRegistryTemplate) (
	*mcpv1alpha1.MCPRegistry, *corev1.ConfigMap, error) {
	// Generate unique names
	registryName := f.GenerateUniqueName(template.NamePrefix)
	configMapName := f.GenerateUniqueName(template.ConfigMapPrefix)

	// Create ConfigMap with test data
	configMap, err := f.CreateTestConfigMap(configMapName, template.Format, template.ServerCount)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create test ConfigMap: %w", err)
	}

	// Create MCPRegistry
	registry := &mcpv1alpha1.MCPRegistry{
		ObjectMeta: metav1.ObjectMeta{
			Name:        registryName,
			Namespace:   f.Namespace,
			Labels:      copyMap(template.Labels),
			Annotations: copyMap(template.Annotations),
		},
		Spec: mcpv1alpha1.MCPRegistrySpec{
			Source: mcpv1alpha1.MCPRegistrySource{
				Type:   mcpv1alpha1.RegistrySourceTypeConfigMap,
				Format: template.Format,
				ConfigMap: &mcpv1alpha1.ConfigMapSource{
					Name: configMapName,
					Key:  "registry.json",
				},
			},
		},
	}

	// Add sync policy if requested
	if template.WithSyncPolicy {
		registry.Spec.SyncPolicy = &mcpv1alpha1.SyncPolicy{
			Interval: template.SyncInterval,
		}
	}

	// Add filter if requested
	if template.WithFilter {
		registry.Spec.Filter = &mcpv1alpha1.RegistryFilter{
			NameFilters: &mcpv1alpha1.NameFilter{
				Include: []string{"*"},
				Exclude: []string{"test-*"},
			},
		}
	}

	// Create the registry
	if err := f.Client.Create(f.Context, registry); err != nil {
		// Clean up ConfigMap if registry creation fails
		_ = f.Client.Delete(f.Context, configMap)
		return nil, nil, fmt.Errorf("failed to create MCPRegistry: %w", err)
	}

	return registry, configMap, nil
}

// CreateTestConfigMap creates a ConfigMap with test registry data
func (f *TestDataFactory) CreateTestConfigMap(name, format string, serverCount int) (*corev1.ConfigMap, error) {
	configMapHelper := NewConfigMapTestHelper(f.Context, f.Client, f.Namespace)

	switch format {
	case mcpv1alpha1.RegistryFormatToolHive:
		servers := f.GenerateTestServers(serverCount)
		return configMapHelper.NewConfigMapBuilder(name).
			WithToolHiveRegistry("registry.json", servers).
			Create(configMapHelper), nil

	case mcpv1alpha1.RegistryFormatUpstream:
		servers := f.GenerateTestServersMap(serverCount)
		return configMapHelper.NewConfigMapBuilder(name).
			WithUpstreamRegistry("registry.json", servers).
			Create(configMapHelper), nil

	default:
		return nil, fmt.Errorf("unsupported registry format: %s", format)
	}
}

// GenerateTestServers generates a slice of test servers for ToolHive format
func (f *TestDataFactory) GenerateTestServers(count int) []RegistryServer {
	servers := make([]RegistryServer, count)
	for i := 0; i < count; i++ {
		servers[i] = f.GenerateTestServer(i)
	}
	return servers
}

// GenerateTestServersMap generates a map of test servers for upstream format
func (f *TestDataFactory) GenerateTestServersMap(count int) map[string]RegistryServer {
	servers := make(map[string]RegistryServer)
	for i := 0; i < count; i++ {
		server := f.GenerateTestServer(i)
		servers[server.Name] = server
	}
	return servers
}

// GenerateTestServer generates a single test server
func (*TestDataFactory) GenerateTestServer(index int) RegistryServer {
	serverTypes := []string{"filesystem", "fetch", "database", "search", "email"}
	transports := []string{"stdio", "sse", "http"}

	serverType := serverTypes[index%len(serverTypes)]
	transport := transports[index%len(transports)]

	return RegistryServer{
		Name:        fmt.Sprintf("%s-server-%d", serverType, index),
		Description: fmt.Sprintf("Test %s server for e2e testing", serverType),
		Tier:        "Community",
		Status:      "Active",
		Transport:   transport,
		Tools:       []string{fmt.Sprintf("%s_tool", serverType)},
		Image:       fmt.Sprintf("%s/server:1.%d.0", serverType, index),
		Tags:        []string{serverType, "test", fmt.Sprintf("v1-%d", index)},
	}
}

// GenerateUniqueName generates a unique name with timestamp and random suffix
func (*TestDataFactory) GenerateUniqueName(prefix string) string {
	timestamp := time.Now().Unix()
	// Use crypto/rand for secure random number generation
	randomBig, _ := rand.Int(rand.Reader, big.NewInt(1000))
	randomSuffix := randomBig.Int64()
	return fmt.Sprintf("%s-%d-%d", prefix, timestamp, randomSuffix)
}

// CreateTestSecret creates a test secret for authentication
func (f *TestDataFactory) CreateTestSecret(name string, data map[string][]byte) (*corev1.Secret, error) {
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: f.Namespace,
			Labels: map[string]string{
				"test.toolhive.io/suite": "operator-e2e",
			},
		},
		Data: data,
	}

	if err := f.Client.Create(f.Context, secret); err != nil {
		return nil, fmt.Errorf("failed to create secret: %w", err)
	}

	return secret, nil
}

// TestScenario represents a complete test scenario with multiple resources
type TestScenario struct {
	Name        string
	Description string
	Registries  []MCPRegistryTemplate
	ConfigMaps  []string
	Secrets     []string
}

// CommonTestScenarios returns a set of common test scenarios
func (f *TestDataFactory) CommonTestScenarios() map[string]TestScenario {
	return map[string]TestScenario{
		"basic-registry": {
			Name:        "Basic Registry",
			Description: "Single registry with ConfigMap source and sync policy",
			Registries: []MCPRegistryTemplate{
				f.DefaultMCPRegistryTemplate(),
			},
		},
		"manual-sync-registry": {
			Name:        "Manual Sync Registry",
			Description: "Registry without automatic sync policy",
			Registries: []MCPRegistryTemplate{
				func() MCPRegistryTemplate {
					template := f.DefaultMCPRegistryTemplate()
					template.WithSyncPolicy = false
					return template
				}(),
			},
		},
		"upstream-format-registry": {
			Name:        "Upstream Format Registry",
			Description: "Registry using upstream MCP format",
			Registries: []MCPRegistryTemplate{
				func() MCPRegistryTemplate {
					template := f.DefaultMCPRegistryTemplate()
					template.Format = mcpv1alpha1.RegistryFormatUpstream
					return template
				}(),
			},
		},
		"filtered-registry": {
			Name:        "Filtered Registry",
			Description: "Registry with content filtering",
			Registries: []MCPRegistryTemplate{
				func() MCPRegistryTemplate {
					template := f.DefaultMCPRegistryTemplate()
					template.WithFilter = true
					return template
				}(),
			},
		},
		"multiple-registries": {
			Name:        "Multiple Registries",
			Description: "Multiple registries with different configurations",
			Registries: []MCPRegistryTemplate{
				f.DefaultMCPRegistryTemplate(),
				func() MCPRegistryTemplate {
					template := f.DefaultMCPRegistryTemplate()
					template.NamePrefix = "secondary-registry"
					template.Format = mcpv1alpha1.RegistryFormatUpstream
					template.SyncInterval = "30m"
					return template
				}(),
			},
		},
	}
}

// CreateTestScenario creates all resources for a test scenario
func (f *TestDataFactory) CreateTestScenario(scenario TestScenario) (*TestScenarioResources, error) {
	resources := &TestScenarioResources{
		Registries: make([]*mcpv1alpha1.MCPRegistry, 0),
		ConfigMaps: make([]*corev1.ConfigMap, 0),
		Secrets:    make([]*corev1.Secret, 0),
	}

	// Create registries
	for _, template := range scenario.Registries {
		registry, configMap, err := f.CreateMCPRegistryFromTemplate(template)
		if err != nil {
			// Clean up already created resources
			_ = f.CleanupTestScenarioResources(resources)
			return nil, fmt.Errorf("failed to create registry from template: %w", err)
		}
		resources.Registries = append(resources.Registries, registry)
		resources.ConfigMaps = append(resources.ConfigMaps, configMap)
	}

	return resources, nil
}

// TestScenarioResources holds all resources created for a test scenario
type TestScenarioResources struct {
	Registries []*mcpv1alpha1.MCPRegistry
	ConfigMaps []*corev1.ConfigMap
	Secrets    []*corev1.Secret
}

// CleanupTestScenarioResources cleans up all resources in a test scenario
func (f *TestDataFactory) CleanupTestScenarioResources(resources *TestScenarioResources) error {
	var errors []error

	// Delete registries
	for _, registry := range resources.Registries {
		if err := f.Client.Delete(f.Context, registry); err != nil {
			errors = append(errors, fmt.Errorf("failed to delete registry %s: %w", registry.Name, err))
		}
	}

	// Delete ConfigMaps
	for _, cm := range resources.ConfigMaps {
		if err := f.Client.Delete(f.Context, cm); err != nil {
			errors = append(errors, fmt.Errorf("failed to delete ConfigMap %s: %w", cm.Name, err))
		}
	}

	// Delete Secrets
	for _, secret := range resources.Secrets {
		if err := f.Client.Delete(f.Context, secret); err != nil {
			errors = append(errors, fmt.Errorf("failed to delete Secret %s: %w", secret.Name, err))
		}
	}

	if len(errors) > 0 {
		return fmt.Errorf("cleanup errors: %v", errors)
	}

	return nil
}

// RandomRegistryData generates random registry data for stress testing
func (f *TestDataFactory) RandomRegistryData(serverCount int) []RegistryServer {
	servers := make([]RegistryServer, serverCount)

	for i := 0; i < serverCount; i++ {
		serverName := f.randomServerName()
		servers[i] = RegistryServer{
			Name:        serverName,
			Description: f.randomDescription(),
			Tier:        f.randomTier(),
			Status:      "Active",
			Transport:   f.randomTransport(),
			Tools:       []string{fmt.Sprintf("%s_tool", serverName)},
			Image:       fmt.Sprintf("%s/server:%s", serverName, f.randomVersion()),
			Tags:        f.randomTags(),
		}
	}

	return servers
}

// Helper functions for random data generation
func (*TestDataFactory) randomServerName() string {
	prefixes := []string{"test", "demo", "sample", "mock", "fake"}
	suffixes := []string{"server", "service", "tool", "handler", "processor"}

	prefixBig, _ := rand.Int(rand.Reader, big.NewInt(int64(len(prefixes))))
	suffixBig, _ := rand.Int(rand.Reader, big.NewInt(int64(len(suffixes))))
	numBig, _ := rand.Int(rand.Reader, big.NewInt(1000))

	prefix := prefixes[prefixBig.Int64()]
	suffix := suffixes[suffixBig.Int64()]

	return fmt.Sprintf("%s-%s-%d", prefix, suffix, numBig.Int64())
}

func (*TestDataFactory) randomDescription() string {
	templates := []string{
		"A test server for %s operations",
		"Mock %s implementation for testing",
		"Sample %s service with basic functionality",
		"Demo %s tool for development purposes",
	}

	operations := []string{"file", "network", "database", "authentication", "processing"}

	templateBig, _ := rand.Int(rand.Reader, big.NewInt(int64(len(templates))))
	operationBig, _ := rand.Int(rand.Reader, big.NewInt(int64(len(operations))))

	template := templates[templateBig.Int64()]
	operation := operations[operationBig.Int64()]

	return fmt.Sprintf(template, operation)
}

func (*TestDataFactory) randomVersion() string {
	majorBig, _ := rand.Int(rand.Reader, big.NewInt(3))
	minorBig, _ := rand.Int(rand.Reader, big.NewInt(10))
	patchBig, _ := rand.Int(rand.Reader, big.NewInt(20))

	major := majorBig.Int64() + 1
	minor := minorBig.Int64()
	patch := patchBig.Int64()

	return fmt.Sprintf("%d.%d.%d", major, minor, patch)
}

func (*TestDataFactory) randomTransport() string {
	transports := []string{"stdio", "sse", "http"}
	transportBig, _ := rand.Int(rand.Reader, big.NewInt(int64(len(transports))))
	return transports[transportBig.Int64()]
}

func (*TestDataFactory) randomTier() string {
	tiers := []string{"Community", "Official", "Enterprise"}
	tierBig, _ := rand.Int(rand.Reader, big.NewInt(int64(len(tiers))))
	return tiers[tierBig.Int64()]
}

func (*TestDataFactory) randomTags() []string {
	allTags := []string{"test", "demo", "sample", "mock", "development", "staging", "production"}
	countBig, _ := rand.Int(rand.Reader, big.NewInt(3))
	count := int(countBig.Int64()) + 1

	tags := make([]string, count)
	for i := 0; i < count; i++ {
		tagBig, _ := rand.Int(rand.Reader, big.NewInt(int64(len(allTags))))
		tags[i] = allTags[tagBig.Int64()]
	}

	return tags
}

// Utility function to copy maps
func copyMap(m map[string]string) map[string]string {
	if m == nil {
		return nil
	}

	result := make(map[string]string)
	for k, v := range m {
		result[k] = v
	}
	return result
}

// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package operator_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
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

const (
	sourceTypeFile = "file"
	sourceTypeGit  = "git"
	sourceTypeAPI  = "api"
)

// registryBuilderConfig holds the configuration data used to generate configYAML
type registryBuilderConfig struct {
	SourceName   string
	Format       string
	SourceType   string
	FilePath     string // for file sources: path inside the mounted volume
	GitRepo      string
	GitBranch    string
	GitPath      string
	APIEndpoint  string
	SyncInterval string
	NameInclude  []string
	NameExclude  []string
	TagInclude   []string
	TagExclude   []string
	// ConfigMap source details (for volume/mount generation)
	ConfigMapName string
	ConfigMapKey  string
}

// RegistryBuilder provides a fluent interface for building MCPRegistry objects
type RegistryBuilder struct {
	name        string
	namespace   string
	labels      map[string]string
	annotations map[string]string
	config      registryBuilderConfig
}

// NewRegistryBuilder creates a new MCPRegistry builder
func (h *MCPRegistryTestHelper) NewRegistryBuilder(name string) *RegistryBuilder {
	return &RegistryBuilder{
		name:      name,
		namespace: h.Namespace,
		labels: map[string]string{
			"test.toolhive.io/suite": "operator-e2e",
		},
		config: registryBuilderConfig{
			SourceName: "default",
			Format:     "toolhive",
		},
	}
}

// WithConfigMapSource configures the registry with a ConfigMap-backed file source.
// It sets source type to file and records ConfigMap details for volume/mount generation.
func (rb *RegistryBuilder) WithConfigMapSource(configMapName, key string) *RegistryBuilder {
	rb.config.SourceType = sourceTypeFile
	rb.config.ConfigMapName = configMapName
	rb.config.ConfigMapKey = key
	rb.config.FilePath = fmt.Sprintf("/config/registry/%s/registry.json", rb.config.SourceName)
	return rb
}

// WithGitSource configures the registry with a Git source
func (rb *RegistryBuilder) WithGitSource(repository, branch, path string) *RegistryBuilder {
	rb.config.SourceType = sourceTypeGit
	rb.config.GitRepo = repository
	rb.config.GitBranch = branch
	rb.config.GitPath = path
	return rb
}

// WithAPISource configures the registry with an API source
func (rb *RegistryBuilder) WithAPISource(endpoint string) *RegistryBuilder {
	rb.config.SourceType = sourceTypeAPI
	rb.config.APIEndpoint = endpoint
	return rb
}

// WithRegistryName sets the name for the source config
func (rb *RegistryBuilder) WithRegistryName(name string) *RegistryBuilder {
	rb.config.SourceName = name
	// Recalculate file path if this is a file source
	if rb.config.SourceType == sourceTypeFile {
		rb.config.FilePath = fmt.Sprintf("/config/registry/%s/registry.json", name)
	}
	return rb
}

// WithUpstreamFormat configures the source to use upstream MCP format
func (rb *RegistryBuilder) WithUpstreamFormat() *RegistryBuilder {
	rb.config.Format = "upstream"
	return rb
}

// WithSyncPolicy configures the sync policy interval for the source
func (rb *RegistryBuilder) WithSyncPolicy(interval string) *RegistryBuilder {
	rb.config.SyncInterval = interval
	return rb
}

// WithAnnotation adds an annotation to the registry
func (rb *RegistryBuilder) WithAnnotation(key, value string) *RegistryBuilder {
	if rb.annotations == nil {
		rb.annotations = make(map[string]string)
	}
	rb.annotations[key] = value
	return rb
}

// WithLabel adds a label to the registry
func (rb *RegistryBuilder) WithLabel(key, value string) *RegistryBuilder {
	if rb.labels == nil {
		rb.labels = make(map[string]string)
	}
	rb.labels[key] = value
	return rb
}

// WithNameIncludeFilter sets name include patterns for filtering on the source
func (rb *RegistryBuilder) WithNameIncludeFilter(patterns []string) *RegistryBuilder {
	rb.config.NameInclude = patterns
	return rb
}

// WithNameExcludeFilter sets name exclude patterns for filtering on the source
func (rb *RegistryBuilder) WithNameExcludeFilter(patterns []string) *RegistryBuilder {
	rb.config.NameExclude = patterns
	return rb
}

// WithTagIncludeFilter sets tag include patterns for filtering on the source
func (rb *RegistryBuilder) WithTagIncludeFilter(tags []string) *RegistryBuilder {
	rb.config.TagInclude = tags
	return rb
}

// WithTagExcludeFilter sets tag exclude patterns for filtering on the source
func (rb *RegistryBuilder) WithTagExcludeFilter(tags []string) *RegistryBuilder {
	rb.config.TagExclude = tags
	return rb
}

// Build returns the constructed MCPRegistry with configYAML generated from the builder config.
func (rb *RegistryBuilder) Build() *mcpv1alpha1.MCPRegistry {
	configYAML := rb.buildConfigYAML()

	spec := mcpv1alpha1.MCPRegistrySpec{
		ConfigYAML: configYAML,
	}

	// For ConfigMap file sources, add the volume and volume mount
	if rb.config.SourceType == sourceTypeFile && rb.config.ConfigMapName != "" {
		vol := corev1.Volume{
			Name: fmt.Sprintf("registry-data-source-%s", rb.config.SourceName),
			VolumeSource: corev1.VolumeSource{
				ConfigMap: &corev1.ConfigMapVolumeSource{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: rb.config.ConfigMapName,
					},
					Items: []corev1.KeyToPath{
						{
							Key:  rb.config.ConfigMapKey,
							Path: "registry.json",
						},
					},
				},
			},
		}
		volJSON, err := json.Marshal(vol)
		gomega.Expect(err).NotTo(gomega.HaveOccurred(), "Failed to marshal volume")
		spec.Volumes = []apiextensionsv1.JSON{{Raw: volJSON}}

		mount := corev1.VolumeMount{
			Name:      fmt.Sprintf("registry-data-source-%s", rb.config.SourceName),
			MountPath: fmt.Sprintf("/config/registry/%s", rb.config.SourceName),
			ReadOnly:  true,
		}
		mountJSON, err := json.Marshal(mount)
		gomega.Expect(err).NotTo(gomega.HaveOccurred(), "Failed to marshal volume mount")
		spec.VolumeMounts = []apiextensionsv1.JSON{{Raw: mountJSON}}
	}

	return &mcpv1alpha1.MCPRegistry{
		ObjectMeta: metav1.ObjectMeta{
			Name:        rb.name,
			Namespace:   rb.namespace,
			Labels:      rb.labels,
			Annotations: rb.annotations,
		},
		Spec: spec,
	}
}

// Create builds and creates the MCPRegistry in the cluster
func (rb *RegistryBuilder) Create(h *MCPRegistryTestHelper) *mcpv1alpha1.MCPRegistry {
	registry := rb.Build()
	err := h.Client.Create(h.Context, registry)
	gomega.Expect(err).NotTo(gomega.HaveOccurred(), "Failed to create MCPRegistry")
	return registry
}

// buildConfigYAML generates the config.yaml content from the builder config
func (rb *RegistryBuilder) buildConfigYAML() string {
	var b strings.Builder

	// Sources section
	b.WriteString("sources:\n")
	fmt.Fprintf(&b, "  - name: %s\n", rb.config.SourceName)
	fmt.Fprintf(&b, "    format: %s\n", rb.config.Format)

	// Source type specific fields
	switch rb.config.SourceType {
	case sourceTypeFile:
		b.WriteString("    file:\n")
		fmt.Fprintf(&b, "      path: %s\n", rb.config.FilePath)
	case sourceTypeGit:
		b.WriteString("    git:\n")
		fmt.Fprintf(&b, "      repository: %s\n", rb.config.GitRepo)
		fmt.Fprintf(&b, "      branch: %s\n", rb.config.GitBranch)
		fmt.Fprintf(&b, "      path: %s\n", rb.config.GitPath)
	case sourceTypeAPI:
		b.WriteString("    api:\n")
		fmt.Fprintf(&b, "      endpoint: %s\n", rb.config.APIEndpoint)
	}

	// Sync policy
	if rb.config.SyncInterval != "" {
		b.WriteString("    syncPolicy:\n")
		fmt.Fprintf(&b, "      interval: %s\n", rb.config.SyncInterval)
	}

	// Filter
	rb.writeFilterYAML(&b)

	// Registries section
	b.WriteString("registries:\n")
	b.WriteString("  - name: default\n")
	fmt.Fprintf(&b, "    sources:\n      - %s\n", rb.config.SourceName)

	// Database defaults
	b.WriteString("database:\n")
	b.WriteString("  host: postgres\n")
	b.WriteString("  port: 5432\n")
	b.WriteString("  user: db_app\n")
	b.WriteString("  database: registry\n")

	// Auth defaults
	b.WriteString("auth:\n")
	b.WriteString("  mode: anonymous\n")

	return b.String()
}

// writeFilterYAML writes filter configuration to the YAML builder
func (rb *RegistryBuilder) writeFilterYAML(b *strings.Builder) {
	hasNames := len(rb.config.NameInclude) > 0 || len(rb.config.NameExclude) > 0
	hasTags := len(rb.config.TagInclude) > 0 || len(rb.config.TagExclude) > 0

	if !hasNames && !hasTags {
		return
	}

	b.WriteString("    filter:\n")

	if hasNames {
		b.WriteString("      names:\n")
		writeStringList(b, "        include:\n", rb.config.NameInclude)
		writeStringList(b, "        exclude:\n", rb.config.NameExclude)
	}

	if hasTags {
		b.WriteString("      tags:\n")
		writeStringList(b, "        include:\n", rb.config.TagInclude)
		writeStringList(b, "        exclude:\n", rb.config.TagExclude)
	}
}

// writeStringList writes a labeled YAML list if items is non-empty
func writeStringList(b *strings.Builder, label string, items []string) {
	if len(items) == 0 {
		return
	}
	b.WriteString(label)
	for _, item := range items {
		fmt.Fprintf(b, "          - %s\n", item)
	}
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

// WaitForRegistryInitialization waits for common initialization steps after registry creation:
// 1. Wait for finalizer to be added
// 2. Wait for controller to process the registry into an acceptable initial phase
func (h *MCPRegistryTestHelper) WaitForRegistryInitialization(registryName string,
	timingHelper *TimingTestHelper, statusHelper *StatusTestHelper) {
	// Wait for finalizer to be added
	ginkgo.By("waiting for finalizer to be added")
	timingHelper.WaitForControllerReconciliation(func() interface{} {
		updatedRegistry, err := h.GetRegistry(registryName)
		if err != nil {
			return false
		}
		return containsFinalizer(updatedRegistry.Finalizers, "mcpregistry.toolhive.stacklok.dev/finalizer")
	}).Should(gomega.BeTrue())

	// Wait for controller to process and verify initial status
	ginkgo.By("waiting for controller to process and verify initial status")
	statusHelper.WaitForPhaseAny(registryName, []mcpv1alpha1.MCPRegistryPhase{
		mcpv1alpha1.MCPRegistryPhasePending,
		mcpv1alpha1.MCPRegistryPhaseReady,
	}, MediumTimeout)
}

// containsFinalizer checks if the registry finalizer exists in the list
func containsFinalizer(finalizers []string, _ string) bool {
	const registryFinalizer = "mcpregistry.toolhive.stacklok.dev/finalizer"
	for _, f := range finalizers {
		if f == registryFinalizer {
			return true
		}
	}
	return false
}

// buildConfigYAMLForMultipleSources generates a configYAML string for multiple sources.
// Each source is specified as a map with keys: name, format, sourceType, and type-specific fields.
func buildConfigYAMLForMultipleSources(sources []map[string]string) string {
	var b strings.Builder

	b.WriteString("sources:\n")
	for _, src := range sources {
		fmt.Fprintf(&b, "  - name: %s\n", src["name"])
		format := src["format"]
		if format == "" {
			format = "toolhive"
		}
		fmt.Fprintf(&b, "    format: %s\n", format)

		switch src["sourceType"] {
		case sourceTypeFile:
			b.WriteString("    file:\n")
			fmt.Fprintf(&b, "      path: %s\n", src["filePath"])
		case sourceTypeGit:
			b.WriteString("    git:\n")
			fmt.Fprintf(&b, "      repository: %s\n", src["repository"])
			fmt.Fprintf(&b, "      branch: %s\n", src["branch"])
			fmt.Fprintf(&b, "      path: %s\n", src["path"])
			if src["authUsername"] != "" {
				b.WriteString("      auth:\n")
				fmt.Fprintf(&b, "        username: %s\n", src["authUsername"])
				fmt.Fprintf(&b, "        passwordFile: %s\n", src["authPasswordFile"])
			}
		case sourceTypeAPI:
			b.WriteString("    api:\n")
			fmt.Fprintf(&b, "      endpoint: %s\n", src["endpoint"])
		}

		if interval, ok := src["interval"]; ok && interval != "" {
			b.WriteString("    syncPolicy:\n")
			fmt.Fprintf(&b, "      interval: %s\n", interval)
		}
	}

	// Registries section with all source names
	b.WriteString("registries:\n")
	b.WriteString("  - name: default\n")
	b.WriteString("    sources:\n")
	for _, src := range sources {
		fmt.Fprintf(&b, "      - %s\n", src["name"])
	}

	// Database defaults
	b.WriteString("database:\n")
	b.WriteString("  host: postgres\n")
	b.WriteString("  port: 5432\n")
	b.WriteString("  user: db_app\n")
	b.WriteString("  database: registry\n")

	// Auth defaults
	b.WriteString("auth:\n")
	b.WriteString("  mode: anonymous\n")

	return b.String()
}

// mustMarshalJSON marshals a value to JSON, panicking on error (for test helpers only)
func mustMarshalJSON(v interface{}) []byte {
	data, err := json.Marshal(v)
	gomega.Expect(err).NotTo(gomega.HaveOccurred(), "Failed to marshal JSON in test helper")
	return data
}

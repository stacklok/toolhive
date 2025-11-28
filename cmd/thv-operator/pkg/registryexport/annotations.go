// Package registryexport provides functionality for automatically exporting
// MCP resources to the registry based on annotations.
package registryexport

const (
	// AnnotationRegistryURL triggers export when present on an MCP resource.
	AnnotationRegistryURL = "toolhive.stacklok.dev/registry-url"
	// AnnotationRegistryDescription is required for registry entries.
	AnnotationRegistryDescription = "toolhive.stacklok.dev/registry-description"
	// AnnotationRegistryName overrides the auto-generated server name.
	AnnotationRegistryName = "toolhive.stacklok.dev/registry-name"

	// ConfigMapSuffix is appended to namespace for the export ConfigMap name.
	ConfigMapSuffix = "-registry-export"
	// ConfigMapKey is the key in the ConfigMap for registry data.
	ConfigMapKey = "registry.json"
	// LabelRegistryExport identifies ConfigMaps created by this controller.
	LabelRegistryExport = "toolhive.stacklok.dev/registry-export"
	// LabelRegistryExportValue is the value for the registry export label.
	LabelRegistryExportValue = "true"
	// EnvEnableRegistryExport toggles the registry export feature.
	EnvEnableRegistryExport = "ENABLE_REGISTRY_EXPORT"
)

// GetConfigMapName returns the ConfigMap name for a given namespace.
func GetConfigMapName(namespace string) string {
	return namespace + ConfigMapSuffix
}

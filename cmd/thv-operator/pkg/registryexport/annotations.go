// Package registryexport provides functionality for automatically exporting
// MCP resources to the registry based on annotations.
package registryexport

const (
	// AnnotationRegistryURL is the external URL for registry export.
	// When present on an MCP resource, the resource will be exported to the registry.
	AnnotationRegistryURL = "toolhive.stacklok.dev/registry-url"

	// AnnotationRegistryDescription is the description for servers not in a source registry.
	// Required when creating new registry entries for internal/custom servers.
	AnnotationRegistryDescription = "toolhive.stacklok.dev/registry-description"

	// AnnotationRegistryName overrides the generated server name in the registry.
	// If not specified, a name is generated from namespace/name in reverse-DNS format.
	AnnotationRegistryName = "toolhive.stacklok.dev/registry-name"

	// ConfigMapSuffix is appended to namespace for the export ConfigMap name.
	ConfigMapSuffix = "-registry-export"

	// ConfigMapKey is the key in the ConfigMap for registry data.
	ConfigMapKey = "registry.json"

	// LabelRegistryExport identifies ConfigMaps created by the registry export controller.
	LabelRegistryExport = "toolhive.stacklok.dev/registry-export"

	// LabelRegistryExportValue is the value for the registry export label.
	LabelRegistryExportValue = "true"

	// EnvEnableRegistryExport is the environment variable to enable/disable registry export.
	EnvEnableRegistryExport = "ENABLE_REGISTRY_EXPORT"
)

// GetConfigMapName returns the ConfigMap name for a given namespace.
func GetConfigMapName(namespace string) string {
	return namespace + ConfigMapSuffix
}

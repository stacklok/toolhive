// Package accessors provides accessor functions for the ToolHive operator
package accessors

import (
	"maps"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
)

// MCPServerFieldAccessor provides accessor methods for handling labels and annotations
type MCPServerFieldAccessor interface {
	// GetProxyDeploymentLabelsAndAnnotations returns labels and annotations for the deployment
	GetProxyDeploymentLabelsAndAnnotations(mcpServer *mcpv1alpha1.MCPServer) (labels, annotations map[string]string)

	// GetProxyDeploymentTemplateLabelsAndAnnotations returns labels and annotations for the deployment pod template
	GetProxyDeploymentTemplateLabelsAndAnnotations(mcpServer *mcpv1alpha1.MCPServer) (labels, annotations map[string]string)
}

// mcpServerFieldAccessor implements MCPServerFieldAccessor
type mcpServerFieldAccessor struct{}

// NewMCPServerFieldAccessor creates a new MCPServerFieldAccessor instance
func NewMCPServerFieldAccessor() MCPServerFieldAccessor {
	return &mcpServerFieldAccessor{}
}

// GetProxyDeploymentLabelsAndAnnotations returns labels and annotations for the deployment
func (*mcpServerFieldAccessor) GetProxyDeploymentLabelsAndAnnotations(
	mcpServer *mcpv1alpha1.MCPServer,
) (map[string]string, map[string]string) {
	baseAnnotations := make(map[string]string)
	baseLabels := make(map[string]string)

	if mcpServer.Spec.ResourceOverrides == nil ||
		mcpServer.Spec.ResourceOverrides.ProxyDeployment == nil {
		return baseLabels, baseAnnotations
	}

	deploymentLabels := baseLabels
	deploymentAnnotations := baseAnnotations

	if mcpServer.Spec.ResourceOverrides.ProxyDeployment.Labels != nil {
		deploymentLabels = mergeLabels(baseLabels, mcpServer.Spec.ResourceOverrides.ProxyDeployment.Labels)
	}
	if mcpServer.Spec.ResourceOverrides.ProxyDeployment.Annotations != nil {
		deploymentAnnotations = mergeAnnotations(baseAnnotations, mcpServer.Spec.ResourceOverrides.ProxyDeployment.Annotations)
	}

	return deploymentLabels, deploymentAnnotations
}

// GetProxyDeploymentTemplateLabelsAndAnnotations returns labels and annotations for the deployment pod template
func (*mcpServerFieldAccessor) GetProxyDeploymentTemplateLabelsAndAnnotations(
	mcpServer *mcpv1alpha1.MCPServer,
) (map[string]string, map[string]string) {
	baseAnnotations := make(map[string]string)
	baseLabels := make(map[string]string)

	if mcpServer.Spec.ResourceOverrides == nil ||
		mcpServer.Spec.ResourceOverrides.ProxyDeployment == nil ||
		mcpServer.Spec.ResourceOverrides.ProxyDeployment.PodTemplateMetadataOverrides == nil {
		return baseLabels, baseAnnotations
	}

	deploymentLabels := baseLabels
	deploymentAnnotations := baseAnnotations

	if mcpServer.Spec.ResourceOverrides.ProxyDeployment.PodTemplateMetadataOverrides.Labels != nil {
		deploymentLabels = mergeLabels(baseLabels, mcpServer.Spec.ResourceOverrides.ProxyDeployment.PodTemplateMetadataOverrides.Labels)
	}
	if mcpServer.Spec.ResourceOverrides.ProxyDeployment.PodTemplateMetadataOverrides.Annotations != nil {
		overrides := mcpServer.Spec.ResourceOverrides.ProxyDeployment.PodTemplateMetadataOverrides.Annotations
		deploymentAnnotations = mergeAnnotations(baseAnnotations, overrides)
	}

	return deploymentLabels, deploymentAnnotations
}

// mergeLabels merges override labels with default labels, with default labels taking precedence
func mergeLabels(defaultLabels, overrideLabels map[string]string) map[string]string {
	return mergeStringMaps(defaultLabels, overrideLabels)
}

// mergeAnnotations merges override annotations with default annotations, with default annotations taking precedence
func mergeAnnotations(defaultAnnotations, overrideAnnotations map[string]string) map[string]string {
	return mergeStringMaps(defaultAnnotations, overrideAnnotations)
}

// mergeStringMaps merges override map with default map, with default map taking precedence
// This ensures that operator-required metadata is preserved for proper functionality
func mergeStringMaps(defaultMap, overrideMap map[string]string) map[string]string {
	if overrideMap == nil && defaultMap == nil {
		return make(map[string]string)
	}
	if overrideMap == nil {
		return maps.Clone(defaultMap)
	}
	result := maps.Clone(overrideMap)
	if defaultMap != nil {
		maps.Copy(result, defaultMap) // default map takes precedence
	}
	return result
}

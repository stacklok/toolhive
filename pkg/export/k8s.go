// Package export provides functionality for exporting ToolHive configurations to various formats.
package export

import (
	"fmt"
	"io"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/yaml"

	v1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	"github.com/stacklok/toolhive/pkg/runner"
	"github.com/stacklok/toolhive/pkg/transport/types"
)

// WriteK8sManifest converts a RunConfig to a Kubernetes MCPServer resource and writes it as YAML
func WriteK8sManifest(config *runner.RunConfig, w io.Writer) error {
	mcpServer, err := runConfigToMCPServer(config)
	if err != nil {
		return fmt.Errorf("failed to convert RunConfig to MCPServer: %w", err)
	}

	yamlBytes, err := yaml.Marshal(mcpServer)
	if err != nil {
		return fmt.Errorf("failed to marshal MCPServer to YAML: %w", err)
	}

	_, err = w.Write(yamlBytes)
	return err
}

// runConfigToMCPServer converts a RunConfig to a Kubernetes MCPServer resource
// nolint:gocyclo // Complexity due to mapping multiple config fields to K8s resource
func runConfigToMCPServer(config *runner.RunConfig) (*v1alpha1.MCPServer, error) {
	// Check if this is a remote server - not supported in Kubernetes
	if config.RemoteURL != "" {
		return nil, fmt.Errorf("remote MCP servers are not supported in Kubernetes deployments")
	}

	// Verify we have an image - required for Kubernetes
	if config.Image == "" {
		return nil, fmt.Errorf("image is required for Kubernetes export")
	}

	// Use the base name or container name for the Kubernetes resource name
	name := config.BaseName
	if name == "" {
		name = config.ContainerName
	}
	if name == "" {
		name = config.Name
	}

	// Sanitize the name to be a valid Kubernetes resource name
	name = sanitizeK8sName(name)

	mcpServer := &v1alpha1.MCPServer{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "toolhive.stacklok.dev/v1alpha1",
			Kind:       "MCPServer",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		Spec: v1alpha1.MCPServerSpec{
			Image:     config.Image,
			Transport: string(config.Transport),
			Args:      config.CmdArgs,
		},
	}

	// Set port if specified
	if config.Port > 0 {
		// #nosec G115 -- Port values are validated elsewhere, safe conversion
		mcpServer.Spec.Port = int32(config.Port)
	}

	// Set target port if specified
	if config.TargetPort > 0 {
		// #nosec G115 -- Port values are validated elsewhere, safe conversion
		mcpServer.Spec.TargetPort = int32(config.TargetPort)
	}

	// Set proxy mode if transport is stdio
	if config.Transport == types.TransportTypeStdio && config.ProxyMode != "" {
		mcpServer.Spec.ProxyMode = string(config.ProxyMode)
	}

	// Convert environment variables
	if len(config.EnvVars) > 0 {
		mcpServer.Spec.Env = make([]v1alpha1.EnvVar, 0, len(config.EnvVars))
		for key, value := range config.EnvVars {
			mcpServer.Spec.Env = append(mcpServer.Spec.Env, v1alpha1.EnvVar{
				Name:  key,
				Value: value,
			})
		}
	}

	// Convert volumes
	if len(config.Volumes) > 0 {
		mcpServer.Spec.Volumes = make([]v1alpha1.Volume, 0, len(config.Volumes))
		for i, vol := range config.Volumes {
			volume, err := parseVolumeString(vol, i)
			if err != nil {
				return nil, fmt.Errorf("failed to parse volume %q: %w", vol, err)
			}
			mcpServer.Spec.Volumes = append(mcpServer.Spec.Volumes, volume)
		}
	}

	// Convert permission profile
	if config.PermissionProfile != nil {
		// For now, we export permission profiles as inline ConfigMaps would need to be created separately
		// This is a simplified export - users may need to adjust this
		mcpServer.Spec.PermissionProfile = &v1alpha1.PermissionProfileRef{
			Type: v1alpha1.PermissionProfileTypeBuiltin,
			Name: "none", // Default to none, user should adjust based on their needs
		}
	}

	// Convert OIDC config
	if config.OIDCConfig != nil {
		mcpServer.Spec.OIDCConfig = &v1alpha1.OIDCConfigRef{
			Type: v1alpha1.OIDCConfigTypeInline,
			Inline: &v1alpha1.InlineOIDCConfig{
				Issuer:   config.OIDCConfig.Issuer,
				Audience: config.OIDCConfig.Audience,
			},
		}

		if config.OIDCConfig.JWKSURL != "" {
			mcpServer.Spec.OIDCConfig.Inline.JWKSURL = config.OIDCConfig.JWKSURL
		}
	}

	// Convert authz config
	if config.AuthzConfig != nil && config.AuthzConfig.Cedar != nil && len(config.AuthzConfig.Cedar.Policies) > 0 {
		mcpServer.Spec.AuthzConfig = &v1alpha1.AuthzConfigRef{
			Type: v1alpha1.AuthzConfigTypeInline,
			Inline: &v1alpha1.InlineAuthzConfig{
				Policies: config.AuthzConfig.Cedar.Policies,
			},
		}

		if config.AuthzConfig.Cedar.EntitiesJSON != "" {
			mcpServer.Spec.AuthzConfig.Inline.EntitiesJSON = config.AuthzConfig.Cedar.EntitiesJSON
		}
	}

	// Convert audit config - audit is always enabled if config exists
	if config.AuditConfig != nil {
		mcpServer.Spec.Audit = &v1alpha1.AuditConfig{
			Enabled: true,
		}
	}

	// Convert telemetry config
	if config.TelemetryConfig != nil {
		mcpServer.Spec.Telemetry = &v1alpha1.TelemetryConfig{}

		if config.TelemetryConfig.Endpoint != "" {
			mcpServer.Spec.Telemetry.OpenTelemetry = &v1alpha1.OpenTelemetryConfig{
				Enabled:  true,
				Endpoint: config.TelemetryConfig.Endpoint,
				Insecure: config.TelemetryConfig.Insecure,
			}

			if config.TelemetryConfig.ServiceName != "" {
				mcpServer.Spec.Telemetry.OpenTelemetry.ServiceName = config.TelemetryConfig.ServiceName
			}
		}

		// Convert Prometheus metrics path setting
		if config.TelemetryConfig.EnablePrometheusMetricsPath {
			if mcpServer.Spec.Telemetry.Prometheus == nil {
				mcpServer.Spec.Telemetry.Prometheus = &v1alpha1.PrometheusConfig{}
			}
			mcpServer.Spec.Telemetry.Prometheus.Enabled = true
		}
	}

	// Convert tools filter
	if len(config.ToolsFilter) > 0 {
		mcpServer.Spec.ToolsFilter = config.ToolsFilter
	}

	return mcpServer, nil
}

// parseVolumeString parses a volume string in the format "host-path:container-path[:ro]"
func parseVolumeString(volStr string, index int) (v1alpha1.Volume, error) {
	parts := strings.Split(volStr, ":")
	if len(parts) < 2 {
		return v1alpha1.Volume{}, fmt.Errorf("invalid volume format, expected 'host-path:container-path[:ro]'")
	}

	volume := v1alpha1.Volume{
		Name:      fmt.Sprintf("volume-%d", index),
		HostPath:  parts[0],
		MountPath: parts[1],
		ReadOnly:  false,
	}

	// Check for read-only flag
	if len(parts) == 3 && parts[2] == "ro" {
		volume.ReadOnly = true
	}

	return volume, nil
}

// sanitizeK8sName sanitizes a string to be a valid Kubernetes resource name
// Kubernetes names must be lowercase alphanumeric with hyphens, max 253 chars
func sanitizeK8sName(name string) string {
	// Convert to lowercase
	name = strings.ToLower(name)

	// Replace invalid characters with hyphens
	var result strings.Builder
	for _, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			result.WriteRune(r)
		} else {
			result.WriteRune('-')
		}
	}

	// Remove leading/trailing hyphens
	sanitized := strings.Trim(result.String(), "-")

	// Limit length to 253 characters (Kubernetes limit)
	if len(sanitized) > 253 {
		sanitized = sanitized[:253]
	}

	// Ensure we don't end with a hyphen after truncation
	sanitized = strings.TrimRight(sanitized, "-")

	return sanitized
}

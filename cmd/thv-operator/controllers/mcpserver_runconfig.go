package controllers

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/log"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	ctrlutil "github.com/stacklok/toolhive/cmd/thv-operator/pkg/controllerutil"
	runconfig "github.com/stacklok/toolhive/cmd/thv-operator/pkg/runconfig"
	"github.com/stacklok/toolhive/cmd/thv-operator/pkg/runconfig/configmap"
	configMapChecksum "github.com/stacklok/toolhive/cmd/thv-operator/pkg/runconfig/configmap/checksum"
	"github.com/stacklok/toolhive/pkg/operator/accessors"
	"github.com/stacklok/toolhive/pkg/runner"
	transporttypes "github.com/stacklok/toolhive/pkg/transport/types"
)

// defaultProxyHost is the default host for proxy binding
const defaultProxyHost = "0.0.0.0"

// defaultAPITimeout is the default timeout for Kubernetes API calls made during reconciliation
const defaultAPITimeout = 15 * time.Second

// ensureRunConfigConfigMap ensures the RunConfig ConfigMap exists and is up to date
func (r *MCPServerReconciler) ensureRunConfigConfigMap(ctx context.Context, m *mcpv1alpha1.MCPServer) error {
	runConfig, err := r.createRunConfigFromMCPServer(m)
	if err != nil {
		return fmt.Errorf("failed to create RunConfig from MCPServer: %w", err)
	}

	// Validate the RunConfig before creating the ConfigMap
	if err := r.validateRunConfig(ctx, runConfig); err != nil {
		return fmt.Errorf("invalid RunConfig: %w", err)
	}

	runConfigJSON, err := json.MarshalIndent(runConfig, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal run config: %w", err)
	}

	configMapName := fmt.Sprintf("%s-runconfig", m.Name)
	configMap := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      configMapName,
			Namespace: m.Namespace,
			Labels:    labelsForRunConfig(m.Name),
		},
		Data: map[string]string{
			"runconfig.json": string(runConfigJSON),
		},
	}

	checksum := configMapChecksum.NewRunConfigConfigMapChecksum()
	// Compute and add content checksum annotation
	cs := checksum.ComputeConfigMapChecksum(configMap)
	configMap.Annotations = map[string]string{
		"toolhive.stacklok.dev/content-checksum": cs,
	}

	runConfigConfigMap := configmap.NewRunConfigConfigMap(r.Client, r.Scheme, checksum)
	err = runConfigConfigMap.UpsertRunConfigMap(ctx, m, configMap)
	if err != nil {
		return fmt.Errorf("failed to upsert RunConfig ConfigMap: %w", err)
	}
	return nil
}

// createRunConfigFromMCPServer converts MCPServer spec to RunConfig using the builder pattern
// This creates a RunConfig for serialization to ConfigMap, not for direct execution
//
//nolint:gocyclo
func (r *MCPServerReconciler) createRunConfigFromMCPServer(m *mcpv1alpha1.MCPServer) (*runner.RunConfig, error) {
	proxyHost := defaultProxyHost
	if envHost := os.Getenv("TOOLHIVE_PROXY_HOST"); envHost != "" {
		proxyHost = envHost
	}

	// Helper functions to convert MCPServer spec to builder format
	envVars := convertEnvVarsFromMCPServer(m.Spec.Env)
	volumes := convertVolumesFromMCPServer(m.Spec.Volumes)
	// For ConfigMap mode, secrets are NOT included in runconfig - they're handled via k8s pod patch
	// This avoids secrets provider errors in Kubernetes environment

	// Get tool configuration from MCPToolConfig if referenced
	toolsFilter := m.Spec.ToolsFilter
	var toolsOverride map[string]runner.ToolOverride

	if m.Spec.ToolConfigRef != nil {
		// ToolConfigRef takes precedence over inline ToolsFilter
		toolConfig, err := ctrlutil.GetToolConfigForMCPServer(context.Background(), r.Client, m)
		if err != nil {
			return nil, fmt.Errorf("failed to get MCPToolConfig: %w", err)
		}

		if toolConfig != nil {
			// Use configuration from MCPToolConfig
			toolsFilter = toolConfig.Spec.ToolsFilter

			// Convert ToolOverride from CRD format to runner format
			if len(toolConfig.Spec.ToolsOverride) > 0 {
				toolsOverride = make(map[string]runner.ToolOverride)
				for toolName, override := range toolConfig.Spec.ToolsOverride {
					toolsOverride[toolName] = runner.ToolOverride{
						Name:        override.Name,
						Description: override.Description,
					}
				}
			}
		}
	}

	// For ConfigMap mode, we don't put the K8s pod template patch in the runconfig.
	// Instead, the operator will pass it via the --k8s-pod-patch CLI flag.
	// This avoids redundancy and follows the same pattern as regular flags.
	var k8sPodPatch string

	proxyMode := m.Spec.ProxyMode
	if proxyMode == "" {
		proxyMode = "sse" // Default to SSE for backward compatibility
	}

	options := []runner.RunConfigBuilderOption{
		runner.WithName(m.Name),
		runner.WithImage(m.Spec.Image),
		runner.WithCmdArgs(m.Spec.Args),
		runner.WithTransportAndPorts(m.Spec.Transport, int(m.GetProxyPort()), int(m.GetMcpPort())),
		runner.WithProxyMode(transporttypes.ProxyMode(proxyMode)),
		runner.WithHost(proxyHost),
		runner.WithTrustProxyHeaders(m.Spec.TrustProxyHeaders),
		runner.WithToolsFilter(toolsFilter),
		runner.WithEnvVars(envVars),
		runner.WithVolumes(volumes),
		// Secrets are NOT included in runconfig for ConfigMap mode - handled via k8s pod patch
		runner.WithK8sPodPatch(k8sPodPatch),
	}

	// Add tools override if present
	if toolsOverride != nil {
		options = append(options, runner.WithToolsOverride(toolsOverride))
	}

	// Add permission profile if specified
	if m.Spec.PermissionProfile != nil {
		switch m.Spec.PermissionProfile.Type {
		case mcpv1alpha1.PermissionProfileTypeBuiltin:
			options = append(options,
				runner.WithPermissionProfileNameOrPath(
					m.Spec.PermissionProfile.Name,
				),
			)
		case mcpv1alpha1.PermissionProfileTypeConfigMap:
			// For ConfigMap-based permission profiles, we store the path
			options = append(options,
				runner.WithPermissionProfileNameOrPath(
					fmt.Sprintf("/etc/toolhive/profiles/%s", m.Spec.PermissionProfile.Key),
				),
			)
		}
	}

	// Add telemetry configuration if specified
	runconfig.AddTelemetryConfigOptions(&options, m.Spec.Telemetry, m.Name)

	// Add authorization configuration if specified
	ctx, cancel := context.WithTimeout(context.Background(), defaultAPITimeout)
	defer cancel()

	if err := ctrlutil.AddAuthzConfigOptions(ctx, r.Client, m.Namespace, m.Spec.AuthzConfig, &options); err != nil {
		return nil, fmt.Errorf("failed to process AuthzConfig: %w", err)
	}

	if err := ctrlutil.AddOIDCConfigOptions(ctx, r.Client, m, &options); err != nil {
		return nil, fmt.Errorf("failed to process OIDCConfig: %w", err)
	}

	// Add external auth configuration if specified
	if err := ctrlutil.AddExternalAuthConfigOptions(ctx, r.Client, m.Namespace, m.Spec.ExternalAuthConfigRef, &options); err != nil {
		return nil, fmt.Errorf("failed to process ExternalAuthConfig: %w", err)
	}

	// Add audit configuration if specified
	runconfig.AddAuditConfigOptions(&options, m.Spec.Audit)

	// Check for Vault Agent Injection and add env-file-dir if needed
	vaultDetected := false

	// Check for Vault injection in pod template annotations
	if m.Spec.PodTemplateSpec != nil && m.Spec.PodTemplateSpec.Raw != nil {
		// Try to unmarshal the raw extension to check annotations
		var podTemplateSpec corev1.PodTemplateSpec
		if err := json.Unmarshal(m.Spec.PodTemplateSpec.Raw, &podTemplateSpec); err == nil {
			if podTemplateSpec.Annotations != nil {
				vaultDetected = hasVaultAgentInjection(podTemplateSpec.Annotations)
			}
		}
	}

	// Also check resource overrides annotations using the accessor for safe access
	if !vaultDetected {
		accessor := accessors.NewMCPServerFieldAccessor()
		_, annotations := accessor.GetProxyDeploymentTemplateLabelsAndAnnotations(m)
		if len(annotations) > 0 {
			vaultDetected = hasVaultAgentInjection(annotations)
		}
	}

	if vaultDetected {
		options = append(options, runner.WithEnvFileDir("/vault/secrets"))
	}

	// Use the RunConfigBuilder for operator context with full builder pattern
	return runner.NewOperatorRunConfigBuilder(
		context.Background(),
		nil,
		envVars,
		nil,
		options...,
	)
}

// labelsForRunConfig returns labels for run config ConfigMap
func labelsForRunConfig(mcpServerName string) map[string]string {
	return map[string]string{
		"toolhive.stacklok.io/component":  "run-config",
		"toolhive.stacklok.io/mcp-server": mcpServerName,
		"toolhive.stacklok.io/managed-by": "toolhive-operator",
	}
}

// validateRunConfig validates a RunConfig for operator-managed deployments
func (r *MCPServerReconciler) validateRunConfig(ctx context.Context, config *runner.RunConfig) error {
	if config == nil {
		return fmt.Errorf("RunConfig cannot be nil")
	}

	if err := r.validateRequiredFields(config); err != nil {
		return err
	}

	if err := r.validateTransportAndPorts(config); err != nil {
		return err
	}

	if err := r.validateHost(config); err != nil {
		return err
	}

	if err := r.validateEnvironmentVariables(config); err != nil {
		return err
	}

	if err := r.validateVolumeMounts(config); err != nil {
		return err
	}

	if err := r.validateSecrets(config); err != nil {
		return err
	}

	if err := r.validateToolsFilter(config); err != nil {
		return err
	}

	ctxLogger := log.FromContext(ctx)
	ctxLogger.V(1).Info("RunConfig validation passed", "name", config.Name)
	return nil
}

// validateRequiredFields validates required fields in the RunConfig
func (*MCPServerReconciler) validateRequiredFields(config *runner.RunConfig) error {
	if config.Image == "" {
		return fmt.Errorf("image is required")
	}

	if config.Name == "" {
		return fmt.Errorf("name is required")
	}

	if config.Transport == "" {
		return fmt.Errorf("transport is required")
	}

	return nil
}

// validateTransportAndPorts validates transport type and associated port configuration
func (*MCPServerReconciler) validateTransportAndPorts(config *runner.RunConfig) error {
	validTransports := []transporttypes.TransportType{
		transporttypes.TransportTypeStdio,
		transporttypes.TransportTypeSSE,
		transporttypes.TransportTypeStreamableHTTP,
	}

	validTransport := false
	for _, valid := range validTransports {
		if config.Transport == valid {
			validTransport = true
			break
		}
	}
	if !validTransport {
		return fmt.Errorf("invalid transport type: %s, must be one of: stdio, sse, streamable-http", config.Transport)
	}

	// Validate ports for HTTP-based transports
	if config.Transport == transporttypes.TransportTypeSSE || config.Transport == transporttypes.TransportTypeStreamableHTTP {
		if config.Port <= 0 {
			return fmt.Errorf("port is required for transport type %s", config.Transport)
		}
		if config.TargetPort <= 0 {
			return fmt.Errorf("target port is required for transport type %s", config.Transport)
		}
		if config.Port < 1 || config.Port > 65535 {
			return fmt.Errorf("port must be between 1 and 65535, got: %d", config.Port)
		}
		if config.TargetPort < 1 || config.TargetPort > 65535 {
			return fmt.Errorf("target port must be between 1 and 65535, got: %d", config.TargetPort)
		}
	}

	return nil
}

// validateHost validates the host configuration
func (*MCPServerReconciler) validateHost(config *runner.RunConfig) error {
	if config.Host == "" {
		return nil
	}

	// Basic validation - could be enhanced with more sophisticated checks
	if config.Host != defaultProxyHost && config.Host != "127.0.0.1" && config.Host != "localhost" {
		// For custom hosts, basic format check
		if len(config.Host) == 0 || strings.Contains(config.Host, " ") {
			return fmt.Errorf("invalid host format: %s", config.Host)
		}
	}

	return nil
}

// validateEnvironmentVariables validates environment variable format
func (*MCPServerReconciler) validateEnvironmentVariables(config *runner.RunConfig) error {
	for key, value := range config.EnvVars {
		if key == "" {
			return fmt.Errorf("environment variable key cannot be empty")
		}
		// Check for invalid characters in key (basic validation)
		if strings.ContainsAny(key, "=\n\r") {
			return fmt.Errorf("invalid environment variable key: %s", key)
		}
		// Check for control characters in value
		if strings.ContainsAny(value, "\n\r") {
			return fmt.Errorf("environment variable value for %s contains invalid characters", key)
		}
	}

	return nil
}

// validateVolumeMounts validates volume mount format
func (*MCPServerReconciler) validateVolumeMounts(config *runner.RunConfig) error {
	for _, volume := range config.Volumes {
		if volume == "" {
			return fmt.Errorf("volume mount cannot be empty")
		}
		parts := strings.Split(volume, ":")
		if len(parts) < 2 || len(parts) > 3 {
			return fmt.Errorf("invalid volume mount format: %s, expected host-path:container-path[:ro]", volume)
		}
		if parts[0] == "" || parts[1] == "" {
			return fmt.Errorf("volume mount paths cannot be empty in: %s", volume)
		}
		if len(parts) == 3 && parts[2] != "ro" {
			return fmt.Errorf("invalid volume mount option: %s, only 'ro' is supported", parts[2])
		}
	}

	return nil
}

// validateSecrets validates secret format
func (*MCPServerReconciler) validateSecrets(config *runner.RunConfig) error {
	for _, secret := range config.Secrets {
		if secret == "" {
			return fmt.Errorf("secret cannot be empty")
		}
		// Basic format validation: should contain secret name and target
		if !strings.Contains(secret, ",target=") {
			return fmt.Errorf("invalid secret format: %s, expected secret-name,target=env-var-name", secret)
		}
		parts := strings.Split(secret, ",target=")
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
			return fmt.Errorf("invalid secret format: %s, expected secret-name,target=env-var-name", secret)
		}
	}

	return nil
}

// validateToolsFilter validates tools filter format
func (*MCPServerReconciler) validateToolsFilter(config *runner.RunConfig) error {
	for _, tool := range config.ToolsFilter {
		if tool == "" {
			return fmt.Errorf("tool filter cannot contain empty values")
		}
		if strings.ContainsAny(tool, ",\n\r") {
			return fmt.Errorf("invalid tool name: %s, cannot contain commas or newlines", tool)
		}
	}

	return nil
}

// convertEnvVarsFromMCPServer converts MCPServer environment variables to builder format
func convertEnvVarsFromMCPServer(envs []mcpv1alpha1.EnvVar) map[string]string {
	if len(envs) == 0 {
		return nil
	}
	envVars := make(map[string]string, len(envs))
	for _, env := range envs {
		envVars[env.Name] = env.Value
	}
	return envVars
}

// convertVolumesFromMCPServer converts MCPServer volumes to builder format
func convertVolumesFromMCPServer(vols []mcpv1alpha1.Volume) []string {
	if len(vols) == 0 {
		return nil
	}
	volumes := make([]string, 0, len(vols))
	for _, vol := range vols {
		volStr := fmt.Sprintf("%s:%s", vol.HostPath, vol.MountPath)
		if vol.ReadOnly {
			volStr += ":ro"
		}
		volumes = append(volumes, volStr)
	}
	return volumes
}

// hasVaultAgentInjection checks if Vault Agent Injection is enabled in the pod annotations
func hasVaultAgentInjection(annotations map[string]string) bool {
	if annotations == nil {
		return false
	}

	// Check if vault.hashicorp.com/agent-inject annotation is present and set to "true"
	value, exists := annotations["vault.hashicorp.com/agent-inject"]
	return exists && value == "true"
}

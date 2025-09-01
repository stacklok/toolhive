package controllers

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	"github.com/stacklok/toolhive/pkg/logger"
	"github.com/stacklok/toolhive/pkg/runner"
	transporttypes "github.com/stacklok/toolhive/pkg/transport/types"
)

// RunConfig management methods

// ensureRunConfigConfigMap ensures the RunConfig ConfigMap exists and is up to date
func (r *MCPServerReconciler) ensureRunConfigConfigMap(ctx context.Context, m *mcpv1alpha1.MCPServer) error {
	runConfig := r.createRunConfigFromMCPServer(m)

	// Validate the RunConfig before creating the ConfigMap
	if err := r.validateRunConfig(runConfig); err != nil {
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

	return r.ensureRBACResource(ctx, m, "runconfig-configmap", func() client.Object {
		return configMap
	})
}

// createRunConfigFromMCPServer converts MCPServer spec to RunConfig
func (*MCPServerReconciler) createRunConfigFromMCPServer(m *mcpv1alpha1.MCPServer) *runner.RunConfig {
	proxyHost := "0.0.0.0"
	if envHost := os.Getenv("TOOLHIVE_PROXY_HOST"); envHost != "" {
		proxyHost = envHost
	}

	transport := transporttypes.TransportTypeStdio
	if m.Spec.Transport != "" {
		transport = transporttypes.TransportType(m.Spec.Transport)
	}

	port := 8080
	if m.Spec.Port != 0 {
		port = int(m.Spec.Port)
	}

	config := &runner.RunConfig{
		SchemaVersion:   runner.CurrentSchemaVersion,
		Name:            m.Name,
		Image:           m.Spec.Image,
		CmdArgs:         m.Spec.Args,
		Transport:       transport,
		Host:            proxyHost,
		Port:            port,
		TargetPort:      int(m.Spec.TargetPort),
		ToolsFilter:     m.Spec.ToolsFilter,
		EnvVars:         make(map[string]string, len(m.Spec.Env)),
		ContainerLabels: make(map[string]string),
		Volumes:         make([]string, 0, len(m.Spec.Volumes)),
		Secrets:         make([]string, 0, len(m.Spec.Secrets)),
	}

	// Convert environment variables, volumes, and secrets inline
	for _, env := range m.Spec.Env {
		config.EnvVars[env.Name] = env.Value
	}

	for _, vol := range m.Spec.Volumes {
		volStr := fmt.Sprintf("%s:%s", vol.HostPath, vol.MountPath)
		if vol.ReadOnly {
			volStr += ":ro"
		}
		config.Volumes = append(config.Volumes, volStr)
	}

	for _, secret := range m.Spec.Secrets {
		target := secret.TargetEnvName
		if target == "" {
			target = secret.Key
		}
		config.Secrets = append(config.Secrets, fmt.Sprintf("%s,target=%s", secret.Name, target))
	}

	// Add K8s pod template patch if needed
	if podSpec := NewMCPServerPodTemplateSpecBuilder(m.Spec.PodTemplateSpec).
		WithServiceAccount(m.Spec.ServiceAccount).
		WithSecrets(m.Spec.Secrets).
		Build(); podSpec != nil {
		if patch, err := json.Marshal(podSpec); err == nil {
			config.K8sPodTemplatePatch = string(patch)
		} else {
			logger.Errorf("Failed to marshal pod template spec: %v", err)
		}
	}

	return config
}

// buildArgsFromRunConfig translates a RunConfig into CLI arguments for thv-proxyrunner
func (r *MCPServerReconciler) buildArgsFromRunConfig(
	ctx context.Context,
	m *mcpv1alpha1.MCPServer,
	config *runner.RunConfig,
) []string {
	// Check if RunConfig ConfigMap exists
	configMapName := fmt.Sprintf("%s-runconfig", m.Name)
	if r.configMapExists(ctx, m.Namespace, configMapName) {
		// Use --from-configmap flag instead of individual flags
		return []string{"run", config.Image, "--from-configmap", fmt.Sprintf("%s/%s", m.Namespace, configMapName)}
	}

	// Fallback to individual flags if ConfigMap doesn't exist
	args := []string{"run", config.Image}
	args = addBasicArgs(args, config)
	args = addEnvironmentArgs(args, config)
	args = addTelemetryArgs(args, config)
	args = addContainerArgs(args, config)
	return args
}

// addBasicArgs adds basic configuration arguments
func addBasicArgs(args []string, config *runner.RunConfig) []string {
	if config.Transport != "" {
		args = append(args, "--transport", string(config.Transport))
	}
	if config.Host != "" && config.Host != "127.0.0.1" {
		args = append(args, "--host", config.Host)
	}
	if config.Port != 0 {
		args = append(args, "--proxy-port", fmt.Sprintf("%d", config.Port))
	}
	if config.TargetPort != 0 {
		args = append(args, "--target-port", fmt.Sprintf("%d", config.TargetPort))
	}
	return args
}

// addEnvironmentArgs adds environment variables, volumes, secrets, and tools
func addEnvironmentArgs(args []string, config *runner.RunConfig) []string {
	for key, value := range config.EnvVars {
		args = append(args, "--env", fmt.Sprintf("%s=%s", key, value))
	}
	for _, volume := range config.Volumes {
		args = append(args, "--volume", volume)
	}
	for _, secret := range config.Secrets {
		args = append(args, "--secret", secret)
	}
	if len(config.ToolsFilter) > 0 {
		sortedTools := make([]string, len(config.ToolsFilter))
		copy(sortedTools, config.ToolsFilter)
		slices.Sort(sortedTools)
		args = append(args, fmt.Sprintf("--tools=%s", strings.Join(sortedTools, ",")))
	}
	return args
}

// addTelemetryArgs adds OpenTelemetry configuration arguments
func addTelemetryArgs(args []string, config *runner.RunConfig) []string {
	if config.TelemetryConfig == nil {
		return args
	}
	args = append(args, "--otel-enabled")
	if config.TelemetryConfig.Endpoint != "" {
		args = append(args, "--otel-endpoint", config.TelemetryConfig.Endpoint)
	}
	if config.TelemetryConfig.ServiceName != "" {
		args = append(args, "--otel-service-name", config.TelemetryConfig.ServiceName)
	}
	for _, header := range config.TelemetryConfig.Headers {
		args = append(args, "--otel-headers", header)
	}
	if config.TelemetryConfig.Insecure {
		args = append(args, "--otel-insecure")
	}
	return args
}

// addContainerArgs adds container command arguments and Kubernetes pod template patch
func addContainerArgs(args []string, config *runner.RunConfig) []string {
	// Add K8s pod template patch if provided
	if config.K8sPodTemplatePatch != "" {
		args = append(args, fmt.Sprintf("--k8s-pod-patch=%s", config.K8sPodTemplatePatch))
	}

	if len(config.CmdArgs) > 0 {
		args = append(args, "--")
		args = append(args, config.CmdArgs...)
	}
	return args
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
func (r *MCPServerReconciler) validateRunConfig(config *runner.RunConfig) error {
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

	logger.Debugf("RunConfig validation passed for %s", config.Name)
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
	if config.Host != "0.0.0.0" && config.Host != "127.0.0.1" && config.Host != "localhost" {
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

// configMapExists checks if a ConfigMap exists in the specified namespace
func (r *MCPServerReconciler) configMapExists(ctx context.Context, namespace, name string) bool {
	// Return false if client is not available (e.g., in tests)
	if r.Client == nil {
		return false
	}
	err := r.Get(ctx, client.ObjectKey{
		Namespace: namespace,
		Name:      name,
	}, &corev1.ConfigMap{})
	return err == nil
}

// configMapNewerThanDeployment checks if a ConfigMap has been modified after the deployment was last updated
func (r *MCPServerReconciler) configMapNewerThanDeployment(
	ctx context.Context,
	deployment *appsv1.Deployment,
	namespace,
	configMapName string,
) bool {
	// Return false if client is not available (e.g., in tests)
	if r.Client == nil {
		return false
	}

	// Get the ConfigMap
	var configMap corev1.ConfigMap
	err := r.Get(ctx, client.ObjectKey{
		Namespace: namespace,
		Name:      configMapName,
	}, &configMap)
	if err != nil {
		// ConfigMap doesn't exist or can't be read, deployment needs update to create it
		return true
	}

	// Get the last update time of the deployment
	// Use the deployment's generation and observed generation to determine if it was recently updated
	deploymentLastUpdated := deployment.CreationTimestamp.Time
	if deployment.Status.ObservedGeneration == deployment.Generation {
		// Deployment is up to date, use the last transition time of the "Progressing" condition
		for _, condition := range deployment.Status.Conditions {
			if condition.Type == appsv1.DeploymentProgressing {
				deploymentLastUpdated = condition.LastUpdateTime.Time
				break
			}
		}
	}

	// Compare ConfigMap's last modification time with deployment's last update time
	configMapLastModified := configMap.CreationTimestamp.Time
	if configMap.GetResourceVersion() != "" {
		// Use the ConfigMap's creation time as a proxy for modification time
		// In a real scenario, you might want to use annotations to track the last modification
		configMapLastModified = configMap.CreationTimestamp.Time
	}

	// ConfigMap is newer if it was modified after the deployment was last updated
	return configMapLastModified.After(deploymentLastUpdated)
}

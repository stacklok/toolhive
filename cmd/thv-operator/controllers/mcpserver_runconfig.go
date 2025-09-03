package controllers

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"slices"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

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
			Annotations: map[string]string{
				"toolhive.stacklok.io/last-modified": time.Now().UTC().Format(time.RFC3339),
			},
		},
		Data: map[string]string{
			"runconfig.json": string(runConfigJSON),
		},
	}

	return r.ensureRunConfigConfigMapResource(ctx, m, configMap)
}

// ensureRunConfigConfigMapResource ensures the RunConfig ConfigMap exists and is up to date
// This method handles the special case of updating the last-modified annotation when content changes
func (r *MCPServerReconciler) ensureRunConfigConfigMapResource(
	ctx context.Context,
	mcpServer *mcpv1alpha1.MCPServer,
	desired *corev1.ConfigMap,
) error {
	current := &corev1.ConfigMap{}
	objectKey := types.NamespacedName{Name: desired.Name, Namespace: desired.Namespace}
	err := r.Get(ctx, objectKey, current)

	if errors.IsNotFound(err) {
		// ConfigMap doesn't exist, create it
		if err := controllerutil.SetControllerReference(mcpServer, desired, r.Scheme); err != nil {
			logger.Errorf("Failed to set controller reference for RunConfig ConfigMap: %v", err)
			return nil
		}

		ctxLogger.Info("RunConfig ConfigMap does not exist, creating", "ConfigMap.Name", desired.Name)
		if err := r.Create(ctx, desired); err != nil {
			return fmt.Errorf("failed to create RunConfig ConfigMap: %w", err)
		}
		ctxLogger.Info("RunConfig ConfigMap created", "ConfigMap.Name", desired.Name)
		return nil
	} else if err != nil {
		return fmt.Errorf("failed to get RunConfig ConfigMap: %w", err)
	}

	// ConfigMap exists, check if content has changed
	if !r.runConfigContentEquals(current, desired) {
		// Content changed, update the timestamp annotation and the ConfigMap
		if desired.Annotations == nil {
			desired.Annotations = make(map[string]string)
		}
		desired.Annotations["toolhive.stacklok.io/last-modified"] = time.Now().UTC().Format(time.RFC3339)

		// Copy resource version and other metadata for update
		desired.ResourceVersion = current.ResourceVersion
		desired.UID = current.UID

		if err := controllerutil.SetControllerReference(mcpServer, desired, r.Scheme); err != nil {
			logger.Errorf("Failed to set controller reference for RunConfig ConfigMap: %v", err)
			return nil
		}

		ctxLogger.Info("RunConfig ConfigMap content changed, updating", "ConfigMap.Name", desired.Name)
		if err := r.Update(ctx, desired); err != nil {
			return fmt.Errorf("failed to update RunConfig ConfigMap: %w", err)
		}
		ctxLogger.Info("RunConfig ConfigMap updated", "ConfigMap.Name", desired.Name)
	}

	return nil
}

// runConfigContentEquals compares the actual content of RunConfig ConfigMaps, ignoring metadata
func (*MCPServerReconciler) runConfigContentEquals(current, desired *corev1.ConfigMap) bool {
	// Compare the data content
	if !reflect.DeepEqual(current.Data, desired.Data) {
		return false
	}

	// Compare labels (excluding the last-modified annotation)
	if !reflect.DeepEqual(current.Labels, desired.Labels) {
		return false
	}

	// Compare other annotations (excluding last-modified)
	currentAnnotations := make(map[string]string)
	desiredAnnotations := make(map[string]string)

	for k, v := range current.Annotations {
		if k != "toolhive.stacklok.io/last-modified" {
			currentAnnotations[k] = v
		}
	}

	for k, v := range desired.Annotations {
		if k != "toolhive.stacklok.io/last-modified" {
			desiredAnnotations[k] = v
		}
	}

	return reflect.DeepEqual(currentAnnotations, desiredAnnotations)
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
	// Try to get the RunConfig ConfigMap once
	configMapName := fmt.Sprintf("%s-runconfig", m.Name)
	configMap := &corev1.ConfigMap{}
	// Return false if client is not available (e.g., in tests)
	if r.Client != nil {
		err := r.Get(ctx, client.ObjectKey{Namespace: m.Namespace, Name: configMapName}, configMap)
		if err == nil {
			// Use --from-configmap flag instead of individual flags
			return []string{"run", config.Image, "--from-configmap", fmt.Sprintf("%s/%s", m.Namespace, configMapName)}
		}
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

// configMapNewerThanDeployment checks if a ConfigMap has been modified after the deployment was last updated
// If configMap is provided, it will be used instead of fetching it again
func (r *MCPServerReconciler) configMapNewerThanDeployment(
	ctx context.Context,
	deployment *appsv1.Deployment,
	namespace,
	configMapName string,
	configMap *corev1.ConfigMap,
) bool {
	// Return false if client is not available (e.g., in tests)
	if r.Client == nil {
		return false
	}

	// Get the ConfigMap if not provided
	var cm corev1.ConfigMap
	if configMap != nil {
		cm = *configMap
	} else {
		err := r.Get(ctx, client.ObjectKey{
			Namespace: namespace,
			Name:      configMapName,
		}, &cm)
		if err != nil {
			// ConfigMap doesn't exist or can't be read, deployment needs update to create it
			return true
		}
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
	// Use annotation-based tracking for precise modification time
	configMapLastModified := cm.CreationTimestamp.Time
	if lastModifiedStr, exists := cm.Annotations["toolhive.stacklok.io/last-modified"]; exists {
		if parsedTime, err := time.Parse(time.RFC3339, lastModifiedStr); err == nil {
			configMapLastModified = parsedTime
		}
	}

	// ConfigMap is newer if it was modified after the deployment was last updated
	return configMapLastModified.After(deploymentLastUpdated)
}

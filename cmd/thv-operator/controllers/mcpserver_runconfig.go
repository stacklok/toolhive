package controllers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	"github.com/stacklok/toolhive/pkg/logger"
	"github.com/stacklok/toolhive/pkg/runner"
	transporttypes "github.com/stacklok/toolhive/pkg/transport/types"
)

// defaultProxyHost is the default host for proxy binding
const defaultProxyHost = "0.0.0.0"

// RunConfig management methods

// computeConfigMapChecksum computes a SHA256 checksum of the ConfigMap content for change detection
func computeConfigMapChecksum(cm *corev1.ConfigMap) string {
	h := sha256.New()

	// Include data content in checksum
	var dataKeys []string
	for key := range cm.Data {
		dataKeys = append(dataKeys, key)
	}
	sort.Strings(dataKeys)

	for _, key := range dataKeys {
		h.Write([]byte(key))
		h.Write([]byte(cm.Data[key]))
	}

	// Include labels in checksum (excluding checksum annotation itself)
	var labelKeys []string
	for key := range cm.Labels {
		labelKeys = append(labelKeys, key)
	}
	sort.Strings(labelKeys)

	for _, key := range labelKeys {
		h.Write([]byte(key))
		h.Write([]byte(cm.Labels[key]))
	}

	// Include relevant annotations in checksum (excluding checksum annotation itself)
	var annotationKeys []string
	for key := range cm.Annotations {
		if key != "toolhive.stacklok.dev/content-checksum" {
			annotationKeys = append(annotationKeys, key)
		}
	}
	sort.Strings(annotationKeys)

	for _, key := range annotationKeys {
		h.Write([]byte(key))
		h.Write([]byte(cm.Annotations[key]))
	}

	return hex.EncodeToString(h.Sum(nil))
}

// ensureRunConfigConfigMap ensures the RunConfig ConfigMap exists and is up to date
func (r *MCPServerReconciler) ensureRunConfigConfigMap(ctx context.Context, m *mcpv1alpha1.MCPServer) error {
	runConfig, err := r.createRunConfigFromMCPServer(m)
	if err != nil {
		return fmt.Errorf("failed to create RunConfig from MCPServer: %w", err)
	}

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

	// Compute and add content checksum annotation
	checksum := computeConfigMapChecksum(configMap)
	configMap.Annotations = map[string]string{
		"toolhive.stacklok.dev/content-checksum": checksum,
	}

	return r.ensureRunConfigConfigMapResource(ctx, m, configMap)
}

// ensureRunConfigConfigMapResource ensures the RunConfig ConfigMap exists and is up to date
// This method handles content changes by comparing checksums
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
			return fmt.Errorf("failed to set controller reference for RunConfig ConfigMap: %w", err)
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

	// ConfigMap exists, check if content has changed by comparing checksums
	currentChecksum := current.Annotations["toolhive.stacklok.dev/content-checksum"]
	desiredChecksum := desired.Annotations["toolhive.stacklok.dev/content-checksum"]

	if currentChecksum != desiredChecksum {
		// Content changed, update the ConfigMap with new checksum
		// Copy resource version and other metadata for update
		desired.ResourceVersion = current.ResourceVersion
		desired.UID = current.UID

		if err := controllerutil.SetControllerReference(mcpServer, desired, r.Scheme); err != nil {
			return fmt.Errorf("failed to set controller reference for RunConfig ConfigMap: %w", err)
		}

		ctxLogger.Info("RunConfig ConfigMap content changed, updating",
			"ConfigMap.Name", desired.Name,
			"oldChecksum", currentChecksum,
			"newChecksum", desiredChecksum)
		if err := r.Update(ctx, desired); err != nil {
			return fmt.Errorf("failed to update RunConfig ConfigMap: %w", err)
		}
		ctxLogger.Info("RunConfig ConfigMap updated", "ConfigMap.Name", desired.Name)
	}

	return nil
}

// runConfigContentEquals compares the actual content of RunConfig ConfigMaps using checksums
func (*MCPServerReconciler) runConfigContentEquals(current, desired *corev1.ConfigMap) bool {
	// Compare checksums - if both have checksums, use them for comparison
	currentChecksum := current.Annotations["toolhive.stacklok.dev/content-checksum"]
	desiredChecksum := desired.Annotations["toolhive.stacklok.dev/content-checksum"]

	if currentChecksum != "" && desiredChecksum != "" {
		return currentChecksum == desiredChecksum
	}

	// Fallback to compute checksums if they don't exist (for backward compatibility)
	if currentChecksum == "" {
		currentChecksum = computeConfigMapChecksum(current)
	}
	if desiredChecksum == "" {
		desiredChecksum = computeConfigMapChecksum(desired)
	}

	return currentChecksum == desiredChecksum
}

// createRunConfigFromMCPServer converts MCPServer spec to RunConfig using the builder pattern
// This creates a RunConfig for serialization to ConfigMap, not for direct execution
func (*MCPServerReconciler) createRunConfigFromMCPServer(m *mcpv1alpha1.MCPServer) (*runner.RunConfig, error) {
	proxyHost := defaultProxyHost
	if envHost := os.Getenv("TOOLHIVE_PROXY_HOST"); envHost != "" {
		proxyHost = envHost
	}

	port := 8080
	if m.Spec.Port != 0 {
		port = int(m.Spec.Port)
	}

	// Helper functions to convert MCPServer spec to builder format
	envVars := convertEnvVarsFromMCPServer(m.Spec.Env)
	volumes := convertVolumesFromMCPServer(m.Spec.Volumes)
	secrets := convertSecretsFromMCPServer(m.Spec.Secrets)

	// Create K8s pod template patch if needed
	var k8sPodPatch string
	if podSpec := NewMCPServerPodTemplateSpecBuilder(m.Spec.PodTemplateSpec).
		WithServiceAccount(m.Spec.ServiceAccount).
		WithSecrets(m.Spec.Secrets).
		Build(); podSpec != nil {
		if patch, err := json.Marshal(podSpec); err == nil {
			k8sPodPatch = string(patch)
		} else {
			logger.Errorf("Failed to marshal pod template spec: %v", err)
		}
	}

	proxyMode := m.Spec.ProxyMode
	if proxyMode == "" {
		proxyMode = "sse" // Default to SSE for backward compatibility
	}

	options := []runner.RunConfigBuilderOption{
		runner.WithName(m.Name),
		runner.WithImage(m.Spec.Image),
		runner.WithCmdArgs(m.Spec.Args),
		runner.WithTransportAndPorts(m.Spec.Transport, port, int(m.Spec.TargetPort)),
		runner.WithProxyMode(transporttypes.ProxyMode(proxyMode)),
		runner.WithHost(proxyHost),
		runner.WithToolsFilter(m.Spec.ToolsFilter),
		runner.WithEnvVars(envVars),
		runner.WithVolumes(volumes),
		runner.WithSecrets(secrets),
		runner.WithK8sPodPatch(k8sPodPatch),
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

// convertSecretsFromMCPServer converts MCPServer secrets to builder format
func convertSecretsFromMCPServer(secs []mcpv1alpha1.SecretRef) []string {
	if len(secs) == 0 {
		return nil
	}
	secrets := make([]string, 0, len(secs))
	for _, secret := range secs {
		target := secret.TargetEnvName
		if target == "" {
			target = secret.Key
		}
		secrets = append(secrets, fmt.Sprintf("%s,target=%s", secret.Name, target))
	}
	return secrets
}

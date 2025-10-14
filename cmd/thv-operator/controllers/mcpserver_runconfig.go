package controllers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	"github.com/stacklok/toolhive/pkg/authz"
	"github.com/stacklok/toolhive/pkg/operator/accessors"
	"github.com/stacklok/toolhive/pkg/runner"
	transporttypes "github.com/stacklok/toolhive/pkg/transport/types"
)

// defaultProxyHost is the default host for proxy binding
const defaultProxyHost = "0.0.0.0"

// defaultAPITimeout is the default timeout for Kubernetes API calls made during reconciliation
const defaultAPITimeout = 15 * time.Second

// defaultAuthzKey is the default key in the ConfigMap for authorization configuration
const defaultAuthzKey = "authz.json"

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
	ctxLogger := log.FromContext(ctx)
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
//
//nolint:gocyclo
func (r *MCPServerReconciler) createRunConfigFromMCPServer(m *mcpv1alpha1.MCPServer) (*runner.RunConfig, error) {
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
	// For ConfigMap mode, secrets are NOT included in runconfig - they're handled via k8s pod patch
	// This avoids secrets provider errors in Kubernetes environment

	// Get tool configuration from MCPToolConfig if referenced
	toolsFilter := m.Spec.ToolsFilter
	var toolsOverride map[string]runner.ToolOverride

	if m.Spec.ToolConfigRef != nil {
		// ToolConfigRef takes precedence over inline ToolsFilter
		toolConfig, err := GetToolConfigForMCPServer(context.Background(), r.Client, m)
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
		runner.WithTransportAndPorts(m.Spec.Transport, port, int(m.Spec.TargetPort)),
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
	addTelemetryConfigOptions(&options, m.Spec.Telemetry, m.Name)

	// Add authorization configuration if specified
	ctx, cancel := context.WithTimeout(context.Background(), defaultAPITimeout)
	defer cancel()

	if err := r.addAuthzConfigOptions(ctx, m, &options, m.Spec.AuthzConfig); err != nil {
		return nil, fmt.Errorf("failed to process AuthzConfig: %w", err)
	}

	// Add OIDC authentication configuration if specified
	r.addOIDCConfigOptions(ctx, &options, m.Spec.OIDCConfig, m)

	// Add audit configuration if specified
	addAuditConfigOptions(&options, m.Spec.Audit)

	// Check for Vault Agent Injection and add env-file-dir if needed
	vaultDetected := false

	// Check for Vault injection in pod template annotations
	if m.Spec.PodTemplateSpec != nil &&
		m.Spec.PodTemplateSpec.Annotations != nil {
		vaultDetected = hasVaultAgentInjection(m.Spec.PodTemplateSpec.Annotations)
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

// addTelemetryConfigOptions adds telemetry configuration options to the builder options
func addTelemetryConfigOptions(
	options *[]runner.RunConfigBuilderOption,
	telemetryConfig *mcpv1alpha1.TelemetryConfig,
	mcpServerName string,
) {
	if telemetryConfig == nil {
		return
	}

	// Default values
	var otelEndpoint string
	var otelEnablePrometheusMetricsPath bool
	var otelTracingEnabled bool
	var otelMetricsEnabled bool
	var otelServiceName string
	var otelSamplingRate = 0.05 // Default sampling rate
	var otelHeaders []string
	var otelInsecure bool
	var otelEnvironmentVariables []string

	// Process OpenTelemetry configuration
	if telemetryConfig.OpenTelemetry != nil && telemetryConfig.OpenTelemetry.Enabled {
		otel := telemetryConfig.OpenTelemetry

		// Strip http:// or https:// prefix if present, as OTLP client expects host:port format
		otelEndpoint = strings.TrimPrefix(strings.TrimPrefix(otel.Endpoint, "https://"), "http://")
		otelInsecure = otel.Insecure
		otelHeaders = otel.Headers

		// Use MCPServer name as service name if not specified
		if otel.ServiceName != "" {
			otelServiceName = otel.ServiceName
		} else {
			otelServiceName = mcpServerName
		}

		// Handle tracing configuration
		if otel.Tracing != nil {
			otelTracingEnabled = otel.Tracing.Enabled
			if otel.Tracing.SamplingRate != "" {
				// Parse sampling rate string to float64
				if rate, err := strconv.ParseFloat(otel.Tracing.SamplingRate, 64); err == nil {
					otelSamplingRate = rate
				}
			}
		}

		// Handle metrics configuration
		if otel.Metrics != nil {
			otelMetricsEnabled = otel.Metrics.Enabled
		}
	}

	// Process Prometheus configuration
	if telemetryConfig.Prometheus != nil {
		otelEnablePrometheusMetricsPath = telemetryConfig.Prometheus.Enabled
	}

	// Add telemetry config to options
	*options = append(*options, runner.WithTelemetryConfig(
		otelEndpoint,
		otelEnablePrometheusMetricsPath,
		otelTracingEnabled,
		otelMetricsEnabled,
		otelServiceName,
		otelSamplingRate,
		otelHeaders,
		otelInsecure,
		otelEnvironmentVariables,
	))
}

// addAuthzConfigOptions adds authorization configuration options to the builder options
// Supports both inline and ConfigMap-based configurations.
func (r *MCPServerReconciler) addAuthzConfigOptions(
	ctx context.Context,
	m *mcpv1alpha1.MCPServer,
	options *[]runner.RunConfigBuilderOption,
	authzRef *mcpv1alpha1.AuthzConfigRef,
) error {
	if authzRef == nil {
		return nil
	}

	switch authzRef.Type {
	case mcpv1alpha1.AuthzConfigTypeInline:
		if authzRef.Inline == nil {
			return fmt.Errorf("inline authz config type specified but inline config is nil")
		}

		policies := authzRef.Inline.Policies
		entitiesJSON := authzRef.Inline.EntitiesJSON

		// Create authorization config
		authzCfg := &authz.Config{
			Version: "v1",
			Type:    authz.ConfigTypeCedarV1,
			Cedar: &authz.CedarConfig{
				Policies:     policies,
				EntitiesJSON: entitiesJSON,
			},
		}

		// Add authorization config to options
		*options = append(*options, runner.WithAuthzConfig(authzCfg))
		return nil

	case mcpv1alpha1.AuthzConfigTypeConfigMap:
		// Validate reference
		if authzRef.ConfigMap == nil || authzRef.ConfigMap.Name == "" {
			return fmt.Errorf("configMap authz config type specified but reference is missing name")
		}
		key := authzRef.ConfigMap.Key
		if key == "" {
			key = defaultAuthzKey
		}

		// Ensure we have a Kubernetes client to fetch the ConfigMap
		if r.Client == nil {
			return fmt.Errorf("kubernetes client is not configured for ConfigMap authz resolution")
		}

		// Fetch the ConfigMap from the same namespace as the MCPServer
		var cm corev1.ConfigMap
		if err := r.Get(ctx, types.NamespacedName{
			Namespace: m.Namespace,
			Name:      authzRef.ConfigMap.Name,
		}, &cm); err != nil {
			return fmt.Errorf("failed to get Authz ConfigMap %s/%s: %w", m.Namespace, authzRef.ConfigMap.Name, err)
		}

		raw, ok := cm.Data[key]
		if !ok {
			return fmt.Errorf("authz ConfigMap %s/%s is missing key %q", m.Namespace, authzRef.ConfigMap.Name, key)
		}
		if strings.TrimSpace(raw) == "" {
			return fmt.Errorf("authz ConfigMap %s/%s key %q is empty", m.Namespace, authzRef.ConfigMap.Name, key)
		}

		// Unmarshal into authz.Config supporting YAML or JSON
		var cfg authz.Config
		// Try YAML first (it also handles JSON)
		if err := yaml.Unmarshal([]byte(raw), &cfg); err != nil {
			// Fallback to JSON explicitly for clearer error paths
			if err2 := json.Unmarshal([]byte(raw), &cfg); err2 != nil {
				return fmt.Errorf("failed to parse authz config from ConfigMap %s/%s key %q: %v; json fallback error: %v",
					m.Namespace, authzRef.ConfigMap.Name, key, err, err2)
			}
		}

		// Validate the config
		if err := cfg.Validate(); err != nil {
			return fmt.Errorf("invalid authz config from ConfigMap %s/%s key %q: %w",
				m.Namespace, authzRef.ConfigMap.Name, key, err)
		}

		*options = append(*options, runner.WithAuthzConfig(&cfg))
		return nil

	default:
		// Unknown type
		return fmt.Errorf("unknown authz config type: %s", authzRef.Type)
	}
}

func (r *MCPServerReconciler) addOIDCConfigOptions(
	ctx context.Context,
	options *[]runner.RunConfigBuilderOption,
	oidcConfig *mcpv1alpha1.OIDCConfigRef,
	m *mcpv1alpha1.MCPServer,
) {
	if oidcConfig == nil {
		return
	}

	// Add OAuth discovery resource URL for RFC 9728 compliance
	resourceURL := oidcConfig.ResourceURL
	if resourceURL == "" {
		resourceURL = createServiceURL(m.Name, m.Namespace, m.Spec.Port)
	}

	switch oidcConfig.Type {
	case mcpv1alpha1.OIDCConfigTypeKubernetes:
		config := oidcConfig.Kubernetes

		// Set defaults if config is nil
		if config == nil {
			ctxLogger := log.FromContext(ctx)
			ctxLogger.Info("Kubernetes OIDCConfig is nil, using default configuration", "mcpServer", m.Name)
			defaultUseClusterAuth := true
			config = &mcpv1alpha1.KubernetesOIDCConfig{
				UseClusterAuth: &defaultUseClusterAuth, // Default to true
			}
		}

		// Handle UseClusterAuth with default of true if nil
		useClusterAuth := true // default value
		if config.UseClusterAuth != nil {
			useClusterAuth = *config.UseClusterAuth
		}

		if oidcConfig.Kubernetes != nil {
			config := oidcConfig.Kubernetes
			issuer := config.Issuer
			if issuer == "" {
				issuer = "https://kubernetes.default.svc"
			}

			audience := config.Audience
			if audience == "" {
				audience = "toolhive"
			}

			thvCABundlePath := ""
			jwksAuthTokenPath := ""
			jwksAllowPrivateIP := false

			if useClusterAuth {
				thvCABundlePath = "/var/run/secrets/kubernetes.io/serviceaccount/ca.crt"
				jwksAuthTokenPath = "/var/run/secrets/kubernetes.io/serviceaccount/token"
				jwksAllowPrivateIP = true
			}

			// Add OIDC config to options
			*options = append(*options, runner.WithOIDCConfig(
				issuer,
				audience,
				config.JWKSURL,
				config.IntrospectionURL,
				"",
				"",
				thvCABundlePath,
				jwksAuthTokenPath,
				resourceURL,
				jwksAllowPrivateIP,
			))
		}
	case mcpv1alpha1.OIDCConfigTypeConfigMap:

		if oidcConfig.ConfigMap == nil {
			return
		}

		config := oidcConfig.ConfigMap

		// Read the ConfigMap and extract OIDC configuration from documented keys
		configMap := &corev1.ConfigMap{}
		err := r.Get(ctx, types.NamespacedName{
			Name:      config.Name,
			Namespace: m.Namespace,
		}, configMap)
		if err != nil {
			ctxLogger := log.FromContext(ctx)
			ctxLogger.Error(err, "Failed to get ConfigMap", "configMapName", config.Name)
			return
		}

		// Extract OIDC configuration from well-known keys
		issuer := ""
		if i, exists := configMap.Data["issuer"]; exists && i != "" {
			issuer = i
		}
		audience := ""
		if a, exists := configMap.Data["audience"]; exists && a != "" {
			audience = a
		}
		jwksURL := ""
		if j, exists := configMap.Data["jwksUrl"]; exists && j != "" {
			jwksURL = j
		}
		introspectionURL := ""
		if i, exists := configMap.Data["introspectionUrl"]; exists && i != "" {
			introspectionURL = i
		}
		clientID := ""
		if c, exists := configMap.Data["clientId"]; exists && c != "" {
			clientID = c
		}
		clientSecret := ""
		if c, exists := configMap.Data["clientSecret"]; exists && c != "" {
			clientSecret = c
		}
		thvCABundlePath := ""
		if thvCABundlePath, exists := configMap.Data["thvCABundlePath"]; exists && thvCABundlePath != "" {
			thvCABundlePath = thvCABundlePath
		}
		jwksAuthTokenPath := ""
		if j, exists := configMap.Data["jwksAuthTokenPath"]; exists && j != "" {
			jwksAuthTokenPath = j
		}
		jwksAllowPrivateIP := false
		if jwksAllowPrivateIP, exists := configMap.Data["jwksAllowPrivateIP"]; exists && jwksAllowPrivateIP == "true" {
			jwksAllowPrivateIP = "true"
		}

		*options = append(*options, runner.WithOIDCConfig(
			issuer,
			audience,
			jwksURL,
			introspectionURL,
			clientID,
			clientSecret,
			thvCABundlePath,
			jwksAuthTokenPath,
			resourceURL,
			jwksAllowPrivateIP,
		))
	case mcpv1alpha1.OIDCConfigTypeInline:
		if oidcConfig.Inline != nil {
			inline := oidcConfig.Inline

			// Add OIDC config to options
			*options = append(*options, runner.WithOIDCConfig(
				inline.Issuer,
				inline.Audience,
				inline.JWKSURL,
				inline.IntrospectionURL,
				inline.ClientID,
				inline.ClientSecret,
				inline.ThvCABundlePath,
				inline.JWKSAuthTokenPath,
				resourceURL,
				inline.JWKSAllowPrivateIP,
			))
		}
	}
}

// addAuditConfigOptions adds audit configuration options to the builder options
func addAuditConfigOptions(
	options *[]runner.RunConfigBuilderOption,
	auditConfig *mcpv1alpha1.AuditConfig,
) {
	if auditConfig == nil {
		return
	}

	// Add audit config to options with default config (no custom config path for now)
	*options = append(*options, runner.WithAuditEnabled(auditConfig.Enabled, ""))
}

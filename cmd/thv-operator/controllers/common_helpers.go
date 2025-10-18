package controllers

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"sync"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/yaml"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	"github.com/stacklok/toolhive/cmd/thv-operator/pkg/oidc"
	"github.com/stacklok/toolhive/pkg/authz"
	"github.com/stacklok/toolhive/pkg/container/kubernetes"
	"github.com/stacklok/toolhive/pkg/runner"
	transporttypes "github.com/stacklok/toolhive/pkg/transport/types"
)

const (
	defaultAuthzKey = "authz.json"
)

// PlatformDetectorInterface provides platform detection capabilities
type PlatformDetectorInterface interface {
	DetectPlatform(ctx context.Context) (kubernetes.Platform, error)
}

// SharedPlatformDetector provides shared platform detection across controllers
type SharedPlatformDetector struct {
	detector         kubernetes.PlatformDetector
	detectedPlatform kubernetes.Platform
	once             sync.Once
	config           *rest.Config // Optional config for testing
}

// NewSharedPlatformDetector creates a new shared platform detector
func NewSharedPlatformDetector() *SharedPlatformDetector {
	return &SharedPlatformDetector{
		detector: kubernetes.NewDefaultPlatformDetector(),
	}
}

// NewSharedPlatformDetectorWithDetector creates a new shared platform detector with a custom detector (for testing)
func NewSharedPlatformDetectorWithDetector(detector kubernetes.PlatformDetector) *SharedPlatformDetector {
	return &SharedPlatformDetector{
		detector: detector,
		config:   &rest.Config{}, // Provide a dummy config for testing
	}
}

// DetectPlatform detects the platform once and caches the result
func (s *SharedPlatformDetector) DetectPlatform(ctx context.Context) (kubernetes.Platform, error) {
	var err error
	s.once.Do(func() {
		var cfg *rest.Config
		if s.config != nil {
			cfg = s.config
		} else {
			var configErr error
			cfg, configErr = rest.InClusterConfig()
			if configErr != nil {
				err = fmt.Errorf("failed to get in-cluster config for platform detection: %w", configErr)
				return
			}
		}

		s.detectedPlatform, err = s.detector.DetectPlatform(cfg)
		if err != nil {
			err = fmt.Errorf("failed to detect platform: %w", err)
			return
		}

		ctxLogger := log.FromContext(ctx)
		ctxLogger.Info("Platform detected", "platform", s.detectedPlatform.String())
	})

	return s.detectedPlatform, err
}

// EnsureRBACResource is a generic helper function to ensure a Kubernetes RBAC resource exists
func EnsureRBACResource(
	ctx context.Context,
	c client.Client,
	scheme *runtime.Scheme,
	owner client.Object,
	resourceType string,
	createResource func() client.Object,
) error {
	current := createResource()
	objectKey := types.NamespacedName{Name: current.GetName(), Namespace: current.GetNamespace()}
	err := c.Get(ctx, objectKey, current)

	if errors.IsNotFound(err) {
		return createRBACResource(ctx, c, scheme, owner, resourceType, createResource)
	} else if err != nil {
		return fmt.Errorf("failed to get %s: %w", resourceType, err)
	}

	return nil
}

// createRBACResource creates a new RBAC resource with owner reference
func createRBACResource(
	ctx context.Context,
	c client.Client,
	scheme *runtime.Scheme,
	owner client.Object,
	resourceType string,
	createResource func() client.Object,
) error {
	ctxLogger := log.FromContext(ctx)
	desired := createResource()
	if err := controllerutil.SetControllerReference(owner, desired, scheme); err != nil {
		ctxLogger.Error(err, "Failed to set controller reference", "resourceType", resourceType)
		return fmt.Errorf("failed to set controller reference for %s: %w", resourceType, err)
	}

	ctxLogger.Info(
		fmt.Sprintf("%s does not exist, creating", resourceType),
		"resourceType", resourceType,
		"name", desired.GetName(),
	)
	if err := c.Create(ctx, desired); err != nil {
		return fmt.Errorf("failed to create %s: %w", resourceType, err)
	}
	ctxLogger.Info(fmt.Sprintf("%s created", resourceType), "resourceType", resourceType, "name", desired.GetName())
	return nil
}

// GenerateOpenTelemetryEnvVars generates OpenTelemetry environment variables
func GenerateOpenTelemetryEnvVars(
	telemetryConfig *mcpv1alpha1.TelemetryConfig,
	resourceName string,
	namespace string,
) []corev1.EnvVar {
	var envVars []corev1.EnvVar

	if telemetryConfig == nil || telemetryConfig.OpenTelemetry == nil {
		return envVars
	}

	otel := telemetryConfig.OpenTelemetry

	serviceName := otel.ServiceName
	if serviceName == "" {
		serviceName = resourceName
	}

	envVars = append(envVars, corev1.EnvVar{
		Name:  "OTEL_RESOURCE_ATTRIBUTES",
		Value: fmt.Sprintf("service.name=%s,service.namespace=%s", serviceName, namespace),
	})

	return envVars
}

// GenerateTokenExchangeEnvVars generates environment variables for token exchange
func GenerateTokenExchangeEnvVars(
	ctx context.Context,
	c client.Client,
	namespace string,
	externalAuthConfigRef *mcpv1alpha1.ExternalAuthConfigRef,
	getExternalAuthConfig func(context.Context, client.Client, string, string) (*mcpv1alpha1.MCPExternalAuthConfig, error),
) ([]corev1.EnvVar, error) {
	var envVars []corev1.EnvVar

	if externalAuthConfigRef == nil {
		return envVars, nil
	}

	externalAuthConfig, err := getExternalAuthConfig(ctx, c, namespace, externalAuthConfigRef.Name)
	if err != nil {
		return nil, fmt.Errorf("failed to get MCPExternalAuthConfig: %w", err)
	}

	if externalAuthConfig == nil {
		return nil, fmt.Errorf("MCPExternalAuthConfig %s not found", externalAuthConfigRef.Name)
	}

	if externalAuthConfig.Spec.Type != mcpv1alpha1.ExternalAuthTypeTokenExchange {
		return envVars, nil
	}

	tokenExchangeSpec := externalAuthConfig.Spec.TokenExchange
	if tokenExchangeSpec == nil {
		return envVars, nil
	}

	envVars = append(envVars, corev1.EnvVar{
		Name: "TOOLHIVE_TOKEN_EXCHANGE_CLIENT_SECRET",
		ValueFrom: &corev1.EnvVarSource{
			SecretKeyRef: &corev1.SecretKeySelector{
				LocalObjectReference: corev1.LocalObjectReference{
					Name: tokenExchangeSpec.ClientSecretRef.Name,
				},
				Key: tokenExchangeSpec.ClientSecretRef.Key,
			},
		},
	})

	return envVars, nil
}

// GenerateAuthzVolumeConfig generates volume mount and volume for authorization policies
func GenerateAuthzVolumeConfig(
	authzConfig *mcpv1alpha1.AuthzConfigRef,
	resourceName string,
) (*corev1.VolumeMount, *corev1.Volume) {
	if authzConfig == nil {
		return nil, nil
	}

	switch authzConfig.Type {
	case mcpv1alpha1.AuthzConfigTypeConfigMap:
		if authzConfig.ConfigMap == nil {
			return nil, nil
		}

		volumeMount := &corev1.VolumeMount{
			Name:      "authz-config",
			MountPath: "/etc/toolhive/authz",
			ReadOnly:  true,
		}

		volume := &corev1.Volume{
			Name: "authz-config",
			VolumeSource: corev1.VolumeSource{
				ConfigMap: &corev1.ConfigMapVolumeSource{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: authzConfig.ConfigMap.Name,
					},
					Items: []corev1.KeyToPath{
						{
							Key: func() string {
								if authzConfig.ConfigMap.Key != "" {
									return authzConfig.ConfigMap.Key
								}
								return defaultAuthzKey
							}(),
							Path: defaultAuthzKey,
						},
					},
				},
			},
		}

		return volumeMount, volume

	case mcpv1alpha1.AuthzConfigTypeInline:
		if authzConfig.Inline == nil {
			return nil, nil
		}

		volumeMount := &corev1.VolumeMount{
			Name:      "authz-config",
			MountPath: "/etc/toolhive/authz",
			ReadOnly:  true,
		}

		volume := &corev1.Volume{
			Name: "authz-config",
			VolumeSource: corev1.VolumeSource{
				ConfigMap: &corev1.ConfigMapVolumeSource{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: fmt.Sprintf("%s-authz-inline", resourceName),
					},
					Items: []corev1.KeyToPath{
						{
							Key:  defaultAuthzKey,
							Path: defaultAuthzKey,
						},
					},
				},
			},
		}

		return volumeMount, volume

	default:
		return nil, nil
	}
}

// EnsureAuthzConfigMap ensures the authorization ConfigMap exists for inline configuration
func EnsureAuthzConfigMap(
	ctx context.Context,
	c client.Client,
	scheme *runtime.Scheme,
	owner client.Object,
	namespace string,
	resourceName string,
	authzConfig *mcpv1alpha1.AuthzConfigRef,
	labels map[string]string,
) error {
	ctxLogger := log.FromContext(ctx)
	if authzConfig == nil || authzConfig.Type != mcpv1alpha1.AuthzConfigTypeInline ||
		authzConfig.Inline == nil {
		return nil
	}

	configMapName := fmt.Sprintf("%s-authz-inline", resourceName)

	authzConfigData := map[string]interface{}{
		"version": "1.0",
		"type":    "cedarv1",
		"cedar": map[string]interface{}{
			"policies": authzConfig.Inline.Policies,
			"entities_json": func() string {
				if authzConfig.Inline.EntitiesJSON != "" {
					return authzConfig.Inline.EntitiesJSON
				}
				return "[]"
			}(),
		},
	}

	authzConfigJSON, err := json.Marshal(authzConfigData)
	if err != nil {
		return fmt.Errorf("failed to marshal inline authz config: %w", err)
	}

	configMap := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      configMapName,
			Namespace: namespace,
			Labels:    labels,
		},
		Data: map[string]string{
			defaultAuthzKey: string(authzConfigJSON),
		},
	}

	if err := controllerutil.SetControllerReference(owner, configMap, scheme); err != nil {
		return fmt.Errorf("failed to set controller reference for authorization ConfigMap: %w", err)
	}

	existingConfigMap := &corev1.ConfigMap{}
	err = c.Get(ctx, types.NamespacedName{Name: configMapName, Namespace: namespace}, existingConfigMap)
	if err != nil && errors.IsNotFound(err) {
		ctxLogger.Info("Creating authorization ConfigMap", "ConfigMap.Namespace", configMap.Namespace, "ConfigMap.Name", configMap.Name)
		if err := c.Create(ctx, configMap); err != nil {
			return fmt.Errorf("failed to create authorization ConfigMap: %w", err)
		}
	} else if err != nil {
		return fmt.Errorf("failed to get authorization ConfigMap: %w", err)
	} else {
		// ConfigMap exists, check if it needs to be updated
		if !reflect.DeepEqual(existingConfigMap.Data, configMap.Data) {
			ctxLogger.Info("Updating authorization ConfigMap",
				"ConfigMap.Namespace", configMap.Namespace,
				"ConfigMap.Name", configMap.Name)
			existingConfigMap.Data = configMap.Data
			if err := c.Update(ctx, existingConfigMap); err != nil {
				return fmt.Errorf("failed to update authorization ConfigMap: %w", err)
			}
		}
	}

	return nil
}

// GetToolConfigForMCPRemoteProxy fetches MCPToolConfig referenced by MCPRemoteProxy
func GetToolConfigForMCPRemoteProxy(
	ctx context.Context,
	c client.Client,
	proxy *mcpv1alpha1.MCPRemoteProxy,
) (*mcpv1alpha1.MCPToolConfig, error) {
	if proxy.Spec.ToolConfigRef == nil {
		return nil, fmt.Errorf("MCPRemoteProxy %s does not reference a MCPToolConfig", proxy.Name)
	}

	toolConfig := &mcpv1alpha1.MCPToolConfig{}
	err := c.Get(ctx, types.NamespacedName{
		Name:      proxy.Spec.ToolConfigRef.Name,
		Namespace: proxy.Namespace,
	}, toolConfig)

	if err != nil {
		return nil, fmt.Errorf("failed to get MCPToolConfig %s: %w", proxy.Spec.ToolConfigRef.Name, err)
	}

	return toolConfig, nil
}

// GetExternalAuthConfigForMCPRemoteProxy fetches MCPExternalAuthConfig referenced by MCPRemoteProxy
func GetExternalAuthConfigForMCPRemoteProxy(
	ctx context.Context,
	c client.Client,
	proxy *mcpv1alpha1.MCPRemoteProxy,
) (*mcpv1alpha1.MCPExternalAuthConfig, error) {
	if proxy.Spec.ExternalAuthConfigRef == nil {
		return nil, fmt.Errorf("MCPRemoteProxy %s does not reference a MCPExternalAuthConfig", proxy.Name)
	}

	externalAuthConfig := &mcpv1alpha1.MCPExternalAuthConfig{}
	err := c.Get(ctx, types.NamespacedName{
		Name:      proxy.Spec.ExternalAuthConfigRef.Name,
		Namespace: proxy.Namespace,
	}, externalAuthConfig)

	if err != nil {
		return nil, fmt.Errorf("failed to get MCPExternalAuthConfig %s: %w", proxy.Spec.ExternalAuthConfigRef.Name, err)
	}

	return externalAuthConfig, nil
}

// GetExternalAuthConfigByName is a generic helper for fetching MCPExternalAuthConfig by name
func GetExternalAuthConfigByName(
	ctx context.Context,
	c client.Client,
	namespace string,
	name string,
) (*mcpv1alpha1.MCPExternalAuthConfig, error) {
	externalAuthConfig := &mcpv1alpha1.MCPExternalAuthConfig{}
	err := c.Get(ctx, types.NamespacedName{
		Name:      name,
		Namespace: namespace,
	}, externalAuthConfig)

	if err != nil {
		return nil, fmt.Errorf("failed to get MCPExternalAuthConfig %s: %w", name, err)
	}

	return externalAuthConfig, nil
}

// AddAuthzConfigOptions adds authorization configuration options to builder options
// This is a shared helper that works for both MCPServer and MCPRemoteProxy
func AddAuthzConfigOptions(
	ctx context.Context,
	c client.Client,
	namespace string,
	authzRef *mcpv1alpha1.AuthzConfigRef,
	options *[]runner.RunConfigBuilderOption,
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
		if c == nil {
			return fmt.Errorf("kubernetes client is not configured for ConfigMap authz resolution")
		}

		// Fetch the ConfigMap
		var cm corev1.ConfigMap
		if err := c.Get(ctx, types.NamespacedName{
			Namespace: namespace,
			Name:      authzRef.ConfigMap.Name,
		}, &cm); err != nil {
			return fmt.Errorf("failed to get Authz ConfigMap %s/%s: %w", namespace, authzRef.ConfigMap.Name, err)
		}

		raw, ok := cm.Data[key]
		if !ok {
			return fmt.Errorf("authz ConfigMap %s/%s is missing key %q", namespace, authzRef.ConfigMap.Name, key)
		}
		if len(strings.TrimSpace(raw)) == 0 {
			return fmt.Errorf("authz ConfigMap %s/%s key %q is empty", namespace, authzRef.ConfigMap.Name, key)
		}

		// Unmarshal into authz.Config supporting YAML or JSON
		var cfg authz.Config
		// Try YAML first (it also handles JSON)
		if err := yaml.Unmarshal([]byte(raw), &cfg); err != nil {
			// Fallback to JSON explicitly for clearer error paths
			if err2 := json.Unmarshal([]byte(raw), &cfg); err2 != nil {
				return fmt.Errorf("failed to parse authz config from ConfigMap %s/%s key %q: %v; json fallback error: %v",
					namespace, authzRef.ConfigMap.Name, key, err, err2)
			}
		}

		// Validate the config
		if err := cfg.Validate(); err != nil {
			return fmt.Errorf("invalid authz config from ConfigMap %s/%s key %q: %w",
				namespace, authzRef.ConfigMap.Name, key, err)
		}

		*options = append(*options, runner.WithAuthzConfig(&cfg))
		return nil

	default:
		// Unknown type
		return fmt.Errorf("unknown authz config type: %s", authzRef.Type)
	}
}

// AddExternalAuthConfigOptions adds external authentication configuration options to builder options
// This creates middleware configuration for token exchange and is shared between MCPServer and MCPRemoteProxy
func AddExternalAuthConfigOptions(
	ctx context.Context,
	c client.Client,
	namespace string,
	externalAuthConfigRef *mcpv1alpha1.ExternalAuthConfigRef,
	options *[]runner.RunConfigBuilderOption,
) error {
	if externalAuthConfigRef == nil {
		return nil
	}

	// Fetch the MCPExternalAuthConfig
	externalAuthConfig, err := GetExternalAuthConfigByName(ctx, c, namespace, externalAuthConfigRef.Name)
	if err != nil {
		return fmt.Errorf("failed to get MCPExternalAuthConfig: %w", err)
	}

	// Only token exchange type is supported currently
	if externalAuthConfig.Spec.Type != mcpv1alpha1.ExternalAuthTypeTokenExchange {
		return fmt.Errorf("unsupported external auth type: %s", externalAuthConfig.Spec.Type)
	}

	tokenExchangeSpec := externalAuthConfig.Spec.TokenExchange
	if tokenExchangeSpec == nil {
		return fmt.Errorf("token exchange configuration is nil for type tokenExchange")
	}

	// Validate that the referenced Kubernetes secret exists
	var secret corev1.Secret
	if err := c.Get(ctx, types.NamespacedName{
		Namespace: namespace,
		Name:      tokenExchangeSpec.ClientSecretRef.Name,
	}, &secret); err != nil {
		return fmt.Errorf("failed to get client secret %s/%s: %w",
			namespace, tokenExchangeSpec.ClientSecretRef.Name, err)
	}

	if _, ok := secret.Data[tokenExchangeSpec.ClientSecretRef.Key]; !ok {
		return fmt.Errorf("client secret %s/%s is missing key %q",
			namespace, tokenExchangeSpec.ClientSecretRef.Name, tokenExchangeSpec.ClientSecretRef.Key)
	}

	// Use scopes array directly from spec
	scopes := tokenExchangeSpec.Scopes

	// Determine header strategy based on ExternalTokenHeaderName
	headerStrategy := "replace" // Default strategy
	if tokenExchangeSpec.ExternalTokenHeaderName != "" {
		headerStrategy = "custom"
	}

	// Build token exchange middleware configuration
	// Client secret is provided via TOOLHIVE_TOKEN_EXCHANGE_CLIENT_SECRET environment variable
	// to avoid embedding plaintext secrets in the ConfigMap
	tokenExchangeConfig := map[string]interface{}{
		"token_url": tokenExchangeSpec.TokenURL,
		"client_id": tokenExchangeSpec.ClientID,
		"audience":  tokenExchangeSpec.Audience,
	}

	if len(scopes) > 0 {
		tokenExchangeConfig["scopes"] = scopes
	}

	if headerStrategy != "" {
		tokenExchangeConfig["header_strategy"] = headerStrategy
	}

	if tokenExchangeSpec.ExternalTokenHeaderName != "" {
		tokenExchangeConfig["external_token_header_name"] = tokenExchangeSpec.ExternalTokenHeaderName
	}

	// Create middleware parameters
	middlewareParams := map[string]interface{}{
		"token_exchange_config": tokenExchangeConfig,
	}

	// Marshal parameters to JSON
	paramsJSON, err := json.Marshal(middlewareParams)
	if err != nil {
		return fmt.Errorf("failed to marshal token exchange middleware parameters: %w", err)
	}

	// Create middleware config
	middlewareConfig := transporttypes.MiddlewareConfig{
		Type:       "tokenexchange",
		Parameters: json.RawMessage(paramsJSON),
	}

	// Add to options using the WithMiddlewareConfig builder option
	*options = append(*options, runner.WithMiddlewareConfig([]transporttypes.MiddlewareConfig{middlewareConfig}))

	return nil
}

// AddOIDCConfigOptions adds OIDC authentication configuration options to builder options
// This is shared between MCPServer and MCPRemoteProxy and uses the OIDC resolver
func AddOIDCConfigOptions(
	ctx context.Context,
	c client.Client,
	res oidc.OIDCConfigurable,
	options *[]runner.RunConfigBuilderOption,
) error {
	// Use the OIDC resolver to get configuration
	resolver := oidc.NewResolver(c)
	oidcConfig, err := resolver.Resolve(ctx, res)
	if err != nil {
		return fmt.Errorf("failed to resolve OIDC configuration: %w", err)
	}

	if oidcConfig == nil {
		return nil
	}

	// Add OIDC config to options
	*options = append(*options, runner.WithOIDCConfig(
		oidcConfig.Issuer,
		oidcConfig.Audience,
		oidcConfig.JWKSURL,
		oidcConfig.IntrospectionURL,
		oidcConfig.ClientID,
		oidcConfig.ClientSecret,
		oidcConfig.ThvCABundlePath,
		oidcConfig.JWKSAuthTokenPath,
		oidcConfig.ResourceURL,
		oidcConfig.JWKSAllowPrivateIP,
	))

	return nil
}

// BuildResourceRequirements builds Kubernetes resource requirements from spec
// Shared between MCPServer and MCPRemoteProxy
func BuildResourceRequirements(resourceSpec mcpv1alpha1.ResourceRequirements) corev1.ResourceRequirements {
	resources := corev1.ResourceRequirements{}

	if resourceSpec.Limits.CPU != "" || resourceSpec.Limits.Memory != "" {
		resources.Limits = corev1.ResourceList{}
		if resourceSpec.Limits.CPU != "" {
			resources.Limits[corev1.ResourceCPU] = resource.MustParse(resourceSpec.Limits.CPU)
		}
		if resourceSpec.Limits.Memory != "" {
			resources.Limits[corev1.ResourceMemory] = resource.MustParse(resourceSpec.Limits.Memory)
		}
	}

	if resourceSpec.Requests.CPU != "" || resourceSpec.Requests.Memory != "" {
		resources.Requests = corev1.ResourceList{}
		if resourceSpec.Requests.CPU != "" {
			resources.Requests[corev1.ResourceCPU] = resource.MustParse(resourceSpec.Requests.CPU)
		}
		if resourceSpec.Requests.Memory != "" {
			resources.Requests[corev1.ResourceMemory] = resource.MustParse(resourceSpec.Requests.Memory)
		}
	}

	return resources
}

// BuildHealthProbe builds a Kubernetes health probe configuration
// Shared between MCPServer and MCPRemoteProxy
func BuildHealthProbe(
	path, port string, initialDelay, period, timeout, failureThreshold int32,
) *corev1.Probe {
	return &corev1.Probe{
		ProbeHandler: corev1.ProbeHandler{
			HTTPGet: &corev1.HTTPGetAction{
				Path: path,
				Port: intstr.FromString(port),
			},
		},
		InitialDelaySeconds: initialDelay,
		PeriodSeconds:       period,
		TimeoutSeconds:      timeout,
		FailureThreshold:    failureThreshold,
	}
}

// EnsureRequiredEnvVars ensures required environment variables are set with defaults
// Shared between MCPServer and MCPRemoteProxy
func EnsureRequiredEnvVars(ctx context.Context, env []corev1.EnvVar) []corev1.EnvVar {
	ctxLogger := log.FromContext(ctx)
	xdgConfigHomeFound := false
	homeFound := false
	toolhiveRuntimeFound := false
	unstructuredLogsFound := false

	for _, envVar := range env {
		switch envVar.Name {
		case "XDG_CONFIG_HOME":
			xdgConfigHomeFound = true
		case "HOME":
			homeFound = true
		case "TOOLHIVE_RUNTIME":
			toolhiveRuntimeFound = true
		case "UNSTRUCTURED_LOGS":
			unstructuredLogsFound = true
		}
	}

	if !xdgConfigHomeFound {
		ctxLogger.V(1).Info("XDG_CONFIG_HOME not found, setting to /tmp")
		env = append(env, corev1.EnvVar{
			Name:  "XDG_CONFIG_HOME",
			Value: "/tmp",
		})
	}

	if !homeFound {
		ctxLogger.V(1).Info("HOME not found, setting to /tmp")
		env = append(env, corev1.EnvVar{
			Name:  "HOME",
			Value: "/tmp",
		})
	}

	if !toolhiveRuntimeFound {
		ctxLogger.V(1).Info("TOOLHIVE_RUNTIME not found, setting to kubernetes")
		env = append(env, corev1.EnvVar{
			Name:  "TOOLHIVE_RUNTIME",
			Value: "kubernetes",
		})
	}

	// Always use structured JSON logs in Kubernetes (not configurable)
	if !unstructuredLogsFound {
		ctxLogger.V(1).Info("UNSTRUCTURED_LOGS not found, setting to false for structured JSON logging")
		env = append(env, corev1.EnvVar{
			Name:  "UNSTRUCTURED_LOGS",
			Value: "false",
		})
	}

	return env
}

// MergeLabels merges override labels with default labels
// Default labels take precedence to ensure operator-required metadata is preserved
// Shared between MCPServer and MCPRemoteProxy
func MergeLabels(defaultLabels, overrideLabels map[string]string) map[string]string {
	return mergeStringMaps(defaultLabels, overrideLabels)
}

// MergeAnnotations merges override annotations with default annotations
// Default annotations take precedence to ensure operator-required metadata is preserved
// Shared between MCPServer and MCPRemoteProxy
func MergeAnnotations(defaultAnnotations, overrideAnnotations map[string]string) map[string]string {
	return mergeStringMaps(defaultAnnotations, overrideAnnotations)
}

// mergeStringMaps merges override map with default map, with default map taking precedence
func mergeStringMaps(defaultMap, overrideMap map[string]string) map[string]string {
	result := make(map[string]string)
	for k, v := range overrideMap {
		result[k] = v
	}
	for k, v := range defaultMap {
		result[k] = v // default takes precedence
	}
	return result
}

// CreateProxyServiceName generates the service name for a proxy (MCPServer or MCPRemoteProxy)
// Shared naming convention across both controllers
func CreateProxyServiceName(resourceName string) string {
	return fmt.Sprintf("mcp-%s-proxy", resourceName)
}

// CreateProxyServiceURL generates the full cluster-local service URL
// Shared between MCPServer and MCPRemoteProxy
func CreateProxyServiceURL(resourceName, namespace string, port int32) string {
	serviceName := CreateProxyServiceName(resourceName)
	return fmt.Sprintf("http://%s.%s.svc.cluster.local:%d", serviceName, namespace, port)
}

// ProxyRunnerServiceAccountName generates the service account name for the proxy runner
// Shared between MCPServer and MCPRemoteProxy
func ProxyRunnerServiceAccountName(resourceName string) string {
	return fmt.Sprintf("%s-proxy-runner", resourceName)
}

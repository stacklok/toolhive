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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/yaml"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	"github.com/stacklok/toolhive/pkg/authz"
	"github.com/stacklok/toolhive/pkg/container/kubernetes"
	"github.com/stacklok/toolhive/pkg/runner"
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

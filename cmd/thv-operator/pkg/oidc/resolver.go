package oidc

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/log"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	// K8s service account paths
	defaultK8sCABundlePath = "/var/run/secrets/kubernetes.io/serviceaccount/ca.crt"
	defaultK8sTokenPath    = "/var/run/secrets/kubernetes.io/serviceaccount/token" //nolint:gosec
	defaultK8sIssuer       = "https://kubernetes.default.svc"
	defaultK8sAudience     = "toolhive"

	// ConfigMap keys for OIDC configuration
	configKeyIssuer             = "issuer"
	configKeyAudience           = "audience"
	configKeyJWKSURL            = "jwksUrl"
	configKeyIntrospectionURL   = "introspectionUrl"
	configKeyClientID           = "clientId"
	configKeyClientSecret       = "clientSecret"
	configKeyThvCABundlePath    = "thvCABundlePath"
	configKeyJWKSAuthTokenPath  = "jwksAuthTokenPath"
	configKeyJWKSAllowPrivateIP = "jwksAllowPrivateIP"

	// trueValue is the string representation of true for ConfigMap values
	trueValue = "true"
)

// OIDCConfig represents the resolved OIDC configuration values
type OIDCConfig struct {
	Issuer             string
	Audience           string
	JWKSURL            string
	IntrospectionURL   string
	ClientID           string
	ClientSecret       string
	ThvCABundlePath    string
	JWKSAuthTokenPath  string
	ResourceURL        string
	JWKSAllowPrivateIP bool
}

// Resolver is the interface for resolving OIDC configuration from various sources
type Resolver interface {
	// Resolve takes an MCPServer and its OIDC configuration reference and returns the resolved OIDC config
	Resolve(ctx context.Context, mcpServer *mcpv1alpha1.MCPServer) (*OIDCConfig, error)
}

// NewResolver creates a new OIDC configuration resolver
// It accepts an optional Kubernetes client for ConfigMap resolution
func NewResolver(client client.Client) Resolver {
	return &resolver{
		client: client,
	}
}

// resolver is the concrete implementation of the Resolver interface
type resolver struct {
	client client.Client
}

// Resolve resolves the OIDC configuration based on the type specified in OIDCConfigRef
func (r *resolver) Resolve(ctx context.Context, mcpServer *mcpv1alpha1.MCPServer) (*OIDCConfig, error) {
	if mcpServer.Spec.OIDCConfig == nil {
		return nil, nil
	}

	oidcConfig := mcpServer.Spec.OIDCConfig

	// Calculate resource URL for RFC 9728 compliance
	resourceURL := oidcConfig.ResourceURL
	if resourceURL == "" {
		resourceURL = createServiceURL(mcpServer.Name, mcpServer.Namespace, mcpServer.Spec.Port)
	}

	switch oidcConfig.Type {
	case mcpv1alpha1.OIDCConfigTypeKubernetes:
		return r.resolveKubernetesConfig(ctx, oidcConfig.Kubernetes, resourceURL, mcpServer)
	case mcpv1alpha1.OIDCConfigTypeConfigMap:
		return r.resolveConfigMapConfig(ctx, oidcConfig.ConfigMap, resourceURL, mcpServer)
	case mcpv1alpha1.OIDCConfigTypeInline:
		return r.resolveInlineConfig(oidcConfig.Inline, resourceURL)
	default:
		return nil, fmt.Errorf("unknown OIDC config type: %s", oidcConfig.Type)
	}
}

// resolveKubernetesConfig resolves OIDC configuration for Kubernetes type
func (r *resolver) resolveKubernetesConfig(
	ctx context.Context,
	config *mcpv1alpha1.KubernetesOIDCConfig,
	resourceURL string,
	mcpServer *mcpv1alpha1.MCPServer,
) (*OIDCConfig, error) {
	// Set defaults if config is nil
	if config == nil {
		ctxLogger := log.FromContext(ctx)
		ctxLogger.Info("Kubernetes OIDCConfig is nil, using default configuration", "mcpServer", mcpServer.Name)
		defaultUseClusterAuth := true
		config = &mcpv1alpha1.KubernetesOIDCConfig{
			UseClusterAuth: &defaultUseClusterAuth,
		}
	}

	// Handle UseClusterAuth with default of true if nil
	useClusterAuth := true // default value
	if config.UseClusterAuth != nil {
		useClusterAuth = *config.UseClusterAuth
	}

	result := &OIDCConfig{
		ResourceURL: resourceURL,
	}

	// Set issuer with default
	result.Issuer = config.Issuer
	if result.Issuer == "" {
		result.Issuer = defaultK8sIssuer
	}

	// Set audience with default
	result.Audience = config.Audience
	if result.Audience == "" {
		result.Audience = defaultK8sAudience
	}

	// Set JWKS and introspection URLs
	result.JWKSURL = config.JWKSURL
	result.IntrospectionURL = config.IntrospectionURL

	// Apply cluster auth settings if enabled
	if useClusterAuth {
		result.ThvCABundlePath = defaultK8sCABundlePath
		result.JWKSAuthTokenPath = defaultK8sTokenPath
		result.JWKSAllowPrivateIP = true
	}

	return result, nil
}

// resolveConfigMapConfig resolves OIDC configuration from a ConfigMap
func (r *resolver) resolveConfigMapConfig(
	ctx context.Context,
	configRef *mcpv1alpha1.ConfigMapOIDCRef,
	resourceURL string,
	mcpServer *mcpv1alpha1.MCPServer,
) (*OIDCConfig, error) {
	if configRef == nil {
		return nil, nil
	}

	if r.client == nil {
		return nil, fmt.Errorf("kubernetes client is required for ConfigMap OIDC resolution")
	}

	// Read the ConfigMap
	configMap := &corev1.ConfigMap{}
	err := r.client.Get(ctx, types.NamespacedName{
		Name:      configRef.Name,
		Namespace: mcpServer.Namespace,
	}, configMap)
	if err != nil {
		return nil, fmt.Errorf("failed to get OIDC ConfigMap %s/%s: %w",
			mcpServer.Namespace, configRef.Name, err)
	}

	// Extract OIDC configuration from ConfigMap data
	return extractConfigFromMap(configMap.Data, resourceURL), nil
}

// resolveInlineConfig resolves inline OIDC configuration
func (r *resolver) resolveInlineConfig(
	config *mcpv1alpha1.InlineOIDCConfig,
	resourceURL string,
) (*OIDCConfig, error) {
	if config == nil {
		return nil, nil
	}

	return &OIDCConfig{
		Issuer:             config.Issuer,
		Audience:           config.Audience,
		JWKSURL:            config.JWKSURL,
		IntrospectionURL:   config.IntrospectionURL,
		ClientID:           config.ClientID,
		ClientSecret:       config.ClientSecret,
		ThvCABundlePath:    config.ThvCABundlePath,
		JWKSAuthTokenPath:  config.JWKSAuthTokenPath,
		ResourceURL:        resourceURL,
		JWKSAllowPrivateIP: config.JWKSAllowPrivateIP,
	}, nil
}

// extractConfigFromMap extracts OIDC configuration from a map of key-value pairs
func extractConfigFromMap(data map[string]string, resourceURL string) *OIDCConfig {
	config := &OIDCConfig{
		ResourceURL: resourceURL,
	}

	// Extract string values
	config.Issuer = getMapValue(data, configKeyIssuer)
	config.Audience = getMapValue(data, configKeyAudience)
	config.JWKSURL = getMapValue(data, configKeyJWKSURL)
	config.IntrospectionURL = getMapValue(data, configKeyIntrospectionURL)
	config.ClientID = getMapValue(data, configKeyClientID)
	config.ClientSecret = getMapValue(data, configKeyClientSecret)
	config.ThvCABundlePath = getMapValue(data, configKeyThvCABundlePath)
	config.JWKSAuthTokenPath = getMapValue(data, configKeyJWKSAuthTokenPath)

	// Handle boolean value
	if v, exists := data[configKeyJWKSAllowPrivateIP]; exists && v == trueValue {
		config.JWKSAllowPrivateIP = true
	}

	return config
}

// getMapValue is a helper to extract string values from a map
func getMapValue(data map[string]string, key string) string {
	if v, exists := data[key]; exists && v != "" {
		return v
	}
	return ""
}

// createServiceURL creates a service URL from MCPServer details
func createServiceURL(name, namespace string, port int32) string {
	if port == 0 {
		port = 8080
	}
	return fmt.Sprintf("http://%s.%s.svc.cluster.local:%d", name, namespace, port)
}

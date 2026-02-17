// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package oidc provides utilities for resolving OIDC configuration from various sources
// including Kubernetes service accounts, ConfigMaps, and inline configurations.
package oidc

import (
	"context"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	"github.com/stacklok/toolhive/cmd/thv-operator/pkg/validation"
)

const (
	// K8s service account paths
	defaultK8sCABundlePath = "/var/run/secrets/kubernetes.io/serviceaccount/ca.crt"
	defaultK8sTokenPath    = "/var/run/secrets/kubernetes.io/serviceaccount/token" //nolint:gosec
	defaultK8sIssuer       = "https://kubernetes.default.svc"
	defaultK8sAudience     = "toolhive"
)

// OIDCConfig represents the resolved OIDC configuration values
type OIDCConfig struct { //nolint:revive // Keeping OIDCConfig name for backward compatibility
	Issuer             string
	Audience           string
	JWKSURL            string
	IntrospectionURL   string
	ClientID           string
	ClientSecret       string // #nosec G117 -- not a hardcoded credential, populated at runtime from config
	ThvCABundlePath    string
	JWKSAuthTokenPath  string
	ResourceURL        string
	JWKSAllowPrivateIP bool
	InsecureAllowHTTP  bool
	Scopes             []string
}

// OIDCConfigurable is an interface for resources that have OIDC configuration
//
//nolint:revive // Intentionally named OIDCConfigurable for clarity
type OIDCConfigurable interface {
	GetName() string
	GetNamespace() string
	GetOIDCConfig() *mcpv1alpha1.OIDCConfigRef
	GetProxyPort() int32
}

//go:generate mockgen -destination=mocks/mock_resolver.go -package=mocks -source=resolver.go Resolver

// Resolver is the interface for resolving OIDC configuration from various sources
type Resolver interface {
	// Resolve takes any resource implementing OIDCConfigurable and resolves its OIDC config
	Resolve(ctx context.Context, resource OIDCConfigurable) (*OIDCConfig, error)
}

// NewResolver creates a new OIDC configuration resolver
// It accepts an optional Kubernetes client for ConfigMap resolution
func NewResolver(k8sClient client.Client) Resolver {
	return &resolver{
		client: k8sClient,
	}
}

// resolver is the concrete implementation of the Resolver interface
type resolver struct {
	client client.Client
}

// Resolve resolves the OIDC configuration from any resource implementing OIDCConfigurable
func (r *resolver) Resolve(ctx context.Context, resource OIDCConfigurable) (*OIDCConfig, error) {
	oidcConfig := resource.GetOIDCConfig()
	if oidcConfig == nil {
		return nil, nil
	}

	// Calculate resource URL for RFC 9728 compliance
	resourceURL := oidcConfig.ResourceURL
	if resourceURL == "" {
		resourceURL = createServiceURL(resource.GetName(), resource.GetNamespace(), resource.GetProxyPort())
	}

	switch oidcConfig.Type {
	case mcpv1alpha1.OIDCConfigTypeKubernetes:
		return r.resolveKubernetesConfig(ctx, oidcConfig.Kubernetes, resourceURL, resource.GetNamespace())
	case mcpv1alpha1.OIDCConfigTypeConfigMap:
		return r.resolveConfigMapConfig(ctx, oidcConfig.ConfigMap, resourceURL, resource.GetNamespace())
	case mcpv1alpha1.OIDCConfigTypeInline:
		return r.resolveInlineConfig(oidcConfig.Inline, resourceURL)
	default:
		return nil, fmt.Errorf("unknown OIDC config type: %s", oidcConfig.Type)
	}
}

// resolveKubernetesConfig resolves Kubernetes OIDC config using namespace directly
func (*resolver) resolveKubernetesConfig(
	ctx context.Context,
	config *mcpv1alpha1.KubernetesOIDCConfig,
	resourceURL string,
	namespace string,
) (*OIDCConfig, error) {
	if config == nil {
		ctxLogger := log.FromContext(ctx)
		ctxLogger.Info("Kubernetes OIDCConfig is nil, using default configuration", "namespace", namespace)
		defaultUseClusterAuth := true
		config = &mcpv1alpha1.KubernetesOIDCConfig{
			UseClusterAuth: &defaultUseClusterAuth,
		}
	}

	// Handle UseClusterAuth with default of true if nil
	useClusterAuth := true
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

// resolveConfigMapConfig resolves ConfigMap OIDC config using namespace directly
func (r *resolver) resolveConfigMapConfig(
	ctx context.Context,
	configRef *mcpv1alpha1.ConfigMapOIDCRef,
	resourceURL string,
	namespace string,
) (*OIDCConfig, error) {
	if configRef == nil {
		return nil, nil
	}

	if r.client == nil {
		return nil, fmt.Errorf("kubernetes client is required for ConfigMap OIDC resolution")
	}

	// Validate CABundleRef if present
	if err := validation.ValidateCABundleSource(configRef.CABundleRef); err != nil {
		return nil, err
	}

	// Read the ConfigMap
	configMap := &corev1.ConfigMap{}
	err := r.client.Get(ctx, types.NamespacedName{
		Name:      configRef.Name,
		Namespace: namespace,
	}, configMap)
	if err != nil {
		return nil, fmt.Errorf("failed to get OIDC ConfigMap %s/%s: %w",
			namespace, configRef.Name, err)
	}

	config := &OIDCConfig{
		ResourceURL: resourceURL,
	}

	// Extract string values
	config.Issuer = getMapValue(configMap.Data, "issuer")
	config.Audience = getMapValue(configMap.Data, "audience")
	config.JWKSURL = getMapValue(configMap.Data, "jwksUrl")
	config.IntrospectionURL = getMapValue(configMap.Data, "introspectionUrl")
	config.ClientID = getMapValue(configMap.Data, "clientId")
	config.ClientSecret = getMapValue(configMap.Data, "clientSecret")
	config.ThvCABundlePath = getMapValue(configMap.Data, "thvCABundlePath")
	//nolint:gosec // This is just a config key name, not a credential
	config.JWKSAuthTokenPath = getMapValue(configMap.Data, "jwksAuthTokenPath")

	// Handle boolean values
	if v, exists := configMap.Data["jwksAllowPrivateIP"]; exists && v == "true" {
		config.JWKSAllowPrivateIP = true
	}
	if v, exists := configMap.Data["insecureAllowHTTP"]; exists && v == "true" {
		config.InsecureAllowHTTP = true
	}

	// Handle scopes as comma-separated values
	config.Scopes = parseCommaSeparatedList(getMapValue(configMap.Data, "scopes"))

	// Compute ThvCABundlePath from CABundleRef if not explicitly set in ConfigMap
	if config.ThvCABundlePath == "" {
		config.ThvCABundlePath = computeCABundlePath(configRef.CABundleRef)
	}

	return config, nil
}

// resolveInlineConfig resolves inline OIDC configuration
func (*resolver) resolveInlineConfig(
	config *mcpv1alpha1.InlineOIDCConfig,
	resourceURL string,
) (*OIDCConfig, error) {
	if config == nil {
		return nil, nil
	}

	// Validate CABundleRef if present
	if err := validation.ValidateCABundleSource(config.CABundleRef); err != nil {
		return nil, err
	}

	// Don't embed ClientSecret in the config if ClientSecretRef is set
	// The secret will be injected via environment variable instead
	clientSecret := config.ClientSecret
	if config.ClientSecretRef != nil {
		clientSecret = ""
	}

	// Compute ThvCABundlePath: use explicit value if set, otherwise auto-compute from CABundleRef
	//nolint:staticcheck // SA1019: ThvCABundlePath is deprecated but still supported for backwards compatibility
	thvCABundlePath := config.ThvCABundlePath
	if thvCABundlePath == "" {
		thvCABundlePath = computeCABundlePath(config.CABundleRef)
	}

	return &OIDCConfig{
		Issuer:             config.Issuer,
		Audience:           config.Audience,
		JWKSURL:            config.JWKSURL,
		IntrospectionURL:   config.IntrospectionURL,
		ClientID:           config.ClientID,
		ClientSecret:       clientSecret,
		ThvCABundlePath:    thvCABundlePath,
		JWKSAuthTokenPath:  config.JWKSAuthTokenPath,
		ResourceURL:        resourceURL,
		JWKSAllowPrivateIP: config.JWKSAllowPrivateIP,
		InsecureAllowHTTP:  config.InsecureAllowHTTP,
		Scopes:             config.Scopes,
	}, nil
}

// getMapValue is a helper to extract string values from a map
func getMapValue(data map[string]string, key string) string {
	if v, exists := data[key]; exists && v != "" {
		return v
	}
	return ""
}

// computeCABundlePath computes the CA bundle mount path from a CABundleSource.
// Returns empty string if caBundleRef is nil or has no ConfigMapRef.
func computeCABundlePath(caBundleRef *mcpv1alpha1.CABundleSource) string {
	if caBundleRef == nil || caBundleRef.ConfigMapRef == nil {
		return ""
	}
	ref := caBundleRef.ConfigMapRef
	key := ref.Key
	if key == "" {
		key = validation.OIDCCABundleDefaultKey
	}
	return fmt.Sprintf("%s/%s/%s", validation.OIDCCABundleMountBasePath, ref.Name, key)
}

// parseCommaSeparatedList parses a comma-separated string into a slice of strings.
// It trims whitespace from each element and filters out empty strings.
func parseCommaSeparatedList(value string) []string {
	if value == "" {
		return nil
	}

	parts := strings.Split(value, ",")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		trimmed := strings.TrimSpace(part)
		if trimmed != "" {
			result = append(result, trimmed)
		}
	}

	if len(result) == 0 {
		return nil
	}
	return result
}

// createServiceURL creates a service URL from MCPServer details
func createServiceURL(name, namespace string, port int32) string {
	if port == 0 {
		port = 8080
	}
	return fmt.Sprintf("http://%s.%s.svc.cluster.local:%d", name, namespace, port)
}

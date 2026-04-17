// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package oidc provides utilities for resolving OIDC configuration from MCPOIDCConfig resources.
package oidc

import (
	"context"
	"fmt"

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
)

// OIDCConfig represents the resolved OIDC configuration values
type OIDCConfig struct { //nolint:revive // Keeping OIDCConfig name for backward compatibility
	Issuer                          string
	Audience                        string
	JWKSURL                         string
	IntrospectionURL                string
	ClientID                        string
	ClientSecret                    string // #nosec G117 -- not a hardcoded credential, populated at runtime from config
	ThvCABundlePath                 string
	JWKSAuthTokenPath               string
	ResourceURL                     string
	JWKSAllowPrivateIP              bool
	ProtectedResourceAllowPrivateIP bool
	InsecureAllowHTTP               bool
	Scopes                          []string
}

//go:generate mockgen -destination=mocks/mock_resolver.go -package=mocks -source=resolver.go Resolver

// Resolver is the interface for resolving OIDC configuration from various sources
type Resolver interface {
	// ResolveFromConfigRef resolves OIDC configuration from an MCPOIDCConfig reference.
	// It fetches the MCPOIDCConfig resource and merges shared provider config with
	// per-server overrides (audience, scopes) from the reference.
	ResolveFromConfigRef(
		ctx context.Context,
		oidcConfigRef *mcpv1alpha1.MCPOIDCConfigReference,
		oidcConfig *mcpv1alpha1.MCPOIDCConfig,
		serverName, namespace string,
		proxyPort int32,
	) (*OIDCConfig, error)
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

// ResolveFromConfigRef resolves OIDC configuration from an MCPOIDCConfig reference.
// It merges shared provider config from the MCPOIDCConfig with per-server overrides
// (audience, scopes) from the MCPOIDCConfigReference.
func (r *resolver) ResolveFromConfigRef(
	ctx context.Context,
	ref *mcpv1alpha1.MCPOIDCConfigReference,
	oidcCfg *mcpv1alpha1.MCPOIDCConfig,
	serverName, namespace string,
	proxyPort int32,
) (*OIDCConfig, error) {
	if ref == nil || oidcCfg == nil {
		return nil, nil
	}

	resourceURL := ref.ResourceURL
	if resourceURL == "" {
		resourceURL = createServiceURL(serverName, namespace, proxyPort)
	}

	switch oidcCfg.Spec.Type {
	case mcpv1alpha1.MCPOIDCConfigTypeKubernetesServiceAccount:
		return r.resolveFromK8sServiceAccountConfig(ctx, oidcCfg.Spec.KubernetesServiceAccount, ref, resourceURL)
	case mcpv1alpha1.MCPOIDCConfigTypeInline:
		return r.resolveFromInlineSharedConfig(oidcCfg.Spec.Inline, ref, resourceURL)
	default:
		return nil, fmt.Errorf("unknown MCPOIDCConfig type: %s", oidcCfg.Spec.Type)
	}
}

// resolveFromK8sServiceAccountConfig resolves OIDC config from a shared KubernetesServiceAccount config
// with per-server audience override from the MCPOIDCConfigReference.
func (*resolver) resolveFromK8sServiceAccountConfig(
	ctx context.Context,
	config *mcpv1alpha1.KubernetesServiceAccountOIDCConfig,
	ref *mcpv1alpha1.MCPOIDCConfigReference,
	resourceURL string,
) (*OIDCConfig, error) {
	if config == nil {
		ctxLogger := log.FromContext(ctx)
		ctxLogger.Info("KubernetesServiceAccount OIDCConfig is nil, using defaults")
		defaultUseClusterAuth := true
		config = &mcpv1alpha1.KubernetesServiceAccountOIDCConfig{
			UseClusterAuth: &defaultUseClusterAuth,
		}
	}

	useClusterAuth := true
	if config.UseClusterAuth != nil {
		useClusterAuth = *config.UseClusterAuth
	}

	result := &OIDCConfig{
		ResourceURL: resourceURL,
		// Audience comes from the per-server reference, not the shared config
		Audience: ref.Audience,
		Scopes:   ref.Scopes,
	}

	result.Issuer = config.Issuer
	if result.Issuer == "" {
		result.Issuer = defaultK8sIssuer
	}

	result.JWKSURL = config.JWKSURL
	result.IntrospectionURL = config.IntrospectionURL

	if useClusterAuth {
		result.ThvCABundlePath = defaultK8sCABundlePath
		result.JWKSAuthTokenPath = defaultK8sTokenPath
		result.JWKSAllowPrivateIP = true
	}

	return result, nil
}

// resolveFromInlineSharedConfig resolves OIDC config from a shared inline config
// with per-server audience and scopes override from the MCPOIDCConfigReference.
func (*resolver) resolveFromInlineSharedConfig(
	config *mcpv1alpha1.InlineOIDCSharedConfig,
	ref *mcpv1alpha1.MCPOIDCConfigReference,
	resourceURL string,
) (*OIDCConfig, error) {
	if config == nil {
		return nil, nil
	}

	if err := validation.ValidateCABundleSource(config.CABundleRef); err != nil {
		return nil, err
	}

	return &OIDCConfig{
		Issuer:                          config.Issuer,
		Audience:                        ref.Audience,
		JWKSURL:                         config.JWKSURL,
		IntrospectionURL:                config.IntrospectionURL,
		ClientID:                        config.ClientID,
		ThvCABundlePath:                 computeCABundlePath(config.CABundleRef),
		JWKSAuthTokenPath:               config.JWKSAuthTokenPath,
		ResourceURL:                     resourceURL,
		JWKSAllowPrivateIP:              config.JWKSAllowPrivateIP,
		ProtectedResourceAllowPrivateIP: config.ProtectedResourceAllowPrivateIP,
		InsecureAllowHTTP:               config.InsecureAllowHTTP,
		Scopes:                          ref.Scopes,
	}, nil
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

// createServiceURL creates a service URL from MCPServer details
func createServiceURL(name, namespace string, port int32) string {
	if port == 0 {
		port = 8080
	}
	return fmt.Sprintf("http://%s.%s.svc.cluster.local:%d", name, namespace, port)
}

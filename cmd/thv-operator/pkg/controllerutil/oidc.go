package controllerutil

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	"github.com/stacklok/toolhive/cmd/thv-operator/pkg/oidc"
	"github.com/stacklok/toolhive/pkg/runner"
)

// AddOIDCConfigOptions adds OIDC configuration options to builder options
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
	// The auth middleware will be automatically created from this config by
	// PopulateMiddlewareConfigs() when the runner starts. This avoids code duplication
	// and ensures consistent middleware creation between CLI and operator.
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
		oidcConfig.InsecureAllowHTTP,
	))

	return nil
}

// GenerateOIDCClientSecretEnvVar generates environment variable for OIDC client secret
// when using a SecretKeyRef.
// Returns nil if clientSecretRef is nil.
func GenerateOIDCClientSecretEnvVar(
	ctx context.Context,
	c client.Client,
	namespace string,
	clientSecretRef *mcpv1alpha1.SecretKeyRef,
) (*corev1.EnvVar, error) {
	if clientSecretRef == nil {
		return nil, nil
	}

	// Validate that the referenced secret exists
	var secret corev1.Secret
	if err := c.Get(ctx, types.NamespacedName{
		Namespace: namespace,
		Name:      clientSecretRef.Name,
	}, &secret); err != nil {
		return nil, fmt.Errorf("failed to get OIDC client secret %s/%s: %w",
			namespace, clientSecretRef.Name, err)
	}

	// Validate that the key exists in the secret
	if _, ok := secret.Data[clientSecretRef.Key]; !ok {
		return nil, fmt.Errorf("OIDC client secret %s/%s is missing key %q",
			namespace, clientSecretRef.Name, clientSecretRef.Key)
	}

	// Return environment variable with secret reference
	return &corev1.EnvVar{
		Name: "TOOLHIVE_OIDC_CLIENT_SECRET",
		ValueFrom: &corev1.EnvVarSource{
			SecretKeyRef: &corev1.SecretKeySelector{
				LocalObjectReference: corev1.LocalObjectReference{
					Name: clientSecretRef.Name,
				},
				Key: clientSecretRef.Key,
			},
		},
	}, nil
}

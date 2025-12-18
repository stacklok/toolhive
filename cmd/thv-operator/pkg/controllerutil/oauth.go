package controllerutil

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	"github.com/stacklok/toolhive/pkg/authserver"
	"github.com/stacklok/toolhive/pkg/runner"
)

const (
	// OAuthSigningKeyMountPath is the path where the OAuth signing key is mounted in the container
	OAuthSigningKeyMountPath = "/etc/authserver/signing-key.pem"

	// OAuthSigningKeyVolumeName is the name of the volume for the OAuth signing key
	OAuthSigningKeyVolumeName = "oauth-signing-key"

	// OAuthUpstreamClientSecretEnvVar is the environment variable name for the upstream client secret
	// nolint:gosec // G101: This is an environment variable name, not a hardcoded credential
	OAuthUpstreamClientSecretEnvVar = "TOOLHIVE_OAUTH_UPSTREAM_CLIENT_SECRET"

	// DefaultAccessTokenLifespan is the default access token lifespan if not specified
	DefaultAccessTokenLifespan = 1 * time.Hour
)

// OAuthVolumeConfig holds the volume and volume mount configuration for OAuth
type OAuthVolumeConfig struct {
	Volume      corev1.Volume
	VolumeMount corev1.VolumeMount
}

// GenerateOAuthEnvVars generates environment variables for OAuth authentication
func GenerateOAuthEnvVars(
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

	if externalAuthConfig.Spec.Type != mcpv1alpha1.ExternalAuthTypeOAuth {
		return envVars, nil
	}

	oauthSpec := externalAuthConfig.Spec.OAuth
	if oauthSpec == nil {
		return envVars, nil
	}

	// Add upstream client secret env var
	envVars = append(envVars, corev1.EnvVar{
		Name: OAuthUpstreamClientSecretEnvVar,
		ValueFrom: &corev1.EnvVarSource{
			SecretKeyRef: &corev1.SecretKeySelector{
				LocalObjectReference: corev1.LocalObjectReference{
					Name: oauthSpec.Upstream.ClientSecretRef.Name,
				},
				Key: oauthSpec.Upstream.ClientSecretRef.Key,
			},
		},
	})

	return envVars, nil
}

// GenerateOAuthVolumeConfig generates volume and volume mount configuration for OAuth signing key
func GenerateOAuthVolumeConfig(
	oauthSpec *mcpv1alpha1.OAuthConfig,
) *OAuthVolumeConfig {
	if oauthSpec == nil {
		return nil
	}

	return &OAuthVolumeConfig{
		Volume: corev1.Volume{
			Name: OAuthSigningKeyVolumeName,
			VolumeSource: corev1.VolumeSource{
				Secret: &corev1.SecretVolumeSource{
					SecretName: oauthSpec.AuthServer.SigningKeyRef.Name,
					Items: []corev1.KeyToPath{
						{
							Key:  oauthSpec.AuthServer.SigningKeyRef.Key,
							Path: "signing-key.pem",
						},
					},
					DefaultMode: func() *int32 { mode := int32(0400); return &mode }(),
				},
			},
		},
		VolumeMount: corev1.VolumeMount{
			Name:      OAuthSigningKeyVolumeName,
			MountPath: "/etc/authserver",
			ReadOnly:  true,
		},
	}
}

// addOAuthConfig adds OAuth configuration to the runner options
func addOAuthConfig(
	ctx context.Context,
	c client.Client,
	namespace string,
	externalAuthConfig *mcpv1alpha1.MCPExternalAuthConfig,
	options *[]runner.RunConfigBuilderOption,
) error {
	oauthSpec := externalAuthConfig.Spec.OAuth
	if oauthSpec == nil {
		return fmt.Errorf("oauth configuration is nil for type oauth")
	}

	// Validate that the signing key secret exists
	if err := validateSecretExists(ctx, c, namespace, oauthSpec.AuthServer.SigningKeyRef); err != nil {
		return fmt.Errorf("signing key secret validation failed: %w", err)
	}

	// Validate that the upstream client secret exists
	if err := validateSecretExists(ctx, c, namespace, oauthSpec.Upstream.ClientSecretRef); err != nil {
		return fmt.Errorf("upstream client secret validation failed: %w", err)
	}

	// Parse access token lifespan
	accessTokenLifespan, err := parseAccessTokenLifespan(oauthSpec.AuthServer.AccessTokenLifespan)
	if err != nil {
		return fmt.Errorf("invalid access token lifespan: %w", err)
	}

	// Build auth server clients configuration
	clients := make([]authserver.RunClientConfig, 0, len(oauthSpec.AuthServer.Clients))
	for _, clientSpec := range oauthSpec.AuthServer.Clients {
		clients = append(clients, authserver.RunClientConfig{
			ID:           clientSpec.ID,
			Secret:       clientSpec.Secret,
			RedirectURIs: clientSpec.RedirectURIs,
			Public:       clientSpec.Public,
		})
	}

	// Build upstream configuration
	// Note: ClientSecret is provided via environment variable TOOLHIVE_OAUTH_UPSTREAM_CLIENT_SECRET
	upstreamConfig := &authserver.RunUpstreamConfig{
		Issuer:   oauthSpec.Upstream.Issuer,
		ClientID: oauthSpec.Upstream.ClientID,
		Scopes:   oauthSpec.Upstream.Scopes,
	}

	// Build the auth server run config
	authServerConfig := &authserver.RunConfig{
		Enabled:             true,
		Issuer:              oauthSpec.AuthServer.Issuer,
		SigningKeyPath:      OAuthSigningKeyMountPath,
		AccessTokenLifespan: accessTokenLifespan,
		Upstream:            upstreamConfig,
		Clients:             clients,
	}

	// Add auth server config to runner options
	*options = append(*options, runner.WithAuthServerRunConfig(authServerConfig))

	return nil
}

// validateSecretExists validates that a referenced secret exists and contains the required key
func validateSecretExists(
	ctx context.Context,
	c client.Client,
	namespace string,
	secretRef mcpv1alpha1.SecretKeyRef,
) error {
	var secret corev1.Secret
	if err := c.Get(ctx, types.NamespacedName{
		Namespace: namespace,
		Name:      secretRef.Name,
	}, &secret); err != nil {
		return fmt.Errorf("failed to get secret %s/%s: %w", namespace, secretRef.Name, err)
	}

	if _, ok := secret.Data[secretRef.Key]; !ok {
		return fmt.Errorf("secret %s/%s is missing key %q", namespace, secretRef.Name, secretRef.Key)
	}

	return nil
}

// parseAccessTokenLifespan parses the access token lifespan string to a time.Duration
func parseAccessTokenLifespan(lifespanStr string) (time.Duration, error) {
	if lifespanStr == "" {
		return DefaultAccessTokenLifespan, nil
	}

	duration, err := time.ParseDuration(lifespanStr)
	if err != nil {
		return 0, fmt.Errorf("invalid duration format %q: %w", lifespanStr, err)
	}

	if duration <= 0 {
		return 0, fmt.Errorf("access token lifespan must be positive, got %v", duration)
	}

	return duration, nil
}

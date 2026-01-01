package controllerutil

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	"github.com/stacklok/toolhive/pkg/authserver/runconfig"
	"github.com/stacklok/toolhive/pkg/runner"
)

const (
	// OAuthSigningKeyMountPath is the path where the OAuth signing key is mounted in the container
	OAuthSigningKeyMountPath = "/etc/authserver/signing-key.pem"

	// OAuthSigningKeyVolumeName is the name of the volume for the OAuth signing key
	OAuthSigningKeyVolumeName = "oauth-signing-key"

	// OAuthHMACSecretMountPath is the path where the HMAC secret is mounted
	//nolint:gosec // G101: This is a file path constant, not a credential
	OAuthHMACSecretMountPath = "/etc/authserver/hmac-secret"

	// OAuthHMACSecretVolumeName is the name of the volume for the HMAC secret
	//nolint:gosec // G101: This is a volume name constant, not a credential
	OAuthHMACSecretVolumeName = "oauth-hmac-secret"

	// OAuthHMACSecretFilePath is the full path to the HMAC secret file inside the container
	//nolint:gosec // G101: This is a file path constant, not a credential
	OAuthHMACSecretFilePath = "/etc/authserver/hmac/hmac-secret"

	// OAuthUpstreamClientSecretEnvVar is the environment variable name for the upstream client secret
	// nolint:gosec // G101: This is an environment variable name, not a hardcoded credential
	OAuthUpstreamClientSecretEnvVar = "TOOLHIVE_OAUTH_UPSTREAM_CLIENT_SECRET"

	// OAuthRedisPasswordEnvVar is the environment variable for Redis password
	// nolint:gosec // G101: This is an environment variable name, not a hardcoded credential
	OAuthRedisPasswordEnvVar = "TOOLHIVE_AUTHSERVER_REDIS_PASSWORD"

	// DefaultAccessTokenLifespan is the default access token lifespan if not specified
	DefaultAccessTokenLifespan = 1 * time.Hour
)

// OAuthVolumeConfig holds the volume and volume mount configuration for OAuth
type OAuthVolumeConfig struct {
	Volumes      []corev1.Volume
	VolumeMounts []corev1.VolumeMount
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

	// Add Redis password env var if Redis storage is configured
	if oauthSpec.AuthServer.Storage != nil &&
		oauthSpec.AuthServer.Storage.Type == "redis" &&
		oauthSpec.AuthServer.Storage.Redis != nil &&
		oauthSpec.AuthServer.Storage.Redis.PasswordRef != nil {
		envVars = append(envVars, corev1.EnvVar{
			Name: OAuthRedisPasswordEnvVar,
			ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: oauthSpec.AuthServer.Storage.Redis.PasswordRef.Name,
					},
					Key: oauthSpec.AuthServer.Storage.Redis.PasswordRef.Key,
				},
			},
		})
	}

	return envVars, nil
}

// GenerateOAuthVolumeConfig generates volume and volume mount configuration for OAuth signing key and HMAC secret
func GenerateOAuthVolumeConfig(
	oauthSpec *mcpv1alpha1.OAuthConfig,
) *OAuthVolumeConfig {
	if oauthSpec == nil {
		return nil
	}

	volumes := []corev1.Volume{}
	volumeMounts := []corev1.VolumeMount{}

	// Add signing key volume
	volumes = append(volumes, corev1.Volume{
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
	})

	volumeMounts = append(volumeMounts, corev1.VolumeMount{
		Name:      OAuthSigningKeyVolumeName,
		MountPath: "/etc/authserver",
		ReadOnly:  true,
	})

	// Add HMAC secret volume if configured
	if oauthSpec.AuthServer.HMACSecretRef != nil {
		volumes = append(volumes, corev1.Volume{
			Name: OAuthHMACSecretVolumeName,
			VolumeSource: corev1.VolumeSource{
				Secret: &corev1.SecretVolumeSource{
					SecretName: oauthSpec.AuthServer.HMACSecretRef.Name,
					Items: []corev1.KeyToPath{
						{
							Key:  oauthSpec.AuthServer.HMACSecretRef.Key,
							Path: "hmac-secret",
						},
					},
					DefaultMode: func() *int32 { mode := int32(0400); return &mode }(),
				},
			},
		})

		volumeMounts = append(volumeMounts, corev1.VolumeMount{
			Name:      OAuthHMACSecretVolumeName,
			MountPath: "/etc/authserver/hmac",
			ReadOnly:  true,
		})
	}

	return &OAuthVolumeConfig{
		Volumes:      volumes,
		VolumeMounts: volumeMounts,
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

	// Validate all required secrets
	if err := validateOAuthSecrets(ctx, c, namespace, oauthSpec); err != nil {
		return err
	}

	// Parse access token lifespan
	accessTokenLifespan, err := parseAccessTokenLifespan(oauthSpec.AuthServer.AccessTokenLifespan)
	if err != nil {
		return fmt.Errorf("invalid access token lifespan: %w", err)
	}

	// Build auth server clients configuration
	clients := buildAuthServerClients(oauthSpec.AuthServer.Clients)

	// Build upstream configuration
	// Note: ClientSecret is provided via environment variable TOOLHIVE_OAUTH_UPSTREAM_CLIENT_SECRET
	upstreamConfig := &runconfig.UpstreamConfig{
		Issuer:   oauthSpec.Upstream.Issuer,
		ClientID: oauthSpec.Upstream.ClientID,
		Scopes:   oauthSpec.Upstream.Scopes,
	}

	// Build storage configuration
	storageConfig := buildStorageConfig(oauthSpec.AuthServer.Storage)

	// Set HMAC secret path if configured
	hmacSecretPath := getHMACSecretPath(oauthSpec.AuthServer.HMACSecretRef)

	// Build the auth server run config
	authServerConfig := &runconfig.RunConfig{
		Enabled:             true,
		Issuer:              oauthSpec.AuthServer.Issuer,
		SigningKeyPath:      OAuthSigningKeyMountPath,
		HMACSecretPath:      hmacSecretPath,
		AccessTokenLifespan: accessTokenLifespan,
		Upstream:            upstreamConfig,
		Clients:             clients,
		Storage:             storageConfig,
	}

	// Add auth server config to runner options
	*options = append(*options, runner.WithAuthServerRunConfig(authServerConfig))

	return nil
}

// validateOAuthSecrets validates that all required OAuth secrets exist
func validateOAuthSecrets(
	ctx context.Context,
	c client.Client,
	namespace string,
	oauthSpec *mcpv1alpha1.OAuthConfig,
) error {
	// Validate that the signing key secret exists
	if err := validateSecretExists(ctx, c, namespace, oauthSpec.AuthServer.SigningKeyRef); err != nil {
		return fmt.Errorf("signing key secret validation failed: %w", err)
	}

	// Validate that the upstream client secret exists
	if err := validateSecretExists(ctx, c, namespace, oauthSpec.Upstream.ClientSecretRef); err != nil {
		return fmt.Errorf("upstream client secret validation failed: %w", err)
	}

	// Validate HMAC secret if configured
	if oauthSpec.AuthServer.HMACSecretRef != nil {
		if err := validateSecretExists(ctx, c, namespace, *oauthSpec.AuthServer.HMACSecretRef); err != nil {
			return fmt.Errorf("HMAC secret validation failed: %w", err)
		}
	}

	// Validate Redis password secret if configured
	if oauthSpec.AuthServer.Storage != nil &&
		oauthSpec.AuthServer.Storage.Type == "redis" &&
		oauthSpec.AuthServer.Storage.Redis != nil &&
		oauthSpec.AuthServer.Storage.Redis.PasswordRef != nil {
		if err := validateSecretExists(ctx, c, namespace, *oauthSpec.AuthServer.Storage.Redis.PasswordRef); err != nil {
			return fmt.Errorf("redis password secret validation failed: %w", err)
		}
	}

	return nil
}

// buildAuthServerClients builds the auth server clients configuration from the spec
func buildAuthServerClients(clientSpecs []mcpv1alpha1.OAuthClientConfig) []runconfig.ClientConfig {
	clients := make([]runconfig.ClientConfig, 0, len(clientSpecs))
	for _, clientSpec := range clientSpecs {
		clients = append(clients, runconfig.ClientConfig{
			ID:           clientSpec.ID,
			Secret:       clientSpec.Secret,
			RedirectURIs: clientSpec.RedirectURIs,
			Public:       clientSpec.Public,
		})
	}
	return clients
}

// buildStorageConfig builds the storage configuration from the spec
func buildStorageConfig(storageSpec *mcpv1alpha1.OAuthStorageConfig) *runconfig.StorageConfig {
	if storageSpec == nil {
		return nil
	}

	storageConfig := &runconfig.StorageConfig{
		Type: storageSpec.Type,
	}

	if storageSpec.Redis != nil {
		storageConfig.RedisURL = storageSpec.Redis.URL
		storageConfig.KeyPrefix = storageSpec.Redis.KeyPrefix
		// Password is provided via environment variable TOOLHIVE_AUTHSERVER_REDIS_PASSWORD
	}

	return storageConfig
}

// getHMACSecretPath returns the HMAC secret path if configured, empty string otherwise
func getHMACSecretPath(hmacSecretRef *mcpv1alpha1.SecretKeyRef) string {
	if hmacSecretRef != nil {
		return OAuthHMACSecretFilePath
	}
	return ""
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

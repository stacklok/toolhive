// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllerutil

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	k8sptr "k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	"github.com/stacklok/toolhive/pkg/authserver"
	"github.com/stacklok/toolhive/pkg/runner"
)

// Constants for auth server volume mounting
const (
	// AuthServerKeysVolumePrefix is the prefix for signing key volume names
	AuthServerKeysVolumePrefix = "authserver-signing-key-"

	// AuthServerHMACVolumePrefix is the prefix for HMAC secret volume names
	AuthServerHMACVolumePrefix = "authserver-hmac-secret-"

	// AuthServerKeysMountPath is the base path where signing keys are mounted
	AuthServerKeysMountPath = "/etc/toolhive/authserver/keys"

	// AuthServerHMACMountPath is the base path where HMAC secrets are mounted
	AuthServerHMACMountPath = "/etc/toolhive/authserver/hmac"

	// AuthServerKeyFilePattern is the pattern for signing key filenames
	AuthServerKeyFilePattern = "key-%d.pem"

	// AuthServerHMACFilePattern is the pattern for HMAC secret filenames
	AuthServerHMACFilePattern = "hmac-%d"

	// UpstreamClientSecretEnvVar is the environment variable name for the upstream client secret
	// #nosec G101 -- This is an environment variable name, not a hardcoded credential
	UpstreamClientSecretEnvVar = "TOOLHIVE_UPSTREAM_CLIENT_SECRET"
)

// GenerateAuthServerConfig generates volumes, volume mounts, and environment variables
// for the embedded auth server if the external auth config is of type embeddedAuthServer.
//
// This is a convenience function that combines GenerateAuthServerVolumes and GenerateAuthServerEnvVars,
// with the added logic to fetch and check the MCPExternalAuthConfig type.
//
// Returns empty slices if externalAuthConfigRef is nil or if the auth type is not embeddedAuthServer.
func GenerateAuthServerConfig(
	ctx context.Context,
	c client.Client,
	namespace string,
	externalAuthConfigRef *mcpv1alpha1.ExternalAuthConfigRef,
) ([]corev1.Volume, []corev1.VolumeMount, []corev1.EnvVar, error) {
	if externalAuthConfigRef == nil {
		return nil, nil, nil, nil
	}

	// Fetch the MCPExternalAuthConfig
	externalAuthConfig, err := GetExternalAuthConfigByName(ctx, c, namespace, externalAuthConfigRef.Name)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to get MCPExternalAuthConfig: %w", err)
	}

	// Only process embeddedAuthServer type
	if externalAuthConfig.Spec.Type != mcpv1alpha1.ExternalAuthTypeEmbeddedAuthServer {
		return nil, nil, nil, nil
	}

	authServerConfig := externalAuthConfig.Spec.EmbeddedAuthServer
	if authServerConfig == nil {
		return nil, nil, nil, fmt.Errorf("embedded auth server configuration is nil for type embeddedAuthServer")
	}

	// Generate volumes and mounts
	volumes, volumeMounts := GenerateAuthServerVolumes(authServerConfig)

	// Generate environment variables
	envVars := GenerateAuthServerEnvVars(authServerConfig)

	return volumes, volumeMounts, envVars, nil
}

// GenerateAuthServerVolumes creates volumes and volume mounts for embedded auth server
// signing keys and HMAC secrets. Returns slices of volumes and volume mounts.
// The volumes are configured with 0400 permissions for security.
//
// For signing keys, files are mounted at /etc/toolhive/authserver/keys/key-{N}.pem
// For HMAC secrets, files are mounted at /etc/toolhive/authserver/hmac/hmac-{N}
//
// Returns nil slices if authConfig is nil.
func GenerateAuthServerVolumes(
	authConfig *mcpv1alpha1.EmbeddedAuthServerConfig,
) ([]corev1.Volume, []corev1.VolumeMount) {
	if authConfig == nil {
		return nil, nil
	}

	var volumes []corev1.Volume
	var volumeMounts []corev1.VolumeMount

	// Generate volumes for signing keys
	for idx, keyRef := range authConfig.SigningKeySecretRefs {
		volumeName := fmt.Sprintf("%s%d", AuthServerKeysVolumePrefix, idx)
		fileName := fmt.Sprintf(AuthServerKeyFilePattern, idx)

		volumes = append(volumes, corev1.Volume{
			Name: volumeName,
			VolumeSource: corev1.VolumeSource{
				Secret: &corev1.SecretVolumeSource{
					SecretName: keyRef.Name,
					Items: []corev1.KeyToPath{{
						Key:  keyRef.Key,
						Path: fileName,
					}},
					DefaultMode: k8sptr.To(int32(0400)), // Read-only for owner
				},
			},
		})

		volumeMounts = append(volumeMounts, corev1.VolumeMount{
			Name:      volumeName,
			MountPath: fmt.Sprintf("%s/%s", AuthServerKeysMountPath, fileName),
			SubPath:   fileName,
			ReadOnly:  true,
		})
	}

	// Generate volumes for HMAC secrets
	for idx, hmacRef := range authConfig.HMACSecretRefs {
		volumeName := fmt.Sprintf("%s%d", AuthServerHMACVolumePrefix, idx)
		fileName := fmt.Sprintf(AuthServerHMACFilePattern, idx)

		volumes = append(volumes, corev1.Volume{
			Name: volumeName,
			VolumeSource: corev1.VolumeSource{
				Secret: &corev1.SecretVolumeSource{
					SecretName: hmacRef.Name,
					Items: []corev1.KeyToPath{{
						Key:  hmacRef.Key,
						Path: fileName,
					}},
					DefaultMode: k8sptr.To(int32(0400)), // Read-only for owner
				},
			},
		})

		volumeMounts = append(volumeMounts, corev1.VolumeMount{
			Name:      volumeName,
			MountPath: fmt.Sprintf("%s/%s", AuthServerHMACMountPath, fileName),
			SubPath:   fileName,
			ReadOnly:  true,
		})
	}

	return volumes, volumeMounts
}

// GenerateAuthServerEnvVars creates environment variables for embedded auth server.
// Currently generates TOOLHIVE_UPSTREAM_CLIENT_SECRET from the upstream provider's
// client secret reference.
//
// The function looks at the first upstream provider (currently only one is supported)
// and generates an environment variable for its client secret if one is configured.
//
// Returns nil slice if authConfig is nil or if no client secret is configured.
func GenerateAuthServerEnvVars(
	authConfig *mcpv1alpha1.EmbeddedAuthServerConfig,
) []corev1.EnvVar {
	if authConfig == nil {
		return nil
	}

	var envVars []corev1.EnvVar

	// Currently only one upstream provider is supported
	if len(authConfig.UpstreamProviders) == 0 {
		return nil
	}

	provider := authConfig.UpstreamProviders[0]

	// Extract client secret reference based on provider type
	var clientSecretRef *mcpv1alpha1.SecretKeyRef

	switch provider.Type {
	case mcpv1alpha1.UpstreamProviderTypeOIDC:
		if provider.OIDCConfig != nil {
			clientSecretRef = provider.OIDCConfig.ClientSecretRef
		}
	case mcpv1alpha1.UpstreamProviderTypeOAuth2:
		if provider.OAuth2Config != nil {
			clientSecretRef = provider.OAuth2Config.ClientSecretRef
		}
	}

	// Generate env var for upstream client secret if provided
	if clientSecretRef != nil {
		envVars = append(envVars, corev1.EnvVar{
			Name: UpstreamClientSecretEnvVar,
			ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: clientSecretRef.Name,
					},
					Key: clientSecretRef.Key,
				},
			},
		})
	}

	return envVars
}

// AddEmbeddedAuthServerConfigOptions adds embedded auth server configuration to
// runner options when the external auth type is embeddedAuthServer.
// This is called by the runconfig generation logic to configure the auth server.
//
// The function:
// 1. Fetches the MCPExternalAuthConfig by name
// 2. Checks if the type is embeddedAuthServer
// 3. Adds the appropriate runner options for embedded auth server configuration
//
// Returns nil if externalAuthConfigRef is nil or if the auth type is not embeddedAuthServer.
func AddEmbeddedAuthServerConfigOptions(
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

	// Only process embeddedAuthServer type
	if externalAuthConfig.Spec.Type != mcpv1alpha1.ExternalAuthTypeEmbeddedAuthServer {
		return nil
	}

	authServerConfig := externalAuthConfig.Spec.EmbeddedAuthServer
	if authServerConfig == nil {
		return fmt.Errorf("embedded auth server configuration is nil for type embeddedAuthServer")
	}

	// Build the embedded auth server config for runner
	embeddedConfig := buildEmbeddedAuthServerRunnerConfig(authServerConfig)

	// Add the configuration option
	*options = append(*options, runner.WithEmbeddedAuthServerConfig(embeddedConfig))

	return nil
}

// buildEmbeddedAuthServerRunnerConfig converts CRD EmbeddedAuthServerConfig to authserver.RunConfig.
// The RunConfig is serializable and contains file paths for secrets (not the secrets themselves).
func buildEmbeddedAuthServerRunnerConfig(
	authConfig *mcpv1alpha1.EmbeddedAuthServerConfig,
) *authserver.RunConfig {
	config := &authserver.RunConfig{
		SchemaVersion: authserver.CurrentSchemaVersion,
		Issuer:        authConfig.Issuer,
	}

	// Build signing key configuration
	if len(authConfig.SigningKeySecretRefs) > 0 {
		signingKeyConfig := &authserver.SigningKeyRunConfig{
			KeyDir: AuthServerKeysMountPath,
		}
		for idx := range authConfig.SigningKeySecretRefs {
			fileName := fmt.Sprintf(AuthServerKeyFilePattern, idx)
			if idx == 0 {
				signingKeyConfig.SigningKeyFile = fileName
			} else {
				signingKeyConfig.FallbackKeyFiles = append(signingKeyConfig.FallbackKeyFiles, fileName)
			}
		}
		config.SigningKeyConfig = signingKeyConfig
	}

	// Build HMAC secret file paths
	for idx := range authConfig.HMACSecretRefs {
		hmacPath := fmt.Sprintf("%s/%s", AuthServerHMACMountPath, fmt.Sprintf(AuthServerHMACFilePattern, idx))
		config.HMACSecretFiles = append(config.HMACSecretFiles, hmacPath)
	}

	// Set token lifespans from config (as strings, will be parsed at runtime)
	if authConfig.TokenLifespans != nil {
		config.TokenLifespans = &authserver.TokenLifespanRunConfig{
			AccessTokenLifespan:  authConfig.TokenLifespans.AccessTokenLifespan,
			RefreshTokenLifespan: authConfig.TokenLifespans.RefreshTokenLifespan,
			AuthCodeLifespan:     authConfig.TokenLifespans.AuthCodeLifespan,
		}
	}

	// Build upstream provider config (currently only one supported)
	if len(authConfig.UpstreamProviders) > 0 {
		provider := authConfig.UpstreamProviders[0]
		config.Upstreams = []authserver.UpstreamRunConfig{*buildUpstreamRunConfig(&provider)}
	}

	return config
}

// buildUpstreamRunConfig converts CRD UpstreamProviderConfig to authserver.UpstreamRunConfig.
// Client secrets are passed via environment variable reference (UpstreamClientSecretEnvVar).
func buildUpstreamRunConfig(
	provider *mcpv1alpha1.UpstreamProviderConfig,
) *authserver.UpstreamRunConfig {
	config := &authserver.UpstreamRunConfig{
		Name: provider.Name,
		Type: authserver.UpstreamProviderType(provider.Type),
	}

	switch provider.Type {
	case mcpv1alpha1.UpstreamProviderTypeOIDC:
		if provider.OIDCConfig != nil {
			config.OIDCConfig = &authserver.OIDCUpstreamRunConfig{
				IssuerURL:   provider.OIDCConfig.IssuerURL,
				ClientID:    provider.OIDCConfig.ClientID,
				RedirectURI: provider.OIDCConfig.RedirectURI,
				Scopes:      provider.OIDCConfig.Scopes,
			}
			// If client secret is configured, reference it via env var
			if provider.OIDCConfig.ClientSecretRef != nil {
				config.OIDCConfig.ClientSecretEnvVar = UpstreamClientSecretEnvVar
			}
			if provider.OIDCConfig.UserInfoOverride != nil {
				config.OIDCConfig.UserInfoOverride = buildUserInfoRunConfig(provider.OIDCConfig.UserInfoOverride)
			}
		}
	case mcpv1alpha1.UpstreamProviderTypeOAuth2:
		if provider.OAuth2Config != nil {
			config.OAuth2Config = &authserver.OAuth2UpstreamRunConfig{
				AuthorizationEndpoint: provider.OAuth2Config.AuthorizationEndpoint,
				TokenEndpoint:         provider.OAuth2Config.TokenEndpoint,
				ClientID:              provider.OAuth2Config.ClientID,
				RedirectURI:           provider.OAuth2Config.RedirectURI,
				Scopes:                provider.OAuth2Config.Scopes,
			}
			// If client secret is configured, reference it via env var
			if provider.OAuth2Config.ClientSecretRef != nil {
				config.OAuth2Config.ClientSecretEnvVar = UpstreamClientSecretEnvVar
			}
			if provider.OAuth2Config.UserInfo != nil {
				config.OAuth2Config.UserInfo = buildUserInfoRunConfig(provider.OAuth2Config.UserInfo)
			}
		}
	}

	return config
}

// buildUserInfoRunConfig converts CRD UserInfoConfig to authserver.UserInfoRunConfig.
func buildUserInfoRunConfig(
	userInfo *mcpv1alpha1.UserInfoConfig,
) *authserver.UserInfoRunConfig {
	config := &authserver.UserInfoRunConfig{
		EndpointURL:       userInfo.EndpointURL,
		HTTPMethod:        userInfo.HTTPMethod,
		AdditionalHeaders: userInfo.AdditionalHeaders,
	}

	if userInfo.FieldMapping != nil {
		config.FieldMapping = &authserver.UserInfoFieldMappingRunConfig{
			SubjectFields: userInfo.FieldMapping.SubjectFields,
			NameFields:    userInfo.FieldMapping.NameFields,
			EmailFields:   userInfo.FieldMapping.EmailFields,
		}
	}

	return config
}

// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllerutil

import (
	"context"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	k8sptr "k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	"github.com/stacklok/toolhive/cmd/thv-operator/pkg/oidc"
	"github.com/stacklok/toolhive/pkg/authserver"
	authrunner "github.com/stacklok/toolhive/pkg/authserver/runner"
	"github.com/stacklok/toolhive/pkg/authserver/storage"
	"github.com/stacklok/toolhive/pkg/runner"
)

// Constants for auth server volume mounting
const (
	// AuthServerKeysVolumePrefix is the prefix for signing key volume names
	AuthServerKeysVolumePrefix = "authserver-signing-key-"

	// AuthServerHMACVolumePrefix is the prefix for HMAC secret volume names
	AuthServerHMACVolumePrefix = "authserver-hmac-secret-"

	// RedisTLSCACertVolumePrefix is the prefix for Redis TLS CA cert volume names
	RedisTLSCACertVolumePrefix = "redis-tls-ca-"

	// RedisTLSCACertMountPath is the base path where Redis TLS CA certs are mounted
	RedisTLSCACertMountPath = "/etc/toolhive/authserver/redis-tls"

	// RedisTLSCACertFileName is the filename for the master CA cert
	RedisTLSCACertFileName = "ca.crt"

	// RedisSentinelTLSCACertFileName is the filename for the sentinel CA cert
	RedisSentinelTLSCACertFileName = "sentinel-ca.crt"

	// AuthServerKeysMountPath is the base path where signing keys are mounted
	AuthServerKeysMountPath = "/etc/toolhive/authserver/keys"

	// AuthServerHMACMountPath is the base path where HMAC secrets are mounted
	AuthServerHMACMountPath = "/etc/toolhive/authserver/hmac"

	// AuthServerKeyFilePattern is the pattern for signing key filenames
	AuthServerKeyFilePattern = "key-%d.pem"

	// AuthServerHMACFilePattern is the pattern for HMAC secret filenames
	AuthServerHMACFilePattern = "hmac-%d"

	// UpstreamClientSecretEnvVar is the prefix for upstream client secret environment variables.
	// Actual names are TOOLHIVE_UPSTREAM_CLIENT_SECRET_<PROVIDER> where PROVIDER is the
	// upstream name uppercased with hyphens replaced by underscores.
	// #nosec G101 -- This is an environment variable name, not a hardcoded credential
	UpstreamClientSecretEnvVar = "TOOLHIVE_UPSTREAM_CLIENT_SECRET"

	// DefaultSentinelPort is the default Redis Sentinel port
	DefaultSentinelPort = 26379
)

// upstreamSecretBinding binds an upstream provider to its env var name for the
// client secret. Both GenerateAuthServerEnvVars (Pod env) and
// buildUpstreamRunConfig (runtime config) MUST use these bindings so the
// env var names stay consistent.
type upstreamSecretBinding struct {
	Provider   *mcpv1alpha1.UpstreamProviderConfig
	EnvVarName string
}

// buildUpstreamSecretBindings computes the canonical env var name for each
// upstream provider's client secret. The env var name is derived from the
// provider's Name field (uppercased, hyphens replaced with underscores) to
// keep bindings stable across provider reordering in the CRD.
func buildUpstreamSecretBindings(
	providers []mcpv1alpha1.UpstreamProviderConfig,
) []upstreamSecretBinding {
	bindings := make([]upstreamSecretBinding, len(providers))
	for i := range providers {
		suffix := strings.ToUpper(strings.ReplaceAll(providers[i].Name, "-", "_"))
		bindings[i] = upstreamSecretBinding{
			Provider:   &providers[i],
			EnvVarName: fmt.Sprintf("%s_%s", UpstreamClientSecretEnvVar, suffix),
		}
	}
	return bindings
}

// EmbeddedAuthServerConfigName returns the config name that should be used for
// embedded auth server volume/env generation, or empty string if neither ref applies.
// AuthServerRef takes precedence; externalAuthConfigRef is used as a fallback.
func EmbeddedAuthServerConfigName(
	extAuthRef *mcpv1alpha1.ExternalAuthConfigRef,
	authServerRef *mcpv1alpha1.AuthServerRef,
) string {
	if authServerRef != nil {
		return authServerRef.Name
	}
	if extAuthRef != nil {
		return extAuthRef.Name
	}
	return ""
}

// GenerateAuthServerConfigByName fetches an MCPExternalAuthConfig by name and, if its type
// is embeddedAuthServer, returns the corresponding volumes, volume mounts, and env vars.
// Returns empty slices (no error) if the config type is not embeddedAuthServer, because
// this function may be called via the externalAuthConfigRef fallback path where non-embedded
// types (headerInjection, tokenExchange, etc.) are valid — they simply don't need auth
// server volumes. Type validation for the authServerRef path is handled earlier by
// handleAuthServerRef which sets an InvalidType condition.
func GenerateAuthServerConfigByName(
	ctx context.Context,
	c client.Client,
	namespace string,
	configName string,
) ([]corev1.Volume, []corev1.VolumeMount, []corev1.EnvVar, error) {
	externalAuthConfig, err := GetExternalAuthConfigByName(ctx, c, namespace, configName)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to get MCPExternalAuthConfig: %w", err)
	}

	if externalAuthConfig.Spec.Type != mcpv1alpha1.ExternalAuthTypeEmbeddedAuthServer {
		return nil, nil, nil, nil
	}

	authServerConfig := externalAuthConfig.Spec.EmbeddedAuthServer
	if authServerConfig == nil {
		return nil, nil, nil, fmt.Errorf("embedded auth server configuration is nil for type embeddedAuthServer")
	}

	volumes, volumeMounts := GenerateAuthServerVolumes(authServerConfig)
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

	// Generate volumes for Redis TLS CA certificates
	if authConfig.Storage != nil && authConfig.Storage.Redis != nil {
		redis := authConfig.Storage.Redis
		if redis.TLS != nil && redis.TLS.CACertSecretRef != nil {
			ref := redis.TLS.CACertSecretRef
			volumeName := RedisTLSCACertVolumePrefix + "master"
			volumes = append(volumes, corev1.Volume{
				Name: volumeName,
				VolumeSource: corev1.VolumeSource{
					Secret: &corev1.SecretVolumeSource{
						SecretName: ref.Name,
						Items: []corev1.KeyToPath{{
							Key:  ref.Key,
							Path: RedisTLSCACertFileName,
						}},
						DefaultMode: k8sptr.To(int32(0400)),
					},
				},
			})
			volumeMounts = append(volumeMounts, corev1.VolumeMount{
				Name:      volumeName,
				MountPath: fmt.Sprintf("%s/%s", RedisTLSCACertMountPath, RedisTLSCACertFileName),
				SubPath:   RedisTLSCACertFileName,
				ReadOnly:  true,
			})
		}
		if redis.SentinelTLS != nil && redis.SentinelTLS.CACertSecretRef != nil {
			ref := redis.SentinelTLS.CACertSecretRef
			volumeName := RedisTLSCACertVolumePrefix + "sentinel"
			volumes = append(volumes, corev1.Volume{
				Name: volumeName,
				VolumeSource: corev1.VolumeSource{
					Secret: &corev1.SecretVolumeSource{
						SecretName: ref.Name,
						Items: []corev1.KeyToPath{{
							Key:  ref.Key,
							Path: RedisSentinelTLSCACertFileName,
						}},
						DefaultMode: k8sptr.To(int32(0400)),
					},
				},
			})
			volumeMounts = append(volumeMounts, corev1.VolumeMount{
				Name:      volumeName,
				MountPath: fmt.Sprintf("%s/%s", RedisTLSCACertMountPath, RedisSentinelTLSCACertFileName),
				SubPath:   RedisSentinelTLSCACertFileName,
				ReadOnly:  true,
			})
		}
	}

	return volumes, volumeMounts
}

// GenerateAuthServerEnvVars creates environment variables for embedded auth server.
// Generates TOOLHIVE_UPSTREAM_CLIENT_SECRET_<PROVIDER> env vars for each upstream
// provider that has a client secret reference configured, where PROVIDER is the
// provider name uppercased with hyphens replaced by underscores.
//
// Returns nil slice if authConfig is nil or if no client secrets are configured.
func GenerateAuthServerEnvVars(
	authConfig *mcpv1alpha1.EmbeddedAuthServerConfig,
) []corev1.EnvVar {
	if authConfig == nil {
		return nil
	}

	var envVars []corev1.EnvVar

	// Generate env vars for upstream client secrets using shared bindings
	for _, b := range buildUpstreamSecretBindings(authConfig.UpstreamProviders) {
		// Extract client secret reference based on provider type
		var clientSecretRef *mcpv1alpha1.SecretKeyRef

		switch b.Provider.Type {
		case mcpv1alpha1.UpstreamProviderTypeOIDC:
			if b.Provider.OIDCConfig != nil {
				clientSecretRef = b.Provider.OIDCConfig.ClientSecretRef
			}
		case mcpv1alpha1.UpstreamProviderTypeOAuth2:
			if b.Provider.OAuth2Config != nil {
				clientSecretRef = b.Provider.OAuth2Config.ClientSecretRef
			}
		}

		if clientSecretRef != nil {
			envVars = append(envVars, corev1.EnvVar{
				Name: b.EnvVarName,
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
	}

	// Generate env vars for Redis ACL credentials if configured
	if authConfig.Storage != nil &&
		authConfig.Storage.Type == mcpv1alpha1.AuthServerStorageTypeRedis &&
		authConfig.Storage.Redis != nil &&
		authConfig.Storage.Redis.ACLUserConfig != nil {
		aclConfig := authConfig.Storage.Redis.ACLUserConfig

		if aclConfig.UsernameSecretRef != nil {
			envVars = append(envVars, corev1.EnvVar{
				Name: authrunner.RedisUsernameEnvVar,
				ValueFrom: &corev1.EnvVarSource{
					SecretKeyRef: &corev1.SecretKeySelector{
						LocalObjectReference: corev1.LocalObjectReference{
							Name: aclConfig.UsernameSecretRef.Name,
						},
						Key: aclConfig.UsernameSecretRef.Key,
					},
				},
			})
		}

		if aclConfig.PasswordSecretRef != nil {
			envVars = append(envVars, corev1.EnvVar{
				Name: authrunner.RedisPasswordEnvVar,
				ValueFrom: &corev1.EnvVarSource{
					SecretKeyRef: &corev1.SecretKeySelector{
						LocalObjectReference: corev1.LocalObjectReference{
							Name: aclConfig.PasswordSecretRef.Name,
						},
						Key: aclConfig.PasswordSecretRef.Key,
					},
				},
			})
		}
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
// 3. Validates that oidcConfig is provided with ResourceURL (required for RFC 8707 compliance)
// 4. Adds the appropriate runner options for embedded auth server configuration
//
// The oidcConfig parameter provides:
//   - AllowedAudiences: from oidcConfig.ResourceURL (REQUIRED)
//   - ScopesSupported: from oidcConfig.Scopes (optional, defaults to ["openid", "offline_access"])
//
// Returns nil if externalAuthConfigRef is nil or if the auth type is not embeddedAuthServer.
// Returns error if oidcConfig is nil or oidcConfig.ResourceURL is empty when using embedded auth server.
func AddEmbeddedAuthServerConfigOptions(
	ctx context.Context,
	c client.Client,
	namespace string,
	mcpServerName string,
	externalAuthConfigRef *mcpv1alpha1.ExternalAuthConfigRef,
	oidcConfig *oidc.OIDCConfig,
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

	// Validate OIDC config is provided with ResourceURL (required for embedded auth server)
	if oidcConfig == nil {
		return fmt.Errorf("OIDC config is required for embedded auth server: OIDCConfigRef must be set on the MCPServer")
	}
	if oidcConfig.ResourceURL == "" {
		return fmt.Errorf("OIDC config resourceUrl is required for embedded auth server: set resourceUrl in OIDCConfigRef")
	}

	// Build the embedded auth server config for runner
	embeddedConfig, err := BuildAuthServerRunConfig(
		namespace, mcpServerName, authServerConfig,
		[]string{oidcConfig.ResourceURL}, oidcConfig.Scopes,
	)
	if err != nil {
		return fmt.Errorf("failed to build embedded auth server config: %w", err)
	}

	// Add the configuration option
	*options = append(*options, runner.WithEmbeddedAuthServerConfig(embeddedConfig))

	return nil
}

// BuildAuthServerRunConfig converts CRD EmbeddedAuthServerConfig to authserver.RunConfig.
// The RunConfig is serializable and contains file paths for secrets (not the secrets themselves).
//
// AllowedAudiences and ScopesSupported are caller-provided because different controllers
// derive them from different sources (MCPServer uses oidcConfig.ResourceURL/Scopes;
// VirtualMCPServer derives from the resolved vmcp Config).
func BuildAuthServerRunConfig(
	namespace string,
	name string,
	authConfig *mcpv1alpha1.EmbeddedAuthServerConfig,
	allowedAudiences []string,
	scopesSupported []string,
) (*authserver.RunConfig, error) {
	config := &authserver.RunConfig{
		SchemaVersion:                authserver.CurrentSchemaVersion,
		Issuer:                       authConfig.Issuer,
		AuthorizationEndpointBaseURL: authConfig.AuthorizationEndpointBaseURL,
		AllowedAudiences:             allowedAudiences,
		ScopesSupported:              scopesSupported,
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

	// Build upstream provider configs using shared bindings
	bindings := buildUpstreamSecretBindings(authConfig.UpstreamProviders)
	config.Upstreams = make([]authserver.UpstreamRunConfig, 0, len(bindings))
	for _, b := range bindings {
		config.Upstreams = append(config.Upstreams, *buildUpstreamRunConfig(b.Provider, b.EnvVarName))
	}

	// Build storage configuration
	storageCfg, err := buildStorageRunConfig(namespace, name, authConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to build storage config: %w", err)
	}
	config.Storage = storageCfg

	return config, nil
}

// buildStorageRunConfig converts CRD AuthServerStorageConfig to storage.RunConfig.
// Returns nil (memory storage default) if no storage config is specified.
func buildStorageRunConfig(
	namespace string,
	mcpServerName string,
	authConfig *mcpv1alpha1.EmbeddedAuthServerConfig,
) (*storage.RunConfig, error) {
	if authConfig.Storage == nil || authConfig.Storage.Type == mcpv1alpha1.AuthServerStorageTypeMemory {
		return nil, nil
	}

	if authConfig.Storage.Type != mcpv1alpha1.AuthServerStorageTypeRedis {
		return nil, fmt.Errorf("unsupported storage type: %s", authConfig.Storage.Type)
	}

	redisConfig := authConfig.Storage.Redis
	if redisConfig == nil {
		return nil, fmt.Errorf("redis config is required when storage type is redis")
	}

	if redisConfig.SentinelConfig == nil {
		return nil, fmt.Errorf("sentinel config is required for Redis storage")
	}

	if redisConfig.ACLUserConfig == nil ||
		redisConfig.ACLUserConfig.UsernameSecretRef == nil ||
		redisConfig.ACLUserConfig.PasswordSecretRef == nil {
		return nil, fmt.Errorf("ACL user config is required for Redis storage")
	}

	// Resolve Sentinel addresses (static or via Kubernetes Service discovery)
	sentinelAddrs, err := resolveSentinelAddrs(redisConfig.SentinelConfig, namespace)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve sentinel addresses: %w", err)
	}

	// Build key prefix for multi-tenancy using namespace and MCP server name
	keyPrefix := storage.DeriveKeyPrefix(namespace, mcpServerName)

	return &storage.RunConfig{
		Type: string(storage.TypeRedis),
		RedisConfig: &storage.RedisRunConfig{
			SentinelConfig: &storage.SentinelRunConfig{
				MasterName:    redisConfig.SentinelConfig.MasterName,
				SentinelAddrs: sentinelAddrs,
				DB:            int(redisConfig.SentinelConfig.DB),
			},
			AuthType: storage.AuthTypeACLUser,
			ACLUserConfig: &storage.ACLUserRunConfig{
				UsernameEnvVar: authrunner.RedisUsernameEnvVar,
				PasswordEnvVar: authrunner.RedisPasswordEnvVar,
			},
			KeyPrefix:    keyPrefix,
			DialTimeout:  redisConfig.DialTimeout,
			ReadTimeout:  redisConfig.ReadTimeout,
			WriteTimeout: redisConfig.WriteTimeout,
			TLS:          convertRedisTLSConfig(redisConfig.TLS, false),
			SentinelTLS:  convertRedisTLSConfig(redisConfig.SentinelTLS, true),
		},
	}, nil
}

// convertRedisTLSConfig converts CRD RedisTLSConfig to RunConfig.
// isSentinel determines which mount path to use for the CA cert file.
func convertRedisTLSConfig(cfg *mcpv1alpha1.RedisTLSConfig, isSentinel bool) *storage.RedisTLSRunConfig {
	if cfg == nil {
		return nil
	}
	rc := &storage.RedisTLSRunConfig{
		InsecureSkipVerify: cfg.InsecureSkipVerify,
	}
	if cfg.CACertSecretRef != nil {
		fileName := RedisTLSCACertFileName
		if isSentinel {
			fileName = RedisSentinelTLSCACertFileName
		}
		rc.CACertFile = fmt.Sprintf("%s/%s", RedisTLSCACertMountPath, fileName)
	}
	return rc
}

// resolveSentinelAddrs resolves Sentinel addresses from static config or Kubernetes Service DNS.
func resolveSentinelAddrs(
	sentinelConfig *mcpv1alpha1.RedisSentinelConfig,
	defaultNamespace string,
) ([]string, error) {
	// If static addresses are provided, use them directly
	if len(sentinelConfig.SentinelAddrs) > 0 {
		return sentinelConfig.SentinelAddrs, nil
	}

	// Otherwise, construct the Kubernetes Service DNS name.
	// go-redis tries all sentinel addresses in parallel and auto-discovers
	// other sentinels via the SENTINEL SENTINELS command after connecting,
	// so a single DNS name is sufficient.
	if sentinelConfig.SentinelService == nil {
		return nil, fmt.Errorf("either sentinelAddrs or sentinelService must be specified")
	}

	svc := sentinelConfig.SentinelService
	namespace := svc.Namespace
	if namespace == "" {
		namespace = defaultNamespace
	}
	port := svc.Port
	if port == 0 {
		port = DefaultSentinelPort
	}

	dnsName := fmt.Sprintf("%s.%s.svc.cluster.local:%d", svc.Name, namespace, port)
	return []string{dnsName}, nil
}

// buildUpstreamRunConfig converts CRD UpstreamProviderConfig to authserver.UpstreamRunConfig.
// The envVarName is computed by buildUpstreamSecretBindings to keep Pod env
// and runtime config in sync.
func buildUpstreamRunConfig(
	provider *mcpv1alpha1.UpstreamProviderConfig,
	envVarName string,
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
				config.OIDCConfig.ClientSecretEnvVar = envVarName
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
				config.OAuth2Config.ClientSecretEnvVar = envVarName
			}
			if provider.OAuth2Config.UserInfo != nil {
				config.OAuth2Config.UserInfo = buildUserInfoRunConfig(provider.OAuth2Config.UserInfo)
			}
			if provider.OAuth2Config.TokenResponseMapping != nil {
				m := provider.OAuth2Config.TokenResponseMapping
				config.OAuth2Config.TokenResponseMapping = &authserver.TokenResponseMappingRunConfig{
					AccessTokenPath:  m.AccessTokenPath,
					ScopePath:        m.ScopePath,
					RefreshTokenPath: m.RefreshTokenPath,
					ExpiresInPath:    m.ExpiresInPath,
				}
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

// ValidateAndAddAuthServerRefOptions performs conflict validation between authServerRef
// and externalAuthConfigRef, then resolves authServerRef if present.
// Returns error if both fields point to an embedded auth server configuration.
func ValidateAndAddAuthServerRefOptions(
	ctx context.Context,
	c client.Client,
	namespace string,
	mcpServerName string,
	authServerRef *mcpv1alpha1.AuthServerRef,
	externalAuthConfigRef *mcpv1alpha1.ExternalAuthConfigRef,
	oidcConfig *oidc.OIDCConfig,
	options *[]runner.RunConfigBuilderOption,
) error {
	// Conflict validation: both authServerRef and externalAuthConfigRef pointing to
	// embedded auth server is an error (use one or the other, not both)
	if authServerRef != nil && externalAuthConfigRef != nil {
		extConfig, err := GetExternalAuthConfigByName(ctx, c, namespace, externalAuthConfigRef.Name)
		if err != nil {
			if !apierrors.IsNotFound(err) {
				return fmt.Errorf("failed to fetch externalAuthConfigRef for conflict validation: %w", err)
			}
			// Not found - skip conflict check, will be caught by AddExternalAuthConfigOptions
		} else if extConfig.Spec.Type == mcpv1alpha1.ExternalAuthTypeEmbeddedAuthServer {
			return fmt.Errorf(
				"conflict: both authServerRef and externalAuthConfigRef reference an embedded auth server; " +
					"use authServerRef for the embedded auth server and externalAuthConfigRef for outgoing auth only",
			)
		}
	}

	// Add auth server ref configuration if specified
	return AddAuthServerRefOptions(ctx, c, namespace, mcpServerName, authServerRef, oidcConfig, options)
}

// AddAuthServerRefOptions resolves an authServerRef (TypedLocalObjectReference),
// validates the kind and type, and appends the corresponding RunConfigBuilderOption.
// Returns nil if authServerRef is nil (no-op).
// Returns error if the kind is not MCPExternalAuthConfig, the type is not embeddedAuthServer,
// or if fetching or building the config fails.
func AddAuthServerRefOptions(
	ctx context.Context,
	c client.Client,
	namespace string,
	mcpServerName string,
	authServerRef *mcpv1alpha1.AuthServerRef,
	oidcConfig *oidc.OIDCConfig,
	options *[]runner.RunConfigBuilderOption,
) error {
	if authServerRef == nil {
		return nil
	}

	// Validate the Kind
	if authServerRef.Kind != "MCPExternalAuthConfig" {
		return fmt.Errorf("unsupported authServerRef kind %q: only MCPExternalAuthConfig is supported", authServerRef.Kind)
	}

	// Fetch the MCPExternalAuthConfig
	externalAuthConfig, err := GetExternalAuthConfigByName(ctx, c, namespace, authServerRef.Name)
	if err != nil {
		return fmt.Errorf("failed to get MCPExternalAuthConfig for authServerRef: %w", err)
	}

	// Validate the type is embeddedAuthServer
	if externalAuthConfig.Spec.Type != mcpv1alpha1.ExternalAuthTypeEmbeddedAuthServer {
		return fmt.Errorf(
			"authServerRef must reference a MCPExternalAuthConfig with type %q, got %q",
			mcpv1alpha1.ExternalAuthTypeEmbeddedAuthServer, externalAuthConfig.Spec.Type,
		)
	}

	authServerConfig := externalAuthConfig.Spec.EmbeddedAuthServer
	if authServerConfig == nil {
		return fmt.Errorf("embedded auth server configuration is nil for type embeddedAuthServer")
	}

	// Validate OIDC config is provided with ResourceURL (required for embedded auth server)
	if oidcConfig == nil {
		return fmt.Errorf("OIDC config is required for embedded auth server: OIDCConfigRef must be set on the MCPServer")
	}
	if oidcConfig.ResourceURL == "" {
		return fmt.Errorf("OIDC config resourceUrl is required for embedded auth server: set resourceUrl in OIDCConfigRef")
	}

	// Build the embedded auth server config for runner
	embeddedConfig, err := BuildAuthServerRunConfig(
		namespace, mcpServerName, authServerConfig,
		[]string{oidcConfig.ResourceURL}, oidcConfig.Scopes,
	)
	if err != nil {
		return fmt.Errorf("failed to build embedded auth server config: %w", err)
	}

	// Add the configuration option
	*options = append(*options, runner.WithEmbeddedAuthServerConfig(embeddedConfig))

	return nil
}

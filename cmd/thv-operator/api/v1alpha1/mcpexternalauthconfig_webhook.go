// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package v1alpha1

import (
	"context"
	"fmt"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

// AWS STS session duration bounds (match AWS STS limits).
const (
	minSessionDuration int32 = 900   // 15 minutes
	maxSessionDuration int32 = 43200 // 12 hours
)

// SetupWebhookWithManager sets up the webhook with the Manager
func (r *MCPExternalAuthConfig) SetupWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr).
		For(r).
		Complete()
}

//nolint:lll // kubebuilder webhook marker cannot be split
// +kubebuilder:webhook:path=/validate-toolhive-stacklok-dev-v1alpha1-mcpexternalauthconfig,mutating=false,failurePolicy=fail,sideEffects=None,groups=toolhive.stacklok.dev,resources=mcpexternalauthconfigs,verbs=create;update,versions=v1alpha1,name=vmcpexternalauthconfig.kb.io,admissionReviewVersions=v1

var _ webhook.CustomValidator = &MCPExternalAuthConfig{}

// ValidateCreate implements webhook.CustomValidator
func (r *MCPExternalAuthConfig) ValidateCreate(_ context.Context, _ runtime.Object) (admission.Warnings, error) {
	var warnings admission.Warnings
	if r.Spec.Type == ExternalAuthTypeUnauthenticated {
		warnings = append(warnings,
			"'unauthenticated' type disables authentication to the backend. "+
				"Only use for backends on trusted networks or when authentication is handled by network-level security.")
	}
	return warnings, r.validate()
}

// ValidateUpdate implements webhook.CustomValidator
func (r *MCPExternalAuthConfig) ValidateUpdate(
	_ context.Context, _ runtime.Object, _ runtime.Object,
) (admission.Warnings, error) {
	var warnings admission.Warnings
	if r.Spec.Type == ExternalAuthTypeUnauthenticated {
		warnings = append(warnings,
			"'unauthenticated' type disables authentication to the backend. "+
				"Only use for backends on trusted networks or when authentication is handled by network-level security.")
	}
	return warnings, r.validate()
}

// ValidateDelete implements webhook.CustomValidator
func (*MCPExternalAuthConfig) ValidateDelete(_ context.Context, _ runtime.Object) (admission.Warnings, error) {
	// No validation needed for deletion
	return nil, nil
}

// validate performs validation on the MCPExternalAuthConfig spec
func (r *MCPExternalAuthConfig) validate() error {
	// Validate type-specific configuration presence
	if err := r.validateConfigPresence(); err != nil {
		return err
	}

	// Delegate to type-specific validation
	return r.validateTypeSpecific()
}

// validateConfigPresence ensures the correct configuration is set for the selected type
func (r *MCPExternalAuthConfig) validateConfigPresence() error {
	if (r.Spec.TokenExchange == nil) == (r.Spec.Type == ExternalAuthTypeTokenExchange) {
		return fmt.Errorf("tokenExchange configuration must be set if and only if type is 'tokenExchange'")
	}
	if (r.Spec.HeaderInjection == nil) == (r.Spec.Type == ExternalAuthTypeHeaderInjection) {
		return fmt.Errorf("headerInjection configuration must be set if and only if type is 'headerInjection'")
	}
	if (r.Spec.BearerToken == nil) == (r.Spec.Type == ExternalAuthTypeBearerToken) {
		return fmt.Errorf("bearerToken configuration must be set if and only if type is 'bearerToken'")
	}
	if (r.Spec.EmbeddedAuthServer == nil) == (r.Spec.Type == ExternalAuthTypeEmbeddedAuthServer) {
		return fmt.Errorf("embeddedAuthServer configuration must be set if and only if type is 'embeddedAuthServer'")
	}
	if (r.Spec.AWSSts == nil) == (r.Spec.Type == ExternalAuthTypeAWSSts) {
		return fmt.Errorf("awsSts configuration must be set if and only if type is 'awsSts'")
	}
	if r.Spec.Type == ExternalAuthTypeUnauthenticated {
		if r.Spec.TokenExchange != nil ||
			r.Spec.HeaderInjection != nil ||
			r.Spec.BearerToken != nil ||
			r.Spec.EmbeddedAuthServer != nil ||
			r.Spec.AWSSts != nil {
			return fmt.Errorf("no configuration must be set when type is 'unauthenticated'")
		}
	}
	return nil
}

// validateTypeSpecific delegates to the appropriate type-specific validation
func (r *MCPExternalAuthConfig) validateTypeSpecific() error {
	switch r.Spec.Type {
	case ExternalAuthTypeTokenExchange:
		return r.validateTokenExchange()
	case ExternalAuthTypeHeaderInjection:
		return r.validateHeaderInjection()
	case ExternalAuthTypeBearerToken:
		return r.validateBearerToken()
	case ExternalAuthTypeUnauthenticated:
		return r.validateUnauthenticated()
	case ExternalAuthTypeEmbeddedAuthServer:
		return r.validateEmbeddedAuthServer()
	case ExternalAuthTypeAWSSts:
		return r.validateAWSSts()
	default:
		return fmt.Errorf("unsupported auth type: %s", r.Spec.Type)
	}
}

// validateTokenExchange validates tokenExchange type configuration
func (*MCPExternalAuthConfig) validateTokenExchange() error {
	return nil
}

// validateHeaderInjection validates headerInjection type configuration
func (*MCPExternalAuthConfig) validateHeaderInjection() error {
	return nil
}

// validateBearerToken validates bearerToken type configuration
func (*MCPExternalAuthConfig) validateBearerToken() error {
	return nil
}

// validateUnauthenticated validates unauthenticated type configuration
func (*MCPExternalAuthConfig) validateUnauthenticated() error {
	return nil
}

// validateEmbeddedAuthServer validates embeddedAuthServer type configuration
func (r *MCPExternalAuthConfig) validateEmbeddedAuthServer() error {
	// Validate upstream providers
	cfg := r.Spec.EmbeddedAuthServer
	if cfg == nil {
		return nil
	}

	// Note: MinItems=1 is enforced by kubebuilder markers,
	// but we add runtime validation for clarity and future-proofing
	if len(cfg.UpstreamProviders) == 0 {
		return fmt.Errorf("at least one upstream provider is required")
	}
	// Note: we add runtime validation for 'max items = 1' here since multi-provider support is not yet implemented.
	if len(cfg.UpstreamProviders) > 1 {
		return fmt.Errorf("currently only one upstream provider is supported (found %d)", len(cfg.UpstreamProviders))
	}

	for i, provider := range cfg.UpstreamProviders {
		if err := r.validateUpstreamProvider(i, &provider); err != nil {
			return err
		}
	}

	if cfg.Storage != nil {
		if err := validateStorageConfig(cfg.Storage); err != nil {
			return err
		}
	}

	return nil
}

// validateUpstreamProvider validates a single upstream provider configuration
func (*MCPExternalAuthConfig) validateUpstreamProvider(index int, provider *UpstreamProviderConfig) error {
	prefix := fmt.Sprintf("upstreamProviders[%d]", index)

	if (provider.OIDCConfig == nil) == (provider.Type == UpstreamProviderTypeOIDC) {
		return fmt.Errorf("%s: oidcConfig must be set when type is 'oidc' and must not be set otherwise", prefix)
	}
	if (provider.OAuth2Config == nil) == (provider.Type == UpstreamProviderTypeOAuth2) {
		return fmt.Errorf("%s: oauth2Config must be set when type is 'oauth2' and must not be set otherwise", prefix)
	}
	if provider.Type != UpstreamProviderTypeOIDC && provider.Type != UpstreamProviderTypeOAuth2 {
		return fmt.Errorf("%s: unsupported provider type: %s", prefix, provider.Type)
	}

	return nil
}

// validateAWSSts validates awsSts type configuration
func (r *MCPExternalAuthConfig) validateAWSSts() error {
	cfg := r.Spec.AWSSts
	if cfg == nil {
		return nil
	}

	// Region is required
	if cfg.Region == "" {
		return fmt.Errorf("awsSts.region is required")
	}

	// At least one of fallbackRoleArn or roleMappings must be configured
	// Both can be set: fallbackRoleArn is used when no mapping matches
	hasRoleArn := cfg.FallbackRoleArn != ""
	hasRoleMappings := len(cfg.RoleMappings) > 0

	if !hasRoleArn && !hasRoleMappings {
		return fmt.Errorf("awsSts: at least one of fallbackRoleArn or roleMappings must be configured")
	}

	// Validate role mappings if present
	for i, mapping := range cfg.RoleMappings {
		if mapping.RoleArn == "" {
			return fmt.Errorf("awsSts.roleMappings[%d].roleArn is required", i)
		}
		// Exactly one of claim or matcher must be set
		if mapping.Claim == "" && mapping.Matcher == "" {
			return fmt.Errorf("awsSts.roleMappings[%d]: exactly one of claim or matcher must be set", i)
		}
		if mapping.Claim != "" && mapping.Matcher != "" {
			return fmt.Errorf("awsSts.roleMappings[%d]: claim and matcher are mutually exclusive", i)
		}
	}

	// Validate session duration if set
	// Bounds match AWS STS limits: 900s (15 min) to 43200s (12 hours)
	if cfg.SessionDuration != nil {
		duration := *cfg.SessionDuration
		if duration < minSessionDuration || duration > maxSessionDuration {
			return fmt.Errorf("awsSts.sessionDuration must be between %d and %d seconds",
				minSessionDuration, maxSessionDuration)
		}
	}

	return nil
}

// validateStorageConfig validates the auth server storage configuration
func validateStorageConfig(cfg *AuthServerStorageConfig) error {
	switch cfg.Type {
	case AuthServerStorageTypeMemory, "":
		// Memory storage requires no additional configuration
		if cfg.Redis != nil {
			return fmt.Errorf("storage: redis configuration must not be set when type is 'memory'")
		}
		return nil
	case AuthServerStorageTypeRedis:
		if cfg.Redis == nil {
			return fmt.Errorf("storage: redis configuration is required when type is 'redis'")
		}
		return validateRedisStorageConfig(cfg.Redis)
	default:
		return fmt.Errorf("storage: unsupported storage type: %s", cfg.Type)
	}
}

// validateRedisStorageConfig validates the Redis storage configuration
func validateRedisStorageConfig(cfg *RedisStorageConfig) error {
	if cfg.SentinelConfig == nil {
		return fmt.Errorf("storage.redis: sentinelConfig is required")
	}

	if err := validateRedisSentinelConfig(cfg.SentinelConfig); err != nil {
		return err
	}

	if cfg.ACLUserConfig == nil {
		return fmt.Errorf("storage.redis: aclUserConfig is required")
	}

	if err := validateRedisACLUserConfig(cfg.ACLUserConfig); err != nil {
		return err
	}

	// Validate timeout durations
	if cfg.DialTimeout != "" {
		if _, err := time.ParseDuration(cfg.DialTimeout); err != nil {
			return fmt.Errorf("storage.redis: invalid dialTimeout %q: %w", cfg.DialTimeout, err)
		}
	}
	if cfg.ReadTimeout != "" {
		if _, err := time.ParseDuration(cfg.ReadTimeout); err != nil {
			return fmt.Errorf("storage.redis: invalid readTimeout %q: %w", cfg.ReadTimeout, err)
		}
	}
	if cfg.WriteTimeout != "" {
		if _, err := time.ParseDuration(cfg.WriteTimeout); err != nil {
			return fmt.Errorf("storage.redis: invalid writeTimeout %q: %w", cfg.WriteTimeout, err)
		}
	}

	return nil
}

// validateRedisSentinelConfig validates the Redis Sentinel configuration
func validateRedisSentinelConfig(cfg *RedisSentinelConfig) error {
	if cfg.MasterName == "" {
		return fmt.Errorf("storage.redis.sentinelConfig: masterName is required")
	}

	hasSentinelAddrs := len(cfg.SentinelAddrs) > 0
	hasSentinelService := cfg.SentinelService != nil

	if hasSentinelAddrs && hasSentinelService {
		return fmt.Errorf("storage.redis.sentinelConfig: exactly one of sentinelAddrs or sentinelService must be specified, not both")
	}
	if !hasSentinelAddrs && !hasSentinelService {
		return fmt.Errorf("storage.redis.sentinelConfig: exactly one of sentinelAddrs or sentinelService must be specified")
	}

	if hasSentinelService && cfg.SentinelService.Name == "" {
		return fmt.Errorf("storage.redis.sentinelConfig.sentinelService: name is required")
	}

	return nil
}

// validateRedisACLUserConfig validates the Redis ACL user configuration
func validateRedisACLUserConfig(cfg *RedisACLUserConfig) error {
	if cfg.UsernameSecretRef == nil {
		return fmt.Errorf("storage.redis.aclUserConfig: usernameSecretRef is required")
	}
	if cfg.PasswordSecretRef == nil {
		return fmt.Errorf("storage.redis.aclUserConfig: passwordSecretRef is required")
	}
	return nil
}

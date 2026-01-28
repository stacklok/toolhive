// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package v1alpha1

import (
	"context"
	"fmt"

	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

// SetupWebhookWithManager sets up the webhook with the Manager
func (r *MCPExternalAuthConfig) SetupWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr).
		For(r).
		Complete()
}

//nolint:lll // kubebuilder webhook marker cannot be split
// +kubebuilder:webhook:path=/validate-toolhive-stacklok-com-v1alpha1-mcpexternalauthconfig,mutating=false,failurePolicy=fail,sideEffects=None,groups=toolhive.stacklok.com,resources=mcpexternalauthconfigs,verbs=create;update,versions=v1alpha1,name=vmcpexternalauthconfig.kb.io,admissionReviewVersions=v1

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
	default:
		return fmt.Errorf("unsupported auth type: %s", r.Spec.Type)
	}
}

// validateTokenExchange validates tokenExchange type configuration
func (r *MCPExternalAuthConfig) validateTokenExchange() error {
	if r.Spec.TokenExchange == nil {
		return fmt.Errorf("tokenExchange configuration is required when type is 'tokenExchange'")
	}
	if r.Spec.HeaderInjection != nil {
		return fmt.Errorf("headerInjection must not be set when type is 'tokenExchange'")
	}
	if r.Spec.BearerToken != nil {
		return fmt.Errorf("bearerToken must not be set when type is 'tokenExchange'")
	}
	if r.Spec.EmbeddedAuthServer != nil {
		return fmt.Errorf("embeddedAuthServer must not be set when type is 'tokenExchange'")
	}
	return nil
}

// validateHeaderInjection validates headerInjection type configuration
func (r *MCPExternalAuthConfig) validateHeaderInjection() error {
	if r.Spec.HeaderInjection == nil {
		return fmt.Errorf("headerInjection configuration is required when type is 'headerInjection'")
	}
	if r.Spec.TokenExchange != nil {
		return fmt.Errorf("tokenExchange must not be set when type is 'headerInjection'")
	}
	if r.Spec.BearerToken != nil {
		return fmt.Errorf("bearerToken must not be set when type is 'headerInjection'")
	}
	if r.Spec.EmbeddedAuthServer != nil {
		return fmt.Errorf("embeddedAuthServer must not be set when type is 'headerInjection'")
	}
	return nil
}

// validateBearerToken validates bearerToken type configuration
func (r *MCPExternalAuthConfig) validateBearerToken() error {
	if r.Spec.BearerToken == nil {
		return fmt.Errorf("bearerToken configuration is required when type is 'bearerToken'")
	}
	if r.Spec.TokenExchange != nil {
		return fmt.Errorf("tokenExchange must not be set when type is 'bearerToken'")
	}
	if r.Spec.HeaderInjection != nil {
		return fmt.Errorf("headerInjection must not be set when type is 'bearerToken'")
	}
	if r.Spec.EmbeddedAuthServer != nil {
		return fmt.Errorf("embeddedAuthServer must not be set when type is 'bearerToken'")
	}
	return nil
}

// validateUnauthenticated validates unauthenticated type configuration
func (r *MCPExternalAuthConfig) validateUnauthenticated() error {
	if r.Spec.TokenExchange != nil {
		return fmt.Errorf("tokenExchange must not be set when type is 'unauthenticated'")
	}
	if r.Spec.HeaderInjection != nil {
		return fmt.Errorf("headerInjection must not be set when type is 'unauthenticated'")
	}
	if r.Spec.BearerToken != nil {
		return fmt.Errorf("bearerToken must not be set when type is 'unauthenticated'")
	}
	if r.Spec.EmbeddedAuthServer != nil {
		return fmt.Errorf("embeddedAuthServer must not be set when type is 'unauthenticated'")
	}
	return nil
}

// validateEmbeddedAuthServer validates embeddedAuthServer type configuration
func (r *MCPExternalAuthConfig) validateEmbeddedAuthServer() error {
	if r.Spec.EmbeddedAuthServer == nil {
		return fmt.Errorf("embeddedAuthServer configuration is required when type is 'embeddedAuthServer'")
	}
	if r.Spec.TokenExchange != nil {
		return fmt.Errorf("tokenExchange must not be set when type is 'embeddedAuthServer'")
	}
	if r.Spec.HeaderInjection != nil {
		return fmt.Errorf("headerInjection must not be set when type is 'embeddedAuthServer'")
	}
	if r.Spec.BearerToken != nil {
		return fmt.Errorf("bearerToken must not be set when type is 'embeddedAuthServer'")
	}

	// Validate upstream providers
	return r.validateUpstreamProviders()
}

// validateUpstreamProviders validates the upstream provider configurations
func (r *MCPExternalAuthConfig) validateUpstreamProviders() error {
	cfg := r.Spec.EmbeddedAuthServer
	if cfg == nil {
		return nil
	}

	// Note: MinItems=1 and MaxItems=1 are enforced by kubebuilder markers,
	// but we add runtime validation for clarity and future-proofing
	if len(cfg.UpstreamProviders) == 0 {
		return fmt.Errorf("at least one upstream provider is required")
	}
	if len(cfg.UpstreamProviders) > 1 {
		return fmt.Errorf("currently only one upstream provider is supported (found %d)", len(cfg.UpstreamProviders))
	}

	for i, provider := range cfg.UpstreamProviders {
		if err := r.validateUpstreamProvider(i, &provider); err != nil {
			return err
		}
	}

	return nil
}

// validateUpstreamProvider validates a single upstream provider configuration
func (*MCPExternalAuthConfig) validateUpstreamProvider(index int, provider *UpstreamProviderConfig) error {
	prefix := fmt.Sprintf("upstreamProviders[%d]", index)

	switch provider.Type {
	case UpstreamProviderTypeOIDC:
		if provider.OIDCConfig == nil {
			return fmt.Errorf("%s: oidcConfig is required when type is 'oidc'", prefix)
		}
		if provider.OAuth2Config != nil {
			return fmt.Errorf("%s: oauth2Config must not be set when type is 'oidc'", prefix)
		}
	case UpstreamProviderTypeOAuth2:
		if provider.OAuth2Config == nil {
			return fmt.Errorf("%s: oauth2Config is required when type is 'oauth2'", prefix)
		}
		if provider.OIDCConfig != nil {
			return fmt.Errorf("%s: oidcConfig must not be set when type is 'oauth2'", prefix)
		}
	default:
		return fmt.Errorf("%s: unsupported provider type: %s", prefix, provider.Type)
	}

	return nil
}

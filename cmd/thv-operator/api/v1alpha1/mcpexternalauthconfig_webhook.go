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
	// Ensure the correct configuration is set for the selected type
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
	if r.Spec.Type == ExternalAuthTypeUnauthenticated {
		if r.Spec.TokenExchange != nil ||
			r.Spec.HeaderInjection != nil ||
			r.Spec.BearerToken != nil ||
			r.Spec.EmbeddedAuthServer != nil {
			return fmt.Errorf("no configuration must be set when type is 'unauthenticated'")
		}
	}

	// Delegate to type-specific validation
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

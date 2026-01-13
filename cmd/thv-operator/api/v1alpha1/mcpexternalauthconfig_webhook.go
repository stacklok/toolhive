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
		if r.Spec.TokenExchange == nil {
			return fmt.Errorf("tokenExchange configuration is required when type is 'tokenExchange'")
		}
		if r.Spec.HeaderInjection != nil {
			return fmt.Errorf("headerInjection must not be set when type is 'tokenExchange'")
		}

	case ExternalAuthTypeHeaderInjection:
		if r.Spec.HeaderInjection == nil {
			return fmt.Errorf("headerInjection configuration is required when type is 'headerInjection'")
		}
		if r.Spec.TokenExchange != nil {
			return fmt.Errorf("tokenExchange must not be set when type is 'headerInjection'")
		}

	case ExternalAuthTypeAWSSts:
		if r.Spec.AWSSts == nil {
			return fmt.Errorf("awsSts configuration is required when type is 'awsSts'")
		}
		if r.Spec.TokenExchange != nil {
			return fmt.Errorf("tokenExchange must not be set when type is 'awsSts'")
		}
		if r.Spec.HeaderInjection != nil {
			return fmt.Errorf("headerInjection must not be set when type is 'awsSts'")
		}

	case ExternalAuthTypeUnauthenticated:
		if r.Spec.TokenExchange != nil {
			return fmt.Errorf("tokenExchange must not be set when type is 'unauthenticated'")
		}
		if r.Spec.HeaderInjection != nil {
			return fmt.Errorf("headerInjection must not be set when type is 'unauthenticated'")
		}

	default:
		return fmt.Errorf("unsupported auth type: %s", r.Spec.Type)
	}

	return nil
}

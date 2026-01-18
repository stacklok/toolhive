package v1alpha1

import (
	"context"

	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	"github.com/stacklok/toolhive/pkg/vmcp/config"
)

// SetupWebhookWithManager registers the webhook with the manager
func (r *VirtualMCPCompositeToolDefinition) SetupWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr).
		For(r).
		Complete()
}

//nolint:lll // kubebuilder webhook marker cannot be split
// +kubebuilder:webhook:path=/validate-toolhive-stacklok-dev-v1alpha1-virtualmcpcompositetooldefinition,mutating=false,failurePolicy=fail,sideEffects=None,groups=toolhive.stacklok.dev,resources=virtualmcpcompositetooldefinitions,verbs=create;update,versions=v1alpha1,name=vvirtualmcpcompositetooldefinition.kb.io,admissionReviewVersions=v1

var _ webhook.CustomValidator = &VirtualMCPCompositeToolDefinition{}

// ValidateCreate implements webhook.CustomValidator
func (r *VirtualMCPCompositeToolDefinition) ValidateCreate(_ context.Context, _ runtime.Object) (admission.Warnings, error) {
	return nil, r.Validate()
}

// ValidateUpdate implements webhook.CustomValidator
//
//nolint:lll // function signature cannot be shortened
func (r *VirtualMCPCompositeToolDefinition) ValidateUpdate(_ context.Context, _ runtime.Object, _ runtime.Object) (admission.Warnings, error) {
	return nil, r.Validate()
}

// ValidateDelete implements webhook.CustomValidator
func (*VirtualMCPCompositeToolDefinition) ValidateDelete(_ context.Context, _ runtime.Object) (admission.Warnings, error) {
	// No validation needed on delete
	return nil, nil
}

// Validate performs validation for VirtualMCPCompositeToolDefinition
// This method can be called by the controller during reconciliation or by the webhook.
// It delegates to the shared ValidateCompositeToolConfig in pkg/vmcp/config.
func (r *VirtualMCPCompositeToolDefinition) Validate() error {
	return config.ValidateCompositeToolConfig("spec", &r.Spec.CompositeToolConfig)
}

// Note: All composite tool validation functions have been moved to
// pkg/vmcp/config/composite_validation.go for shared use across webhooks and runtime.

// GetValidationErrors returns a list of validation errors
// This is a helper method for the controller to populate status.validationErrors
func (r *VirtualMCPCompositeToolDefinition) GetValidationErrors() []string {
	if err := r.Validate(); err != nil {
		return []string{err.Error()}
	}
	return nil
}

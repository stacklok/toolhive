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

	vmcp "github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/config"
)

// SetupWebhookWithManager registers the webhook with the manager
func (r *VirtualMCPServer) SetupWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr).
		For(r).
		Complete()
}

//nolint:lll // kubebuilder webhook marker cannot be split
// +kubebuilder:webhook:path=/validate-toolhive-stacklok-dev-v1alpha1-virtualmcpserver,mutating=false,failurePolicy=fail,sideEffects=None,groups=toolhive.stacklok.dev,resources=virtualmcpservers,verbs=create;update,versions=v1alpha1,name=vvirtualmcpserver.kb.io,admissionReviewVersions=v1

var _ webhook.CustomValidator = &VirtualMCPServer{}

// ValidateCreate implements webhook.CustomValidator
func (r *VirtualMCPServer) ValidateCreate(_ context.Context, _ runtime.Object) (admission.Warnings, error) {
	return nil, r.Validate()
}

// ValidateUpdate implements webhook.CustomValidator
func (r *VirtualMCPServer) ValidateUpdate(_ context.Context, _ runtime.Object, _ runtime.Object) (admission.Warnings, error) {
	return nil, r.Validate()
}

// ValidateDelete implements webhook.CustomValidator
func (*VirtualMCPServer) ValidateDelete(_ context.Context, _ runtime.Object) (admission.Warnings, error) {
	// No validation needed on delete
	return nil, nil
}

// Validate performs validation for VirtualMCPServer
// This method can be called by the controller during reconciliation or by the webhook
func (r *VirtualMCPServer) Validate() error {
	// Validate Group is set (required field)
	if r.Spec.Config.Group == "" {
		return fmt.Errorf("spec.config.groupRef is required")
	}

	// Validate IncomingAuth configuration
	if r.Spec.IncomingAuth != nil {
		if err := r.validateIncomingAuth(); err != nil {
			return err
		}
	}

	// Validate OutgoingAuth configuration
	if r.Spec.OutgoingAuth != nil {
		if err := r.validateOutgoingAuth(); err != nil {
			return err
		}
	}

	// Validate Aggregation configuration
	if r.Spec.Config.Aggregation != nil {
		if err := r.validateAggregation(); err != nil {
			return err
		}
	}

	// Validate CompositeTools
	if len(r.Spec.Config.CompositeTools) > 0 {
		if err := r.validateCompositeTools(); err != nil {
			return err
		}
	}

	return nil
}

// validateIncomingAuth validates IncomingAuth configuration
func (r *VirtualMCPServer) validateIncomingAuth() error {
	auth := r.Spec.IncomingAuth

	// Type is required when IncomingAuth is specified
	if auth.Type == "" {
		return fmt.Errorf("spec.incomingAuth.type is required")
	}

	// Validate type-specific requirements
	if auth.Type == "oidc" && auth.OIDCConfig == nil {
		return fmt.Errorf("spec.incomingAuth.oidcConfig is required when type is oidc")
	}

	return nil
}

// validateOutgoingAuth validates OutgoingAuth configuration
func (r *VirtualMCPServer) validateOutgoingAuth() error {
	auth := r.Spec.OutgoingAuth

	// Validate source enum values (already validated by kubebuilder but being explicit)
	validSources := map[string]bool{
		"discovered": true,
		"inline":     true,
	}
	if auth.Source != "" && !validSources[auth.Source] {
		return fmt.Errorf("spec.outgoingAuth.source must be one of: discovered, inline")
	}

	// Validate backend configurations
	for backendName, backendAuth := range auth.Backends {
		if err := r.validateBackendAuth(backendName, backendAuth); err != nil {
			return err
		}
	}

	return nil
}

// validateBackendAuth validates a single backend auth configuration
func (*VirtualMCPServer) validateBackendAuth(backendName string, auth BackendAuthConfig) error {
	// Validate type is set
	if auth.Type == "" {
		return fmt.Errorf("spec.outgoingAuth.backends[%s].type is required", backendName)
	}

	// Validate type-specific configurations
	switch auth.Type {
	case BackendAuthTypeExternalAuthConfigRef:
		if auth.ExternalAuthConfigRef == nil {
			return fmt.Errorf(
				"spec.outgoingAuth.backends[%s].externalAuthConfigRef is required when type is external_auth_config_ref",
				backendName)
		}
		if auth.ExternalAuthConfigRef.Name == "" {
			return fmt.Errorf("spec.outgoingAuth.backends[%s].externalAuthConfigRef.name is required", backendName)
		}

	case BackendAuthTypeDiscovered:
		// No additional validation needed

	default:
		return fmt.Errorf(
			"spec.outgoingAuth.backends[%s].type must be one of: discovered, external_auth_config_ref",
			backendName)
	}

	return nil
}

// validateAggregation validates Aggregation configuration
func (r *VirtualMCPServer) validateAggregation() error {
	agg := r.Spec.Config.Aggregation

	// Validate conflict resolution strategy
	if agg.ConflictResolution != "" {
		validStrategies := map[vmcp.ConflictResolutionStrategy]bool{
			vmcp.ConflictStrategyPrefix:   true,
			vmcp.ConflictStrategyPriority: true,
			vmcp.ConflictStrategyManual:   true,
		}
		if !validStrategies[agg.ConflictResolution] {
			return fmt.Errorf("config.aggregation.conflictResolution must be one of: prefix, priority, manual")
		}
	}

	// Validate conflict resolution config based on strategy
	if agg.ConflictResolutionConfig != nil {
		resConfig := agg.ConflictResolutionConfig

		switch agg.ConflictResolution {
		case vmcp.ConflictStrategyPrefix:
			// Prefix strategy uses PrefixFormat if specified, otherwise defaults
			// No additional validation required

		case vmcp.ConflictStrategyPriority:
			if len(resConfig.PriorityOrder) == 0 {
				return fmt.Errorf("config.aggregation.conflictResolutionConfig.priorityOrder is required when conflictResolution is priority")
			}

		case vmcp.ConflictStrategyManual:
			// For manual resolution, tools must define explicit overrides
			// This will be validated at runtime when conflicts are detected
		}
	}

	// Validate per-workload tool configurations
	for i, toolConfig := range agg.Tools {
		if toolConfig.Workload == "" {
			return fmt.Errorf("config.aggregation.tools[%d].workload is required", i)
		}

		// If ToolConfigRef is specified, ensure it has a name
		if toolConfig.ToolConfigRef != nil && toolConfig.ToolConfigRef.Name == "" {
			return fmt.Errorf("config.aggregation.tools[%d].toolConfigRef.name is required when toolConfigRef is specified", i)
		}
	}

	return nil
}

// validateCompositeTools validates composite tool definitions in spec.config.compositeTools.
// Uses shared validation from pkg/vmcp/config/composite_validation.go.
func (r *VirtualMCPServer) validateCompositeTools() error {
	toolNames := make(map[string]bool)

	for i := range r.Spec.Config.CompositeTools {
		tool := &r.Spec.Config.CompositeTools[i]

		// Check for duplicate tool names
		if toolNames[tool.Name] {
			return fmt.Errorf("spec.config.compositeTools[%d].name %q is duplicated", i, tool.Name)
		}
		toolNames[tool.Name] = true

		// Use shared validation
		if err := config.ValidateCompositeToolConfig(
			fmt.Sprintf("spec.config.compositeTools[%d]", i), tool,
		); err != nil {
			return err
		}
	}

	return nil
}

// Package validation provides image validation functionality for the ToolHive operator.
package validation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	k8serr "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	"github.com/stacklok/toolhive/pkg/registry"
)

// Sentinel errors for image validation.
// These errors can be checked using errors.Is() to determine the specific validation failure.
var (
	// ErrImageInvalid indicates that the image failed validation for any reason.
	// The wrapped error and message provide specific details about the validation failure.
	// This is the generic error that controllers should check for to handle any validation failure.
	ErrImageInvalid = errors.New("image validation failed")

	// ErrImageNotChecked indicates that no validation was performed on the image
	ErrImageNotChecked = errors.New("image validation was not performed")
)

// ImageValidation represents the type of image validation to perform.
type ImageValidation string

const (
	// ImageValidationAlwaysAllow indicates that all images are allowed
	ImageValidationAlwaysAllow ImageValidation = "always-allow"
	// ImageValidationRegistryEnforcing indicates that images must be validated against MCPRegistry resources
	ImageValidationRegistryEnforcing ImageValidation = "registry-enforcing"
)

// ImageValidator defines the interface for validating container images
type ImageValidator interface {
	// ValidateImage checks if an image is valid for use.
	// Returns:
	//   - nil if validation passes
	//   - ErrImageNotChecked if no validation was performed
	//   - wrapped ErrImageInvalid if image fails validation (with specific reason in error message)
	//   - other errors for system/infrastructure failures
	ValidateImage(ctx context.Context, image string) error
}

// AlwaysAllowValidator is a no-op validator that always allows images
type AlwaysAllowValidator struct{}

// ValidateImage always returns ErrImageNotChecked, indicating no validation was performed
func (*AlwaysAllowValidator) ValidateImage(_ context.Context, _ string) error {
	return ErrImageNotChecked
}

// NewImageValidator creates an appropriate ImageValidator based on configuration
func NewImageValidator(k8sClient client.Client, namespace string, validation ImageValidation) ImageValidator {
	if validation == ImageValidationRegistryEnforcing {
		return &RegistryEnforcingValidator{
			client:    k8sClient,
			namespace: namespace,
		}
	}
	return &AlwaysAllowValidator{}
}

// RegistryEnforcingValidator provides validation against MCPRegistry resources
type RegistryEnforcingValidator struct {
	client    client.Client
	namespace string
}

// ValidateImage checks if an image should be validated and if it exists in registries
func (v *RegistryEnforcingValidator) ValidateImage(ctx context.Context, image string) error {
	// List all MCPRegistry resources in the namespace
	mcpRegistryList := &mcpv1alpha1.MCPRegistryList{}
	if err := v.client.List(ctx, mcpRegistryList, client.InNamespace(v.namespace)); err != nil {
		return fmt.Errorf("failed to list MCPRegistry resources: %w", err)
	}

	// Check if any registry enforces validation
	// If no enforcement required, return ErrImageNotChecked to indicate no validation was performed
	hasEnforcement := v.hasEnforcingRegistry(mcpRegistryList)
	if !hasEnforcement {
		return ErrImageNotChecked
	}

	// Enforcement is required, check each registry for the image
	var registryErrors []string
	for _, mcpRegistry := range mcpRegistryList.Items {
		found, err := v.checkImageInRegistry(ctx, &mcpRegistry, image)
		if err != nil {
			// Collect errors but continue checking other registries
			registryErrors = append(registryErrors, fmt.Sprintf("registry %s: %v", mcpRegistry.Name, err))
			continue
		}
		if found {
			// Image found, validation passes
			return nil
		}
	}

	// Image not found in any registry and enforcement is required
	// Wrap the generic validation error with context about the specific image and any registry errors
	if len(registryErrors) > 0 {
		return fmt.Errorf("image %q not found in enforced registries (errors: %v): %w",
			image, registryErrors, ErrImageInvalid)
	}
	return fmt.Errorf("image %q not found in enforced registries: %w", image, ErrImageInvalid)
}

func (*RegistryEnforcingValidator) hasEnforcingRegistry(mcpRegistryList *mcpv1alpha1.MCPRegistryList) bool {
	for _, mcpRegistry := range mcpRegistryList.Items {
		if mcpRegistry.Spec.EnforceServers {
			return true
		}
	}

	return false
}

// checkImageInRegistry checks if an image exists in a specific MCPRegistry
func (v *RegistryEnforcingValidator) checkImageInRegistry(
	ctx context.Context,
	mcpRegistry *mcpv1alpha1.MCPRegistry,
	image string,
) (bool, error) {
	// Only check registries that are ready
	if mcpRegistry.Status.Phase != mcpv1alpha1.MCPRegistryPhaseReady {
		return false, nil
	}

	// Get the ConfigMap containing the registry data
	configMapName := mcpRegistry.GetStorageName()
	configMap := &corev1.ConfigMap{}
	if err := v.client.Get(ctx, client.ObjectKey{
		Name:      configMapName,
		Namespace: v.namespace,
	}, configMap); err != nil {
		if k8serr.IsNotFound(err) {
			// ConfigMap not found, registry data not available
			return false, nil
		}
		return false, fmt.Errorf("failed to get ConfigMap %s: %w", configMapName, err)
	}

	// Get the registry data from the ConfigMap
	registryData, exists := configMap.Data["registry.json"]
	if !exists {
		// No registry data in ConfigMap
		return false, nil
	}

	// Parse the registry data
	var reg registry.Registry
	if err := json.Unmarshal([]byte(registryData), &reg); err != nil {
		// Invalid registry data
		return false, fmt.Errorf("failed to parse registry data: %w", err)
	}

	// Search for the image in this registry
	return findImageInRegistry(&reg, image), nil
}

// findImageInRegistry searches for an image in a registry
func findImageInRegistry(reg *registry.Registry, image string) bool {
	// Check top-level servers
	for _, server := range reg.Servers {
		if server.Image == image {
			return true
		}
	}

	// Check servers in groups
	// TODO: check with Rado or Ria, is this needed?
	for _, group := range reg.Groups {
		for _, server := range group.Servers {
			if server.Image == image {
				return true
			}
		}
	}

	return false
}

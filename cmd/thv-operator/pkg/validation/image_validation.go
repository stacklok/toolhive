// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package validation provides image validation functionality for the ToolHive operator.
package validation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"

	v0 "github.com/modelcontextprotocol/registry/pkg/api/v0"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
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

	// RegistryNameLabel is the label key used to specify which registry an MCPServer should use
	RegistryNameLabel = "toolhive.stacklok.io/registry-name"
)

// ImageValidator defines the interface for validating container images
type ImageValidator interface {
	// ValidateImage checks if an image is valid for use.
	// The metadata parameter contains MCPServer metadata (labels, annotations) that may affect validation.
	// Returns:
	//   - nil if validation passes
	//   - ErrImageNotChecked if no validation was performed
	//   - wrapped ErrImageInvalid if image fails validation (with specific reason in error message)
	//   - other errors for system/infrastructure failures
	ValidateImage(ctx context.Context, image string, metadata metav1.ObjectMeta) error
}

// AlwaysAllowValidator is a no-op validator that always allows images
type AlwaysAllowValidator struct{}

// ValidateImage always returns ErrImageNotChecked, indicating no validation was performed
func (*AlwaysAllowValidator) ValidateImage(_ context.Context, _ string, _ metav1.ObjectMeta) error {
	return ErrImageNotChecked
}

// NewImageValidator creates an appropriate ImageValidator based on configuration
func NewImageValidator(k8sClient client.Client, namespace string, validation ImageValidation) ImageValidator {
	if validation == ImageValidationRegistryEnforcing {
		return &RegistryEnforcingValidator{
			client:     k8sClient,
			namespace:  namespace,
			httpClient: &http.Client{Timeout: 10 * time.Second},
		}
	}
	return &AlwaysAllowValidator{}
}

// RegistryEnforcingValidator provides validation against MCPRegistry resources.
// It queries the registry API service (via HTTP) to check whether an image
// exists in a registry's OCI packages.
type RegistryEnforcingValidator struct {
	client     client.Client
	namespace  string
	httpClient *http.Client
}

// ValidateImage checks if an image should be validated and if it exists in registries
// If the MCPServer has a registry-name label, validation is restricted to that specific registry.
// Otherwise, all registries are checked according to the original behavior.
func (v *RegistryEnforcingValidator) ValidateImage(ctx context.Context, image string, metadata metav1.ObjectMeta) error {
	// Check if MCPServer specifies a specific registry to use
	registryName, hasRegistryLabel := metadata.Labels[RegistryNameLabel]

	// List all MCPRegistry resources in the namespace
	mcpRegistryList := &mcpv1alpha1.MCPRegistryList{}
	if err := v.client.List(ctx, mcpRegistryList, client.InNamespace(v.namespace)); err != nil {
		return fmt.Errorf("failed to list MCPRegistry resources: %w", err)
	}

	if hasRegistryLabel {
		// MCPServer specifies a specific registry - validate against that registry only
		return v.validateAgainstSpecificRegistry(ctx, image, registryName, mcpRegistryList)
	}

	// No specific registry specified - use original behavior (check all registries)
	return v.validateAgainstAllRegistries(ctx, image, mcpRegistryList)
}

// validateAgainstSpecificRegistry validates an image against a specific registry
func (v *RegistryEnforcingValidator) validateAgainstSpecificRegistry(
	ctx context.Context,
	image string,
	registryName string,
	mcpRegistryList *mcpv1alpha1.MCPRegistryList,
) error {
	// Find the specified registry
	var targetRegistry *mcpv1alpha1.MCPRegistry
	for i := range mcpRegistryList.Items {
		if mcpRegistryList.Items[i].Name == registryName {
			targetRegistry = &mcpRegistryList.Items[i]
			break
		}
	}

	if targetRegistry == nil {
		return fmt.Errorf("specified registry %q not found: %w", registryName, ErrImageInvalid)
	}

	// Check if the specified registry enforces validation
	if !targetRegistry.Spec.EnforceServers {
		// Registry exists but doesn't enforce - validation not performed
		return ErrImageNotChecked
	}

	// Check if image exists in the specified registry
	found, err := v.checkImageInRegistry(ctx, targetRegistry, image)
	if err != nil {
		return fmt.Errorf("error checking image in registry %q: %v: %w", registryName, err, ErrImageInvalid)
	}

	if !found {
		return fmt.Errorf("image %q not found in specified registry %q: %w", image, registryName, ErrImageInvalid)
	}

	// Image found in specified registry
	return nil
}

// validateAgainstAllRegistries validates an image against all registries (original behavior)
func (v *RegistryEnforcingValidator) validateAgainstAllRegistries(
	ctx context.Context,
	image string,
	mcpRegistryList *mcpv1alpha1.MCPRegistryList,
) error {
	// Get only enforcing registries for efficient processing
	enforcingRegistries := v.getEnforcingRegistries(mcpRegistryList)
	if len(enforcingRegistries) == 0 {
		return ErrImageNotChecked
	}

	// Enforcement is required, check each enforcing registry for the image
	var registryErrors []string
	for _, mcpRegistry := range enforcingRegistries {
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

func (*RegistryEnforcingValidator) getEnforcingRegistries(
	mcpRegistryList *mcpv1alpha1.MCPRegistryList,
) []mcpv1alpha1.MCPRegistry {
	var enforcingRegistries []mcpv1alpha1.MCPRegistry
	for _, mcpRegistry := range mcpRegistryList.Items {
		if mcpRegistry.Spec.EnforceServers {
			enforcingRegistries = append(enforcingRegistries, mcpRegistry)
		}
	}
	return enforcingRegistries
}

// checkImageInRegistry checks if an image exists in a specific MCPRegistry by
// querying the registry API service at the URL stored in the MCPRegistry status.
func (v *RegistryEnforcingValidator) checkImageInRegistry(
	ctx context.Context,
	mcpRegistry *mcpv1alpha1.MCPRegistry,
	image string,
) (bool, error) {
	// Only check registries that are ready
	if mcpRegistry.Status.Phase != mcpv1alpha1.MCPRegistryPhaseReady {
		return false, nil
	}

	// Get the registry API URL from status
	registryURL := mcpRegistry.Status.URL
	if registryURL == "" {
		return false, nil
	}

	// Query the registry API for all servers
	servers, err := v.listRegistryServers(ctx, registryURL)
	if err != nil {
		return false, fmt.Errorf("failed to query registry API at %s: %w", registryURL, err)
	}

	return findImageInServers(servers, image), nil
}

// listRegistryServers queries the registry API to fetch all servers, handling pagination.
func (v *RegistryEnforcingValidator) listRegistryServers(
	ctx context.Context,
	registryURL string,
) ([]v0.ServerResponse, error) {
	var allServers []v0.ServerResponse
	cursor := ""

	for {
		servers, nextCursor, err := v.fetchRegistryPage(ctx, registryURL, cursor)
		if err != nil {
			return nil, err
		}

		allServers = append(allServers, servers...)

		if nextCursor == "" {
			break
		}
		cursor = nextCursor

		// Safety limit to prevent infinite loops
		if len(allServers) > 10000 {
			return nil, fmt.Errorf("exceeded maximum server limit (10000)")
		}
	}

	return allServers, nil
}

// fetchRegistryPage fetches a single page of servers from the registry API.
func (v *RegistryEnforcingValidator) fetchRegistryPage(
	ctx context.Context,
	registryURL string,
	cursor string,
) ([]v0.ServerResponse, string, error) {
	params := url.Values{}
	params.Set("limit", "100")
	if cursor != "" {
		params.Set("cursor", cursor)
	}

	endpoint := fmt.Sprintf("%s/v0.1/servers?%s", registryURL, params.Encode())

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, "", fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := v.httpClient.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("failed to query registry: %w", err)
	}
	defer func() {
		if closeErr := resp.Body.Close(); closeErr != nil {
			log.FromContext(ctx).V(1).Info("Failed to close response body", "error", closeErr)
		}
	}()

	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("registry API returned status %d", resp.StatusCode)
	}

	var listResp v0.ServerListResponse
	if err := json.NewDecoder(resp.Body).Decode(&listResp); err != nil {
		return nil, "", fmt.Errorf("failed to decode registry API response: %w", err)
	}

	return listResp.Servers, listResp.Metadata.NextCursor, nil
}

// findImageInServers searches for an OCI image in the servers returned by the registry API.
func findImageInServers(servers []v0.ServerResponse, image string) bool {
	for i := range servers {
		for j := range servers[i].Server.Packages {
			if servers[i].Server.Packages[j].RegistryType == "oci" &&
				servers[i].Server.Packages[j].Identifier == image {
				return true
			}
		}
	}
	return false
}

package oci

import (
	"fmt"
	"strings"

	"github.com/google/go-containerregistry/pkg/name"
)

// ValidateReference validates an OCI reference using go-containerregistry
func ValidateReference(ref string) error {
	_, err := name.ParseReference(ref)
	if err != nil {
		return fmt.Errorf("invalid OCI reference format: %s", ref)
	}
	return nil
}

// IsOCIReference determines if the reference could be an OCI registry reference
// using go-containerregistry's name package for proper validation
func IsOCIReference(ref string) bool {
	// Try to parse as a reference - if it succeeds, it's likely an OCI reference
	_, err := name.ParseReference(ref)
	return err == nil
}

// ExtractTag extracts the tag from a reference
func ExtractTag(ref string) string {
	parts := strings.Split(ref, ":")
	if len(parts) > 1 {
		return parts[len(parts)-1]
	}
	return ""
}

// GetCurrentTimestamp returns the current timestamp in RFC3339 format
// Uses fixed timestamp for reproducibility in artifacts
func GetCurrentTimestamp() string {
	return "2000-01-01T00:00:00Z"
}

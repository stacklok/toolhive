package sources

import (
	"encoding/json"
	"fmt"
	"slices"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	"github.com/stacklok/toolhive/pkg/registry"
)

// FormatConverter defines the interface for converting between registry formats
type FormatConverter interface {
	// Convert transforms a Registry from one logical format to another
	Convert(reg *registry.Registry, fromFormat, toFormat string) (*registry.Registry, error)
	// ConvertRaw transforms raw data between formats (legacy method)
	ConvertRaw(data []byte, fromFormat, toFormat string) ([]byte, error)
}

// RegistryFormatConverter implements the FormatConverter interface
type RegistryFormatConverter struct {
	validator SourceDataValidator
}

// NewRegistryFormatConverter creates a new registry format converter
func NewRegistryFormatConverter() FormatConverter {
	return &RegistryFormatConverter{
		validator: NewSourceDataValidator(),
	}
}

// Convert transforms a Registry from one logical format to another
func (*RegistryFormatConverter) Convert(reg *registry.Registry, fromFormat, toFormat string) (*registry.Registry, error) {
	// Validate input parameters
	if reg == nil {
		return nil, fmt.Errorf("input registry cannot be nil")
	}

	// Check if conversion is needed
	if fromFormat == toFormat {
		return data, nil
	}

	// Validate source format
	_, err := c.validator.ValidateData(data, fromFormat)
	if err != nil {
		return nil, fmt.Errorf("source data validation failed: %w", err)
	}

	// Check supported formats
	supportedFormats := supportedFormats()
	if !slices.Contains(supportedFormats, fromFormat) {
		return nil, fmt.Errorf("unsupported source format: %s", fromFormat)
	}
	if !slices.Contains(supportedFormats, toFormat) {
		return nil, fmt.Errorf("unsupported target format: %s", toFormat)
	}

	// For now, both ToolHive and Upstream formats use the same Registry structure internally
	// The main difference is in serialization/deserialization and validation
	// So we can return the same registry for most conversions
	switch {
	case fromFormat == mcpv1alpha1.RegistryFormatUpstream && toFormat == mcpv1alpha1.RegistryFormatToolHive:
		return reg, nil // Already converted during parsing
	case fromFormat == mcpv1alpha1.RegistryFormatToolHive && toFormat == mcpv1alpha1.RegistryFormatUpstream:
		return reg, nil // Registry structure is compatible
	default:
		return nil, fmt.Errorf("conversion from %s to %s is not supported", fromFormat, toFormat)
	}
}

// ConvertRaw transforms raw data between formats (legacy method)
func (c *RegistryFormatConverter) ConvertRaw(data []byte, fromFormat, toFormat string) ([]byte, error) {
	// Validate input parameters
	if len(data) == 0 {
		return nil, fmt.Errorf("input data cannot be empty")
	}

	// Check if conversion is needed
	if fromFormat == toFormat {
		return data, nil
	}

	// Parse the source data first
	reg, err := c.validator.ValidateData(data, fromFormat)
	if err != nil {
		return nil, fmt.Errorf("source data validation failed: %w", err)
	}

	// Convert the registry
	convertedReg, err := c.Convert(reg, fromFormat, toFormat)
	if err != nil {
		return nil, err
	}

	// Serialize back to bytes
	result, err := json.Marshal(convertedReg)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal converted registry: %w", err)
	}

	return result, nil
}

// supportedFormats returns the list of supported registry formats
func supportedFormats() []string {
	return []string{
		mcpv1alpha1.RegistryFormatToolHive,
		mcpv1alpha1.RegistryFormatUpstream,
	}
}

// detectFormat attempts to automatically detect the format of the provided data
func detectFormat(data []byte) (string, error) {
	if len(data) == 0 {
		return "", fmt.Errorf("data cannot be empty")
	}

	validator := NewSourceDataValidator()

	// Try to parse as ToolHive format first
	if _, err := validator.ValidateData(data, mcpv1alpha1.RegistryFormatToolHive); err == nil {
		return mcpv1alpha1.RegistryFormatToolHive, nil
	}

	// Try to parse as Upstream format
	if _, err := validator.ValidateData(data, mcpv1alpha1.RegistryFormatUpstream); err == nil {
		return mcpv1alpha1.RegistryFormatUpstream, nil
	}

	return "", fmt.Errorf("unable to detect registry format: data does not match any known format")
}

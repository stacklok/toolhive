package config

import (
	"fmt"
	"sync"
)

// ConfigFieldSpec defines the specification for a generic config field.
// It encapsulates all the logic needed to set, get, unset, and validate a config field.
type ConfigFieldSpec struct {
	// Name is the unique identifier for the field (e.g., "ca-cert", "registry-url")
	Name string

	// SetValidator validates the value before setting it.
	// Returns an error if the value is invalid.
	// This is called before Setter.
	SetValidator func(provider Provider, value string) error

	// Setter sets the value in the Config struct.
	// It receives the config to modify and the validated value.
	Setter func(cfg *Config, value string)

	// Getter retrieves the current value from the Config struct.
	// Returns the current value as a string.
	Getter func(cfg *Config) string

	// Unsetter clears the field in the Config struct.
	// It resets the field to its default/empty state.
	Unsetter func(cfg *Config)

	// IsSet checks if the field has a non-empty value.
	// Returns true if the field is configured, false otherwise.
	IsSet func(cfg *Config) bool

	// DisplayName is the user-friendly name for the field.
	// Used in user-facing messages and help text.
	DisplayName string

	// HelpText provides a description of the field for CLI help.
	HelpText string
}

// fieldRegistry stores all registered config field specifications
var fieldRegistry = make(map[string]ConfigFieldSpec)

// registryMutex protects concurrent access to the field registry
var registryMutex sync.RWMutex

// RegisterConfigField registers a new config field specification.
// This function is typically called during package initialization.
// Panics if a field with the same name is already registered.
func RegisterConfigField(spec ConfigFieldSpec) {
	registryMutex.Lock()
	defer registryMutex.Unlock()

	if spec.Name == "" {
		panic("config field name cannot be empty")
	}

	if _, exists := fieldRegistry[spec.Name]; exists {
		panic(fmt.Sprintf("config field %q is already registered", spec.Name))
	}

	// Validate required fields
	if spec.Setter == nil {
		panic(fmt.Sprintf("config field %q must have a Setter", spec.Name))
	}
	if spec.Getter == nil {
		panic(fmt.Sprintf("config field %q must have a Getter", spec.Name))
	}
	if spec.Unsetter == nil {
		panic(fmt.Sprintf("config field %q must have an Unsetter", spec.Name))
	}

	fieldRegistry[spec.Name] = spec
}

// GetConfigFieldSpec retrieves a registered config field specification by name.
// Returns the field spec and true if found, or an empty spec and false if not found.
func GetConfigFieldSpec(fieldName string) (ConfigFieldSpec, bool) {
	registryMutex.RLock()
	defer registryMutex.RUnlock()

	spec, exists := fieldRegistry[fieldName]
	return spec, exists
}

// ListConfigFields returns a list of all registered config field names.
func ListConfigFields() []string {
	registryMutex.RLock()
	defer registryMutex.RUnlock()

	fields := make([]string, 0, len(fieldRegistry))
	for name := range fieldRegistry {
		fields = append(fields, name)
	}
	return fields
}

// SetConfigField sets a config field value using the generic framework.
// It looks up the field spec, validates the value, and updates the config.
// Returns an error if the field is not registered, validation fails, or update fails.
func SetConfigField(provider Provider, fieldName, value string) error {
	spec, exists := GetConfigFieldSpec(fieldName)
	if !exists {
		return fmt.Errorf("unknown config field: %q", fieldName)
	}

	// Run custom validation if provided
	if spec.SetValidator != nil {
		if err := spec.SetValidator(provider, value); err != nil {
			return err
		}
	}

	// Update the config
	err := provider.UpdateConfig(func(cfg *Config) {
		spec.Setter(cfg, value)
	})
	if err != nil {
		return fmt.Errorf("failed to update configuration: %w", err)
	}

	return nil
}

// GetConfigField retrieves a config field value using the generic framework.
// It looks up the field spec and returns the current value.
// Returns the value, whether it's set, and any error.
func GetConfigField(provider Provider, fieldName string) (value string, isSet bool, err error) {
	spec, exists := GetConfigFieldSpec(fieldName)
	if !exists {
		return "", false, fmt.Errorf("unknown config field: %q", fieldName)
	}

	cfg := provider.GetConfig()
	value = spec.Getter(cfg)

	// Check if the field is set
	if spec.IsSet != nil {
		isSet = spec.IsSet(cfg)
	} else {
		// Default: field is set if value is non-empty
		isSet = value != ""
	}

	return value, isSet, nil
}

// UnsetConfigField clears a config field using the generic framework.
// It looks up the field spec and resets the field to its default state.
// Returns an error if the field is not registered or update fails.
func UnsetConfigField(provider Provider, fieldName string) error {
	spec, exists := GetConfigFieldSpec(fieldName)
	if !exists {
		return fmt.Errorf("unknown config field: %q", fieldName)
	}

	// Update the config
	err := provider.UpdateConfig(func(cfg *Config) {
		spec.Unsetter(cfg)
	})
	if err != nil {
		return fmt.Errorf("failed to update configuration: %w", err)
	}

	return nil
}

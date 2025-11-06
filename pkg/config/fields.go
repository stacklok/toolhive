package config

import (
	"fmt"
	"strconv"
	"strings"
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
// Returns the value, whether it's set (non-empty), and any error.
func GetConfigField(provider Provider, fieldName string) (value string, isSet bool, err error) {
	spec, exists := GetConfigFieldSpec(fieldName)
	if !exists {
		return "", false, fmt.Errorf("unknown config field: %q", fieldName)
	}

	cfg := provider.GetConfig()
	value = spec.Getter(cfg)
	isSet = value != ""

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

// Helper constructors for common field types

// RegisterStringField registers a simple string config field with optional validation.
// The fieldGetter returns a pointer to the string field in the config struct.
func RegisterStringField(
	name string,
	fieldGetter func(*Config) *string,
	validator func(Provider, string) error,
) {
	RegisterConfigField(ConfigFieldSpec{
		Name:         name,
		SetValidator: validator,
		Setter: func(cfg *Config, value string) {
			*fieldGetter(cfg) = value
		},
		Getter: func(cfg *Config) string {
			return *fieldGetter(cfg)
		},
		Unsetter: func(cfg *Config) {
			*fieldGetter(cfg) = ""
		},
	})
}

// RegisterBoolField registers a boolean config field with automatic string conversion.
// The fieldGetter returns a pointer to the bool field in the config struct.
func RegisterBoolField(
	name string,
	fieldGetter func(*Config) *bool,
	validator func(Provider, string) error,
) {
	RegisterConfigField(ConfigFieldSpec{
		Name:         name,
		SetValidator: validator,
		Setter: func(cfg *Config, value string) {
			enabled, _ := strconv.ParseBool(value) // Already validated
			*fieldGetter(cfg) = enabled
		},
		Getter: func(cfg *Config) string {
			return strconv.FormatBool(*fieldGetter(cfg))
		},
		Unsetter: func(cfg *Config) {
			*fieldGetter(cfg) = false
		},
	})
}

// RegisterFloatField registers a float64 config field with automatic string conversion.
// The fieldGetter returns a pointer to the float64 field in the config struct.
// The zeroValue parameter specifies what value indicates "unset" (typically 0.0).
func RegisterFloatField(
	name string,
	fieldGetter func(*Config) *float64,
	zeroValue float64,
	validator func(Provider, string) error,
) {
	RegisterConfigField(ConfigFieldSpec{
		Name:         name,
		SetValidator: validator,
		Setter: func(cfg *Config, value string) {
			floatVal, _ := strconv.ParseFloat(value, 64) // Already validated
			*fieldGetter(cfg) = floatVal
		},
		Getter: func(cfg *Config) string {
			val := *fieldGetter(cfg)
			if val == zeroValue {
				return ""
			}
			return strconv.FormatFloat(val, 'f', -1, 64)
		},
		Unsetter: func(cfg *Config) {
			*fieldGetter(cfg) = zeroValue
		},
	})
}

// RegisterStringSliceField registers a string slice config field with comma-separated string conversion.
// The fieldGetter returns a pointer to the []string field in the config struct.
func RegisterStringSliceField(
	name string,
	fieldGetter func(*Config) *[]string,
	validator func(Provider, string) error,
) {
	RegisterConfigField(ConfigFieldSpec{
		Name:         name,
		SetValidator: validator,
		Setter: func(cfg *Config, value string) {
			vars := strings.Split(value, ",")
			// Trim whitespace from each item
			for i, item := range vars {
				vars[i] = strings.TrimSpace(item)
			}
			*fieldGetter(cfg) = vars
		},
		Getter: func(cfg *Config) string {
			slice := *fieldGetter(cfg)
			if len(slice) == 0 {
				return ""
			}
			return strings.Join(slice, ",")
		},
		Unsetter: func(cfg *Config) {
			*fieldGetter(cfg) = nil
		},
	})
}
